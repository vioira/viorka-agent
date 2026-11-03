package collector

import (
	"context"
	"fmt"
	"math"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/vioira/viorka-agent/internal/config"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

type NetworkObservation struct {
	BootID         string
	FilterRevision string
	Interfaces     []*v1.TelemetryInterface
}
type interfaceBaseline struct {
	index    int
	counters [2]uint64
}

// TelemetryNetwork has a single sampling owner. Mode and period changes do not
// rebuild this object: every rate uses the actual interval between observations.
type TelemetryNetwork struct {
	source         *Metrics
	interfaces     func() ([]net.Interface, error)
	filter         config.NetworkFilter
	revision, boot string
	previous       time.Time
	baseline       map[string]interfaceBaseline
}

func NewTelemetryNetwork(filter config.NetworkFilter) (*TelemetryNetwork, error) {
	revision, err := filter.Revision()
	if err != nil {
		return nil, err
	}
	filter.Include = append([]string(nil), filter.Include...)
	filter.Exclude = append([]string(nil), filter.Exclude...)
	return &TelemetryNetwork{source: NewMetrics(), interfaces: net.Interfaces, filter: filter, revision: revision}, nil
}
func validBootID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}
func (t *TelemetryNetwork) Sample(ctx context.Context, at time.Time) (result NetworkObservation, err error) {
	result = NetworkObservation{FilterRevision: t.revision, Interfaces: []*v1.TelemetryInterface{}}
	defer func() {
		if err != nil {
			t.baseline = nil
			t.previous = time.Time{}
			result.Interfaces = []*v1.TelemetryInterface{}
		}
	}()
	if at.IsZero() {
		return result, fmt.Errorf("missing network sample time")
	}
	boot, e := t.source.read(ctx, "sys/kernel/random/boot_id")
	if e != nil {
		return result, e
	}
	boot = strings.TrimSpace(boot)
	if !validBootID(boot) {
		return result, fmt.Errorf("invalid boot identity")
	}
	result.BootID = boot
	data, e := t.source.read(ctx, "net/dev")
	if e != nil {
		return result, e
	}
	counters, e := parseNetworkCounters(data, true)
	if e != nil {
		return result, e
	}
	interfaces, e := t.interfaces()
	if e != nil {
		return result, e
	}
	if len(interfaces) > 4096 {
		return result, fmt.Errorf("interface enumeration limit exceeded")
	}
	indices := map[string]int{}
	seen := map[int]bool{}
	for _, i := range interfaces {
		if !t.filter.Includes(i.Name) {
			continue
		}
		if i.Index <= 0 || uint64(i.Index) > math.MaxUint32 || indices[i.Name] != 0 || seen[i.Index] {
			return result, fmt.Errorf("invalid interface identity")
		}
		indices[i.Name] = i.Index
		seen[i.Index] = true
		if _, ok := counters[i.Name]; !ok {
			return result, fmt.Errorf("selected interface counters missing")
		}
	}
	names := []string{}
	for name := range counters {
		if t.filter.Includes(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) > 64 {
		return result, fmt.Errorf("selected interface limit exceeded")
	}
	elapsed := at.Sub(t.previous).Seconds()
	valid := !t.previous.IsZero() && elapsed > 0 && elapsed <= 600
	next := map[string]interfaceBaseline{}
	var totalRX, totalTX uint64
	for _, name := range names {
		index := indices[name]
		v := counters[name]
		if index == 0 || strings.Contains(name, "*") || (config.NetworkFilter{Include: []string{name}}).Validate() != nil || v[0] > math.MaxInt64-totalRX || v[1] > math.MaxInt64-totalTX {
			return result, fmt.Errorf("invalid selected interface counters")
		}
		totalRX += v[0]
		totalTX += v[1]
		item := &v1.TelemetryInterface{Name: name, Ifindex: uint64(index), RXBytes: v[0], TXBytes: v[1], Quality: []string{}}
		old, exists := t.baseline[name]
		switch {
		case exists && (old.index != index || t.boot != boot):
			item.Quality = []string{"identity_changed"}
		case !exists || !valid:
			item.Quality = []string{"baseline"}
		case v[0] < old.counters[0] || v[1] < old.counters[1]:
			item.Quality = []string{"counter_reset"}
		default:
			rx, tx := float64(v[0]-old.counters[0])/elapsed, float64(v[1]-old.counters[1])/elapsed
			if rx > 1e20 || tx > 1e20 {
				return result, fmt.Errorf("network rate exceeds protocol range")
			}
			item.RXBytesPerSecond = &rx
			item.TXBytesPerSecond = &tx
		}
		result.Interfaces = append(result.Interfaces, item)
		next[name] = interfaceBaseline{index: index, counters: v}
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	t.baseline = next
	t.previous = at
	t.boot = boot
	return result, nil
}
