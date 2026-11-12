package connection

import (
	"context"
	"log/slog"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

// Owned by the scheduler. No credentials, identifiers, raw sample content or
// arbitrary error strings are copied into operational logs.
type telemetryLogState struct {
	next           time.Time
	collected      [5]time.Time
	failed         [5]bool
	send, accepted time.Time
	sendFailed     bool
	droppedFailure uint64
}

func (d *telemetryLogState) collection(result telemetryWorkResult, err error, now time.Time) {
	d.collected[result.kind] = now
	failed := err != nil
	switch sample := result.value.(type) {
	case *v1.TelemetryFast:
		failed = failed || (sample != nil && len(sample.Errors) > 0)
	case *v1.TelemetryFull:
		failed = failed || (sample != nil && len(sample.Errors) > 0)
	case *v1.TelemetryPublicIP:
		failed = failed || (sample != nil && len(sample.Errors) > 0)
	}
	d.failed[result.kind] = failed
}

func (d *telemetryLogState) record(ctx context.Context, logger *slog.Logger, s *telemetrySchedule, now time.Time) {
	if !d.next.IsZero() && now.Before(d.next) {
		return
	}
	d.next = now.Add(time.Minute)
	mode := "http"
	switch {
	case s.retry.blocked:
		mode = "blocked"
	case s.retry.unsupported:
		mode = "unsupported"
	case s.retry.degraded:
		mode = "degraded"
	case s.cadence.realtime:
		mode = "realtime"
	}
	remaining := func(until time.Time) float64 {
		if until.After(now) {
			return until.Sub(now).Seconds()
		}
		return 0
	}
	inflightCount, inflightBytes := 0, 0
	if s.active != nil {
		inflightCount = len(s.active.flight.samples)
		for _, sample := range s.active.flight.samples {
			inflightBytes += len(sample.raw) + 1
		}
	}
	cfg := s.cadence.settings
	logger.LogAttrs(ctx, slog.LevelInfo, "Telemetry status",
		slog.String("mode", mode), slog.Uint64("collection_seconds", cfg.CollectionSeconds),
		slog.Uint64("http_report_seconds", cfg.HttpReportSeconds), slog.Uint64("wss_report_seconds", cfg.WssReportSeconds),
		slog.Bool("demand_enabled", s.lease.current.Enabled), slog.Bool("management_connected", s.lease.connected),
		slog.Float64("lease_remaining_seconds", remaining(s.lease.expires)),
		slog.Time("last_fast_completed", d.collected[0]), slog.Time("last_full_completed", d.collected[1]),
		slog.Time("last_ip_completed", d.collected[2]), slog.Time("last_traffic_save_completed", d.collected[3]),
		slog.Bool("fast_error", d.failed[0]), slog.Bool("full_error", d.failed[1]),
		slog.Bool("ip_error", d.failed[2]), slog.Bool("traffic_save_error", d.failed[3]),
		slog.Time("last_tcp_probe_completed", d.collected[4]), slog.Bool("tcp_probe_error", d.failed[4]),
		slog.Time("last_send_started", d.send), slog.Time("last_receipt_accepted", d.accepted), slog.Bool("last_send_error", d.sendFailed),
		slog.Int("pending_samples", len(s.window.pending)), slog.Int("pending_sample_bytes", s.window.bytes),
		slog.Int("inflight_samples", inflightCount), slog.Int("inflight_sample_bytes", inflightBytes),
		slog.Uint64("dropped_limit", s.window.droppedLimit), slog.Uint64("dropped_cooldown", s.window.droppedCooldown),
		slog.Uint64("dropped_failed_flight", d.droppedFailure), slog.Uint64("refusals", s.retry.refusals),
		slog.Float64("http_wait_seconds", remaining(s.retry.earliest(true))),
		slog.Float64("wss_wait_seconds", remaining(s.retry.earliest(false))))
}
