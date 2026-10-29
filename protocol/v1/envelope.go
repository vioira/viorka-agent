// Package v1 contains minimal protocol models. The canonical schema lives in
// github.com/vioira/viorka/api/agent/v1. M0 will generate these models.
package v1

import (
	"encoding/json"
	"time"
)

const Version = "1.0"

type Envelope struct {
	Version       string          `json:"version"`
	MessageID     string          `json:"message_id"`
	Type          string          `json:"type"`
	NodeID        string          `json:"node_id,omitempty"`
	CorrelationID string          `json:"correlation_id,omitempty"`
	SentAt        time.Time       `json:"sent_at"`
	Revision      *uint64         `json:"revision,omitempty"`
	Payload       json.RawMessage `json:"payload"`
}

type TaskDispatch struct {
	TaskID         string    `json:"task_id"`
	Operation      string    `json:"operation"`
	IdempotencyKey string    `json:"idempotency_key"`
	ResourceKind   string    `json:"resource_kind"`
	ResourceID     string    `json:"resource_id"`
	ConfigSHA256   string    `json:"config_sha256,omitempty"`
	DeadlineAt     time.Time `json:"deadline_at"`
}

// Task state is tracked persistently once M0/M1 storage is implemented.
const (
	TaskQueued     = "queued"
	TaskDispatched = "dispatched"
	TaskAccepted   = "accepted"
	TaskRunning    = "running"
	TaskSucceeded  = "succeeded"
	TaskFailed     = "failed"
	TaskRolledBack = "rolled_back"
)
