package collector

import (
	"context"
	"errors"
	"fmt"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func fixtureMetrics(t *testing.T) (*Metrics, func(string, string)) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "net"), 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("stat", "cpu 100 0 100 800 0 0 0 0 999 0\ncpu0 1\ncpu1 1\n")
	write("meminfo", "MemTotal: 1000 kB\nMemAvailable: 600 kB\nSwapTotal: 200 kB\nSwapFree: 100 kB\n")
	write("loadavg", "1.2 2.3 3.4 1/100 2\n")
	write("uptime", "1234.5 1000\n")
	write("net/dev", netFixture(1000, 2000))
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return &Metrics{root: root, now: func() time.Time { return now }, disk: func() (*v1.DiskMetrics, error) {
		return &v1.DiskMetrics{TotalBytes: 1000, UsedBytes: 300, AvailableBytes: 600}, nil
	}}, write
}
func netFixture(rx, tx uint64) string {
	return fmt.Sprintf("Inter-| Receive | Transmit\n face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed\n lo: 99 0 0 0 0 0 0 0 99 0 0 0 0 0 0 0\n eth0: %d 0 0 0 0 0 0 0 %d 0 0 0 0 0 0 0\n", rx, tx)
}
func TestMetricsCountersAndResets(t *testing.T) {
	m, write := fixtureMetrics(t)
	first := m.Sample(context.Background())
	if len(first.Errors) != 0 || first.CPU.UsagePercent != nil || first.Interfaces[0].RXBytesPerSecond != nil {
		t.Fatalf("first: %+v", first)
	}
	if first.Memory.UsedBytes != 400*1024 || first.Memory.SwapUsedBytes != 100*1024 || len(first.Interfaces) != 1 || first.CPU.LogicalCPUs != 2 {
		t.Fatalf("counters: %+v", first)
	}
	now := m.now().Add(10 * time.Second)
	m.now = func() time.Time { return now }
	write("stat", "cpu 150 0 150 900 0 0 0 0 1999 0\ncpu0 1\ncpu1 1\n")
	write("net/dev", netFixture(2000, 4000))
	second := m.Sample(context.Background())
	if *second.CPU.UsagePercent != 50 || *second.Interfaces[0].RXBytesPerSecond != 100 || *second.Interfaces[0].TXBytesPerSecond != 200 {
		t.Fatalf("deltas: %+v %+v", second.CPU, second.Interfaces[0])
	}
	now = now.Add(10 * time.Second)
	write("stat", "cpu 1 0 1 8 0 0 0 0\ncpu0 1\n")
	write("net/dev", netFixture(1, 2))
	reset := m.Sample(context.Background())
	if reset.CPU.UsagePercent != nil || reset.Interfaces[0].RXBytesPerSecond != nil {
		t.Fatal("reset fabricated rates")
	}
	now = now.Add(time.Hour)
	gap := m.Sample(context.Background())
	if gap.IntervalSeconds != 0 || gap.CPU.UsagePercent != nil || gap.Interfaces[0].RXBytesPerSecond != nil {
		t.Fatal("long gap fabricated rates")
	}
}
func TestMetricsFailuresAreUnknownAndRecover(t *testing.T) {
	m, write := fixtureMetrics(t)
	m.Sample(context.Background())
	write("meminfo", "MemTotal: 1000 kB\n")
	write("net/dev", "broken")
	write("loadavg", "NaN 0 0")
	write("stat", "cpu 0\n")
	m.disk = func() (*v1.DiskMetrics, error) { return nil, errors.New("denied") }
	bad := m.Sample(context.Background())
	if bad.Memory != nil || bad.CPU != nil || bad.Load != nil || bad.Disk != nil || len(bad.Errors) != 5 {
		t.Fatalf("failure: %+v", bad)
	}
	write("net/dev", netFixture(10000, 10000))
	now := m.now().Add(10 * time.Second)
	m.now = func() time.Time { return now }
	recovered := m.Sample(context.Background())
	if recovered.Interfaces[0].RXBytesPerSecond != nil {
		t.Fatal("recovery reused failed baseline")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	v := m.Sample(ctx)
	if len(v.Errors) != 6 {
		t.Fatalf("cancel: %+v", v)
	}
}
func TestMetricsBounds(t *testing.T) {
	if _, e := parseNetwork(netFixture(1, 2) + " eth0: 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0\n"); e == nil {
		t.Fatal("duplicate interface accepted")
	}
	m, write := fixtureMetrics(t)
	write("stat", string(make([]byte, 262145)))
	v := m.Sample(context.Background())
	if v.CPU != nil {
		t.Fatal("oversized input accepted")
	}
}
func TestLiveLinuxMetrics(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux procfs")
	}
	m := NewMetrics()
	v := m.Sample(context.Background())
	if len(v.Errors) > 0 || v.CPU == nil || v.Memory == nil || v.Disk == nil || v.UptimeSeconds == nil {
		t.Fatalf("live collection: %+v", v)
	}
	time.Sleep(20 * time.Millisecond)
	v = m.Sample(context.Background())
	if v.Host == nil || v.Host.Processes == nil || *v.Host.Processes == 0 || v.Host.TcpSockets == nil || v.Host.UdpSockets == nil {
		t.Fatalf("missing live host metrics: %+v", v.Host)
	}
	if v.CPU.UsagePercent == nil {
		t.Fatal("missing real CPU delta")
	}
}
func TestUnsupportedPlatform(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("non-Linux only")
	}
	v := NewMetrics().Sample(context.Background())
	if len(v.Errors) != 1 || v.Errors[0] != "unsupported_platform" || v.CPU != nil {
		t.Fatal(v)
	}
}
