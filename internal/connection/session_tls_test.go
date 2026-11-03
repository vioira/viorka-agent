package connection

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vioira/viorka-agent/internal/config"
	"github.com/vioira/viorka-agent/internal/state"
)

func TestSharedSessionTLSChallengeSignature(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().UTC().Add(time.Minute)
	var origin string
	var challenges, sessions atomic.Int32
	message := func() string {
		return fmt.Sprintf("viorka-agent-session-v1\n%s\nnode\nnonce\n%s", origin, expires.Format(time.RFC3339Nano))
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/agents/challenge":
			challenges.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"id": "nonce", "message": message(), "expires_at": expires})
		case "/api/v1/agents/session":
			sessions.Add(1)
			var body struct {
				ChallengeID string `json:"challenge_id"`
				Signature   string `json:"signature"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				http.Error(w, "invalid", 400)
				return
			}
			signature, err := base64.StdEncoding.DecodeString(body.Signature)
			if err != nil || body.ChallengeID != "nonce" || !ed25519.Verify(public, []byte(message()), signature) {
				http.Error(w, "invalid", 401)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"token": strings.Repeat("a", 64), "expires_at": time.Now().Add(15 * time.Minute)})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	origin = server.URL
	client := &Client{cfg: config.Local{PanelURL: origin}, http: server.Client()}
	defer client.Close()
	id := state.Identity{PanelURL: origin, NodeID: "node", PrivateKey: private}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := client.credentials(ctx, id)
			if err == nil && (value.Generation != 1 || len(value.Token) != 64) {
				err = fmt.Errorf("invalid credentials")
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if challenges.Load() != 1 || sessions.Load() != 1 {
		t.Fatal("independent transports issued competing sessions", challenges.Load(), sessions.Load())
	}
	if _, err = client.credentials(ctx, id); err != nil || challenges.Load() != 1 {
		t.Fatal("reconnect did not reuse token", err)
	}
}
