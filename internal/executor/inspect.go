package executor

import (
	"errors"
	"fmt"
	"github.com/vioira/viorka-agent/internal/state"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
	"os"
	"runtime"
	"time"
)

type entry struct {
	TaskID   string       `json:"task_id"`
	Deadline time.Time    `json:"deadline"`
	Reply    v1.TaskReply `json:"reply"`
}
type Executor struct {
	store   *state.Store
	node    string
	journal map[string]entry
}

func New(s *state.Store, node string) (*Executor, error) {
	e := &Executor{store: s, node: node, journal: map[string]entry{}}
	err := s.Read("tasks.json", &e.journal)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(e.journal) > 1000 {
		return nil, fmt.Errorf("journal capacity exceeded")
	}
	return e, nil
}
func (e *Executor) Execute(d v1.TaskDispatch) (v1.TaskReply, error) {
	if d.Operation != "node.inspect" || d.ResourceKind != "node" || d.ResourceID != e.node || d.Attempt == nil || *d.Attempt == 0 || len(d.TaskID) < 1 || len(d.TaskID) > 128 || len(d.IdempotencyKey) < 1 || len(d.IdempotencyKey) > 128 || d.ConfigSHA256 != "" || d.DeadlineAt.IsZero() || d.DeadlineAt.After(time.Now().Add(10*time.Minute)) {
		return v1.TaskReply{}, fmt.Errorf("unsupported or invalid task")
	}
	if old, ok := e.journal[d.IdempotencyKey]; ok {
		if old.TaskID != d.TaskID || !old.Deadline.Equal(d.DeadlineAt) {
			return v1.TaskReply{}, fmt.Errorf("idempotency conflict")
		}
		r := old.Reply
		r.Attempt = *d.Attempt
		return r, nil
	}
	for key, v := range e.journal {
		if time.Since(v.Deadline) > 24*time.Hour {
			delete(e.journal, key)
		}
	}
	if len(e.journal) >= 1000 {
		return v1.TaskReply{}, fmt.Errorf("journal full")
	}
	r := v1.TaskReply{TaskID: d.TaskID, Attempt: *d.Attempt, ResourceID: e.node, State: "succeeded"}
	if time.Now().After(d.DeadlineAt) {
		r.State = "failed"
		r.ErrorCode = "deadline_exceeded"
	} else {
		host, err := os.Hostname()
		if err != nil {
			r.State = "failed"
			r.ErrorCode = "inspection_failed"
		} else {
			r.Inspection = &v1.Inspection{OS: runtime.GOOS, Arch: runtime.GOARCH, Hostname: host}
		}
	}
	// node.inspect has no side effects. Persist its real result before sending it.
	e.journal[d.IdempotencyKey] = entry{d.TaskID, d.DeadlineAt, r}
	if err := e.store.Write("tasks.json", e.journal); err != nil {
		delete(e.journal, d.IdempotencyKey)
		return v1.TaskReply{}, err
	}
	return r, nil
}
