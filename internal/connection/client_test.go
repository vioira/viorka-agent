package connection

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
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
