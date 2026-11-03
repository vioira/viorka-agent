package connection

import (
	"encoding/json"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
	"testing"
	"time"
)

func leaseDemand(revision uint64, enabled bool) v1.TelemetryDemand {
	d := v1.TelemetryDemand{SessionGeneration: "generation-1", DemandRevision: revision, LeaseID: "lease-1", Enabled: enabled}
	if enabled {
		d.TtlSeconds = 90
	}
	return d
}
func checkLeaseResult(t *testing.T, r *v1.TelemetryDemandResult, state, mode, code string) {
	t.Helper()
	if r == nil || r.State != state || r.Mode != mode || r.ErrorCode != code {
		t.Fatalf("result %+v", r)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err = v1.ValidateTelemetryJSON(raw, "TelemetryDemandResult", true); err != nil {
		t.Fatal(err)
	}
}
func TestTelemetryLeaseTransitionAndRenewal(t *testing.T) {
	now := time.Now()
	l := newTelemetryLease("generation-1")
	d := leaseDemand(1, true)
	if r := l.receive(d, now, true); r != nil || l.realtime {
		t.Fatal("ack before POST finished")
	}
	if r := l.receive(d, now.Add(time.Second), true); r != nil {
		t.Fatal("duplicate pending ack")
	}
	checkLeaseResult(t, l.settle(now.Add(15*time.Second), false), "applied", "realtime", "")
	checkLeaseResult(t, l.receive(d, now.Add(60*time.Second), false), "applied", "realtime", "")
	if !l.expires.Equal(now.Add(90 * time.Second)) {
		t.Fatal("duplicate extended lease")
	}
	checkLeaseResult(t, l.receive(leaseDemand(2, true), now.Add(65*time.Second), false), "applied", "realtime", "")
	if !l.expires.Equal(now.Add(155*time.Second)) || l.expire(now.Add(154*time.Second)) {
		t.Fatal("renewal")
	}
	if !l.expire(now.Add(155*time.Second)) || l.realtime {
		t.Fatal("expiry")
	}
	checkLeaseResult(t, l.receive(leaseDemand(2, true), now.Add(156*time.Second), false), "rejected", "http", "lease_expired")
}
func TestTelemetryLeaseSupersededDisconnectAndExpiry(t *testing.T) {
	now := time.Now()
	l := newTelemetryLease("generation-1")
	l.receive(leaseDemand(1, true), now, true)
	if r := l.receive(leaseDemand(2, false), now.Add(time.Second), true); r != nil {
		t.Fatal("disable settled during flight")
	}
	checkLeaseResult(t, l.settle(now.Add(2*time.Second), false), "applied", "http", "")
	checkLeaseResult(t, l.receive(leaseDemand(1, true), now.Add(3*time.Second), false), "rejected", "http", "stale_demand")
	checkLeaseResult(t, l.receive(leaseDemand(2, true), now.Add(3*time.Second), false), "rejected", "http", "stale_demand")
	l.receive(leaseDemand(3, true), now.Add(4*time.Second), true)
	checkLeaseResult(t, l.settle(now.Add(94*time.Second), false), "rejected", "http", "lease_expired")
	l.disconnect()
	checkLeaseResult(t, l.receive(leaseDemand(4, true), now.Add(95*time.Second), false), "rejected", "http", "stale_session")
	replacement := newTelemetryLease("generation-2")
	checkLeaseResult(t, replacement.receive(leaseDemand(5, true), now, false), "rejected", "http", "stale_session")
	if replacement.realtime || !replacement.expires.IsZero() {
		t.Fatal("lease survived reconnect")
	}
}
