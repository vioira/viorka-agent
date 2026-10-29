package executor

import (
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
