package executor

import (
	"context"
	"github.com/vioira/viorka-agent/internal/state"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestJournalSurvivesRestartAndRejectsConflicts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	s, err := state.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(s, "node")
	if err != nil {
		t.Fatal(err)
	}
	attempt := uint64(1)
	d := v1.TaskDispatch{TaskID: "task", Operation: "node.inspect", IdempotencyKey: "key", ResourceKind: "node", ResourceID: "node", Attempt: &attempt, DeadlineAt: time.Now().UTC().Add(time.Minute)}
	r, err := e.Execute(d)
	if err != nil || r.State != "succeeded" || r.Inspection.Hostname == "" {
		t.Fatal(r, err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "tasks.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = state.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e, err = New(s, "node")
	if err != nil {
		t.Fatal(err)
	}
	attempt = 2
	again, err := e.Execute(d)
	if err != nil || again.Inspection.Hostname != r.Inspection.Hostname || again.Attempt != 2 {
		t.Fatal(again, err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "tasks.json"))
	if string(before) != string(after) {
		t.Fatal("duplicate rewrote journal")
	}
	d.TaskID = "different"
	if _, err = e.Execute(d); err == nil {
		t.Fatal("conflicting key accepted")
	}
	d.Operation = "shell.exec"
	if _, err = e.Execute(d); err == nil {
		t.Fatal("arbitrary shell accepted")
	}
}

func TestCalibrationJournalIntentAndTerminalReplay(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e, err := New(store, "node")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	attempt := uint64(1)
	d := v1.TaskDispatch{TaskID: "calibration", Operation: "traffic.calibrate", IdempotencyKey: "once", ResourceKind: "node", ResourceID: "node", Attempt: &attempt, DeadlineAt: now.Add(time.Minute), TrafficCalibration: &v1.TrafficCalibration{PeriodStart: now.Add(-time.Hour), RXBytes: "10", TXBytes: "20"}}
	e.Calibrate = func(_ context.Context, key string, c v1.TrafficCalibration, _ time.Time) (*v1.TrafficCalibration, error) {
		var disk map[string]entry
		if err := store.Read("tasks.json", &disk); err != nil {
			return nil, err
		}
		if disk[key].Reply.State != "running" {
			t.Fatal("mutation before durable intent")
		}
		return &v1.TrafficCalibration{PeriodStart: d.TrafficCalibration.PeriodStart, RXBytes: "0", TXBytes: "0"}, nil
	}
	reply, err := e.Execute(d)
	if err != nil || reply.State != "succeeded" || reply.TrafficCalibration == nil {
		t.Fatal(reply, err)
	}
	restarted, err := New(store, "node")
	if err != nil {
		t.Fatal(err)
	}
	restarted.Calibrate = func(context.Context, string, v1.TrafficCalibration, time.Time) (*v1.TrafficCalibration, error) {
		t.Fatal("completed calibration executed twice")
		return &v1.TrafficCalibration{PeriodStart: d.TrafficCalibration.PeriodStart, RXBytes: "0", TXBytes: "0"}, nil
	}
	attempt = 2
	reply, err = restarted.Execute(d)
	if err != nil || reply.Attempt != 2 || reply.State != "succeeded" {
		t.Fatal(reply, err)
	}
	changed := *d.TrafficCalibration
	changed.RXBytes = "11"
	d.TrafficCalibration = &changed
	if _, err = restarted.Execute(d); err == nil {
		t.Fatal("changed payload reused idempotency key")
	}
}
