package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	managed "github.com/vioira/viorka-agent/internal/runtime"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

type engineEntry struct {
	Stage      string       `json:"stage,omitempty"`
	TaskID     string       `json:"task_id"`
	ResourceID string       `json:"resource_id"`
	Operation  string       `json:"operation"`
	Hash       string       `json:"hash"`
	Deadline   time.Time    `json:"deadline"`
	Reply      v1.TaskReply `json:"reply"`
}

// ExecuteEngine journals intent before touching the runtime. An interrupted
// operation is never replayed blindly: it reports unknown for reconciliation.
func (e *Executor) ExecuteEngine(ctx context.Context, d v1.TaskDispatch) (reply v1.TaskReply, retErr error) {
	if d.Engine == nil || d.Engine.Validate(d.Operation) != nil || d.ResourceKind != "engine" || d.ResourceID == "" || len(d.ResourceID) > 128 || d.Attempt == nil || *d.Attempt == 0 || len(d.TaskID) < 1 || len(d.TaskID) > 128 || len(d.IdempotencyKey) < 1 || len(d.IdempotencyKey) > 128 || d.TrafficCalibration != nil || d.DeadlineAt.IsZero() || d.DeadlineAt.After(time.Now().Add(10*time.Minute)) {
		return v1.TaskReply{}, fmt.Errorf("invalid engine dispatch")
	}
	logger := e.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	logger = logger.With("task_id", d.TaskID, "resource_id", d.ResourceID, "operation", d.Operation, "attempt", *d.Attempt, "engine", d.Engine.Name, "version", d.Engine.Version, "revision", d.Engine.Revision)
	started := time.Now()
	stage := "digest"
	logger.Info("Engine task started")
	defer func() {
		if retErr != nil {
			logger.Error("Engine task failed", "stage", stage, "error", retErr, "duration", time.Since(started))
		} else if reply.ErrorCode != "" {
			logger.Error("Engine task failed", "stage", stage, "error_code", reply.ErrorCode, "duration", time.Since(started))
		} else {
			logger.Info("Engine task completed", "state", reply.State, "duration", time.Since(started))
		}
	}()
	data, err := json.Marshal(d.Engine)
	if err != nil {
		return v1.TaskReply{}, err
	}
	digest := sha256.Sum256(data)
	hash := hex.EncodeToString(digest[:])
	if d.ConfigSHA256 != hash {
		return v1.TaskReply{}, fmt.Errorf("engine digest mismatch")
	}
	stage = "journal_read"
	journal := map[string]engineEntry{}
	if err = e.store.Read("engine-tasks.json", &journal); err != nil && !errors.Is(err, os.ErrNotExist) {
		return v1.TaskReply{}, err
	}
	if old, ok := journal[d.IdempotencyKey]; ok {
		if old.TaskID != d.TaskID || old.ResourceID != d.ResourceID || old.Operation != d.Operation || old.Hash != hash || !old.Deadline.Equal(d.DeadlineAt) {
			return v1.TaskReply{}, fmt.Errorf("engine idempotency conflict")
		}
		stage = "journal_replay"
		logger.Debug("Engine task journal replay", "previous_state", old.Reply.State)
		r := old.Reply
		r.Attempt = *d.Attempt
		if r.State == "running" {
			logger.Warn("Engine task interrupted; refusing to repeat side effects")
			r.State = "failed"
			r.ErrorCode = "engine_result_unknown"
			lastStage := old.Stage
			if lastStage == "" {
				lastStage = "prepare"
			}
			r.Engine = &v1.EngineResult{Diagnostic: &v1.EngineDiagnostic{Stage: lastStage, Message: "上次执行异常中断，未重复执行有副作用的操作", Interrupted: true}, Name: d.Engine.Name, Version: d.Engine.Version, Revision: d.Engine.Revision, Status: "unknown", ErrorCode: r.ErrorCode}
		}
		return r, nil
	}
	for key, old := range journal {
		if time.Since(old.Deadline) > 24*time.Hour {
			delete(journal, key)
		}
	}
	if len(journal) >= 1000 {
		return v1.TaskReply{}, fmt.Errorf("engine journal full")
	}
	reply = v1.TaskReply{TaskID: d.TaskID, ResourceID: d.ResourceID, Attempt: *d.Attempt, State: "running"}
	item := engineEntry{Stage: "prepare", TaskID: d.TaskID, ResourceID: d.ResourceID, Operation: d.Operation, Hash: hash, Deadline: d.DeadlineAt, Reply: reply}
	journal[d.IdempotencyKey] = item
	stage = "journal_intent"
	if err = e.store.Write("engine-tasks.json", journal); err != nil {
		return v1.TaskReply{}, err
	}
	logger.Debug("Engine task intent persisted")
	stage = "runtime"
	bounded, cancel := context.WithDeadline(ctx, d.DeadlineAt)
	defer cancel()
	bounded, diagnostics := managed.TrackDiagnostics(bounded, func(next string) error {
		item.Stage = next
		journal[d.IdempotencyKey] = item
		return e.store.Write("engine-tasks.json", journal)
	})
	var result v1.EngineResult
	if bounded.Err() != nil {
		result = v1.EngineResult{Name: d.Engine.Name, Version: d.Engine.Version, Revision: d.Engine.Revision, Status: "unknown", ErrorCode: "deadline_exceeded"}
	} else {
		result, err = managed.NewManaged(logger).Execute(bounded, d.ResourceID, d.Operation, *d.Engine)
		if err != nil {
			return v1.TaskReply{}, err
		}
	}
	reply.State = "succeeded"
	reply.Engine = &result
	if result.ErrorCode != "" {
		reply.State = "failed"
		reply.ErrorCode = result.ErrorCode
		result.Diagnostic = diagnostics.Result(result.ErrorCode)
	}
	stage = "journal_result"
	item.Reply = reply
	journal[d.IdempotencyKey] = item
	if err = e.store.Write("engine-tasks.json", journal); err != nil {
		return v1.TaskReply{}, err
	}
	logger.Debug("Engine task result persisted", "state", reply.State)
	return reply, nil
}
