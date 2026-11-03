package connection

import (
	"testing"
	"time"
)

func TestTelemetrySharedRefusalLadderAndSingleProbe(t *testing.T) {
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	r, err := newTelemetryRetry(60, 1)
	if err != nil {
		t.Fatal(err)
	}
	for i, wait := range []time.Duration{time.Minute, time.Minute, 2 * time.Minute, 3 * time.Minute, 5 * time.Minute, 7 * time.Minute, 10 * time.Minute, 10 * time.Minute} {
		a, ok := r.begin(now, i != 0)
		if !ok {
			t.Fatal("probe denied", i)
		}
		if _, ok = r.begin(now, true); ok {
			t.Fatal("parallel probe allowed")
		}
		if !r.finish(a, now, "overloaded", 0) {
			t.Fatal("owned response ignored")
		}
		if r.refusals != uint64(min(i+1, 7)) || !r.earliest(true).Equal(now.Add(wait)) {
			t.Fatal(i, r.refusals, r.earliest(true))
		}
		if _, ok = r.begin(now.Add(wait-time.Nanosecond), true); ok {
			t.Fatal("early probe")
		}
		if _, ok = r.begin(now.Add(wait), false); ok {
			t.Fatal("realtime bypassed half-open POST")
		}
		now = now.Add(wait)
	}
	probe, ok := r.begin(now, true)
	if !ok {
		t.Fatal("half-open denied")
	}
	if !r.finish(probe, now, "", 0) || r.refusals != 0 || r.degraded {
		t.Fatal("acceptance did not clear circuit")
	}
	if _, ok = r.begin(now, false); !ok {
		t.Fatal("real-time cannot resume after accepted probe")
	}
}
func TestTelemetryRetryLatestDeadlineAndLateResult(t *testing.T) {
	now := time.Now()
	r, _ := newTelemetryRetry(180, 1)
	a, _ := r.begin(now, true)
	r.finish(a, now.Add(time.Second), "rate_limited", 20*time.Minute)
	until := now.Add(time.Second + 20*time.Minute)
	if !r.earliest(true).Equal(until) {
		t.Fatal("Retry-After capped", r.earliest(true))
	}
	if err := r.sessionChanged(2); err != nil {
		t.Fatal(err)
	}
	if r.finish(a, until, "", 0) || r.refusals != 1 {
		t.Fatal("late acceptance reset new state")
	}
	if _, ok := r.begin(until.Add(-time.Second), true); ok {
		t.Fatal("refresh bypassed throttle")
	}
	b, ok := r.begin(until, true)
	if !ok {
		t.Fatal("probe not allowed")
	}
	r.finish(b, until, "session_expired", 0)
	if !r.httpCooldown.Equal(until.Add(120*time.Second)) || !r.wssCooldown.Equal(until.Add(120*time.Second)) {
		t.Fatal("session cooldown not shared")
	}
	if !r.earliest(true).Equal(until.Add(180 * time.Second)) {
		t.Fatal("R minimum not applied")
	}
}
func TestTelemetryNetworkFallbackAndChannelCooldown(t *testing.T) {
	now := time.Now()
	r, _ := newTelemetryRetry(60, 1)
	ws, _ := r.begin(now, false)
	r.finish(ws, now, "network_error", 0)
	post, ok := r.begin(now, true)
	if !ok {
		t.Fatal("first fallback denied")
	}
	r.finish(post, now.Add(time.Second), "network_error", 0)
	if r.refusals != 0 {
		t.Fatal("network failure counted as refusal")
	}
	if _, ok = r.begin(now.Add(59*time.Second), true); ok {
		t.Fatal("network retry bypassed R")
	}
	post, ok = r.begin(now.Add(time.Minute), true)
	if !ok {
		t.Fatal("R retry denied")
	}
	r.finish(post, now.Add(time.Minute), "", 0)
	ws, ok = r.begin(now.Add(time.Minute), false)
	if !ok {
		t.Fatal("WSS denied")
	}
	r.finish(ws, now.Add(time.Minute), "invalid_response", 0)
	// A later HTTP acceptance must not erase the still-active WSS protocol cooldown.
	post, ok = r.begin(now.Add(2*time.Minute), true)
	if !ok {
		t.Fatal("HTTP fallback denied")
	}
	r.finish(post, now.Add(2*time.Minute), "", 0)
	if _, ok = r.begin(now.Add(2*time.Minute), false); ok {
		t.Fatal("HTTP success erased WSS cooldown")
	}
	if _, ok = r.begin(now.Add(3*time.Minute), false); !ok {
		t.Fatal("WSS cooldown never expires")
	}
}
func TestTelemetryPermanentAndUnsupportedGates(t *testing.T) {
	for _, code := range []string{"identity_revoked", "session_invalid", "node_disabled", "tls_error", "unsupported_protocol"} {
		now := time.Now()
		r, _ := newTelemetryRetry(30, 1)
		a, _ := r.begin(now, true)
		r.finish(a, now, code, 0)
		r.sessionChanged(2)
		if _, ok := r.begin(now.Add(time.Hour), true); ok {
			t.Fatal("identity/capability bypass", code)
		}
		if r.negotiated(1) {
			t.Fatal("stale ready accepted")
		}
		r.negotiated(2)
		_, ok := r.begin(now.Add(time.Hour), true)
		if ok != (code == "unsupported_protocol") {
			t.Fatal("renegotiation gate", code, ok)
		}
	}
}
