package connection

import (
	"fmt"
	"time"
)

type telemetryAttempt struct {
	ID, Generation uint64
	HTTP           bool
	Started        time.Time
}

// telemetryRetry is scheduler-owned state shared by both metric transports.
// Heartbeats and management success never enter this state machine.
type telemetryRetry struct {
	report                                                        time.Duration
	generation, serial                                            uint64
	active                                                        *telemetryAttempt
	lastPOST, refusedUntil, retryUntil, httpCooldown, wssCooldown time.Time
	refusals                                                      uint64
	degraded, unsupported, blocked                                bool
}

func newTelemetryRetry(reportSeconds, generation uint64) (*telemetryRetry, error) {
	if reportSeconds < 30 || reportSeconds > 180 || generation == 0 {
		return nil, fmt.Errorf("invalid telemetry retry configuration")
	}
	return &telemetryRetry{report: time.Duration(reportSeconds) * time.Second, generation: generation}, nil
}
func (r *telemetryRetry) setReport(seconds uint64) error {
	if seconds < 30 || seconds > 180 {
		return fmt.Errorf("invalid telemetry report interval")
	}
	r.report = time.Duration(seconds) * time.Second
	return nil
}
func latestTime(values ...time.Time) time.Time {
	var latest time.Time
	for _, v := range values {
		if v.After(latest) {
			latest = v
		}
	}
	return latest
}
func (r *telemetryRetry) earliest(http bool) time.Time {
	cooldown := r.wssCooldown
	if http {
		cooldown = r.httpCooldown
	}
	var post time.Time
	if http && !r.lastPOST.IsZero() {
		post = r.lastPOST.Add(r.report)
	}
	return latestTime(post, cooldown, r.refusedUntil, r.retryUntil)
}
func (r *telemetryRetry) begin(now time.Time, http bool) (telemetryAttempt, bool) {
	if now.IsZero() || r.blocked || r.unsupported || r.active != nil || (!http && r.degraded) || now.Before(r.earliest(http)) {
		return telemetryAttempt{}, false
	}
	r.serial++
	attempt := telemetryAttempt{ID: r.serial, Generation: r.generation, HTTP: http, Started: now}
	r.active = &attempt
	if http {
		r.lastPOST = now
	}
	return attempt, true
}

// finish applies only to the still-owned attempt. The sender releases its
// window regardless of acceptance, rejection, timeout, or stale completion.
func (r *telemetryRetry) finish(attempt telemetryAttempt, now time.Time, code string, retryAfter time.Duration) bool {
	if r.active == nil || *r.active != attempt || attempt.Generation != r.generation {
		return false
	}
	r.active = nil
	if code == "" {
		r.refusals = 0
		r.degraded = false
		r.refusedUntil = time.Time{}
		r.retryUntil = time.Time{}
		return true
	}
	r.degraded = true
	if retryAfter > 0 {
		r.retryUntil = latestTime(r.retryUntil, now.Add(retryAfter))
	}
	switch code {
	case "session_invalid", "identity_revoked", "node_disabled", "tls_error":
		r.blocked = true
	case "unsupported_protocol":
		r.unsupported = true
	case "overloaded", "rate_limited":
		if r.refusals < 7 {
			r.refusals++
		}
		wait := r.report
		switch r.refusals {
		case 3:
			wait = 2 * time.Minute
		case 4:
			wait = 3 * time.Minute
		case 5:
			wait = 5 * time.Minute
		case 6:
			wait = 7 * time.Minute
		case 7:
			wait = 10 * time.Minute
		}
		r.refusedUntil = latestTime(r.refusedUntil, now.Add(wait))
	case "session_expired", "stale_session":
		until := now.Add(120 * time.Second)
		r.httpCooldown = latestTime(r.httpCooldown, until)
		r.wssCooldown = latestTime(r.wssCooldown, until)
	case "invalid_batch", "invalid_response":
		until := now.Add(120 * time.Second)
		if attempt.HTTP {
			r.httpCooldown = latestTime(r.httpCooldown, until)
		} else {
			r.wssCooldown = latestTime(r.wssCooldown, until)
		}
	case "network_error", "stale_demand", "lease_expired": // no identity refresh or refusal increment
	default:
		// Unknown failures cannot become an immediate busy loop.
		until := now.Add(120 * time.Second)
		r.httpCooldown = latestTime(r.httpCooldown, until)
		r.wssCooldown = latestTime(r.wssCooldown, until)
	}
	return true
}

// sessionChanged abandons the old attempt but preserves throttles and refusal
// history. Credential refresh alone does not clear transport cooldowns; a
// successfully renegotiated management connection calls reconnect separately.
func (r *telemetryRetry) sessionChanged(generation uint64) error {
	if generation <= r.generation {
		return fmt.Errorf("stale telemetry session generation")
	}
	r.generation = generation
	r.active = nil
	r.degraded = true
	return nil
}
func (r *telemetryRetry) negotiated(generation uint64) bool {
	if generation != r.generation || r.blocked {
		return false
	}
	r.unsupported = false
	return true
}

// reconnect discards transport cooldowns and allows a fresh snapshot immediately.
// Keep explicit server throttles and permanent identity failures. Fence the old
// attempt even when the new management connection reuses the same token.
func (r *telemetryRetry) reconnect() {
	r.active = nil
	r.lastPOST = time.Time{}
	r.httpCooldown = time.Time{}
	r.wssCooldown = time.Time{}
	r.degraded = false
}
