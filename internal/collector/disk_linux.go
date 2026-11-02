package collector

import (
	"fmt"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
	"os"
	"syscall"
)

func rootDisk() (*v1.DiskMetrics, error) {
	_, usage, err := statFilesystem("/")
	return usage, err
}

func statFilesystem(path string) (string, *v1.DiskMetrics, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", nil, err
	}
	if !info.IsDir() {
		return "", nil, errNotFilesystemDirectory
	}
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return "", nil, err
	}
	if s.Bsize <= 0 || s.Blocks == 0 || s.Bfree > s.Blocks || s.Bavail > s.Bfree {
		return "", nil, fmt.Errorf("invalid filesystem counters")
	}
	return fmt.Sprintf("%d:%v", s.Type, s.Fsid), &v1.DiskMetrics{TotalBytes: float64(s.Blocks) * float64(s.Bsize), UsedBytes: float64(s.Blocks-s.Bfree) * float64(s.Bsize), AvailableBytes: float64(s.Bavail) * float64(s.Bsize)}, nil
}
