package collector

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFilesystemKernelCountersAndFileMounts(t *testing.T) {
	dir := t.TempDir()
	alias := filepath.Join(dir, "directory")
	if err := os.Mkdir(alias, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	id, usage, err := statFilesystem(dir)
	if err != nil || usage.TotalBytes <= 0 || usage.UsedBytes+usage.AvailableBytes > usage.TotalBytes {
		t.Fatalf("invalid real statfs: %+v %v", usage, err)
	}
	other, _, err := statFilesystem(alias)
	if err != nil || id != other {
		t.Fatal("subdirectory changed filesystem identity", err)
	}
	data := mountLine(1, "8:1", dir, "ext4") + mountLine(2, "8:2", alias, "ext4") + mountLine(3, "8:3", file, "ext4")
	out := collectFilesystems(context.Background(), data, statFilesystem)
	if len(out.Errors) != 0 || len(out.Mounts) != 1 {
		t.Fatalf("real filesystem dedup/file filtering failed: %+v", out)
	}
}

// Opt in on a Linux host with a supported root filesystem. This checks the
// actual mount namespace and kernel counters without changing mounts/services.
func TestFilesystemsLiveLinuxRoot(t *testing.T) {
	if os.Getenv("VIORKA_TEST_LIVE_FILESYSTEMS") != "1" {
		t.Skip("set VIORKA_TEST_LIVE_FILESYSTEMS=1 on a Linux test host")
	}
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out := collectFilesystems(ctx, string(data), statFilesystem)
	if len(out.Errors) != 0 || len(out.Mounts) == 0 || out.Mounts[0].MountPoint != "/" {
		t.Fatalf("live root filesystem missing: %+v", out)
	}
	root := out.Mounts[0]
	u := root.Usage
	if u == nil || u.TotalBytes <= 0 || u.UsedBytes+u.AvailableBytes > u.TotalBytes || u.UsedBytes+u.AvailableBytes <= 0 {
		t.Fatalf("invalid live root counters: %+v", u)
	}
	entries, total, used, available, err := collectTelemetryFilesystems(ctx, string(data), statTelemetryFilesystem)
	if err != nil || len(entries) == 0 || entries[0].MountPoint != "/" || entries[0].FilesystemType != root.FilesystemType || total == 0 || used > total || available > total-used {
		t.Fatalf("invalid live telemetry: %+v total=%d used=%d available=%d err=%v", entries, total, used, available, err)
	}
	t.Logf("root=%s type=%s total=%.0f used=%.0f available=%.0f df_usage=%.0f%% mounts=%d telemetry_mounts=%d", root.MountPoint, root.FilesystemType, u.TotalBytes, u.UsedBytes, u.AvailableBytes, math.Ceil(100*u.UsedBytes/(u.UsedBytes+u.AvailableBytes)), len(out.Mounts), len(entries))
}
