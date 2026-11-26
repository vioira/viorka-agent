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

func TestDailyTrafficBoundaryRestartCalibrationAndAck(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ledger, err := OpenTrafficLedger(store, "https://panel.test", "node")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 31, 23, 59, 50, 0, time.UTC)
	observe := func(at time.Time, rx, tx uint64) {
		t.Helper()
		if _, err := ledger.Observe(at, "11111111-1111-4111-8111-111111111111", strings.Repeat("a", 64), trafficTestInterface(rx, tx)); err != nil {
			t.Fatal(err)
		}
	}
	observe(at, 100, 200)
	observe(at.Add(20*time.Second), 300, 600)
	snapshot, _ := ledger.Snapshot()
	if len(snapshot.Daily) != 2 || snapshot.Daily[0].Quality != v1.DailyTrafficBoundaryEstimated || snapshot.Daily[0].RXBytes != "100" || snapshot.Daily[1].RXBytes != "100" || snapshot.Daily[1].TXBytes != "200" {
		t.Fatalf("bad split: %+v", snapshot.Daily)
	}
	if err = ledger.Save(context.Background(), at.Add(20*time.Second), true); err != nil {
		t.Fatal(err)
	}
	ledger, err = OpenTrafficLedger(store, "https://panel.test", "node")
	if err != nil {
		t.Fatal(err)
	}
	if err = ledger.Calibrate(context.Background(), "calibration", v1.TrafficCalibration{PeriodStart: snapshot.MonthStart, RXBytes: "9000", TXBytes: "8000"}, at.Add(21*time.Second)); err != nil {
		t.Fatal(err)
	}
	observe(at.Add(30*time.Second), 400, 800)
	snapshot, _ = ledger.Snapshot()
	if snapshot.Daily[1].RXBytes != "200" || snapshot.Daily[1].TXBytes != "400" {
		t.Fatal("calibration changed daily totals")
	}
	ended := snapshot.Daily[0]
	ledger.AcknowledgeDaily([]*v1.TelemetryDailyTrafficAck{{DayStart: ended.DayStart, Revision: ended.Revision - 1}})
	snapshot, _ = ledger.Snapshot()
	if len(snapshot.Daily) != 2 {
		t.Fatal("stale acknowledgement deleted pending day")
	}
	ledger.AcknowledgeDaily([]*v1.TelemetryDailyTrafficAck{{DayStart: ended.DayStart, Revision: ended.Revision}})
	if err = ledger.Save(context.Background(), at.Add(31*time.Second), true); err != nil {
		t.Fatal(err)
	}
	ledger, err = OpenTrafficLedger(store, "https://panel.test", "node")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ = ledger.Snapshot()
	if len(snapshot.Daily) != 1 || snapshot.Daily[0].RXBytes != "200" {
		t.Fatal("acknowledgement restart", snapshot.Daily)
	}
}

func TestDailyTrafficTimezoneAndDST(t *testing.T) {
	for _, tc := range []struct {
		date  string
		hours time.Duration
	}{{"2026-03-08T12:00:00Z", 23}, {"2026-11-01T12:00:00Z", 25}} {
		at, _ := time.Parse(time.RFC3339, tc.date)
		start, end := dailyInterval(at, "America/New_York")
		if end.Sub(start) != tc.hours*time.Hour {
			t.Fatal(start, end)
		}
	}
	l := trafficTestLedger(t)
	at := time.Date(2026, 11, 12, 23, 59, 50, 0, time.UTC)
	boot := "11111111-1111-4111-8111-111111111111"
	filter := strings.Repeat("a", 64)
	if _, err := l.Observe(at, boot, filter, trafficTestInterface(100, 200)); err != nil {
		t.Fatal(err)
	}
	settings := v1.DefaultTrafficSettings()
	settings.Timezone = "Asia/Shanghai"
	if err := l.Configure(settings); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Observe(at.Add(20*time.Second), boot, filter, trafficTestInterface(300, 600)); err != nil {
		t.Fatal(err)
	}
	snap, _ := l.Snapshot()
	if len(snap.Daily) != 2 || !snap.Daily[0].DayEnd.Equal(snap.Daily[1].DayStart) || snap.Daily[1].DayEnd.Sub(snap.Daily[1].DayStart) != 16*time.Hour {
		t.Fatal(snap.Daily)
	}
}

func TestDailyReportsLatestObservationAcrossWriteFailureAndRestart(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	l, err := OpenTrafficLedger(store, "https://panel.test", "node")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	observe := func(seconds int) {
		t.Helper()
		_, err := l.Observe(start.Add(time.Duration(seconds)*time.Second), "11111111-1111-4111-8111-111111111111", strings.Repeat("a", 64), trafficTestInterface(uint64(seconds), 0))
		if err != nil {
			t.Fatal(err)
		}
	}
	daily := func(want string) *v1.TelemetryDailyTraffic {
		t.Helper()
		records, err := l.PendingDaily()
		if err != nil || len(records) != 1 || records[0].RXBytes != want {
			t.Fatalf("want RX=%s, records=%v err=%v", want, records, err)
		}
		return records[0]
	}
	observe(0)
	daily("0")
	if err := l.Save(ctx, start, false); err != nil {
		t.Fatal(err)
	}
	observe(60)
	writeErr := errors.New("disk full")
	l.write = func(string, any) error { return writeErr }
	if err := l.Save(ctx, start.Add(time.Minute), false); !errors.Is(err, writeErr) {
		t.Fatal(err)
	}
	reported := daily("60")
	observe(62)
	if daily("62").Revision <= reported.Revision || reported.RXBytes != "60" {
		t.Fatal("new observation did not produce a separate, newer report")
	}
	last := daily("62")
	l, err = OpenTrafficLedger(store, "https://panel.test", "node")
	if err != nil {
		t.Fatal(err)
	}
	daily("0")
	observe(65)
	if daily("65").Revision <= last.Revision {
		t.Fatal("restoring an older checkpoint reused a reported revision")
	}
}
