package connection

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vioira/viorka-agent/internal/state"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

func TestTelemetrySettingsDurableAndIndependent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	store, err := state.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	id := state.Identity{PanelURL: "https://panel.test", NodeID: "node"}
	settings, err := loadTelemetrySettings(store, id)
	if err != nil || settings.current != nil {
		t.Fatal(settings, err)
	}
	next := v1.DefaultTelemetrySettings()
	next.CollectionSeconds = 60
	next.HttpReportSeconds = 30
	result := settings.apply(next)
	if result.State != "applied" || result.Validate() != nil {
		t.Fatal(result)
	}
	if _, err = os.Stat(filepath.Join(dir, "node-settings.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("legacy settings file changed", err)
	}
	restored, err := loadTelemetrySettings(store, id)
	if err != nil || restored.current == nil || *restored.current != next {
		t.Fatal(restored, err)
	}
	next.Revision = 2
	next.CollectionSeconds = 1
	restored.write = func(string, any) error { return errors.New("disk failed") }
	result = restored.apply(next)
	if result.ErrorCode != "persist_failed" || result.AppliedRevision != 1 || restored.current.Revision != 1 {
		t.Fatal("false apply", result)
	}
	restored.write = store.Write
	result = restored.apply(next)
	if result.State != "applied" || result.AppliedRevision != 2 {
		t.Fatal(result)
	}
	conflict := next
	conflict.CollectionSeconds = 2
	if r := restored.apply(conflict); r.ErrorCode != "revision_conflict" {
		t.Fatal(r)
	}
	old := next
	old.Revision = 1
	if r := restored.apply(old); r.ErrorCode != "stale_revision" {
		t.Fatal(r)
	}
	invalid := next
	invalid.CollectionSeconds = 0
	if r := restored.apply(invalid); r.ErrorCode != "invalid_settings" {
		t.Fatal(r)
	}
	if r := restored.apply(next); r.State != "applied" {
		t.Fatal("repeat not idempotent", r)
	}
	if _, err = loadTelemetrySettings(store, state.Identity{PanelURL: id.PanelURL, NodeID: "other"}); err == nil {
		t.Fatal("cross-node settings accepted")
	}
	target := filepath.Join(dir, "node-telemetry-settings.json")
	if err = os.Rename(target, target+".saved"); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target+".saved", target); err != nil {
		t.Fatal(err)
	}
	if _, err = loadTelemetrySettings(store, id); err == nil {
		t.Fatal("symlink settings accepted")
	}
}
func TestTelemetryCadenceIndependentCollectionAndReporting(t *testing.T) {
	at := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		c, r             uint64
		fast, full, post int
	}{{30, 60, 7, 4, 3}, {2, 60, 91, 4, 3}, {60, 30, 4, 7, 6}} {
		settings := v1.DefaultTelemetrySettings()
		settings.CollectionSeconds = tc.c
		settings.HttpReportSeconds = tc.r
		c, err := newTelemetryCadence(settings, at)
		if err != nil {
			t.Fatal(err)
		}
		var fast, full, post int
		for i := 0; i <= 180; i++ {
			d := c.due(at.Add(time.Duration(i) * time.Second))
			if d.Fast {
				fast++
			}
			if d.Full {
				full++
			}
			if d.POST {
				post++
			}
			if d.WSS {
				t.Fatal("HTTP cadence sent WSS")
			}
		}
		if fast != tc.fast || full != tc.full || post != tc.post {
			t.Fatal(tc, fast, full, post)
		}
	}
}
func TestTelemetryCadenceChangesAndNoCatchup(t *testing.T) {
	at := time.Now()
	settings := v1.DefaultTelemetrySettings()
	c, _ := newTelemetryCadence(settings, at)
	c.due(at)
	next := settings
	next.Revision++
	next.HttpReportSeconds = 30
	if err := c.change(next, at.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	if !c.fast.Equal(at.Add(30*time.Second)) || !c.full.Equal(at.Add(40*time.Second)) || !c.post.Equal(at.Add(40*time.Second)) {
		t.Fatal("R changed fast phase")
	}
	c.realtimeMode(true, at.Add(15*time.Second))
	full := c.full
	d := c.due(at.Add(15 * time.Second))
	if !d.Fast || !d.WSS || d.POST {
		t.Fatal(d)
	}
	next.Revision++
	next.CollectionSeconds = 60
	fast := c.fast
	if err := c.change(next, at.Add(16*time.Second)); err != nil {
		t.Fatal(err)
	}
	if !c.fast.Equal(fast) {
		t.Fatal("C changed realtime phase")
	}
	next.Revision++
	next.WssReportSeconds = 5
	if err := c.change(next, at.Add(17*time.Second)); err != nil {
		t.Fatal(err)
	}
	if !c.fast.Equal(at.Add(22*time.Second)) || !c.full.Equal(full) {
		t.Fatal("W changed full phase")
	}
	c.realtimeMode(false, at.Add(20*time.Second))
	if !c.fast.Equal(at.Add(80*time.Second)) || !c.post.Equal(at.Add(50*time.Second)) || !c.full.Equal(full) {
		t.Fatal("mode exit phase")
	}
	d = c.due(at.Add(time.Hour))
	if !d.Fast || !d.Full || !d.POST || d.WSS {
		t.Fatal(d)
	}
	if d = c.due(at.Add(time.Hour)); d != (telemetryDue{}) {
		t.Fatal("catch-up burst", d)
	}
}

func TestTelemetryRealtimeCadenceIgnoresCollectionInterval(t *testing.T) {
	at := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	for _, interval := range []uint64{1, 2} {
		settings := v1.DefaultTelemetrySettings()
		settings.CollectionSeconds = 60
		settings.WssReportSeconds = interval
		cadence, err := newTelemetryCadence(settings, at)
		if err != nil {
			t.Fatal(err)
		}
		cadence.realtimeMode(true, at)
		var fast, full, wss int
		for second := 0; second <= 180; second++ {
			due := cadence.due(at.Add(time.Duration(second) * time.Second))
			wantFast := second%int(interval) == 0
			if due.Fast != wantFast || due.WSS != wantFast || due.POST || due.Full != (second%60 == 0) {
				t.Fatalf("W=%d second=%d unexpected due=%+v", interval, second, due)
			}
			if due.Fast {
				fast++
			}
			if due.Full {
				full++
			}
			if due.WSS {
				wss++
			}
		}
		if fast != 180/int(interval)+1 || wss != fast || full != 4 {
			t.Fatalf("W=%d fast=%d wss=%d full=%d", interval, fast, wss, full)
		}
	}
}
