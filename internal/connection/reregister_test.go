package connection

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vioira/viorka-agent/internal/config"
	"github.com/vioira/viorka-agent/internal/state"
)

func TestReenrollRetainsLocalStateAndRetriesSameKey(t *testing.T) {
	var keys []string
	responseNode := "node"
	fail := true
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]string
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		keys = append(keys, request["public_key"])
		if fail {
			http.Error(w, "unknown outcome", 503)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"node_id": responseNode, "protocol_version": "1.0"})
	}))
	defer server.Close()
	store, err := state.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	old, err := store.Identity(server.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	old.NodeID = "node"
	if err = store.Write("identity.json", old); err != nil {
		t.Fatal(err)
	}
	if err = store.Write("journal.json", map[string]string{"task": "succeeded"}); err != nil {
		t.Fatal(err)
	}
	c := &Client{cfg: config.Local{PanelURL: server.URL}, store: store, http: server.Client()}
	token := strings.Repeat("a", 64)
	if err = c.Reenroll(context.Background(), token, "other"); err == nil {
		t.Fatal("cross-node overwrite allowed")
	}
	if len(keys) != 0 {
		t.Fatal("cross-node request sent")
	}
	if err = c.Reenroll(context.Background(), token, "node"); err == nil {
		t.Fatal("failure hidden")
	}
	kept, _ := store.Identity(server.URL, false)
	if !bytes.Equal(kept.PrivateKey, old.PrivateKey) {
		t.Fatal("old key overwritten on failure")
	}
	fail = false
	responseNode = "wrong"
	if err = c.Reenroll(context.Background(), token, "node"); err == nil {
		t.Fatal("wrong response identity accepted")
	}
	responseNode = "node"
	if err = c.Reenroll(context.Background(), token, "node"); err != nil {
		t.Fatal(err)
	}
	current, err := store.Identity(server.URL, false)
	if err != nil || current.NodeID != "node" || bytes.Equal(current.PrivateKey, old.PrivateKey) {
		t.Fatal("identity not replaced", err)
	}
	oldPublic := base64.StdEncoding.EncodeToString(ed25519.PrivateKey(old.PrivateKey).Public().(ed25519.PublicKey))
	if len(keys) != 3 || keys[0] != keys[1] || keys[1] != keys[2] || keys[0] == oldPublic {
		t.Fatal("retry did not retain new key")
	}
	var journal map[string]string
	if err = store.Read("journal.json", &journal); err != nil || journal["task"] != "succeeded" {
		t.Fatal("journal changed", err)
	}
}
