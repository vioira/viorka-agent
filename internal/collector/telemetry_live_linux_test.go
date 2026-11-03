package collector

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vioira/viorka-agent/internal/config"
	"github.com/vioira/viorka-agent/internal/state"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

// Opt in on the supported Linux test host; restricted procfs/container mounts
// must not be mistaken for evidence that all host metrics are available.
func TestTelemetryLiveLinux(t *testing.T) {
	if os.Getenv("VIORKA_TEST_LIVE_TELEMETRY") != "1" {
		t.Skip("set VIORKA_TEST_LIVE_TELEMETRY=1 on a Linux test host")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := state.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ledger, err := state.OpenTrafficLedger(store, "https://panel.test", "live-linux")
	if err != nil {
		t.Fatal(err)
	}
	fast, err := NewTelemetryFast(config.NetworkFilter{})
	if err != nil {
		t.Fatal(err)
	}
	fast.SetTrafficLedger(ledger)
	full := NewTelemetryFull()
	first, _, err := fast.Sample(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if first.CPUPercent != nil {
		t.Fatal("first CPU sample invented a baseline")
	}
	if _, err = full.Sample(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-time.After(250 * time.Millisecond):
	}
	at := time.Now()
	f, _, err := fast.Sample(ctx, at)
	if err != nil {
		t.Fatal(err)
	}
	s, err := full.Sample(ctx, at)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Errors) > 0 || f.CPUPercent == nil || f.RamTotal == nil || f.TcpInuse == nil || f.UdpInuse == nil || len(f.Interfaces) == 0 || f.Traffic == nil {
		t.Fatalf("live fast metrics incomplete: %+v", f)
	}
	if len(s.Errors) > 0 || s.DiskTotal == nil || s.DiskIo == nil || s.Processes == nil || s.OSID == "" {
		t.Fatalf("live full metrics incomplete: %+v", s)
	}
	batch := v1.TelemetryBatch{NodeID: "live-linux", RunID: "11111111-1111-4111-8111-111111111111", BatchID: "live", Fast: []*v1.TelemetryFast{f}, Full: []*v1.TelemetryFull{s}, PublicIp: []*v1.TelemetryPublicIP{}}
	raw, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = v1.DecodeTelemetryBatch(raw, "live-linux", at, 60, true); err != nil {
		t.Fatal("real host violates protocol", err)
	}
	t.Logf("real procfs/statfs: interfaces=%d disk_total=%d os=%s CPU delta valid; uid=%d", len(f.Interfaces), *s.DiskTotal, s.OSID, os.Getuid())
}
