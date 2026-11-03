package state

import (
	"fmt"
	"math"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

type TrafficInterface struct {
	Name    string `json:"name"`
	Ifindex uint64 `json:"ifindex"`
	RX      uint64 `json:"rx_bytes"`
	TX      uint64 `json:"tx_bytes"`
}
type TrafficSnapshot struct {
	FormatVersion  uint64             `json:"format_version"`
	PanelURL       string             `json:"panel_url"`
	NodeID         string             `json:"node_id"`
	MonthStart     time.Time          `json:"month_start"`
	BootID         string             `json:"boot_id"`
	FilterRevision string             `json:"filter_revision"`
	Interfaces     []TrafficInterface `json:"interfaces"`
	RX             uint64             `json:"rx_bytes"`
	TX             uint64             `json:"tx_bytes"`
	SampledAt      time.Time          `json:"sampled_at"`
	SavedAt        time.Time          `json:"saved_at"`
	Quality        []string           `json:"quality"`
}

// TrafficLedger owns only month accounting, not instantaneous rate baselines.
// Observations serialize under a short lock. Persistence copies one coherent
// snapshot and performs filesystem I/O outside the sampling lock.
type TrafficLedger struct {
	mu            sync.Mutex
	saveMu        sync.Mutex
	store         *Store
	write         func(string, any) error
	etag          string
	nextSave      time.Time
	dirty         bool
	panel, node   string
	snapshot      *TrafficSnapshot
	blocked       error
	persistFailed bool
}

func NewTrafficLedger(panel, node string) (*TrafficLedger, error) {
	u, err := url.Parse(panel)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || node == "" || len(node) > 128 {
		return nil, fmt.Errorf("invalid traffic identity")
	}
	return &TrafficLedger{panel: panel, node: node}, nil
}
func trafficMonth(at time.Time) time.Time {
	at = at.UTC()
	return time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
}
func validTrafficHex(s string, length int) bool {
	if len(s) != length {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}
func validTrafficBoot(s string) bool {
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
func validateTrafficInterfaces(interfaces []TrafficInterface) error {
	if interfaces == nil || len(interfaces) > 64 {
		return fmt.Errorf("invalid traffic interface count")
	}
	names := map[string]bool{}
	indices := map[uint64]bool{}
	var rx, tx uint64
	for _, i := range interfaces {
		if i.Name == "" || len(i.Name) > 64 || names[i.Name] || i.Ifindex == 0 || i.Ifindex > math.MaxUint32 || indices[i.Ifindex] || i.RX > math.MaxInt64-rx || i.TX > math.MaxInt64-tx {
			return fmt.Errorf("invalid traffic interface")
		}
		for _, c := range i.Name {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.ContainsRune("_.:-", c)) {
				return fmt.Errorf("invalid traffic interface name")
			}
		}
		names[i.Name] = true
		indices[i.Ifindex] = true
		rx += i.RX
		tx += i.TX
	}
	return nil
}
func validateTrafficSnapshot(s TrafficSnapshot, panel, node string) error {
	if s.FormatVersion != 1 || s.PanelURL != panel || s.NodeID != node || s.SampledAt.IsZero() || s.SavedAt.IsZero() || s.SavedAt.Before(s.SampledAt) || !s.MonthStart.Equal(trafficMonth(s.SampledAt)) || !validTrafficBoot(s.BootID) || !validTrafficHex(s.FilterRevision, 64) || s.RX > math.MaxInt64 || s.TX > math.MaxInt64 {
		return fmt.Errorf("invalid traffic snapshot identity")
	}
	if err := validateTrafficInterfaces(s.Interfaces); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, q := range s.Quality {
		if seen[q] {
			return fmt.Errorf("duplicate traffic quality")
		}
		seen[q] = true
		switch q {
		case "partial", "counter_reset", "identity_changed", "period_gap", "boundary_estimated", "mixed_filter":
		default:
			return fmt.Errorf("invalid traffic quality")
		}
	}
	return nil
}
func cloneTraffic(s TrafficSnapshot) TrafficSnapshot {
	s.Interfaces = append([]TrafficInterface{}, s.Interfaces...)
	s.Quality = append([]string{}, s.Quality...)
	return s
}
func (l *TrafficLedger) Restore(s TrafficSnapshot) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := validateTrafficSnapshot(s, l.panel, l.node); err != nil {
		l.blocked = err
		return err
	}
	if l.snapshot != nil || l.blocked != nil {
		return fmt.Errorf("traffic restore requires a fresh ledger")
	}
	copy := cloneTraffic(s)
	l.snapshot = &copy
	return nil
}
func (l *TrafficLedger) Snapshot() (TrafficSnapshot, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.snapshot == nil || l.blocked != nil {
		return TrafficSnapshot{}, false
	}
	return cloneTraffic(*l.snapshot), true
}
func (l *TrafficLedger) unknown(at time.Time, filter string) *v1.TelemetryTraffic {
	return &v1.TelemetryTraffic{MonthStart: trafficMonth(at), FilterRevision: filter, Quality: []string{"unknown"}}
}

// Observe accepts complete, identity-validated interface counters. A failed
// network sample must not call Observe with an empty list as a substitute.
func (l *TrafficLedger) Observe(at time.Time, boot, filter string, interfaces []TrafficInterface) (*v1.TelemetryTraffic, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.blocked != nil {
		return l.unknown(at, filter), l.blocked
	}
	if at.IsZero() || !validTrafficBoot(boot) || !validTrafficHex(filter, 64) {
		return l.unknown(at, filter), fmt.Errorf("invalid traffic observation identity")
	}
	if err := validateTrafficInterfaces(interfaces); err != nil {
		return l.unknown(at, filter), err
	}
	next := TrafficSnapshot{FormatVersion: 1, PanelURL: l.panel, NodeID: l.node, MonthStart: trafficMonth(at), BootID: boot, FilterRevision: filter, Interfaces: append([]TrafficInterface{}, interfaces...), SampledAt: at.UTC(), SavedAt: at.UTC(), Quality: []string{}}
	quality := func(q string) {
		if !slices.Contains(next.Quality, q) {
			next.Quality = append(next.Quality, q)
		}
	}
	previous := l.snapshot
	if previous == nil {
		quality("partial")
	} else {
		if !at.After(previous.SampledAt) {
			return l.unknown(at, filter), fmt.Errorf("traffic observation time did not advance")
		}
		months := (next.MonthStart.Year()-previous.MonthStart.Year())*12 + int(next.MonthStart.Month()-previous.MonthStart.Month())
		if months < 0 {
			return l.unknown(at, filter), fmt.Errorf("traffic month moved backward")
		}
		if months == 0 {
			next.RX = previous.RX
			next.TX = previous.TX
			next.Quality = append([]string{}, previous.Quality...)
		}
		if months == 1 {
			quality("boundary_estimated")
		}
		if months > 1 {
			quality("period_gap")
		}
		if previous.FilterRevision != filter {
			quality("mixed_filter")
		}
		old := map[string]TrafficInterface{}
		for _, i := range previous.Interfaces {
			old[i.Name] = i
		}
		if boot != previous.BootID {
			quality("identity_changed")
		}
		for _, i := range interfaces {
			last, exists := old[i.Name]
			delete(old, i.Name)
			if months > 1 || boot != previous.BootID {
				continue
			}
			if !exists || last.Ifindex != i.Ifindex {
				quality("identity_changed")
				continue
			}
			if i.RX < last.RX || i.TX < last.TX {
				quality("counter_reset")
				continue
			}
			rx, tx := i.RX-last.RX, i.TX-last.TX
			if rx > math.MaxInt64-next.RX || tx > math.MaxInt64-next.TX {
				l.blocked = fmt.Errorf("monthly traffic counter overflow")
				return l.unknown(at, filter), l.blocked
			}
			next.RX += rx
			next.TX += tx
		}
		if len(old) > 0 {
			quality("partial")
		}
	}
	l.snapshot = &next
	l.dirty = true
	q := append([]string{}, next.Quality...)
	if l.persistFailed {
		q = append(q, "persist_failed")
	}
	rx, tx := next.RX, next.TX
	return &v1.TelemetryTraffic{MonthStart: next.MonthStart, FilterRevision: filter, RXBytes: &rx, TXBytes: &tx, Quality: q}, nil
}
