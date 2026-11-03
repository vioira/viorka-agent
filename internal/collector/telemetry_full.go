package collector

import (
	"context"
	"fmt"
	"math"
	"os"
	"runtime"
	"strings"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

// TelemetryFull runs on its own bounded worker. It never invokes fast sampling
// or public IP discovery, and owns the full-cadence disk baseline.
type TelemetryFull struct {
	source      *Metrics
	disk        *TelemetryDisk
	stat        telemetryFilesystemStat
	readRelease func(string) ([]byte, error)
	previous    time.Time
	sequence    uint64
}

func NewTelemetryFull() *TelemetryFull {
	source := NewMetrics()
	disk := NewTelemetryDisk()
	disk.source = source
	return &TelemetryFull{source: source, disk: disk, stat: statTelemetryFilesystem, readRelease: readRelease}
}
func (t *TelemetryFull) Sample(ctx context.Context, at time.Time) (*v1.TelemetryFull, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if at.IsZero() || t.sequence >= math.MaxInt64 {
		return nil, fmt.Errorf("invalid full sequence or sample time")
	}
	t.sequence++
	f := &v1.TelemetryFull{Sequence: t.sequence, SampledAt: at.UTC(), Filesystems: []*v1.TelemetryFilesystem{}, Errors: []string{}}
	elapsed := at.Sub(t.previous).Seconds()
	if !t.previous.IsZero() && elapsed > 0 && elapsed <= 600 {
		f.ElapsedSeconds = elapsed
	}
	t.previous = at
	if runtime.GOOS != "linux" && t.source.root == "/proc" {
		f.Errors = []string{"unsupported_platform"}
		return f, nil
	}
	data, err := t.source.read(ctx, "loadavg")
	fields := strings.Fields(data)
	if err == nil && len(fields) >= 3 {
		one, e1 := number(fields[0])
		five, e2 := number(fields[1])
		fifteen, e3 := number(fields[2])
		if e1 == nil && e2 == nil && e3 == nil && one <= 1e20 && five <= 1e20 && fifteen <= 1e20 {
			f.Load1 = &one
			f.Load5 = &five
			f.Load15 = &fifteen
		}
	}
	if f.Load1 == nil {
		f.Errors = append(f.Errors, "load_unavailable")
	}
	count, err := t.source.processCount(ctx)
	if err != nil {
		f.Errors = append(f.Errors, "processes_unavailable")
	} else {
		f.Processes = &count
	}
	data, err = t.source.read(ctx, "self/mountinfo")
	if err == nil {
		var total, used, available uint64
		f.Filesystems, total, used, available, err = collectTelemetryFilesystems(ctx, data, t.stat)
		if err == nil {
			f.DiskTotal = &total
			f.DiskUsed = &used
			f.DiskAvailable = &available
		}
	}
	if err != nil {
		f.Filesystems = []*v1.TelemetryFilesystem{}
		f.Errors = append(f.Errors, "disk_unavailable")
	}
	boot, bootErr := t.source.read(ctx, "sys/kernel/random/boot_id")
	if bootErr == nil {
		f.DiskIo, err = t.disk.Sample(ctx, at, strings.TrimSpace(boot))
	} else {
		err = bootErr
		t.disk.counters = nil
		t.disk.previous = time.Time{}
	}
	if err != nil {
		f.DiskIo = nil
		f.Errors = append(f.Errors, "disk_io_unavailable")
	}
	if ctx.Err() == nil {
		raw, e := t.readRelease("/etc/os-release")
		if os.IsNotExist(e) {
			raw, e = t.readRelease("/usr/lib/os-release")
		}
		if e == nil {
			f.OSID, f.OSName, f.OSVersion = parseRelease(string(raw))
		}
	}
	if f.OSName == "" {
		f.OSID = ""
		f.OSVersion = ""
		f.Errors = append(f.Errors, "os_unavailable")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return f, nil
}
