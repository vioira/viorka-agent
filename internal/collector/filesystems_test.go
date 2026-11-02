package collector

import (
	"context"
	"errors"
	"fmt"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
	"strings"
	"testing"
)

func mountLine(id int, device, point, kind string) string {
	return fmt.Sprintf("%d 1 %s / %s rw - %s /dev/disk rw\n", id, device, point, kind)
}
func TestFilesystemsIncludeDataAndDeduplicate(t *testing.T) {
	data := mountLine(1, "8:1", "/", "ext4") + mountLine(2, "8:2", "/data", "ext4") + mountLine(3, "8:2", "/data-copy", "ext4") + mountLine(4, "0:2", "/tmp", "tmpfs") + mountLine(5, "0:3", "/remote", "nfs4") + mountLine(6, "0:4", "/container", "overlay") + mountLine(7, "8:3", `/space\040disk`, "xfs")
	calls := 0
	out := collectFilesystems(context.Background(), data, func(p string) (string, *v1.DiskMetrics, error) {
		calls++
		total := float64(20)
		if p == "/" {
			total = 16
		}
		return p, &v1.DiskMetrics{TotalBytes: total, UsedBytes: 4, AvailableBytes: total - 5}, nil
	})
	if len(out.Errors) != 0 || len(out.Mounts) != 3 || calls != 3 || out.Mounts[0].MountPoint != "/" || out.Mounts[1].MountPoint != "/data" || out.Mounts[2].MountPoint != "/space disk" {
		t.Fatalf("unexpected inventory: %+v calls %d", out, calls)
	}
	if out.Mounts[0].Usage.TotalBytes+out.Mounts[1].Usage.TotalBytes != 36 {
		t.Fatal("data disk missing")
	}
	// Aliases with the same fsid must not double count capacity.
	out = collectFilesystems(context.Background(), data, func(string) (string, *v1.DiskMetrics, error) {
		return "same-fsid", &v1.DiskMetrics{TotalBytes: 100}, nil
	})
	if len(out.Mounts) != 1 {
		t.Fatal("filesystem aliases double counted")
	}
}
func TestFilesystemsFailureNeverReportsPartialTotal(t *testing.T) {
	valid := mountLine(1, "8:1", "/", "ext4") + mountLine(2, "8:2", "/data", "ext4")
	for _, data := range []string{valid, "bad mountinfo", strings.Repeat("x", 262145), mountLine(1, "8:1", `/bad\011path`, "ext4")} {
		out := collectFilesystems(context.Background(), data, func(p string) (string, *v1.DiskMetrics, error) {
			if p == "/data" {
				return "", nil, errors.New("permission denied")
			}
			return p, &v1.DiskMetrics{TotalBytes: 16}, nil
		})
		if len(out.Mounts) != 0 || len(out.Errors) != 1 {
			t.Fatalf("partial or false success: %+v", out)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := collectFilesystems(ctx, valid, func(string) (string, *v1.DiskMetrics, error) { t.Fatal("called after cancel"); return "", nil, nil })
	if len(out.Errors) != 1 {
		t.Fatal("cancellation ignored")
	}
	data := ""
	for i := 0; i < 33; i++ {
		data += mountLine(i, fmt.Sprint(i), fmt.Sprintf("/disk%d", i), "ext4")
	}
	out = collectFilesystems(context.Background(), data, func(p string) (string, *v1.DiskMetrics, error) { return p, &v1.DiskMetrics{TotalBytes: 100}, nil })
	if len(out.Mounts) != 0 || len(out.Errors) != 1 {
		t.Fatal("mount limit ignored")
	}
}

func TestBtrfsSubvolumesShareCapacity(t *testing.T) {
	data := mountLine(1, "0:31", "/", "btrfs") + mountLine(2, "0:32", "/home", "btrfs")
	calls := 0
	out := collectFilesystems(context.Background(), data, func(p string) (string, *v1.DiskMetrics, error) {
		calls++
		return p, &v1.DiskMetrics{TotalBytes: 100}, nil
	})
	if len(out.Errors) != 0 || len(out.Mounts) != 1 || calls != 1 {
		t.Fatal("btrfs subvolumes double counted")
	}
}
