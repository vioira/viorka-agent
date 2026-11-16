package connection

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/vioira/viorka-agent/internal/config"
	"github.com/vioira/viorka-agent/internal/state"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPostPanelErrorCodes(t *testing.T) {
	for _, tc := range []struct {
		name, body, reason string
		status             int
	}{
		{"identity conflict", `{"code":"conflict","message":"secret"}`, "conflict", 409},
		{"engine conflict", `{"code":"engine_conflict","message":"secret"}`, "engine_conflict", 409},
		{"unauthorized", `{"code":"unauthorized"}`, "unauthorized", 401},
		{"forbidden", `{"code":"engine_forbidden"}`, "engine_forbidden", 403},
		{"empty authentication response", "", "identity_rejected", 401},
		{"proxy HTML", "<html>secret</html>", "http_error", 502},
		{"missing code", `{"message":"secret"}`, "http_error", 409},
		{"invalid code type", `{"code":409}`, "http_error", 409},
		{"control character", `{"code":"conflict\nsecret"}`, "http_error", 409},
		{"long code", `{"code":"` + strings.Repeat("x", 65) + `"}`, "http_error", 409},
		{"oversized body", `{"code":"conflict","message":"` + strings.Repeat("x", 16384) + `"}`, "http_error", 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			c := &Client{cfg: config.Local{PanelURL: server.URL}, http: server.Client()}
			defer c.Close()
			err := c.post(context.Background(), "/api/v1/agents/enroll", struct{}{}, nil)
			want := fmt.Sprintf("Panel connection: authentication: %s (HTTP %d)", tc.reason, tc.status)
			if err == nil || !strings.HasPrefix(err.Error(), want+"：") || strings.Contains(err.Error(), "secret") {
				t.Fatalf("got %v, want %s", err, want)
			}
			if errors.Is(err, ErrIdentityRejected) != (tc.status == 401 || tc.status == 403) {
				t.Fatalf("identity rejection classification changed: %v", err)
			}
		})
	}
}

func TestTLSVerificationAndChallengeBinding(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"id": "nonce", "message": "sign an unrelated context", "expires_at": time.Now().Add(time.Minute)})
	}))
	defer ts.Close()
	dir := t.TempDir()
	s, err := state.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cfg := config.Local{PanelURL: ts.URL, NodeName: "test", StateDir: dir}
	c, err := New(cfg, s)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = c.post(ctx, "/api/v1/agents/challenge", map[string]string{"node_id": "test"}, &map[string]any{}); err == nil {
		t.Fatal("untrusted certificate accepted")
	}
	ca := filepath.Join(dir, "ca.pem")
	if err = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.CAFile = ca
	c, err = New(cfg, s)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	if _, err = c.session(ctx, state.Identity{PanelURL: ts.URL, NodeID: "node", PrivateKey: key}); err == nil {
		t.Fatal("unbound challenge accepted")
	}
	cfg.PanelURL = strings.Replace(ts.URL, "127.0.0.1", "localhost", 1)
	wrong, err := New(cfg, s)
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.Close()
	if err = wrong.post(ctx, "/api/v1/agents/challenge", nil, &map[string]any{}); err == nil {
		t.Fatal("wrong hostname accepted")
	}
}
