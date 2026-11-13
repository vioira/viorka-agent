package connection

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/vioira/viorka-agent/internal/config"
	"github.com/vioira/viorka-agent/internal/executor"
	"github.com/vioira/viorka-agent/internal/state"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

func TestTelemetryClientReadyLiveAndHTTP(t *testing.T) { testTelemetryClientReconnect(t) }
func testTelemetryClientReconnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	fixture := uploadFixture(t)
	settings := v1.DefaultTelemetrySettings()
	settings.WssReportSeconds = 1
	settings.HttpReportSeconds = 30
	done := make(chan error, 1)
	posts := make(chan v1.TelemetryBatch, 1)
	var origin string
	var logLevel slog.LevelVar
	var connections atomic.Int32
	var lastRun atomic.Value
	var lastSequence atomic.Uint64
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/agents/challenge", func(w http.ResponseWriter, r *http.Request) {
		expires := time.Now().UTC().Add(time.Minute)
		json.NewEncoder(w).Encode(map[string]any{"id": "nonce", "message": fmt.Sprintf("viorka-agent-session-v1\n%s\nnode-demo\nnonce\n%s", origin, expires.Format(time.RFC3339Nano)), "expires_at": expires})
	})
	mux.HandleFunc("POST /api/v1/agents/session", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"token": strings.Repeat("a", 64), "expires_at": time.Now().Add(15 * time.Minute)})
	})
	mux.HandleFunc("POST /api/v1/agents/metrics", func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		batch, err := v1.DecodeTelemetryBatch(raw, "node-demo", time.Now(), 30, true)
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 64) {
			t.Error("unshared token")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v1.TelemetryReceipt{BatchID: batch.BatchID, State: "accepted", AcceptedCount: uint64(len(batch.Fast) + len(batch.Full) + len(batch.PublicIp))})
		posts <- batch
	})
	mux.HandleFunc("GET /api/v1/agents/ws", func(w http.ResponseWriter, r *http.Request) {
		done <- func() error {
			connection := connections.Add(1)
			settings := settings
			settings.Revision = uint64(connection*2 - 1)
			settings.LogLevel = "warn"
			generation := fmt.Sprintf("generation-%d", connection)
			demandFor := func(revision uint64, enabled bool) v1.TelemetryDemand {
				d := leaseDemand(revision, enabled)
				d.SessionGeneration = generation
				return d
			}
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return err
			}
			defer conn.CloseNow()
			read := func() (v1.Envelope, error) {
				_, raw, err := conn.Read(ctx)
				if err != nil {
					return v1.Envelope{}, err
				}
				return v1.Parse(raw, "node-demo")
			}
			send := func(e v1.Envelope) error {
				raw, err := json.Marshal(e)
				if err != nil {
					return err
				}
				return conn.Write(ctx, websocket.MessageText, raw)
			}
			hello, err := read()
			if err != nil {
				return err
			}
			var payload v1.Hello
			if v1.Decode(hello.Payload, &payload) != nil || !slices.Contains(payload.Capabilities, v1.TelemetryCapability) {
				return fmt.Errorf("v3 capability missing")
			}
			for _, capability := range payload.Capabilities {
				if capability != "node.inspect" && capability != "node.inventory.v1" && capability != v1.TelemetryCapability && capability != v1.TrafficCapability && capability != v1.DailyTrafficCapability && capability != v1.TCPProbeCapability && capability != v1.TCPProbeSettingsCapability {
					return fmt.Errorf("unexpected or retired capability: %s", capability)
				}
			}
			ack := v1.HelloAck{ProtocolVersion: v1.Version, HeartbeatSeconds: 30}
			if err = send(v1.Message("system.hello_ack", "node-demo", "ack", ack)); err != nil {
				return err
			}
			if err = send(v1.Message("node.telemetry.ready", "node-demo", "ready", v1.TelemetryReady{SessionGeneration: generation, Settings: &settings})); err != nil {
				return err
			}
			receipt, err := read()
			if err != nil {
				return err
			}
			var applied v1.TelemetrySettingsResult
			if receipt.Type != "node.settings.v3_result" || receipt.CorrelationID != "ready" || v1.DecodeTelemetry(receipt.Payload, &applied, false) != nil || applied.State != "applied" {
				return fmt.Errorf("settings not applied: %+v", receipt)
			}
			if logLevel.Level() != slog.LevelWarn {
				return fmt.Errorf("ready log level not applied")
			}
			settings.Revision++
			settings.LogLevel = "debug"
			if err = send(v1.Message("node.settings.v3", "node-demo", "logging", settings)); err != nil {
				return err
			}
			receipt, err = read()
			if err != nil {
				return err
			}
			if receipt.Type != "node.settings.v3_result" || receipt.CorrelationID != "logging" || v1.DecodeTelemetry(receipt.Payload, &applied, false) != nil || applied.State != "applied" || logLevel.Level() != slog.LevelDebug {
				return fmt.Errorf("dynamic log level not applied: %+v", receipt)
			}
			if err = send(v1.Message("node.metrics.demand", "node-demo", "enable", demandFor(1, true))); err != nil {
				return err
			}
			receipt, err = read()
			if err != nil {
				return err
			}
			var demand v1.TelemetryDemandResult
			if receipt.Type != "node.metrics.demand_result" || receipt.CorrelationID != "enable" || v1.DecodeTelemetry(receipt.Payload, &demand, false) != nil || demand.Mode != "realtime" {
				return fmt.Errorf("live mode receipt: %+v", receipt)
			}
			frame, err := read()
			if err != nil {
				return err
			}
			if frame.Type != "node.metrics.v3" {
				return fmt.Errorf("legacy metrics after ready: %s", frame.Type)
			}
			batch, err := v1.DecodeTelemetryBatch(frame.Payload, "node-demo", time.Now(), 30, false)
			if err != nil {
				return err
			}
			if previous := lastRun.Load(); previous != nil {
				if previous.(string) != batch.RunID {
					return fmt.Errorf("run reset on reconnect")
				}
			}
			if len(batch.Fast) == 0 || batch.Fast[len(batch.Fast)-1].Sequence <= lastSequence.Load() {
				return fmt.Errorf("fast baseline reset on reconnect")
			}
			lastRun.Store(batch.RunID)
			lastSequence.Store(batch.Fast[len(batch.Fast)-1].Sequence)
			accepted := v1.Message("node.metrics.receipt", "node-demo", "accepted", v1.TelemetryReceipt{BatchID: batch.BatchID, State: "accepted", AcceptedCount: uint64(len(batch.Fast) + len(batch.Full) + len(batch.PublicIp))})
			accepted.CorrelationID = frame.MessageID
			if err = send(accepted); err != nil {
				return err
			}
			if err = send(v1.Message("node.metrics.demand", "node-demo", "disable", demandFor(2, false))); err != nil {
				return err
			}
			receipt, err = read()
			if err != nil {
				return err
			}
			if receipt.Type != "node.metrics.demand_result" || v1.DecodeTelemetry(receipt.Payload, &demand, false) != nil || demand.Mode != "http" {
				return fmt.Errorf("HTTP mode receipt: %+v", receipt)
			}
			if connection == 1 {
				select {
				case post := <-posts:
					if post.RunID != batch.RunID {
						return fmt.Errorf("run identity changed between transports")
					}
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		}()
	})
	server := httptest.NewTLSServer(mux)
	defer server.Close()
	origin = server.URL
	store, err := state.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	execution, err := executor.New(store, "node-demo")
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{logLevel: &logLevel, cfg: config.Local{PanelURL: origin}, http: server.Client(), store: store, telemetryEnabled: true}
	defer client.Close()
	var final atomic.Int32
	client.telemetrySource = func() (telemetrySources, error) {
		var sequence uint64
		noop := func(context.Context, time.Time) (any, error) { return nil, nil }
		return telemetrySources{fast: func(_ context.Context, at time.Time) (any, error) {
			sequence++
			f := *fixture.Fast[0]
			f.Sequence = sequence
			f.SampledAt = at
			return &f, nil
		}, full: noop, ip: noop, save: func(_ context.Context, _ time.Time, force bool) error {
			if force {
				final.Add(1)
			}
			return nil
		}}, nil
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// Match Run's ownership: reconnects retain the same samplers and runner.
	client.runCtx = ctx
	defer func() {
		cancel()
		if err := client.waitCollectors(); err != nil {
			t.Error(err)
		}
	}()
	attempts := 2
	for i := 0; i < attempts; i++ {
		_ = client.connect(ctx, state.Identity{PanelURL: origin, NodeID: "node-demo", PrivateKey: key}, execution)
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	cancel()
	if err := client.waitCollectors(); err != nil {
		t.Fatal(err)
	}
	expectedSaves := int32(1)
	if final.Load() != expectedSaves {
		t.Fatalf("final ledger saves = %d, want %d", final.Load(), expectedSaves)
	}
	settings.Revision = uint64(attempts * 2)
	settings.LogLevel = "debug"
	var saved savedTelemetrySettings
	if err = store.ReadTelemetrySettings(&saved); err != nil || saved.Settings != settings {
		t.Fatal("settings were not durable", err)
	}
}

func TestTelemetryClientSettingsRetryAndControlGeneration(t *testing.T) {
	runner := &telemetryRunner{events: make(chan telemetryEvent, 1), done: make(chan struct{})}
	client := &Client{telemetryEnabled: true, telemetry: runner}
	session := &telemetryClientSession{client: client, identity: state.Identity{NodeID: "node"}, generation: "new-generation", readyID: "failed-ready", credential: sessionCredential{Generation: 2}}
	settings := v1.DefaultTelemetrySettings()
	settings.Revision = 3
	message := v1.Message("node.settings.v3", "node", "retry-settings", settings)
	sends := 0
	send := func(v1.Envelope) error { sends++; return nil }
	if handled, err := session.handle(message, send); !handled || err != nil {
		t.Fatal("retry after failed ready", err)
	}
	select {
	case event := <-runner.events:
		if event.kind != "ready" || event.serverGeneration != "new-generation" || event.credential.Generation != 2 || event.settings != settings || event.correlation != "retry-settings" {
			t.Fatalf("retry lost negotiation identity: %+v", event)
		}
	default:
		t.Fatal("retry not forwarded")
	}
	result := &v1.TelemetrySettingsResult{Revision: 3, AppliedRevision: 3, State: "applied"}
	if err := session.control(telemetryControl{generation: "old-generation", correlation: "retry-settings", settings: result}, send); err != nil {
		t.Fatal(err)
	}
	if sends != 0 || session.applied {
		t.Fatal("old connection receipt crossed generation")
	}
	if err := session.control(telemetryControl{generation: "new-generation", correlation: "retry-settings", settings: result}, send); err != nil {
		t.Fatal(err)
	}
	if sends != 1 || !session.applied {
		t.Fatal("new ready result not applied")
	}
}
