package connection

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vioira/viorka-agent/internal/config"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

func TestTelemetryRunnerTLSPOSTThenRealtime(t *testing.T) {
	fixture := uploadFixture(t)
	start := time.Now().UTC()
	var nanos atomic.Int64
	nanos.Store(start.UnixNano())
	now := func() time.Time { return time.Unix(0, nanos.Load()) }
	received := make(chan v1.TelemetryBatch, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	var posting atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posting.Store(true)
		defer posting.Store(false)
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		batch, err := v1.DecodeTelemetryBatch(raw, fixture.NodeID, time.Now(), 30, true)
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 64) {
			t.Error("missing shared token")
		}
		received <- batch
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v1.TelemetryReceipt{BatchID: batch.BatchID, State: "accepted", AcceptedCount: uint64(len(batch.Fast) + len(batch.Full) + len(batch.PublicIp))})
	}))
	defer server.Close()
	client := &Client{cfg: config.Local{PanelURL: server.URL}, http: server.Client()}
	defer client.Close()
	credential, err := client.sessions.Get(context.Background(), "runner-test", func(context.Context) (sessionCredential, error) {
		return sessionCredential{Token: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(15 * time.Minute)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	settings := v1.DefaultTelemetrySettings()
	settings.CollectionSeconds = 60
	settings.HttpReportSeconds = 30
	var sequence uint64
	noop := func(context.Context, time.Time) (any, error) { return nil, nil }
	sources := telemetrySources{fast: func(_ context.Context, at time.Time) (any, error) {
		sequence++
		f := *fixture.Fast[0]
		f.Sequence = sequence
		f.SampledAt = at
		return &f, nil
	}, full: noop, ip: noop, save: func(context.Context, time.Time, bool) error { return nil }}
	schedule, err := newTelemetrySchedule(context.Background(), settings, credential.Generation, "generation-1", start, sources)
	if err != nil {
		t.Fatal(err)
	}
	frames := make(chan v1.TelemetryBatch, 1)
	deliver := func(ctx context.Context, raw []byte, id string, count uint64) error {
		if posting.Load() {
			t.Error("WSS overlaps in-flight POST")
		}
		var envelope v1.Envelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return err
		}
		batch, err := v1.DecodeTelemetryBatch(envelope.Payload, fixture.NodeID, time.Now(), 30, true)
		if err != nil {
			return err
		}
		if batch.BatchID != id || uint64(len(batch.Fast)+len(batch.Full)+len(batch.PublicIp)) != count {
			t.Error("frame correlation")
		}
		select {
		case frames <- batch:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	transport := client.telemetryTransport(fixture.NodeID, fixture.RunID, func() telemetryFrameDelivery { return deliver })
	saved := &telemetrySettings{current: &settings, write: func(string, any) error { return nil }}
	runner := startTelemetryRunner(context.Background(), schedule, credential, saved, transport, now)
	defer func() {
		unblock()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := runner.stop(ctx); err != nil {
			t.Error(err)
		}
	}()
	// Wait for a completed initial fast sample without polling internal actor state.
	for {
		select {
		case d := <-runner.diagnostics:
			if d.Completed[0] > 0 {
				goto sampled
			}
		case <-time.After(2 * time.Second):
			t.Fatal("initial sample timeout")
		}
	}
sampled:
	nanos.Store(start.Add(30 * time.Second).UnixNano())
	runner.events <- telemetryEvent{kind: "wake"}
	select {
	case batch := <-received:
		if len(batch.Fast) != 1 || batch.Fast[0].Sequence != 1 {
			t.Fatal("C>R did not reuse sample")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("POST timeout")
	}
	runner.events <- telemetryEvent{kind: "demand", correlation: "enable", demand: leaseDemand(1, true)}
	changed := settings
	changed.Revision++
	runner.events <- telemetryEvent{kind: "settings", correlation: "settings", settings: changed}
	select {
	case c := <-runner.controls:
		if c.settings == nil || c.settings.State != "applied" {
			t.Fatal("enable acknowledged before POST end")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("control blocked behind POST")
	}
	select {
	case <-frames:
		t.Fatal("WSS before POST completion")
	default:
	}
	unblock()
	select {
	case c := <-runner.controls:
		if c.correlation != "enable" {
			t.Fatal("wrong demand correlation")
		}
		checkLeaseResult(t, c.demand, "applied", "realtime", "")
	case <-time.After(2 * time.Second):
		t.Fatal("missing mode receipt")
	}
	select {
	case batch := <-frames:
		if len(batch.Fast) != 1 || batch.Fast[0].Sequence != 2 {
			t.Fatal("live sent old snapshot instead of completed fast")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("live frame timeout")
	}
}
