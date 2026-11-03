package state

import (
	"math"
	"slices"
	"strings"
	"testing"
	"time"
)

func trafficTestLedger(t *testing.T) *TrafficLedger {
	t.Helper()
	l, err := NewTrafficLedger("https://panel.test", "node")
	if err != nil {
		t.Fatal(err)
	}
	return l
}
func trafficTestInterface(rx, tx uint64) []TrafficInterface {
	return []TrafficInterface{{Name: "eth0", Ifindex: 2, RX: rx, TX: tx}}
}
func TestTrafficRestartIdempotenceAndIdentity(t *testing.T) {
	l := trafficTestLedger(t)
	at := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	boot := "11111111-1111-4111-8111-111111111111"
	filter := strings.Repeat("a", 64)
	v, err := l.Observe(at, boot, filter, trafficTestInterface(1000, 2000))
	if err != nil || *v.RXBytes != 0 || !slices.Contains(v.Quality, "partial") {
		t.Fatal(v, err)
	}
	v, err = l.Observe(at.Add(time.Minute), boot, filter, trafficTestInterface(1100, 2200))
	if err != nil || *v.RXBytes != 100 || *v.TXBytes != 200 {
		t.Fatal(v, err)
	}
	saved, ok := l.Snapshot()
	if !ok {
		t.Fatal("missing snapshot")
	}
	restored := trafficTestLedger(t)
	if err = restored.Restore(saved); err != nil {
		t.Fatal(err)
	}
	saved.Interfaces[0].RX = 0 // Restored ledger owns its baseline.
	v, err = restored.Observe(at.Add(2*time.Minute), boot, filter, trafficTestInterface(1150, 2300))
	if err != nil || *v.RXBytes != 150 || *v.TXBytes != 300 {
		t.Fatal(v, err)
	}
	v, err = restored.Observe(at.Add(3*time.Minute), boot, filter, trafficTestInterface(1150, 2300))
	if err != nil || *v.RXBytes != 150 {
		t.Fatal("duplicate counters accumulated", v, err)
	}
	before, _ := restored.Snapshot()
	if _, err = restored.Observe(at, boot, filter, trafficTestInterface(2000, 4000)); err == nil {
		t.Fatal("old time accepted")
	}
	after, _ := restored.Snapshot()
	if after.RX != before.RX || !after.SampledAt.Equal(before.SampledAt) {
		t.Fatal("old observation mutated state")
	}
	boot = "22222222-2222-4222-8222-222222222222"
	v, err = restored.Observe(at.Add(4*time.Minute), boot, filter, trafficTestInterface(3000, 4000))
	if err != nil || *v.RXBytes != 150 || !slices.Contains(v.Quality, "identity_changed") {
		t.Fatal(v, err)
	}
	v, err = restored.Observe(at.Add(5*time.Minute), boot, filter, trafficTestInterface(2, 4))
	if err != nil || *v.RXBytes != 150 || !slices.Contains(v.Quality, "counter_reset") {
		t.Fatal(v, err)
	}
}
func TestTrafficMonthBoundaryAndLongGap(t *testing.T) {
	boot := "11111111-1111-4111-8111-111111111111"
	filter := strings.Repeat("a", 64)
	for _, months := range []int{1, 3} {
		l := trafficTestLedger(t)
		at := time.Date(2026, 10, 31, 23, 59, 30, 0, time.UTC)
		if _, err := l.Observe(at, boot, filter, trafficTestInterface(100, 200)); err != nil {
			t.Fatal(err)
		}
		next := time.Date(2026, time.Month(10+months), 1, 0, 0, 30, 0, time.UTC)
		v, err := l.Observe(next, boot, filter, trafficTestInterface(200, 400))
		if err != nil {
			t.Fatal(err)
		}
		if months == 1 {
			if *v.RXBytes != 100 || !slices.Contains(v.Quality, "boundary_estimated") {
				t.Fatal(v)
			}
		} else if *v.RXBytes != 0 || !slices.Contains(v.Quality, "period_gap") {
			t.Fatal("multi-month delta charged", v)
		}
		if !v.MonthStart.Equal(trafficMonth(next)) {
			t.Fatal(v)
		}
	}
}
func TestTrafficFilterTransitionAndOverflow(t *testing.T) {
	l := trafficTestLedger(t)
	at := time.Now().UTC()
	boot := "11111111-1111-4111-8111-111111111111"
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	if _, err := l.Observe(at, boot, a, trafficTestInterface(100, 200)); err != nil {
		t.Fatal(err)
	}
	interfaces := append(trafficTestInterface(110, 220), TrafficInterface{Name: "wg0", Ifindex: 3, RX: 100000, TX: 200000})
	v, err := l.Observe(at.Add(time.Second), boot, b, interfaces)
	if err != nil || *v.RXBytes != 10 || !slices.Contains(v.Quality, "mixed_filter") {
		t.Fatal("new interface charged history", v, err)
	}
	saved, _ := l.Snapshot()
	saved.RX = math.MaxInt64
	restored := trafficTestLedger(t)
	if err = restored.Restore(saved); err != nil {
		t.Fatal(err)
	}
	interfaces[0].RX++
	v, err = restored.Observe(at.Add(2*time.Second), boot, b, interfaces)
	if err == nil || v.RXBytes != nil || !slices.Contains(v.Quality, "unknown") {
		t.Fatal("overflow not unknown", v, err)
	}
	if _, ok := restored.Snapshot(); ok {
		t.Fatal("overflow ledger may be persisted")
	}
	corrupt := trafficTestLedger(t)
	saved.NodeID = "other"
	if err = corrupt.Restore(saved); err == nil {
		t.Fatal("cross identity restored")
	}
	v, err = corrupt.Observe(at.Add(2*time.Second), boot, b, interfaces)
	if err == nil || v.RXBytes != nil {
		t.Fatal("corrupt state treated as new empty ledger", v, err)
	}
}
