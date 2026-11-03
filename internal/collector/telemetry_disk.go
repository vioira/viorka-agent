package collector

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

// Counters: sectors read/written, completed reads/writes, read/write ms, busy ms.
type diskCounters [7]uint64
type TelemetryDisk struct {
	sysRoot  string
	source   *Metrics
	previous time.Time
	boot     string
	counters map[string]diskCounters
}

func NewTelemetryDisk() *TelemetryDisk { return &TelemetryDisk{source: NewMetrics(), sysRoot: "/sys"} }
func parseTelemetryDisk(data string) (map[string]diskCounters, error) {
	counters := map[string]diskCounters{}
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if len(f) < 14 || len(f[2]) == 0 || len(f[2]) > 64 || f[2] == "." || f[2] == ".." {
			return nil, fmt.Errorf("invalid diskstats fields")
		}
		for _, c := range f[2] {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.ContainsRune("_.:-", c)) {
				return nil, fmt.Errorf("invalid disk name")
			}
		}
		major, e1 := strconv.ParseUint(f[0], 10, 32)
		minor, e2 := strconv.ParseUint(f[1], 10, 32)
		if e1 != nil || e2 != nil {
			return nil, fmt.Errorf("invalid disk identity")
		}
		id := fmt.Sprintf("%d:%d:%s", major, minor, f[2])
		if _, exists := counters[id]; exists {
			return nil, fmt.Errorf("duplicate disk identity")
		}
		var v diskCounters
		for i, column := range []int{5, 9, 3, 7, 6, 10, 12} {
			n, err := strconv.ParseUint(f[column], 10, 64)
			if err != nil || n > math.MaxInt64 {
				return nil, fmt.Errorf("invalid disk counter")
			}
			v[i] = n
		}
		if v[0] > math.MaxInt64/512 || v[1] > math.MaxInt64/512 {
			return nil, fmt.Errorf("disk byte counter overflow")
		}
		counters[id] = v
		if len(counters) > 64 {
			return nil, fmt.Errorf("disk device limit exceeded")
		}
	}
	return counters, nil
}

// Sample owns a full-group baseline, never the fast cadence. boot must be the
// current kernel boot identity. Missing devices disappear instead of contributing
// phantom throughput; reappearing devices start a new baseline.
func (t *TelemetryDisk) Sample(ctx context.Context, at time.Time, boot string) (out *v1.TelemetryDiskIO, err error) {
	out = nil
	defer func() {
		if err != nil {
			t.counters = nil
			t.previous = time.Time{}
			out = nil
		}
	}()
	if at.IsZero() || !validBootID(boot) {
		return out, fmt.Errorf("missing disk sample identity")
	}
	data, err := t.source.read(ctx, "diskstats")
	if err != nil {
		return out, err
	}
	counters := map[string]diskCounters{}
	partial := false
	for _, line := range strings.Split(data, "\n") {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			partial = true
			continue
		}
		name := fields[2]
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "zram") || strings.HasPrefix(name, "sr") {
			continue
		}
		parsed, e := parseTelemetryDisk(line)
		if e != nil {
			partial = true
			continue
		}
		// Count only whole leaf block devices: partitions and stacked dm/md devices
		// would count the same I/O again. sysfs also covers virtual VPS disks.
		base := filepath.Join(t.sysRoot, "class/block", name)
		if _, e = os.Stat(filepath.Join(base, "partition")); e == nil {
			continue
		} else if !os.IsNotExist(e) {
			partial = true
			continue
		}
		slaves, e := os.ReadDir(filepath.Join(base, "slaves"))
		if e != nil {
			partial = true
			continue
		}
		if len(slaves) != 0 {
			continue
		}
		for id, value := range parsed {
			if _, exists := counters[id]; exists {
				return nil, fmt.Errorf("duplicate disk identity")
			}
			counters[id] = value
		}
		if len(counters) > 64 {
			return nil, fmt.Errorf("disk device limit exceeded")
		}
	}
	if len(counters) == 0 {
		return nil, fmt.Errorf("no eligible disk counters")
	}
	samples := []*v1.TelemetryDiskIO{}
	names := make([]string, 0, len(counters))
	for name := range counters {
		names = append(names, name)
	}
	sort.Strings(names)
	elapsed := at.Sub(t.previous).Seconds()
	valid := !t.previous.IsZero() && elapsed > 0 && elapsed <= 600
	for _, name := range names {
		v := counters[name]
		old, exists := t.counters[name]
		item := &v1.TelemetryDiskIO{Quality: []string{}}
		switch {
		case exists && boot != t.boot:
			item.Quality = []string{"identity_changed"}
		case !exists || !valid:
			item.Quality = []string{"baseline"}
		default:
			var delta diskCounters
			reset := false
			for i, n := range v {
				if n < old[i] {
					reset = true
					break
				}
				delta[i] = n - old[i]
			}
			if reset {
				item.Quality = []string{"counter_reset"}
				break
			}
			partial := false
			bounded := func(value, max float64) *float64 {
				if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > max {
					partial = true
					return nil
				}
				return &value
			}
			item.ReadBytesPerSecond = bounded(float64(delta[0])*512/elapsed, 1e20)
			item.WriteBytesPerSecond = bounded(float64(delta[1])*512/elapsed, 1e20)
			item.ReadIops = bounded(float64(delta[2])/elapsed, 1e20)
			item.WriteIops = bounded(float64(delta[3])/elapsed, 1e20)
			if delta[2] > 0 && item.ReadIops != nil {
				item.ReadAwaitMs = bounded(float64(delta[4])/float64(delta[2]), 1e20)
			}
			if delta[3] > 0 && item.WriteIops != nil {
				item.WriteAwaitMs = bounded(float64(delta[5])/float64(delta[3]), 1e20)
			}
			if (delta[2] > 0 || delta[3] > 0) && item.ReadIops != nil && item.WriteIops != nil {
				item.AwaitMs = bounded((float64(delta[4])+float64(delta[5]))/(float64(delta[2])+float64(delta[3])), 1e20)
			}
			item.UtilPercent = bounded(float64(delta[6])/(elapsed*10), 100)
			if partial {
				item.Quality = []string{"partial"}
			}
		}
		samples = append(samples, item)
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	t.counters = counters
	t.boot = boot
	t.previous = at
	return aggregateDiskIO(samples, partial), nil
}

// Each field has its own validity. Idle devices contribute zero rates, but no
// latency weight. Invalid devices never poison valid measurements or become zero.
func aggregateDiskIO(samples []*v1.TelemetryDiskIO, partial bool) *v1.TelemetryDiskIO {
	out := &v1.TelemetryDiskIO{Quality: []string{}}
	sum := func(dst **float64, value *float64) {
		if value == nil {
			partial = true
			return
		}
		if *dst == nil {
			v := 0.0
			*dst = &v
		}
		**dst += *value
	}
	var readTime, writeTime, totalTime, reads, writes, total float64
	for _, d := range samples {
		if len(d.Quality) > 0 {
			partial = true
		}
		sum(&out.ReadBytesPerSecond, d.ReadBytesPerSecond)
		sum(&out.WriteBytesPerSecond, d.WriteBytesPerSecond)
		sum(&out.ReadIops, d.ReadIops)
		sum(&out.WriteIops, d.WriteIops)
		if d.UtilPercent != nil {
			if out.UtilPercent == nil || *d.UtilPercent > *out.UtilPercent {
				v := *d.UtilPercent
				out.UtilPercent = &v
			}
		} else {
			partial = true
		}
		if d.ReadAwaitMs != nil && d.ReadIops != nil && *d.ReadIops > 0 {
			reads += *d.ReadIops
			readTime += *d.ReadAwaitMs * *d.ReadIops
		}
		if d.WriteAwaitMs != nil && d.WriteIops != nil && *d.WriteIops > 0 {
			writes += *d.WriteIops
			writeTime += *d.WriteAwaitMs * *d.WriteIops
		}
		if d.AwaitMs != nil && d.ReadIops != nil && d.WriteIops != nil {
			weight := *d.ReadIops + *d.WriteIops
			total += weight
			totalTime += *d.AwaitMs * weight
		}
	}
	if reads > 0 {
		v := readTime / reads
		out.ReadAwaitMs = &v
	}
	if writes > 0 {
		v := writeTime / writes
		out.WriteAwaitMs = &v
	}
	if total > 0 {
		v := totalTime / total
		out.AwaitMs = &v
	}
	for _, value := range []**float64{&out.ReadBytesPerSecond, &out.WriteBytesPerSecond, &out.ReadIops, &out.WriteIops} {
		if *value != nil && **value > 1e20 {
			*value = nil
			partial = true
		}
	}
	// A dropped aggregate weight must not leave a contradictory latency value.
	if out.ReadIops == nil {
		out.ReadAwaitMs = nil
	}
	if out.WriteIops == nil {
		out.WriteAwaitMs = nil
	}
	if out.ReadIops == nil || out.WriteIops == nil {
		out.AwaitMs = nil
	}

	if out.ReadBytesPerSecond == nil && out.WriteBytesPerSecond == nil && out.ReadIops == nil && out.WriteIops == nil && out.UtilPercent == nil {
		out.Quality = []string{"unknown"}
	} else if partial {
		out.Quality = []string{"partial"}
	}
	return out
}
