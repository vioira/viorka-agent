package state

import (
	"context"
	"errors"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTrafficCalibrationDurableIdempotencyAndCycleChange(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	l, err := OpenTrafficLedger(store, "https://panel.test", "node")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	boot := "11111111-1111-4111-8111-111111111111"
	filter := strings.Repeat("a", 64)
	if _, err = l.Observe(at, boot, filter, trafficTestInterface(100, 200)); err != nil {
		t.Fatal(err)
	}
	c := v1.TrafficCalibration{PeriodStart: trafficMonth(at), RXBytes: "1000", TXBytes: "2000"}
	original := l.write
	l.write = func(string, any) error { return errors.New("disk unavailable") }
	if l.Calibrate(context.Background(), "once", c, at) == nil {
		t.Fatal("write failure hidden")
	}
	l.write = original
	if _, err = l.Observe(at.Add(time.Second), boot, filter, trafficTestInterface(110, 220)); err != nil {
		t.Fatal(err)
	}
	if err = l.Calibrate(context.Background(), "once", c, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenTrafficLedger(store, "https://panel.test", "node")
	if err != nil {
		t.Fatal(err)
	}
	if err = restored.Calibrate(context.Background(), "once", c, at.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := restored.Snapshot()
	if snapshot.RX != 1010 || snapshot.TX != 2020 {
		t.Fatal("retry reset incremental traffic", snapshot)
	}
	conflict := c
	conflict.RXBytes = "999"
	if restored.Calibrate(context.Background(), "once", conflict, at.Add(3*time.Second)) == nil {
		t.Fatal("conflict accepted")
	}
	settings := v1.DefaultTrafficSettings()
	settings.ResetDay = 15
	if err = restored.Configure(settings); err != nil {
		t.Fatal(err)
	}
	p := restored.period(at)
	if !p.start.Equal(c.PeriodStart) || p.end.Day() != 1 || p.end.Month() != 12 {
		t.Fatal("active cycle reset", p)
	}
	next := time.Date(2026, 12, 1, 0, 0, 1, 0, time.UTC)
	v, err := restored.Observe(next, boot, filter, trafficTestInterface(500, 900))
	if err != nil || *v.RXBytes != 0 || v.PeriodEnd.Day() != 15 {
		t.Fatal(v, err)
	}
	if restored.Calibrate(context.Background(), "old", c, next) == nil {
		t.Fatal("expired calibration accepted")
	}
}
