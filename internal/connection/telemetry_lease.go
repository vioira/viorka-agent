package connection

import (
	v1 "github.com/vioira/viorka-agent/protocol/v1"
	"time"
)

// telemetryLease is ephemeral, scheduler-owned state. now must come from the
// process clock (with its monotonic component), never from remote timestamps.
// Receive records intent; settle acknowledges only after the sender is idle.
// A newer command supersedes pending intent without restarting any collector.
type telemetryLease struct {
	generation string
	current    v1.TelemetryDemand
	expires    time.Time
	pending    bool
	realtime   bool
	connected  bool
}

func newTelemetryLease(generation string) *telemetryLease {
	return &telemetryLease{generation: generation, connected: true}
}
func (l *telemetryLease) result(d v1.TelemetryDemand, code string) v1.TelemetryDemandResult {
	r := v1.TelemetryDemandResult{SessionGeneration: d.SessionGeneration, DemandRevision: d.DemandRevision, LeaseID: d.LeaseID, State: "applied", Mode: "http"}
	if l.realtime {
		r.Mode = "realtime"
	}
	if code != "" {
		r.State = "rejected"
		r.ErrorCode = code
	}
	return r
}

// A nil result means the command is waiting for the current bounded send to
// finish. It does not authorize WSS transmission or an applied UI indication.
func (l *telemetryLease) receive(d v1.TelemetryDemand, now time.Time, sending bool) *v1.TelemetryDemandResult {
	reject := func(code string) *v1.TelemetryDemandResult { r := l.result(d, code); return &r }
	if !l.connected || d.SessionGeneration != l.generation {
		return reject("stale_session")
	}
	if d.Validate(l.generation) != nil || now.IsZero() {
		return reject("invalid_settings")
	}
	if d.DemandRevision < l.current.DemandRevision {
		return reject("stale_demand")
	}
	if d.DemandRevision == l.current.DemandRevision {
		if d != l.current {
			return reject("stale_demand")
		}
		// Retransmission acknowledges actual state but cannot renew or resurrect.
		if d.Enabled && !now.Before(l.expires) {
			l.realtime = false
			l.pending = false
			return reject("lease_expired")
		}
		if l.pending {
			return l.settle(now, sending)
		}
		return reject("")
	}
	l.current = d
	l.expires = time.Time{}
	if d.Enabled {
		l.expires = now.Add(90 * time.Second)
	}
	l.pending = true
	return l.settle(now, sending)
}
func (l *telemetryLease) settle(now time.Time, sending bool) *v1.TelemetryDemandResult {
	if !l.connected || !l.pending {
		return nil
	}
	if l.current.Enabled && !now.Before(l.expires) {
		l.realtime = false
		l.pending = false
		r := l.result(l.current, "lease_expired")
		return &r
	}
	if sending {
		return nil
	}
	l.realtime = l.current.Enabled
	l.pending = false
	r := l.result(l.current, "")
	return &r
}

// expire returns true when the cadence must return to HTTP. The scheduler also
// calls settle to reject an expired pending enable command after sender exit.
func (l *telemetryLease) expire(now time.Time) bool {
	if !l.realtime || now.Before(l.expires) {
		return false
	}
	l.realtime = false
	return true
}
func (l *telemetryLease) disconnect() {
	l.connected = false
	l.realtime = false
	l.pending = false
	l.expires = time.Time{}
}
