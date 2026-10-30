//go:build !linux

package collector

import (
	"fmt"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

func rootDisk() (*v1.DiskMetrics, error) { return nil, fmt.Errorf("unsupported platform") }
