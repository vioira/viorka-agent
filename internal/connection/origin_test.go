package connection

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vioira/viorka-agent/internal/config"
	"github.com/vioira/viorka-agent/internal/state"
)

func TestOriginMigrationRequiresSharedChallenge(t *testing.T) {
	var candidateSessions atomic.Int32
	var trustedRedemptions atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agents/challenge" || r.Header.Get("Authorization") != "" {
			candidateSessions.Add(1)
			w.WriteHeader(400)
			return
		}
		origin := r.Header.Get("Viorka-Agent-Origin")
		ch := panelChallenge{ID: "foreign-nonce", ExpiresAt: time.Now().UTC().Add(time.Minute)}
		ch.Message = fmt.Sprintf("viorka-agent-session-v1\n%s\nnode\n%s\n%s", origin, ch.ID, ch.ExpiresAt.Format(time.RFC3339Nano))
		_ = json.NewEncoder(w).Encode(ch)
	}))
	defer target.Close()
	trusted := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/agents/challenge" {
			_ = json.NewEncoder(w).Encode(panelChallenge{MigrateTo: target.URL})
			return
		}
		trustedRedemptions.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer trusted.Close()
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	c := Client{cfg: config.Local{PanelURL: trusted.URL}, http: trusted.Client()}
	_, err = c.issueSession(context.Background(), state.Identity{PanelURL: trusted.URL, NodeID: "node", PrivateKey: key})
	if err == nil || trustedRedemptions.Load() != 1 || candidateSessions.Load() != 0 || c.origin() != trusted.URL {
		t.Fatal("foreign target received credentials or changed trusted origin")
	}
}
func TestMigrationOriginValidation(t *testing.T) {
	for _, s := range []string{"http://localhost", "https://bad@host", "https://host/", "https://host?", "https://host#", "https://*.example.com", "https://:123"} {
		if validMigrationOrigin(s) {
			t.Errorf("accepted %q", s)
		}
	}
	if !validMigrationOrigin("https://[::1]:8443") {
		t.Fatal("valid HTTPS origin rejected")
	}
}
