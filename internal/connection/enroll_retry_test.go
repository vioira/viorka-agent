package connection

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vioira/viorka-agent/internal/config"
	"github.com/vioira/viorka-agent/internal/state"
)

func TestEnrollRetriesPendingIdentityAfter502(t *testing.T) {
	var keys []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]string
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		keys = append(keys, request["public_key"])
		if len(keys) == 1 {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"node_id": "node", "protocol_version": "1.0"})
	}))
	defer server.Close()
	dir := filepath.Join(t.TempDir(), "state")
	store, err := state.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{cfg: config.Local{PanelURL: server.URL}, store: store, http: server.Client()}
	if err = c.Enroll(context.Background(), strings.Repeat("a", 64)); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("expected HTTP 502, got %v", err)
	}
	pending, err := store.Identity(server.URL, false)
	if err != nil || pending.NodeID != "" || len(pending.PrivateKey) == 0 {
		t.Fatalf("expected persisted pending identity: %v", err)
	}
	store.Close()
	// A separate invocation reopens the persisted key after the first failed.
	store, err = state.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	c.store = store
	if err = c.Enroll(context.Background(), strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	registered, err := store.Identity(server.URL, false)
	if err != nil || registered.NodeID != "node" || !bytes.Equal(registered.PrivateKey, pending.PrivateKey) {
		t.Fatalf("retry did not preserve key and save node: %v", err)
	}
	if err = c.Enroll(context.Background(), strings.Repeat("a", 64)); err == nil {
		t.Fatal("completed enrollment must not repeat")
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatal("unexpected enrollment requests or changed public key")
	}
}
