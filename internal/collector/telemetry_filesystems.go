package collector

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

type filesystemCapacity struct {
	identity               string
	total, used, available uint64
}
type telemetryFilesystemStat func(string) (filesystemCapacity, error)

func capacityFromBlocks(identity string, blocks, free, available, size uint64) (filesystemCapacity, error) {
	if identity == "" || size == 0 || blocks == 0 || free > blocks || available > free || blocks > math.MaxInt64/size {
		return filesystemCapacity{}, fmt.Errorf("invalid filesystem capacity")
	}
	return filesystemCapacity{identity: identity, total: blocks * size, used: (blocks - free) * size, available: available * size}, nil
}
func collectTelemetryFilesystems(ctx context.Context, data string, stat telemetryFilesystemStat) ([]*v1.TelemetryFilesystem, uint64, uint64, uint64, error) {
	mounts, err := parseFilesystemMounts(data)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	out := []*v1.TelemetryFilesystem{}
	devices, ids, sources, points := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	var total, used, available uint64
	for _, mount := range mounts {
		if err = ctx.Err(); err != nil {
			return nil, 0, 0, 0, err
		}
		if devices[mount.device] || (mount.kind == "btrfs" && sources[mount.source]) {
			continue
		}
		value, e := stat(mount.point)
		if errors.Is(e, errNotFilesystemDirectory) {
			continue
		}
		if e != nil {
			return nil, 0, 0, 0, e
		}
		if value.identity == "" || len(value.identity) > 256 || strings.IndexFunc(value.identity, unicode.IsControl) >= 0 || value.total > math.MaxInt64 || value.used > value.total || value.available > value.total-value.used {
			return nil, 0, 0, 0, fmt.Errorf("invalid filesystem observation")
		}
		devices[mount.device] = true
		if mount.kind == "btrfs" {
			sources[mount.source] = true
		}
		if ids[value.identity] {
			continue
		}
		ids[value.identity] = true
		if value.total > math.MaxInt64-total {
			return nil, 0, 0, 0, fmt.Errorf("filesystem aggregate overflow")
		}
		if points[mount.point] {
			return nil, 0, 0, 0, fmt.Errorf("ambiguous mount point")
		}
		points[mount.point] = true
		total += value.total
		used += value.used
		available += value.available
		out = append(out, &v1.TelemetryFilesystem{Identity: value.identity, MountPoint: mount.point, FilesystemType: mount.kind, TotalBytes: value.total, UsedBytes: value.used, AvailableBytes: value.available})
		if len(out) > 32 {
			return nil, 0, 0, 0, fmt.Errorf("filesystem budget exceeded")
		}
	}
	if err = ctx.Err(); err != nil {
		return nil, 0, 0, 0, err
	}
	return out, total, used, available, nil
}
