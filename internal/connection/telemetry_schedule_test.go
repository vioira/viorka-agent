package connection

import (
	"context"
	"testing"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

func scheduleFixture(t *testing.T, settings v1.TelemetrySettings) (*telemetrySchedule, time.Time) {
	t.Helper()
	batch := windowFixture(t)
	now := batch.Fast[0].SampledAt
	var seq uint64
	noop := func(context.Context, time.Time) (any, error) { return nil, nil }
	sources := telemetrySources{fast: func(_ context.Context, at time.Time) (any, error) {
		seq++
		f := *batch.Fast[0]
		f.Sequence = seq
		f.SampledAt = at
		return &f, nil
	}, full: noop, ip: noop, save: func(context.Context, time.Time, bool) error { return nil }}
	s, err := newTelemetrySchedule(context.Background(), settings, 1, "generation-1", now, sources)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.workers.stop(ctx); err != nil {
			t.Error(err)
		}
	})
	return s, now
}
func drainSchedule(t *testing.T, s *telemetrySchedule) {
	t.Helper()
	for {
		busy := false
		for _, v := range s.workers.busy {
			busy = busy || v
		}
		if !busy {
			return
		}
		select {
		case r := <-s.workers.results:
			if err := s.collected(r); err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("scheduled work timeout")
		}
	}
}
func TestTelemetryScheduleHTTPRealtimeFallback(t *testing.T) {
	settings := v1.DefaultTelemetrySettings()
	settings.CollectionSeconds = 2
	s, start := scheduleFixture(t, settings)
	for seconds := 0; seconds <= 60; seconds += 2 {
		s.advance(start.Add(time.Duration(seconds) * time.Second))
		drainSchedule(t, s)
	}
	now := start.Add(time.Minute)
	post := s.take(now)
	if post == nil || !post.attempt.HTTP || len(post.flight.samples) != 31 {
		t.Fatalf("HTTP window: %+v", post)
	}
	if s.take(now) != nil {
		t.Fatal("parallel send")
	}
	if r := s.demand(leaseDemand(1, true), now.Add(time.Second)); r != nil || s.cadence.realtime {
		t.Fatal("enabled before POST completion")
	}
	s.advance(now.Add(2 * time.Second))
	drainSchedule(t, s)
	if len(s.window.pending) != 1 {
		t.Fatal("sampling stopped during POST")
	}
	// The POST owns only 1..31; a new sample collected during the flight
	// must survive its completion and become the first live sample.
	for i, sample := range post.flight.samples {
		if sample.sequence != uint64(i+1) {
			t.Fatal("POST window identity changed", i, sample.sequence)
		}
	}
	result := s.finish(post, now.Add(15*time.Second), "", 0)
	checkLeaseResult(t, result, "applied", "realtime", "")
	s.advance(now.Add(15 * time.Second))
	if s.take(now.Add(15*time.Second)) != nil {
		t.Fatal("WSS sent before new fast completed")
	}
	drainSchedule(t, s)
	live := s.take(now.Add(15 * time.Second))
	if live == nil || live.attempt.HTTP || len(live.flight.samples) != 2 {
		t.Fatal("pending window not transferred to live")
	}
	if live.flight.samples[0].sequence != 32 || live.flight.samples[1].sequence != 33 {
		t.Fatal("live transfer repeated POST history or lost pending sample")
	}
	s.finish(live, now.Add(16*time.Second), "network_error", 0)
	if s.cadence.realtime || !s.fallback {
		t.Fatal("WSS failure did not fall back")
	}
	if s.take(now.Add(16*time.Second)) != nil {
		t.Fatal("fallback bypassed R")
	}
	s.advance(now.Add(time.Minute))
	drainSchedule(t, s)
	fallback := s.take(now.Add(time.Minute))
	if fallback == nil || !fallback.attempt.HTTP || len(fallback.flight.samples) != 1 {
		t.Fatal("fallback replayed failed history")
	}
	if s.finish(live, now.Add(time.Minute), "", 0) != nil || s.active != fallback {
		t.Fatal("late WSS completion stole new flight")
	}
	s.finish(fallback, now.Add(time.Minute+time.Second), "", 0)
	if s.retry.degraded || len(s.window.pending) != 0 {
		t.Fatal("recovery left offline backlog")
	}
	checkLeaseResult(t, s.demand(leaseDemand(1, true), now.Add(time.Minute+time.Second)), "rejected", "http", "lease_expired")
}
func TestTelemetryScheduleCachedHTTPAndManagementLoss(t *testing.T) {
	settings := v1.DefaultTelemetrySettings()
	settings.CollectionSeconds = 60
	settings.HttpReportSeconds = 30
	s, start := scheduleFixture(t, settings)
	s.advance(start)
	drainSchedule(t, s)
	for _, seconds := range []int{30, 60, 90} {
		now := start.Add(time.Duration(seconds) * time.Second)
		s.advance(now)
		drainSchedule(t, s)
		send := s.take(now)
		if send == nil || len(send.flight.samples) != 1 {
			t.Fatal("missing HTTP cache")
		}
		expected := uint64(1)
		if seconds >= 60 {
			expected = 2
		}
		if send.flight.samples[0].sequence != expected {
			t.Fatal("POST manufactured extra collection")
		}
		s.disconnect(now)
		if s.active != send {
			t.Fatal("management loss canceled HTTP")
		}
		s.finish(send, now.Add(time.Second), "", 0)
		if !s.nextWake(now.Add(time.Second)).After(now.Add(time.Second)) {
			t.Fatal("timer busy loop")
		}
	}
}
func TestTelemetryScheduleCooldownAndGenerationFence(t *testing.T) {
	s, start := scheduleFixture(t, v1.DefaultTelemetrySettings())
	s.advance(start)
	drainSchedule(t, s)
	now := start.Add(time.Minute)
	s.advance(now)
	drainSchedule(t, s)
	send := s.take(now)
	if send == nil {
		t.Fatal("missing send")
	}
	s.finish(send, now, "invalid_response", 0)
	for _, seconds := range []int{90, 120, 150} {
		at := start.Add(time.Duration(seconds) * time.Second)
		s.advance(at)
		drainSchedule(t, s)
		if s.take(at) != nil {
			t.Fatal("cooldown bypass")
		}
	}
	if s.window.droppedCooldown == 0 {
		t.Fatal("cooldown accumulated history")
	}
	at := start.Add(180 * time.Second)
	s.advance(at)
	drainSchedule(t, s)
	probe := s.take(at)
	if probe == nil || len(probe.flight.samples) != 1 {
		t.Fatal("half-open must send latest only")
	}
	if err := s.reconnected(2, "generation-2", at); err != nil {
		t.Fatal(err)
	}
	if s.take(at) != nil {
		t.Fatal("new generation overlapped old POST")
	}
	s.finish(probe, at, "", 0)
	if s.retry.degraded {
		t.Fatal("reconnect retained degraded state")
	}
	if err := s.reconnected(1, "generation-1", at); err == nil {
		t.Fatal("old credentials accepted")
	}
}

func TestTelemetryScheduleSettingsRephasePendingIntent(t *testing.T) {
	s, start := scheduleFixture(t, v1.DefaultTelemetrySettings())
	s.advance(start)
	drainSchedule(t, s)
	at := start.Add(time.Minute)
	s.advance(at)
	drainSchedule(t, s)
	next := s.cadence.settings
	next.Revision++
	next.HttpReportSeconds = 180
	if err := s.settings(next, at); err != nil {
		t.Fatal(err)
	}
	if s.take(at) != nil || s.postWanted {
		t.Fatal("old POST intention survived new R phase")
	}
	checkLeaseResult(t, s.demand(leaseDemand(1, true), at), "applied", "realtime", "")
	s.advance(at)
	drainSchedule(t, s)
	next.Revision++
	next.WssReportSeconds = 5
	if err := s.settings(next, at); err != nil {
		t.Fatal(err)
	}
	if s.take(at) != nil || s.liveWanted {
		t.Fatal("old WSS intention survived new W phase")
	}
	s.advance(at.Add(5 * time.Second))
	drainSchedule(t, s)
	send := s.take(at.Add(5 * time.Second))
	if send == nil || send.attempt.HTTP {
		t.Fatal("new W phase not scheduled")
	}
	s.finish(send, at.Add(5*time.Second), "", 0)
}

func TestTelemetryHealthyPOSTJitterPreservesWindow(t *testing.T) {
	s, start := scheduleFixture(t, v1.DefaultTelemetrySettings())
	for _, seconds := range []int{0, 30, 60} {
		s.advance(start.Add(time.Duration(seconds) * time.Second))
		drainSchedule(t, s)
	}
	first := s.take(start.Add(time.Minute + 10*time.Millisecond))
	if first == nil {
		t.Fatal("missing first POST")
	}
	s.finish(first, start.Add(time.Minute+15*time.Millisecond), "", 0)
	s.advance(start.Add(90 * time.Second))
	drainSchedule(t, s)
	s.advance(start.Add(120 * time.Second))
	drainSchedule(t, s)
	if s.take(start.Add(120*time.Second)) != nil {
		t.Fatal("bypassed lastPOST+R")
	}
	next := start.Add(120*time.Second + 10*time.Millisecond)
	if !s.nextWake(start.Add(120 * time.Second)).Equal(next) {
		t.Fatal("did not schedule rate-limit boundary")
	}
	second := s.take(next)
	if second == nil || len(second.flight.samples) != 2 || second.flight.samples[0].sequence != 4 || second.flight.samples[1].sequence != 5 || s.window.droppedCooldown != 0 {
		t.Fatalf("healthy timing jitter discarded observations: send=%+v cooldown_drops=%d", second, s.window.droppedCooldown)
	}
	s.finish(second, next.Add(time.Millisecond), "", 0)
}

func TestTelemetryReconnectRestoresCachedGroupsBeforeNextTick(t *testing.T) {
	s, start := scheduleFixture(t, v1.DefaultTelemetrySettings())
	batch := windowFixture(t)
	for _, value := range []any{batch.Fast[0], batch.Full[0], &v1.TelemetryPublicIP{Sequence: 1, SampledAt: start, Ipv4: "8.8.8.8", Errors: []string{"ipv6_unavailable"}}} {
		if err := s.window.add(value); err != nil {
			t.Fatal(err)
		}
	}
	// The previous samples were already sent; only the immutable latest cache remains.
	s.window.collapse()
	s.disconnect(start)
	at := start.Add(5 * time.Second)
	if err := s.reconnected(2, "restarted-panel", at); err != nil {
		t.Fatal(err)
	}
	send := s.take(at)
	if send == nil || !send.attempt.HTTP || len(send.flight.samples) != 3 {
		t.Fatal("reconnect did not immediately restore all cached groups")
	}
	for _, sample := range send.flight.samples {
		cached := s.window.latest[sample.kind]
		if sample.sequence != cached.sequence || string(sample.raw) != string(cached.raw) {
			t.Fatal("reconnect changed observation identity or timestamp")
		}
	}
	if s.take(at) != nil {
		t.Fatal("overlapping reconnect send")
	}
	s.finish(send, at, "", 0)
	// A repeated settings/ready acknowledgement on this same connection must not
	// schedule another cached snapshot after the normal retry budget expires.
	if err := s.reconnected(2, "restarted-panel", at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if s.take(at.Add(time.Minute)) != nil {
		t.Fatal("same connection scheduled another bootstrap")
	}
}

func TestTelemetryReconnectSnapshotRespectsRetryBudget(t *testing.T) {
	for _, code := range []string{"rate_limited", "session_invalid"} {
		t.Run(code, func(t *testing.T) {
			s, start := scheduleFixture(t, v1.DefaultTelemetrySettings())
			if err := s.window.add(windowFixture(t).Fast[0]); err != nil {
				t.Fatal(err)
			}
			s.postWanted = true
			first := s.take(start)
			if first == nil {
				t.Fatal("missing first upload")
			}
			s.finish(first, start, code, 90*time.Second)
			s.disconnect(start)
			at := start.Add(5 * time.Second)
			if err := s.reconnected(2, "new-panel", at); err != nil {
				t.Fatal(err)
			}
			if s.take(at) != nil {
				t.Fatal("reconnect bypassed pacing or rejection")
			}
			if code == "session_invalid" {
				if s.take(start.Add(10*time.Minute)) != nil {
					t.Fatal("reconnect bypassed blocked identity")
				}
				return
			}
			eligible := s.retry.earliest(true)
			if s.take(eligible.Add(-time.Nanosecond)) != nil {
				t.Fatal("early retry")
			}
			if s.take(eligible) == nil {
				t.Fatal("snapshot waited for a cadence tick after budget became available")
			}
		})
	}
}

func TestTelemetryReconnectClearsCooldownAndPacing(t *testing.T) {
	for _, generation := range []uint64{1, 2} {
		for _, code := range []string{"", "stale_session", "session_expired", "invalid_batch", "invalid_response"} {
			t.Run(code, func(t *testing.T) {
				s, start := scheduleFixture(t, v1.DefaultTelemetrySettings())
				if err := s.window.add(windowFixture(t).Fast[0]); err != nil {
					t.Fatal(err)
				}
				s.postWanted = true
				first := s.take(start)
				s.finish(first, start, code, 0)
				s.disconnect(start)
				at := start.Add(time.Second)
				if err := s.reconnected(generation, "new-panel", at); err != nil {
					t.Fatal(err)
				}
				send := s.take(at)
				if send == nil || !send.attempt.HTTP || len(send.flight.samples) == 0 {
					t.Fatal("reconnect did not upload immediately")
				}
				s.finish(send, at, "", 0)
			})
		}
	}
}

func TestTelemetryReconnectFencesLateCooldown(t *testing.T) {
	s, start := scheduleFixture(t, v1.DefaultTelemetrySettings())
	if err := s.window.add(windowFixture(t).Fast[0]); err != nil {
		t.Fatal(err)
	}
	s.postWanted = true
	old := s.take(start)
	s.disconnect(start)
	at := start.Add(time.Second)
	if err := s.reconnected(1, "new-panel", at); err != nil {
		t.Fatal(err)
	}
	if s.take(at) != nil {
		t.Fatal("reconnect overlapped old upload")
	}
	s.finish(old, at, "stale_session", 0)
	if s.retry.degraded || !s.retry.httpCooldown.IsZero() || !s.retry.wssCooldown.IsZero() {
		t.Fatal("old response reinstated cooldown")
	}
	if s.take(at) == nil {
		t.Fatal("snapshot did not follow old upload completion")
	}
}
