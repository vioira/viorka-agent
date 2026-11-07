package executor

import (
	"context"
	"errors"
	"fmt"
	"github.com/vioira/viorka-agent/internal/state"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
	"log/slog"
	"os"
	"reflect"
	"runtime"
	"time"
)

type entry struct {
	Calibration *v1.TrafficCalibration `json:"calibration,omitempty"`
	TaskID      string                 `json:"task_id"`
	Deadline    time.Time              `json:"deadline"`
	Reply       v1.TaskReply           `json:"reply"`
}
type Executor struct {
	Logger    *slog.Logger
	Calibrate func(context.Context, string, v1.TrafficCalibration, time.Time) (*v1.TrafficCalibration, error)
	store     *state.Store
	node      string
	journal   map[string]entry
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
	if d.Engine != nil {
		return v1.TaskReply{}, fmt.Errorf("unexpected engine payload")
	}
	if (d.Operation != "node.inspect" && d.Operation != "traffic.calibrate") || d.ResourceKind != "node" || d.ResourceID != e.node || d.Attempt == nil || *d.Attempt == 0 || len(d.TaskID) < 1 || len(d.TaskID) > 128 || len(d.IdempotencyKey) < 1 || len(d.IdempotencyKey) > 128 || d.ConfigSHA256 != "" || d.DeadlineAt.IsZero() || d.DeadlineAt.After(time.Now().Add(10*time.Minute)) {
		return v1.TaskReply{}, fmt.Errorf("unsupported or invalid task")
	}
	if (d.Operation == "traffic.calibrate" && (d.TrafficCalibration == nil || d.TrafficCalibration.Validate() != nil)) || (d.Operation == "node.inspect" && d.TrafficCalibration != nil) {
		return v1.TaskReply{}, fmt.Errorf("invalid operation payload")
	}
	if old, ok := e.journal[d.IdempotencyKey]; ok {
		if old.TaskID != d.TaskID || !old.Deadline.Equal(d.DeadlineAt) || !reflect.DeepEqual(old.Calibration, d.TrafficCalibration) {
			return v1.TaskReply{}, fmt.Errorf("idempotency conflict")
		}
		r := old.Reply
		if r.State == "running" {
			return e.calibrate(d)
		}
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
	if d.Operation == "traffic.calibrate" {
		return e.calibrate(d)
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
	e.journal[d.IdempotencyKey] = entry{TaskID: d.TaskID, Deadline: d.DeadlineAt, Reply: r}
	if err := e.store.Write("tasks.json", e.journal); err != nil {
		delete(e.journal, d.IdempotencyKey)
		return v1.TaskReply{}, err
	}
	return r, nil
}

func (e *Executor) calibrate(d v1.TaskDispatch) (v1.TaskReply, error) {
	r := v1.TaskReply{TaskID: d.TaskID, Attempt: *d.Attempt, ResourceID: e.node, State: "running"}
	e.journal[d.IdempotencyKey] = entry{TaskID: d.TaskID, Deadline: d.DeadlineAt, Reply: r, Calibration: d.TrafficCalibration}
	if err := e.store.Write("tasks.json", e.journal); err != nil {
		return v1.TaskReply{}, err
	}
	if time.Now().After(d.DeadlineAt) {
		r.State = "failed"
		r.ErrorCode = "deadline_exceeded"
	} else if e.Calibrate == nil {
		r.State = "failed"
		r.ErrorCode = "traffic_unavailable"
	} else {
		ctx, cancel := context.WithDeadline(context.Background(), d.DeadlineAt)
		defer cancel()
		before, err := e.Calibrate(ctx, d.IdempotencyKey, *d.TrafficCalibration, time.Now())
		if errors.Is(err, state.ErrTrafficPeriod) {
			r.State = "failed"
			r.ErrorCode = "traffic_period_changed"
		} else if err != nil {
			return v1.TaskReply{}, fmt.Errorf("calibrate traffic: %w", err)
		} else {
			if before == nil || before.Validate() != nil {
				return v1.TaskReply{}, fmt.Errorf("missing calibration receipt")
			}
			r.State = "succeeded"
			r.TrafficCalibration = d.TrafficCalibration
			r.TrafficBefore = before
		}
	}
	e.journal[d.IdempotencyKey] = entry{TaskID: d.TaskID, Deadline: d.DeadlineAt, Reply: r, Calibration: d.TrafficCalibration}
	if err := e.store.Write("tasks.json", e.journal); err != nil {
		return v1.TaskReply{}, err
	}
	return r, nil
}
