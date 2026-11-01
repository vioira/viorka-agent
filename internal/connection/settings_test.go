package connection

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vioira/viorka-agent/internal/state"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

func TestSettingsDurabilityAndFailure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	store, err := state.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	id := state.Identity{PanelURL: "https://panel.example", NodeID: "n"}
	c := &Client{store: store}
	next := v1.NodeSettings{Revision: 1, CollectionSeconds: 0, WssReportSeconds: 2, ReportSeconds: 60, ConnectionMode: "wss"}
	if result := c.applySettings(id, next); result.State != "applied" {
		t.Fatal(result)
	}
	info, err := os.Stat(filepath.Join(dir, "node-settings.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("unsafe settings file", err)
	}
	restored := &Client{store: store}
	if err := restored.loadSettings(id); err != nil || *restored.settings != next {
		t.Fatal("restart failed", err)
	}
	wrong := id
	wrong.NodeID = "other"
	if err := restored.loadSettings(wrong); err == nil {
		t.Fatal("cross-node settings accepted")
	}
	// A directory in place of the file deterministically fails rename even as root.
	if err := os.Remove(filepath.Join(dir, "node-settings.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "node-settings.json"), 0700); err != nil {
		t.Fatal(err)
	}
	next.Revision++
	if result := c.applySettings(id, next); result.ErrorCode != "persist_failed" || result.AppliedRevision != 1 || c.settings.Revision != 1 {
		t.Fatal("write failure falsely applied", result)
	}
}
func TestSamplingSurvivesDisconnectWithoutBacklog(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &sampling{interval: time.Millisecond, changed: make(chan struct{}, 1), ready: make(chan struct{}), done: make(chan struct{})}
	sampled := make(chan struct{}, 100)
	go s.run(ctx, func(context.Context) v1.NodeMetrics {
		sampled <- struct{}{}
		return v1.NodeMetrics{SampledAt: time.Now()}
	})
	defer func() { cancel(); <-s.done }()
	<-s.ready
	s.setOnline(true)
	wait := func(n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			select {
			case <-sampled:
			case <-time.After(time.Second):
				t.Fatal("collector stopped")
			}
		}
	}
	wait(15)
	if got := len(s.take()); got > 10 {
		t.Fatal("unbounded buffer", got)
	}
	s.setOnline(false)
	before := s.take()[0].SampledAt
	wait(5)
	offline := s.take()
	if len(offline) != 1 || !offline[0].SampledAt.After(before) {
		t.Fatal("offline collection stopped or retained backlog")
	}
	s.setOnline(true)
	if got := len(s.take()); got != 1 {
		t.Fatal("replayed offline samples", got)
	}
	s.setInterval(5 * time.Millisecond)
	wait(2)
}

func TestRepeatedSettingsDoNotPostponeSampling(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := startSampling(ctx, 10*time.Millisecond)
	defer func() { cancel(); <-s.done }()
	<-s.ready
	first := s.take()[0].SampledAt
	// Frequent reconnects replay the same settings; they must not reset the clock.
	deadline := time.Now().Add(60 * time.Millisecond)
	for time.Now().Before(deadline) {
		s.setInterval(10 * time.Millisecond)
		time.Sleep(time.Millisecond)
	}
	if !s.take()[0].SampledAt.After(first) {
		t.Fatal("replayed settings starved sampling")
	}
}
