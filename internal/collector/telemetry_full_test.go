package collector

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

func TestTelemetryFullContractAndIsolation(t *testing.T) {
	m, write := fixtureMetrics(t)
	for _, dir := range []string{"self", "sys/kernel/random", "123", "456"} {
		if err := os.MkdirAll(filepath.Join(m.root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	write("self/mountinfo", mountLine(1, "8:1", "/", "ext4")+mountLine(2, "8:2", "/data", "xfs")+mountLine(3, "0:3", "/remote", "nfs4"))
	write("sys/kernel/random/boot_id", "11111111-1111-4111-8111-111111111111")
	write("diskstats", diskFixture(diskCounters{100, 200, 10, 20, 40, 60, 1000}))
	full := NewTelemetryFull()
	full.source = m
	full.disk.source = m
	full.disk.sysRoot = diskSysFixture(t, "sda")
	full.readRelease = func(string) ([]byte, error) { return []byte("ID=test\nPRETTY_NAME=Test Linux\nVERSION_ID=1"), nil }
	fail := false
	calls := 0
	full.stat = func(path string) (filesystemCapacity, error) {
		calls++
		if path == "/remote" {
			t.Fatal("remote mount probed")
		}
		if fail && path == "/data" {
			return filesystemCapacity{}, errors.New("permission denied")
		}
		return capacityFromBlocks(path, 100, 40, 30, 4096)
	}
	// Remove fast files: a full sample must not depend on them or IP probing.
	for _, name := range []string{"stat", "meminfo", "uptime", "net/dev"} {
		if err := os.Remove(filepath.Join(m.root, name)); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	sample := func() *v1.TelemetryFull {
		t.Helper()
		f, err := full.Sample(context.Background(), at)
		if err != nil {
			t.Fatal(err)
		}
		b := v1.TelemetryBatch{NodeID: "node", RunID: "11111111-1111-4111-8111-111111111111", BatchID: "b", Fast: []*v1.TelemetryFast{}, Full: []*v1.TelemetryFull{f}, PublicIp: []*v1.TelemetryPublicIP{}}
		raw, _ := json.Marshal(b)
		if _, err = v1.DecodeTelemetryBatch(raw, "node", at, 60, true); err != nil {
			t.Fatal(string(raw), err)
		}
		return f
	}
	first := sample()
	if calls != 2 || len(first.Errors) != 0 || len(first.Filesystems) != 2 || *first.DiskTotal != 819200 || *first.DiskUsed != 491520 || *first.DiskAvailable != 245760 || *first.Processes != 2 || first.OSName != "Test Linux" {
		t.Fatal(first, calls)
	}
	write("diskstats", diskFixture(diskCounters{220, 440, 70, 140, 640, 2460, 31000}))
	at = at.Add(60 * time.Second)
	next := sample()
	if next.Sequence != 2 || next.ElapsedSeconds != 60 || *next.DiskIo.ReadIops != 1 {
		t.Fatal(next)
	}
	fail = true
	at = at.Add(time.Minute)
	next = sample()
	if next.DiskTotal != nil || len(next.Filesystems) != 0 || !slices.Contains(next.Errors, "disk_unavailable") {
		t.Fatal("partial filesystem sum leaked", next)
	}
	if first.Sequence != 1 || *first.DiskTotal != 819200 {
		t.Fatal("cached observation mutated")
	}
	full.readRelease = func(string) ([]byte, error) { return nil, errors.New("unavailable") }
	at = at.Add(time.Minute)
	next = sample()
	if next.OSName != "" || !slices.Contains(next.Errors, "os_unavailable") {
		t.Fatal(next)
	}
}
func TestTelemetryCapacityPrecisionAndAliases(t *testing.T) {
	c, err := capacityFromBlocks("disk", 9007199254740993, 1, 1, 1)
	if err != nil || c.total != 9007199254740993 || c.used != 9007199254740992 {
		t.Fatal(c, err)
	}
	for _, v := range [][4]uint64{{math.MaxInt64, 0, 0, 4096}, {1, 2, 0, 1}, {1, 1, 2, 1}, {1, 0, 0, 0}} {
		if _, err := capacityFromBlocks("disk", v[0], v[1], v[2], v[3]); err == nil {
			t.Fatal(v)
		}
	}
	data := mountLine(1, "8:1", "/", "ext4") + mountLine(2, "8:2", "/alias", "ext4")
	entries, total, _, _, err := collectTelemetryFilesystems(context.Background(), data, func(string) (filesystemCapacity, error) {
		return filesystemCapacity{identity: "same", total: math.MaxInt64}, nil
	})
	if err != nil || len(entries) != 1 || total != math.MaxInt64 {
		t.Fatal("dedup before sum", entries, total, err)
	}
	_, _, _, _, err = collectTelemetryFilesystems(context.Background(), data, func(path string) (filesystemCapacity, error) {
		return filesystemCapacity{identity: path, total: math.MaxInt64}, nil
	})
	if err == nil {
		t.Fatal("aggregate overflow accepted")
	}
}
