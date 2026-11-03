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

// Incus can expose a quota-limited ZFS dataset as the container root and bind
// another directory from that dataset under /dev. Other datasets may share pool
// space, so only the root ZFS view belongs in the capacity aggregate.
func TestFilesystemsZFSContainerRoot(t *testing.T) {
	data := "1798 552 0:313 /rootfs / rw,relatime,idmapped shared:1566 master:1562 - zfs default/containers/test rw,xattr,posixacl\n" +
		"1814 1798 0:313 /credentials /dev/.incus-systemd-credentials ro,relatime - zfs default/containers/test rw,xattr,posixacl\n" +
		"1815 1798 0:314 / /data rw - zfs default/data rw\n" +
		mountLine(1816, "0:315", "/dev", "tmpfs") +
		mountLine(1817, "0:316", "/remote", "nfs4") +
		mountLine(1818, "0:317", "/fuse", "fuse.lxcfs")
	want := &v1.DiskMetrics{TotalBytes: 548798464, UsedBytes: 79822848, AvailableBytes: 468975616}
	calls := 0
	out := collectFilesystems(context.Background(), data, func(p string) (string, *v1.DiskMetrics, error) {
		calls++
		if p != "/" {
			t.Fatalf("unexpected probe: %s", p)
		}
		return "zfs-root", want, nil
	})
	if len(out.Errors) != 0 || len(out.Mounts) != 1 || calls != 1 {
		t.Fatalf("missing or duplicate ZFS root: %+v calls=%d", out, calls)
	}
	if m := out.Mounts[0]; m.MountPoint != "/" || m.FilesystemType != "zfs" || *m.Usage != *want {
		t.Fatalf("incorrect root capacity: %+v", m)
	}
	calls = 0
	entries, total, used, available, err := collectTelemetryFilesystems(context.Background(), data, func(p string) (filesystemCapacity, error) {
		calls++
		if p != "/" {
			t.Fatalf("unexpected telemetry probe: %s", p)
		}
		return filesystemCapacity{identity: "zfs-root", total: uint64(want.TotalBytes), used: uint64(want.UsedBytes), available: uint64(want.AvailableBytes)}, nil
	})
	if err != nil || len(entries) != 1 || calls != 1 || total != uint64(want.TotalBytes) || used != uint64(want.UsedBytes) || available != uint64(want.AvailableBytes) {
		t.Fatalf("incorrect telemetry capacity: %+v %d/%d/%d calls=%d err=%v", entries, total, used, available, calls, err)
	}
	if entries[0].MountPoint != "/" || entries[0].FilesystemType != "zfs" {
		t.Fatalf("incorrect telemetry root: %+v", entries[0])
	}
}
