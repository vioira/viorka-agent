package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/vioira/viorka-agent/internal/state"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEngineJournalDoesNotReplayInterruptedEffects(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e, err := New(store, "node")
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	e.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	attempt := uint64(1)
	command := &v1.EngineCommand{Name: "realm", Version: v1.EngineVersion("realm"), Revision: 1, RunIntent: "running", Services: []*v1.EngineService{}}
	raw, _ := json.Marshal(command)
	hash := sha256.Sum256(raw)
	d := v1.TaskDispatch{TaskID: "task", Operation: "engine.install", ResourceKind: "engine", ResourceID: "engine", IdempotencyKey: "key", Attempt: &attempt, DeadlineAt: time.Now().Add(time.Minute), Engine: command, ConfigSHA256: hex.EncodeToString(hash[:])}
	entry := engineEntry{TaskID: d.TaskID, ResourceID: d.ResourceID, Operation: d.Operation, Hash: d.ConfigSHA256, Deadline: d.DeadlineAt, Reply: v1.TaskReply{TaskID: d.TaskID, ResourceID: d.ResourceID, State: "running", Attempt: 1}}
	if err = store.Write("engine-tasks.json", map[string]engineEntry{"key": entry}); err != nil {
		t.Fatal(err)
	}
	reply, err := e.ExecuteEngine(context.Background(), d)
	if err != nil || reply.State != "failed" || reply.ErrorCode != "engine_result_unknown" {
		t.Fatalf("%+v %v", reply, err)
	}
	for _, want := range []string{"task_id=task", "operation=engine.install", "attempt=1", "Engine task journal replay", "Engine task interrupted", "engine_result_unknown", "stage=journal_replay"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("missing %q: %s", want, logs.String())
		}
	}
	d.Engine.Revision++
	if _, err = e.ExecuteEngine(context.Background(), d); err == nil {
		t.Fatal("changed dispatch accepted")
	}
}
