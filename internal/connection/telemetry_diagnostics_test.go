package connection

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

func TestTelemetryOperationalLogBoundAndRedaction(t *testing.T) {
	at := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	cadence, err := newTelemetryCadence(v1.DefaultTelemetrySettings(), at)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := newTelemetryRetry(60, 1)
	if err != nil {
		t.Fatal(err)
	}
	s := &telemetrySchedule{cadence: cadence, retry: retry, lease: newTelemetryLease("secret-generation")}
	s.lease.current.Enabled = true
	s.lease.expires = at.Add(90 * time.Second)
	s.window.droppedLimit = 3
	s.window.droppedCooldown = 5
	s.window.bytes = 42
	s.window.pending = []telemetrySample{{raw: []byte("secret-payload")}}
	s.active = &telemetrySend{flight: &telemetryFlight{samples: []telemetrySample{{raw: []byte("secret-flight")}}}}
	retry.degraded = true
	retry.refusals = 4
	retry.refusedUntil = at.Add(3 * time.Minute)
	d := telemetryLogState{send: at, droppedFailure: 2}
	d.collection(telemetryWorkResult{kind: 0, value: &v1.TelemetryFast{Errors: []string{"secret-sample-error"}}}, nil, at)
	d.collection(telemetryWorkResult{kind: 3}, errors.New("secret-token /private/path"), at)
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	for i := 0; i < 600; i++ {
		d.record(context.Background(), logger, s, at.Add(time.Duration(i)*100*time.Millisecond))
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("unbounded diagnostics: %d", len(lines))
	}
	if strings.Contains(output.String(), "secret") || strings.Contains(output.String(), "/private") {
		t.Fatal("raw state leaked")
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"mode": "degraded", "collection_seconds": float64(30), "http_report_seconds": float64(60),
		"wss_report_seconds": float64(2), "lease_remaining_seconds": float64(90),
		"http_wait_seconds": float64(180), "refusals": float64(4), "pending_samples": float64(1),
		"pending_sample_bytes": float64(42), "inflight_samples": float64(1), "inflight_sample_bytes": float64(14),
		"dropped_limit": float64(3), "dropped_cooldown": float64(5), "dropped_failed_flight": float64(2),
		"fast_error": true, "traffic_save_error": true, "last_fast_completed": at.Format(time.RFC3339),
	} {
		if record[key] != want {
			t.Errorf("%s=%v want=%v", key, record[key], want)
		}
	}
	d.collection(telemetryWorkResult{kind: 0, value: &v1.TelemetryFast{}}, nil, at.Add(time.Minute))
	d.collection(telemetryWorkResult{kind: 3}, nil, at.Add(time.Minute))
	d.record(context.Background(), logger, s, at.Add(time.Minute))
	lines = strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatal("next bounded record missing")
	}
	if err := json.Unmarshal([]byte(lines[1]), &record); err != nil {
		t.Fatal(err)
	}
	if record["fast_error"] != false || record["traffic_save_error"] != false {
		t.Fatal("recovery not visible")
	}
}
