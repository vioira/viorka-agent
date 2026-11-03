package collector

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

// Counters: sectors read/written, completed reads/writes, read/write ms, busy ms.
type diskCounters [7]uint64
type TelemetryDisk struct {
	source   *Metrics
	previous time.Time
	boot     string
	counters map[string]diskCounters
}

func NewTelemetryDisk() *TelemetryDisk { return &TelemetryDisk{source: NewMetrics()} }
func parseTelemetryDisk(data string) (map[string]diskCounters, error) {
	counters := map[string]diskCounters{}
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if len(f) < 14 || len(f[2]) == 0 || len(f[2]) > 64 {
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
func (t *TelemetryDisk) Sample(ctx context.Context, at time.Time, boot string) (out []*v1.TelemetryDiskIO, err error) {
	out = []*v1.TelemetryDiskIO{}
	defer func() {
		if err != nil {
			t.counters = nil
			t.previous = time.Time{}
			out = []*v1.TelemetryDiskIO{}
		}
	}()
	if at.IsZero() || !validBootID(boot) {
		return out, fmt.Errorf("missing disk sample identity")
	}
	data, err := t.source.read(ctx, "diskstats")
	if err != nil {
		return out, err
	}
	counters, err := parseTelemetryDisk(data)
	if err != nil {
		return out, err
	}
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
		item := &v1.TelemetryDiskIO{Device: name, ReadBytes: v[0] * 512, WriteBytes: v[1] * 512, Quality: []string{}}
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
		out = append(out, item)
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	t.counters = counters
	t.boot = boot
	t.previous = at
	return out, nil
}
