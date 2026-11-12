package connection

import (
	"bytes"
	"context"
	"testing"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

func TestTCPProbeTransportNegotiation(t *testing.T) {
	fixture := uploadFixture(t)
	percent, median := 0.0, 12.0
	full := fixture.Full[0]
	full.TcpProbes = []*v1.TelemetryTCPProbe{{Target: "example.com:443", SampledAt: full.SampledAt, WindowSeconds: 600, Samples: 1, FailurePercent: &percent, MedianMs: &median}}
	var window telemetryWindow
	if err := window.add(full); err != nil {
		t.Fatal(err)
	}
	flight := window.take(false)
	defer window.release(flight)
	client := &Client{}
	for _, enabled := range []bool{true, false} {
		client.tcpProbeSupported.Store(enabled)
		called := false
		transport := client.telemetryTransport(fixture.NodeID, fixture.RunID, func() telemetryFrameDelivery {
			return func(_ context.Context, raw []byte, _ string, _ uint64) error {
				called = true
				if bytes.Contains(raw, []byte("tcp_probes")) != enabled {
					t.Errorf("extension negotiation %t: %s", enabled, raw)
				}
				return nil
			}
		})
		err := transport(context.Background(), telemetryTransmission{send: &telemetrySend{flight: flight}, credential: sessionCredential{Changed: make(chan struct{}), ExpiresAt: time.Now().Add(time.Hour)}, report: 60})
		if err != nil || !called {
			t.Fatalf("transport: %v called=%t", err, called)
		}
	}
	if !bytes.Contains(flight.samples[0].raw, []byte("tcp_probes")) {
		t.Fatal("downgrade mutated owned window")
	}
}
