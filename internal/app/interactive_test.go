package app

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vioira/viorka-agent/internal/config"
	"github.com/vioira/viorka-agent/internal/connection"
	"github.com/vioira/viorka-agent/internal/state"
)

type testEnrollmentPrompt struct {
	accept  bool
	token   string
	choices []bool
	reads   int
}

func (p *testEnrollmentPrompt) Retry(replace bool) bool {
	p.choices = append(p.choices, replace)
	return p.accept
}
func (p *testEnrollmentPrompt) Token() (string, error) {
	p.reads++
	return p.token, nil
}

func TestInteractiveEnrollment(t *testing.T) {
	for _, tc := range []struct {
		name, code               string
		status                   int
		accept, noTerminal       bool
		requests, prompts, reads int
	}{
		{"replace token", "unauthorized", 401, true, false, 2, 1, 1},
		{"retry gateway", "unavailable", 502, true, false, 2, 1, 0},
		{"terminate", "unavailable", 502, false, false, 1, 1, 0},
		{"engine conflict", "engine_conflict", 409, true, false, 1, 0, 0},
		{"unattended", "unavailable", 502, true, true, 1, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests []map[string]any
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				requests = append(requests, request)
				if len(requests) == 1 {
					w.WriteHeader(tc.status)
					json.NewEncoder(w).Encode(map[string]string{"code": tc.code})
					return
				}
				json.NewEncoder(w).Encode(map[string]string{"node_id": "node", "protocol_version": "1.0"})
			}))
			defer server.Close()
			dir := t.TempDir()
			ca := filepath.Join(dir, "ca.pem")
			if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := config.Local{PanelURL: server.URL, NodeName: "test", StateDir: filepath.Join(dir, "state"), CAFile: ca}
			store, err := state.Open(cfg.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			client, err := connection.New(cfg, store)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			prompt := &testEnrollmentPrompt{accept: tc.accept, token: strings.Repeat("b", 64)}
			var input enrollmentPrompt = prompt
			if tc.noTerminal {
				input = nil
			}
			var output bytes.Buffer
			err = enrollWithRetry(context.Background(), strings.Repeat("a", 64), client.Enroll, input, &output)
			if (err == nil) != (tc.requests == 2) || len(requests) != tc.requests || len(prompt.choices) != tc.prompts || prompt.reads != tc.reads {
				t.Fatalf("err=%v requests=%d prompts=%v reads=%d", err, len(requests), prompt.choices, prompt.reads)
			}
			identity, err := store.Identity(server.URL, false)
			if err != nil {
				t.Fatal(err)
			}
			if tc.requests == 2 {
				if requests[0]["public_key"] != requests[1]["public_key"] || identity.NodeID != "node" {
					t.Fatal("retry lost identity or failed to save node")
				}
				wantToken := strings.Repeat("a", 64)
				if tc.reads == 1 {
					wantToken = prompt.token
				}
				if requests[1]["token"] != wantToken {
					t.Fatal("wrong retry token")
				}
			} else if identity.NodeID != "" {
				t.Fatal("failed enrollment saved a node")
			}
			if strings.Contains(output.String(), strings.Repeat("a", 64)) || strings.Contains(output.String(), prompt.token) {
				t.Fatal("token leaked")
			}
		})
	}
}

func TestInvalidTokenCanBeReplaced(t *testing.T) {
	prompt := &testEnrollmentPrompt{accept: true, token: strings.Repeat("b", 64)}
	calls := 0
	err := enrollWithRetry(context.Background(), "short", func(_ context.Context, token string) error {
		calls++
		if token != prompt.token {
			t.Fatal("invalid token reached client")
		}
		return nil
	}, prompt, &bytes.Buffer{})
	if err != nil || calls != 1 || prompt.reads != 1 || len(prompt.choices) != 1 || !prompt.choices[0] {
		t.Fatalf("err=%v calls=%d prompt=%+v", err, calls, prompt)
	}
}
