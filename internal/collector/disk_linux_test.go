package collector

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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
