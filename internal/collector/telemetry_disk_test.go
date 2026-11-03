package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

func diskFixture(v diskCounters) string {
	return fmt.Sprintf("8 0 sda %d 0 %d %d %d 0 %d %d 0 %d 0\n", v[2], v[0], v[4], v[3], v[1], v[5], v[6])
}
func TestTelemetryDiskRatesQualityAndContract(t *testing.T) {
	m, write := fixtureMetrics(t)
	d := NewTelemetryDisk()
	d.source = m
	d.sysRoot = diskSysFixture(t, "sda")
	at := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	boot := "11111111-1111-4111-8111-111111111111"
	sample := func(seconds float64) *v1.TelemetryDiskIO {
		t.Helper()
		at = at.Add(time.Duration(seconds * float64(time.Second)))
		items, err := d.Sample(context.Background(), at, boot)
		if err != nil || items == nil {
			t.Fatal(items, err)
		}
		full := &v1.TelemetryFull{Sequence: 1, SampledAt: at, ElapsedSeconds: seconds, DiskIo: items, Errors: []string{"load_unavailable", "processes_unavailable", "os_unavailable", "disk_unavailable"}}
		b := v1.TelemetryBatch{NodeID: "node", RunID: boot, BatchID: "b", Fast: []*v1.TelemetryFast{}, Full: []*v1.TelemetryFull{full}, PublicIp: []*v1.TelemetryPublicIP{}}
		raw, _ := json.Marshal(b)
		if _, err = v1.DecodeTelemetryBatch(raw, "node", at, 60, true); err != nil {
			t.Fatal(string(raw), err)
		}
		return items
	}
	write("diskstats", diskFixture(diskCounters{100, 200, 10, 20, 40, 60, 1000}))
	first := sample(0)
	if first.Quality[0] != "unknown" || first.ReadIops != nil {
		t.Fatal(first)
	}
	write("diskstats", diskFixture(diskCounters{220, 440, 70, 140, 640, 2460, 31000}))
	v := sample(60)
	if *v.ReadBytesPerSecond != 1024 || *v.WriteBytesPerSecond != 2048 || *v.ReadIops != 1 || *v.WriteIops != 2 || *v.ReadAwaitMs != 10 || *v.WriteAwaitMs != 20 || *v.AwaitMs != float64(3000)/180 || *v.UtilPercent != 50 {
		t.Fatal(v)
	}
	v = sample(30)
	if *v.ReadIops != 0 || v.ReadAwaitMs != nil || v.WriteAwaitMs != nil || v.AwaitMs != nil || *v.UtilPercent != 0 {
		t.Fatal("idle must have unknown await", v)
	}
	write("diskstats", diskFixture(diskCounters{221, 441, 71, 141, 641, 2461, 33000}))
	v = sample(1)
	if v.UtilPercent != nil || len(v.Quality) != 1 || v.Quality[0] != "partial" || v.ReadBytesPerSecond == nil {
		t.Fatal("invalid util silently clamped", v)
	}
	write("diskstats", diskFixture(diskCounters{1, 2, 1, 2, 1, 2, 1}))
	v = sample(1)
	if v.Quality[0] != "unknown" || v.ReadBytesPerSecond != nil {
		t.Fatal(v)
	}
	boot = "22222222-2222-4222-8222-222222222222"
	v = sample(1)
	if v.Quality[0] != "unknown" {
		t.Fatal(v)
	}
	// A long gap rebuilds rates without erasing absolute counters.
	at = at.Add(601 * time.Second)
	items, err := d.Sample(context.Background(), at, boot)
	if err != nil || items.Quality[0] != "unknown" {
		t.Fatal(items, err)
	}
	write("diskstats", "bad")
	if items, err = d.Sample(context.Background(), at.Add(time.Second), boot); err == nil || items != nil {
		t.Fatal("malformed diskstats accepted")
	}
	write("diskstats", diskFixture(diskCounters{10, 20, 10, 20, 10, 20, 10}))
	v = sample(1)
	if v.Quality[0] != "unknown" {
		t.Fatal("failure recovery reused baseline")
	}
	if first.ReadIops != nil {
		t.Fatal("old snapshot mutated")
	}
}
func TestTelemetryDiskParserBounds(t *testing.T) {
	valid := diskFixture(diskCounters{1, 2, 3, 4, 5, 6, 7})
	for _, raw := range []string{valid + valid, strings.Replace(valid, "sda", "bad/name", 1), diskFixture(diskCounters{1 << 55, 2, 3, 4, 5, 6, 7}), "8 0 sda -1 0 0 0 0 0 0 0 0 0 0"} {
		if _, err := parseTelemetryDisk(raw); err == nil {
			t.Fatal("invalid diskstats", raw)
		}
	}
	var raw strings.Builder
	for i := 0; i < 65; i++ {
		fmt.Fprintf(&raw, "8 %d sd%d 0 0 0 0 0 0 0 0 0 0 0\n", i, i)
	}
	if _, err := parseTelemetryDisk(raw.String()); err == nil {
		t.Fatal("device budget")
	}
}

func diskSysFixture(t *testing.T, names ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range names {
		if err := os.MkdirAll(filepath.Join(root, "class/block", name, "slaves"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestDiskSummaryFiltersTopologyAndWeightsLatency(t *testing.T) {
	m, write := fixtureMetrics(t)
	d := NewTelemetryDisk()
	d.source = m
	d.sysRoot = diskSysFixture(t, "sda", "sdb", "sda1", "dm-0", "md0")
	if err := os.WriteFile(filepath.Join(d.sysRoot, "class/block/sda1/partition"), []byte("1"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dm-0", "md0"} {
		if err := os.WriteFile(filepath.Join(d.sysRoot, "class/block", name, "slaves/sda"), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	row := func(name string, v diskCounters) string { return strings.Replace(diskFixture(v), "sda", name, 1) }
	ignored := row("sda1", diskCounters{999, 999, 999, 999, 999, 999, 999}) + row("dm-0", diskCounters{999, 999, 999, 999, 999, 999, 999}) + row("md0", diskCounters{999, 999, 999, 999, 999, 999, 999}) + row("loop0", diskCounters{}) + row("ram0", diskCounters{})
	at := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	boot := "11111111-1111-4111-8111-111111111111"
	write("diskstats", row("sda", diskCounters{})+row("sdb", diskCounters{})+ignored)
	if _, err := d.Sample(context.Background(), at, boot); err != nil {
		t.Fatal(err)
	}
	write("diskstats", row("sda", diskCounters{2, 4, 2, 4, 20, 80, 100})+row("sdb", diskCounters{6, 8, 6, 8, 180, 320, 500})+ignored)
	result, err := d.Sample(context.Background(), at.Add(time.Second), boot)
	if err != nil {
		t.Fatal(err)
	}
	if *result.ReadBytesPerSecond != 4096 || *result.WriteBytesPerSecond != 6144 || *result.ReadIops != 8 || *result.WriteIops != 12 || *result.ReadAwaitMs != 25 || *result.WriteAwaitMs != float64(400)/12 || *result.AwaitMs != 30 || *result.UtilPercent != 50 || len(result.Quality) != 0 {
		t.Fatalf("unexpected aggregate: %+v", result)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "device") || strings.Contains(string(raw), "sda") || strings.Contains(string(raw), `"read_bytes":`) {
		t.Fatal("device details leaked", string(raw))
	}
	// A healthy idle disk remains a valid zero even when another disk resets.
	write("diskstats", row("sda", diskCounters{2, 4, 2, 4, 20, 80, 100})+row("sdb", diskCounters{}))
	result, err = d.Sample(context.Background(), at.Add(2*time.Second), boot)
	if err != nil || result.ReadIops == nil || *result.ReadIops != 0 || result.AwaitMs != nil || len(result.Quality) != 1 || result.Quality[0] != "partial" {
		t.Fatal("idle/reset aggregation", result, err)
	}
	// Malformed records and unavailable topology do not discard healthy fields.
	write("diskstats", row("sda", diskCounters{4, 4, 4, 4, 40, 80, 100})+"8 1 broken -1\n"+row("missing", diskCounters{}))
	result, err = d.Sample(context.Background(), at.Add(3*time.Second), boot)
	if err != nil || *result.ReadIops != 2 || result.Quality[0] != "partial" {
		t.Fatal("partial parse", result, err)
	}
	write("diskstats", row("loop0", diskCounters{}))
	if result, err = d.Sample(context.Background(), at.Add(4*time.Second), boot); err == nil || result != nil {
		t.Fatal("no eligible devices became zero", result, err)
	}
}

func TestDiskAggregateKeepsValidFields(t *testing.T) {
	n := func(v float64) *float64 { return &v }
	d := aggregateDiskIO([]*v1.TelemetryDiskIO{
		{ReadBytesPerSecond: n(10), WriteBytesPerSecond: n(0), ReadIops: n(2), WriteIops: n(0), AwaitMs: n(5), UtilPercent: nil, Quality: []string{"partial"}},
		{ReadBytesPerSecond: n(20), WriteBytesPerSecond: n(0), ReadIops: n(6), WriteIops: n(0), AwaitMs: n(15), UtilPercent: n(20), Quality: []string{}},
	}, false)
	if *d.ReadBytesPerSecond != 30 || *d.AwaitMs != 12.5 || *d.UtilPercent != 20 || d.Quality[0] != "partial" {
		t.Fatal(d)
	}
}
