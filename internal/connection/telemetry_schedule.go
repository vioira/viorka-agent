package connection

import (
	"context"
	"fmt"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

type telemetrySend struct {
	attempt telemetryAttempt
	flight  *telemetryFlight
}

// telemetrySchedule is the single owner of cadence, pending samples, leases,
// and send ownership. The management writer and HTTP worker execute a returned
// send; neither may schedule another attempt independently. No method blocks on
// sampling, HTTP, or a WSS receipt.
type telemetrySchedule struct {
	trafficError                     error
	cadence                          *telemetryCadence
	retry                            *telemetryRetry
	lease                            *telemetryLease
	workers                          *telemetryWorkers
	window                           telemetryWindow
	active                           *telemetrySend
	ip, save                         time.Time
	postWanted, liveWanted, fallback bool
}

func newTelemetrySchedule(ctx context.Context, settings v1.TelemetrySettings, generation uint64, serverGeneration string, now time.Time, sources telemetrySources) (*telemetrySchedule, error) {
	cadence, err := newTelemetryCadence(settings, now)
	if err != nil {
		return nil, err
	}
	retry, err := newTelemetryRetry(settings.HttpReportSeconds, generation)
	if err != nil {
		return nil, err
	}
	return &telemetrySchedule{trafficError: sources.trafficError, cadence: cadence, retry: retry, lease: newTelemetryLease(serverGeneration), workers: startTelemetryWorkers(ctx, sources), ip: now, save: now.Add(time.Minute)}, nil
}
func (s *telemetrySchedule) mode(now time.Time) {
	realtime := s.lease.realtime && !s.retry.degraded && !s.retry.blocked && !s.retry.unsupported
	was := s.cadence.realtime
	s.cadence.realtimeMode(realtime, now)
	if was && !realtime {
		s.liveWanted = false
		// Becoming HTTP does not reset the R attempt budget. If already due, the
		// sender may proceed immediately; begin still enforces lastPOST+R.
		s.postWanted = true
	}
	if !was && realtime {
		s.postWanted = false
		s.liveWanted = false
	}
}
func (s *telemetrySchedule) demand(d v1.TelemetryDemand, now time.Time) *v1.TelemetryDemandResult {
	result := s.lease.receive(d, now, s.active != nil)
	s.mode(now)
	if result != nil && result.State == "applied" && d.Enabled && !s.cadence.realtime {
		result.Mode = "degraded"
	}
	return result
}

// advance runs once at each deadline, never catches up missed ticks. A real
// time.Time retains monotonic clock information for leases and retry budgets.
func (s *telemetrySchedule) advance(now time.Time) *v1.TelemetryDemandResult {
	s.lease.expire(now)
	result := s.lease.settle(now, s.active != nil)
	s.mode(now)
	due := s.cadence.due(now)
	if due.Fast {
		s.workers.submit(0, now)
	}
	if due.Full {
		s.workers.submit(1, now)
	}
	if !now.Before(s.ip) {
		s.workers.submit(2, now)
		s.ip = now.Add(600 * time.Second)
	}
	if !now.Before(s.save) {
		s.workers.submit(3, now)
		s.save = now.Add(time.Minute)
	}
	if due.POST {
		// lastPOST+R alone is normal pacing, not failed delivery. A few
		// milliseconds of sender jitter must not discard a healthy window.
		if s.retry.blocked || s.retry.unsupported || (s.retry.degraded && now.Before(s.retry.earliest(true))) {
			s.window.collapse()
		}
		s.postWanted = true
	}
	if due.WSS {
		s.liveWanted = true
	}
	if result != nil && result.State == "applied" && s.lease.current.Enabled && !s.cadence.realtime {
		result.Mode = "degraded"
	}
	return result
}
func (s *telemetrySchedule) collected(result telemetryWorkResult) error {
	s.workers.complete(result)
	if result.value != nil && result.kind < 3 {
		if err := s.window.add(result.value); err != nil {
			return err
		}
	}
	return result.err
}

// take transfers exactly one bounded window. Realtime waits for the fast
// operation scheduled on this W deadline, so it can send the new observation.
// HTTP never waits for a slow full/IP worker or forces extra fast collection.
func (s *telemetrySchedule) take(now time.Time) *telemetrySend {
	if s.active != nil {
		return nil
	}
	http := !s.cadence.realtime
	if http {
		if (!s.postWanted && !s.fallback) || s.lease.pending {
			return nil
		}
	} else if !s.liveWanted || s.workers.busy[0] {
		return nil
	}
	if len(s.window.pending) == 0 && s.window.latest[0] == nil && s.window.latest[1] == nil && s.window.latest[2] == nil {
		return nil
	}
	attempt, ok := s.retry.begin(now, http)
	if !ok {
		return nil
	}
	if s.retry.degraded {
		s.window.collapse()
	}
	flight := s.window.take(s.fallback || s.retry.degraded)
	if flight == nil {
		s.retry.active = nil
		return nil
	}
	s.active = &telemetrySend{attempt, flight}
	if http {
		s.postWanted = false
		s.fallback = false
	} else {
		s.liveWanted = false
	}
	return s.active
}
func (s *telemetrySchedule) finish(send *telemetrySend, now time.Time, code string, retryAfter time.Duration) *v1.TelemetryDemandResult {
	if send == nil || send != s.active {
		return nil
	}
	s.window.release(send.flight)
	s.active = nil
	owned := s.retry.finish(send.attempt, now, code, retryAfter)
	if owned && code != "" && !send.attempt.HTTP {
		// A new demand revision is needed after failed realtime. Duplicate enable
		// cannot resurrect the invalidated lease. Fallback is latest-only.
		s.lease.realtime = false
		s.lease.pending = false
		s.lease.expires = time.Time{}
		s.fallback = true
	}
	result := s.lease.settle(now, false)
	s.mode(now)
	if result != nil && result.State == "applied" && s.lease.current.Enabled && !s.cadence.realtime {
		result.Mode = "degraded"
	}
	return result
}

// disconnect revokes only the live lease. An authenticated HTTP upload survives
// management loss; its token and retry budget remain owned by the same run.
func (s *telemetrySchedule) disconnect(now time.Time) {
	s.lease.disconnect()
	s.mode(now)
}

// settings accepts only an already-persisted revision from telemetrySettings.
func (s *telemetrySchedule) settings(next v1.TelemetrySettings, now time.Time) error {
	old := s.cadence.settings
	if err := s.cadence.change(next, now); err != nil {
		return err
	}
	if next.HttpReportSeconds != old.HttpReportSeconds && !s.cadence.realtime && !s.fallback {
		s.postWanted = false
	}
	if next.WssReportSeconds != old.WssReportSeconds && s.cadence.realtime {
		s.liveWanted = false
	}
	return s.retry.setReport(next.HttpReportSeconds)
}

// nextWake omits already-consumed send intentions while a worker/receipt is
// outstanding. Results and commands wake the owner through their own channels.
func (s *telemetrySchedule) nextWake(now time.Time) time.Time {
	next := time.Time{}
	add := func(at time.Time) {
		if !at.IsZero() && (next.IsZero() || at.Before(next)) {
			next = at
		}
	}
	add(s.cadence.fast)
	add(s.cadence.full)
	add(s.cadence.post)
	add(s.cadence.live)
	add(s.ip)
	add(s.save)
	if (s.lease.realtime || s.lease.pending) && s.lease.current.Enabled {
		add(s.lease.expires)
	}
	if s.active == nil && !s.retry.blocked && !s.retry.unsupported && !s.lease.pending {
		if !s.cadence.realtime && (s.postWanted || s.fallback) {
			at := s.retry.earliest(true)
			// Eligible sends are handled in the same owner iteration. Only future
			// throttles belong in the timer; absence of samples must not busy-loop.
			if at.After(now) {
				add(at)
			}
		}
	}
	return next
}

// reconnected is called only after ready and persisted settings are accepted.
// Local token generation and server WSS generation are distinct identities.
func (s *telemetrySchedule) reconnected(generation uint64, serverGeneration string, now time.Time) error {
	if generation < s.retry.generation {
		return fmt.Errorf("stale telemetry credentials")
	}
	if generation > s.retry.generation {
		if err := s.retry.sessionChanged(generation); err != nil {
			return err
		}
	}
	if s.lease.generation != serverGeneration || !s.lease.connected {
		s.lease = newTelemetryLease(serverGeneration)
	}
	s.retry.negotiated(generation)
	s.mode(now)
	return nil
}
