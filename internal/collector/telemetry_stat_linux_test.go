package collector

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestTelemetryStatActualFilesystemAndSymlink(t *testing.T) {
	dir := t.TempDir()
	got, err := statTelemetryFilesystem(dir)
	if err != nil {
		t.Fatal(err)
	}
	var raw syscall.Statfs_t
	if err = syscall.Statfs(dir, &raw); err != nil {
		t.Fatal(err)
	}
	if got.total != raw.Blocks*uint64(raw.Bsize) || got.used > got.total || got.available > got.total-got.used || got.identity == "" {
		t.Fatal(got)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err = os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err = statTelemetryFilesystem(link); err == nil {
		t.Fatal("followed final symlink")
	}
	file := filepath.Join(dir, "file")
	if err = os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = statTelemetryFilesystem(file); err != errNotFilesystemDirectory {
		t.Fatal(err)
	}
}
