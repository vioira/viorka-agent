package collector

import (
	"context"
	"encoding/json"
	"fmt"
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
	at := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	boot := "11111111-1111-4111-8111-111111111111"
	sample := func(seconds float64) *v1.TelemetryDiskIO {
		t.Helper()
		at = at.Add(time.Duration(seconds * float64(time.Second)))
		items, err := d.Sample(context.Background(), at, boot)
		if err != nil || len(items) != 1 {
			t.Fatal(items, err)
		}
		full := &v1.TelemetryFull{Sequence: 1, SampledAt: at, ElapsedSeconds: seconds, DiskIo: items, Filesystems: []*v1.TelemetryFilesystem{}, Errors: []string{"load_unavailable", "processes_unavailable", "os_unavailable", "disk_unavailable"}}
		b := v1.TelemetryBatch{NodeID: "node", RunID: boot, BatchID: "b", Fast: []*v1.TelemetryFast{}, Full: []*v1.TelemetryFull{full}, PublicIp: []*v1.TelemetryPublicIP{}}
		raw, _ := json.Marshal(b)
		if _, err = v1.DecodeTelemetryBatch(raw, "node", at, 60, true); err != nil {
			t.Fatal(string(raw), err)
		}
		return items[0]
	}
	write("diskstats", diskFixture(diskCounters{100, 200, 10, 20, 40, 60, 1000}))
	first := sample(0)
	if first.Quality[0] != "baseline" || first.ReadBytes != 51200 || first.ReadIops != nil {
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
	if v.Quality[0] != "counter_reset" || v.ReadBytesPerSecond != nil {
		t.Fatal(v)
	}
	boot = "22222222-2222-4222-8222-222222222222"
	v = sample(1)
	if v.Quality[0] != "identity_changed" {
		t.Fatal(v)
	}
	// A long gap rebuilds rates without erasing absolute counters.
	at = at.Add(601 * time.Second)
	items, err := d.Sample(context.Background(), at, boot)
	if err != nil || items[0].Quality[0] != "baseline" {
		t.Fatal(items, err)
	}
	write("diskstats", "bad")
	if items, err = d.Sample(context.Background(), at.Add(time.Second), boot); err == nil || len(items) != 0 {
		t.Fatal("malformed diskstats accepted")
	}
	write("diskstats", diskFixture(diskCounters{10, 20, 10, 20, 10, 20, 10}))
	v = sample(1)
	if v.Quality[0] != "baseline" {
		t.Fatal("failure recovery reused baseline")
	}
	if first.ReadBytes != 51200 {
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
