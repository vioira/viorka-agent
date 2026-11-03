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
	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

func TestTelemetryFastGroupsAndContract(t *testing.T) {
	m, write := fixtureMetrics(t)
	if err := os.MkdirAll(filepath.Join(m.root, "sys/kernel/random"), 0700); err != nil {
		t.Fatal(err)
	}
	write("sys/kernel/random/boot_id", "11111111-1111-4111-8111-111111111111")
	write("net/sockstat", "TCP: inuse 2 orphan 0 tw 99\nUDP: inuse 3 mem 0\n")
	write("net/sockstat6", "TCP6: inuse 4\nUDP6: inuse 5\n")
	if err := os.Remove(filepath.Join(m.root, "loadavg")); err != nil {
		t.Fatal(err)
	}
	sampler, err := NewTelemetryFast(config.NetworkFilter{})
	if err != nil {
		t.Fatal(err)
	}
	sampler.source = m
	sampler.network.source = m
	sampler.network.interfaces = func() ([]net.Interface, error) { return []net.Interface{{Name: "eth0", Index: 2}}, nil }
	at := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	sample := func() *v1.TelemetryFast {
		t.Helper()
		f, _, err := sampler.Sample(context.Background(), at)
		if err != nil {
			t.Fatal(err)
		}
		batch := v1.TelemetryBatch{NodeID: "node", RunID: "11111111-1111-4111-8111-111111111111", BatchID: "batch", Fast: []*v1.TelemetryFast{f}, Full: []*v1.TelemetryFull{}, PublicIp: []*v1.TelemetryPublicIP{}}
		raw, err := json.Marshal(batch)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = v1.DecodeTelemetryBatch(raw, "node", at, 60, true); err != nil {
			t.Fatal("fast contract", string(raw), err)
		}
		return f
	}
	f := sample()
	if f.CPUPercent != nil || !slices.Contains(f.Errors, "cpu_unavailable") || *f.TcpInuse != 6 || *f.UdpInuse != 8 || *f.RamUsed != 400*1024 || *f.SwapUsed != 100*1024 {
		t.Fatal(f)
	}
	write("stat", "cpu 150 0 100 850 0 0 0 0\ncpu0 1\ncpu1 1\n")
	write("net/dev", netFixture(1060, 2120))
	at = at.Add(30 * time.Second)
	f = sample()
	if f.Sequence != 2 || f.ElapsedSeconds != 30 || f.CPUPercent == nil || *f.CPUPercent != 50 || *f.Interfaces[0].RXBytesPerSecond != 2 {
		t.Fatal(f)
	}
	if len(f.Errors) != 1 || f.Errors[0] != "traffic_unavailable" {
		t.Fatal("slow group leaked into fast errors", f.Errors)
	}
	write("sys/kernel/random/boot_id", "22222222-2222-4222-8222-222222222222")
	write("stat", "cpu 200 0 100 900 0 0 0 0\ncpu0 1\ncpu1 1\n")
	at = at.Add(time.Second)
	f = sample()
	if f.CPUPercent != nil || f.Interfaces[0].RXBytesPerSecond != nil {
		t.Fatal("host reboot reused baseline")
	}
	if err := os.Remove(filepath.Join(m.root, "net/sockstat6")); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Second)
	f = sample()
	if *f.TcpInuse != 2 || *f.UdpInuse != 3 {
		t.Fatal("missing IPv6 must be supported", f)
	}
	write("net/sockstat6", "TCP6: invalid 4\n")
	at = at.Add(time.Second)
	f = sample()
	if f.TcpInuse != nil || f.UdpInuse != nil || !slices.Contains(f.Errors, "sockets_unavailable") {
		t.Fatal("broken IPv6 hidden", f)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if f, _, err := sampler.Sample(cancelled, at); err == nil || f != nil {
		t.Fatal("cancellation ignored")
	}
}
func TestTelemetryMemoryLosslessAndInvalid(t *testing.T) {
	base := "MemTotal: 8796093022209 kB\nMemAvailable: 1 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n"
	total, used, swap, swapUsed, err := parseTelemetryMemory(base)
	if err != nil || total != 9007199254742016 || used != 9007199254740992 || swap != 0 || swapUsed != 0 {
		t.Fatal(total, used, err)
	}
	for _, raw := range []string{base + "MemTotal: 1 kB\n", "MemTotal: 1.5 kB\nMemAvailable: 0 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB", "MemTotal: 9007199254740992 kB\nMemAvailable: 0 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB", "MemTotal: 1 kB\nMemAvailable: 2 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB"} {
		if _, _, _, _, err := parseTelemetryMemory(raw); err == nil {
			t.Fatal("invalid memory admitted", raw)
		}
	}
}
