package collector

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/vioira/viorka-agent/internal/config"
	"github.com/vioira/viorka-agent/internal/state"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

func TestFastTrafficPersistentRecoveryAndCorruption(t *testing.T) {
	m, write := fixtureMetrics(t)
	if err := os.MkdirAll(filepath.Join(m.root, "sys/kernel/random"), 0700); err != nil {
		t.Fatal(err)
	}
	boot := "11111111-1111-4111-8111-111111111111"
	write("sys/kernel/random/boot_id", boot)
	dir := filepath.Join(t.TempDir(), "state")
	store, err := state.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ledger, err := state.OpenTrafficLedger(store, "https://panel.test", "node")
	if err != nil {
		t.Fatal(err)
	}
	makeSampler := func(ledger *state.TrafficLedger) *TelemetryFast {
		t.Helper()
		s, err := NewTelemetryFast(config.NetworkFilter{})
		if err != nil {
			t.Fatal(err)
		}
		s.source = m
		s.network.source = m
		s.network.interfaces = func() ([]net.Interface, error) { return []net.Interface{{Name: "eth0", Index: 2}}, nil }
		s.SetTrafficLedger(ledger)
		return s
	}
	sampler := makeSampler(ledger)
	at := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	sample := func() *v1.TelemetryFast {
		t.Helper()
		f, _, err := sampler.Sample(context.Background(), at)
		if err != nil {
			t.Fatal(err)
		}
		b := v1.TelemetryBatch{NodeID: "node", RunID: boot, BatchID: "b", Fast: []*v1.TelemetryFast{f}, Full: []*v1.TelemetryFull{}, PublicIp: []*v1.TelemetryPublicIP{}}
		raw, _ := json.Marshal(b)
		if _, err = v1.DecodeTelemetryBatch(raw, "node", at, 60, true); err != nil {
			t.Fatal(string(raw), err)
		}
		return f
	}
	f := sample()
	if f.Traffic == nil || *f.Traffic.RXBytes != 0 || !slices.Contains(f.Traffic.Quality, "partial") {
		t.Fatal(f)
	}
	write("net/dev", netFixture(1100, 2200))
	at = at.Add(time.Minute)
	f = sample()
	if *f.Traffic.RXBytes != 100 {
		t.Fatal(f.Traffic)
	}
	if err = ledger.Save(context.Background(), at, false); err != nil {
		t.Fatal(err)
	}
	recovered, err := state.OpenTrafficLedger(store, "https://panel.test", "node")
	if err != nil {
		t.Fatal(err)
	}
	sampler = makeSampler(recovered)
	write("net/dev", netFixture(1150, 2300))
	at = at.Add(time.Minute)
	f = sample()
	if f.Interfaces[0].RXBytesPerSecond != nil || *f.Traffic.RXBytes != 150 {
		t.Fatal("rate baseline and month recovery conflated", f)
	}
	write("net/dev", "broken")
	at = at.Add(time.Minute)
	f = sample()
	if f.Traffic != nil || !slices.Contains(f.Errors, "traffic_unavailable") {
		t.Fatal("failed network passed as empty ledger", f)
	}
	write("net/dev", netFixture(1200, 2400))
	at = at.Add(time.Minute)
	f = sample()
	if *f.Traffic.RXBytes != 200 {
		t.Fatal("network recovery lost ledger baseline", f.Traffic)
	}
	if err = os.WriteFile(filepath.Join(dir, "traffic.dat"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	blocked, err := state.OpenTrafficLedger(store, "https://panel.test", "node")
	if err == nil || blocked == nil {
		t.Fatal("corrupt file accepted")
	}
	sampler = makeSampler(blocked)
	at = at.Add(time.Minute)
	f = sample()
	if f.Traffic == nil || f.Traffic.RXBytes != nil || !slices.Contains(f.Traffic.Quality, "unknown") || sampler.TrafficError() == nil || f.RamTotal == nil {
		t.Fatal("ledger failure suppressed other metrics", f)
	}
}
