package connection

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

func TestSlowUptimeTransportAcrossDowngrade(t *testing.T) {
	b := uploadFixture(t)
	f := b.Fast[0]
	f.UptimeSeconds = nil
	f.Errors = slices.DeleteFunc(f.Errors, func(s string) bool { return s == "uptime_unavailable" })
	seconds := 1.25
	ip := &v1.TelemetryPublicIP{Sequence: 1, SampledAt: f.SampledAt, Errors: []string{"ipv4_unavailable", "ipv6_unavailable"}, Uptime: &v1.TelemetryUptime{SampledAt: f.SampledAt, Seconds: &seconds}}
	var window telemetryWindow
	for _, v := range []any{f, ip} {
		if err := window.add(v); err != nil {
			t.Fatal(err)
		}
	}
	flight := window.take()
	defer window.release(flight)
	c := &Client{}
	for _, slow := range []bool{true, false, true} {
		c.slowUptimeSupported.Store(slow)
		var received bool
		transport := c.telemetryTransport(b.NodeID, b.RunID, func() telemetryFrameDelivery {
			return func(_ context.Context, raw []byte, _ string, _ uint64) error {
				var env v1.Envelope
				if err := json.Unmarshal(raw, &env); err != nil {
					return err
				}
				batch, err := v1.DecodeTelemetryBatch(env.Payload, b.NodeID, time.Now(), 60, false, slow)
				if err != nil {
					return err
				}
				if (batch.PublicIp[0].Uptime != nil) != slow || slices.Contains(batch.Fast[0].Errors, "uptime_unavailable") == slow {
					t.Fatal("wire capability mismatch", batch)
				}
				received = true
				return nil
			}
		})
		if err := transport(t.Context(), telemetryTransmission{send: &telemetrySend{flight: flight}, report: 60, credential: sessionCredential{Changed: make(chan struct{})}}); err != nil || !received {
			t.Fatal(err, received)
		}
	}
}

func TestSlowWorkerCadenceSurvivesRealtime(t *testing.T) {
	settings := v1.DefaultTelemetrySettings()
	at := time.Now()
	var calls []time.Time
	noop := func(context.Context, time.Time) (any, error) { return nil, nil }
	s, err := newTelemetrySchedule(t.Context(), settings, 1, "generation-1", at, telemetrySources{
		fast: noop, full: noop, ip: func(_ context.Context, tick time.Time) (any, error) { calls = append(calls, tick); return nil, nil },
		save: func(context.Context, time.Time, bool) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.workers.stop(ctx); err != nil {
			t.Error(err)
		}
	}()
	s.advance(at)
	drainSchedule(t, s)
	s.demand(leaseDemand(1, true), at.Add(time.Second))
	for _, sec := range []int{2, 4, 8, 60, 91, 300, 599, 600} {
		s.advance(at.Add(time.Duration(sec) * time.Second))
		drainSchedule(t, s)
	}
	if len(calls) != 2 || !calls[0].Equal(at) || !calls[1].Equal(at.Add(10*time.Minute)) {
		t.Fatal(calls)
	}
}
