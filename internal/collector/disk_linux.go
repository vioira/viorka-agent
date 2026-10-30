package collector

import (
	"fmt"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
	"syscall"
)

func rootDisk() (*v1.DiskMetrics, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs("/", &s); err != nil {
		return nil, err
	}
	if s.Bsize <= 0 || s.Blocks == 0 || s.Bfree > s.Blocks || s.Bavail > s.Bfree {
		return nil, fmt.Errorf("invalid filesystem counters")
	}
	return &v1.DiskMetrics{TotalBytes: float64(s.Blocks) * float64(s.Bsize), UsedBytes: float64(s.Blocks-s.Bfree) * float64(s.Bsize), AvailableBytes: float64(s.Bavail) * float64(s.Bsize)}, nil
}
