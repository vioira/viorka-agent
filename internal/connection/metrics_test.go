package connection

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"github.com/coder/websocket"
	"github.com/vioira/viorka-agent/internal/config"
	"github.com/vioira/viorka-agent/internal/executor"
	"github.com/vioira/viorka-agent/internal/state"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestMetricsNegotiationAndTaskCoexistence(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		version, collect, report uint64
	}{{"disabled", 0, 0, 0}, {"v1", 1, 1, 2}, {"default-zero", 2, 0, 2}, {"batch", 2, 1, 5}, {"reuse", 2, 10, 1}, {"host", 2, 0, 1}} {
		enabled := tc.version > 0
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			done := make(chan error, 1)
			var origin string
			mux := http.NewServeMux()
			mux.HandleFunc("POST /api/v1/agents/challenge", func(w http.ResponseWriter, r *http.Request) {
				expires := time.Now().UTC().Add(time.Minute)
				json.NewEncoder(w).Encode(map[string]any{"id": "nonce", "message": fmt.Sprintf("viorka-agent-session-v1\n%s\nnode\nnonce\n%s", origin, expires.Format(time.RFC3339Nano)), "expires_at": expires})
			})
			mux.HandleFunc("POST /api/v1/agents/session", func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{"token": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "expires_at": time.Now().Add(time.Minute)})
			})
			mux.HandleFunc("GET /api/v1/agents/ws", func(w http.ResponseWriter, r *http.Request) {
				err := func() error {
					c, e := websocket.Accept(w, r, nil)
					if e != nil {
						return e
					}
					defer c.CloseNow()
					read := func() (v1.Envelope, error) {
						_, b, e := c.Read(ctx)
						if e != nil {
							return v1.Envelope{}, e
						}
						return v1.Parse(b, "node")
					}
					send := func(e v1.Envelope) error { b, _ := json.Marshal(e); return c.Write(ctx, websocket.MessageText, b) }
					hello, e := read()
					if e != nil || hello.Type != "system.hello" {
						return fmt.Errorf("hello: %v", e)
					}
					ack := v1.HelloAck{ProtocolVersion: v1.Version, HeartbeatSeconds: 30}
					if enabled {
						n := uint64(1)
						ack.MetricsVersion = &tc.version
						ack.InventoryVersion = &n
						if tc.name == "host" {
							ack.HostMetricsVersion = &n
						}
						ack.CollectionSettings = &v1.NodeSettings{Revision: 2, CollectionSeconds: tc.collect, ReportSeconds: 60, ConnectionMode: "wss", WssReportSeconds: tc.report}
					}
					if e = send(v1.Message("system.hello_ack", "node", "ack", ack)); e != nil {
						return e
					}
					if enabled {
						inventory, e := read()
						if e != nil || inventory.Type != "node.inventory" {
							return fmt.Errorf("inventory: %s %v", inventory.Type, e)
						}
						readMetric := func() (v1.NodeMetrics, int, error) {
							msg, e := read()
							if e != nil {
								return v1.NodeMetrics{}, 0, e
							}
							if tc.version == 2 {
								var batch v1.NodeMetricsBatch
								if msg.Type != "node.metrics_batch" {
									return v1.NodeMetrics{}, 0, fmt.Errorf("expected batch got %s", msg.Type)
								}
								if e = v1.Decode(msg.Payload, &batch); e != nil || len(batch.Samples) == 0 {
									return v1.NodeMetrics{}, 0, fmt.Errorf("invalid batch: %v", e)
								}
								return *batch.Samples[len(batch.Samples)-1], len(batch.Samples), nil
							}
							var m v1.NodeMetrics
							e = v1.Decode(msg.Payload, &m)
							return m, 1, e
						}
						first, _, e := readMetric()
						if e != nil || first.SampledAt.IsZero() {
							return fmt.Errorf("first sample: %v", e)
						}
						if (first.Host != nil) != (tc.name == "host" && runtime.GOOS == "linux") {
							return fmt.Errorf("host metrics negotiation mismatch")
						}
						newer, count, e := readMetric()
						if e != nil {
							return e
						}
						if tc.collect > tc.report {
							if !newer.SampledAt.Equal(first.SampledAt) {
								return fmt.Errorf("repeated snapshot lost sampling time")
							}
						} else if !newer.SampledAt.After(first.SampledAt) {
							return fmt.Errorf("new sample missing")
						}
						if tc.name == "batch" && count < 3 {
							return fmt.Errorf("intermediate samples lost: %d", count)
						}

					}
					attempt := uint64(1)
					dispatch := v1.Message("task.dispatch", "node", "dispatch", v1.TaskDispatch{TaskID: "task", Operation: "node.inspect", IdempotencyKey: "inspect", ResourceKind: "node", ResourceID: "node", DeadlineAt: time.Now().Add(time.Minute), Attempt: &attempt})
					dispatch.CorrelationID = "task"
					if e = send(dispatch); e != nil {
						return e
					}
					for _, kind := range []string{"task.ack", "task.result"} {
						msg, e := read()
						if e != nil || msg.Type != kind {
							return fmt.Errorf("expected %s, got %s: %v", kind, msg.Type, e)
						}
					}
					return nil
				}()
				done <- err
			})
			server := httptest.NewTLSServer(mux)
			defer server.Close()
			origin = server.URL
			dir := t.TempDir()
			ca := filepath.Join(dir, "ca.pem")
			if e := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); e != nil {
				t.Fatal(e)
			}
			store, e := state.Open(filepath.Join(dir, "state"))
			if e != nil {
				t.Fatal(e)
			}
			defer store.Close()
			exec, e := executor.New(store, "node")
			if e != nil {
				t.Fatal(e)
			}
			client, e := New(config.Local{PanelURL: origin, CAFile: ca, NodeName: "test", StateDir: dir}, store)
			if e != nil {
				t.Fatal(e)
			}
			defer client.Close()
			client.inventory = func(context.Context, time.Duration) v1.NodeInventory {
				return v1.NodeInventory{SampledAt: time.Now().UTC(), OSName: "Ubuntu 24.04", Errors: []string{"public_ipv4_unavailable", "public_ipv6_unavailable"}}
			}
			_, key, e := ed25519.GenerateKey(rand.Reader)
			if e != nil {
				t.Fatal(e)
			}
			_ = client.connect(ctx, state.Identity{PanelURL: origin, NodeID: "node", PrivateKey: key}, exec)
			select {
			case e := <-done:
				if e != nil {
					t.Fatal(e)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}
