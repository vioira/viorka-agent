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

func TestRegisterPersistsIdentityAndRetries(t *testing.T) {
	var keys []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agents/register" {
			t.Error(r.URL.Path)
		}
		var v map[string]string
		if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
			t.Error(err)
		}
		if v["name"] != "自定义节点" || v["hostname"] == "" || v["protocol_version"] != "1.0" {
			t.Error(v["name"], v["hostname"])
		}
		if _, ok := v["engines"]; ok {
			t.Error("batch registration must not recover engines")
		}
		keys = append(keys, v["public_key"])
		if len(keys) == 1 {
			http.Error(w, "lost response", 502)
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
	token := strings.Repeat("a", 64)
	if err = c.Register(context.Background(), token, " 自定义节点 "); err == nil {
		t.Fatal("502 accepted")
	}
	before, err := store.Identity(server.URL, false)
	if err != nil || before.NodeID != "" {
		t.Fatal(err)
	}
	store.Close()
	store, err = state.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	c.store = store
	if err = c.Register(context.Background(), token, "自定义节点"); err != nil {
		t.Fatal(err)
	}
	after, err := store.Identity(server.URL, false)
	if err != nil || after.NodeID != "node" || !bytes.Equal(before.PrivateKey, after.PrivateKey) {
		t.Fatal("identity changed", err)
	}
	if err = c.Register(context.Background(), token, "自定义节点"); err == nil {
		t.Fatal("existing identity overwritten")
	}
	if len(keys) != 2 || keys[0] != keys[1] {
		t.Fatal("retry changed key")
	}
}
func TestRegisterRejectsInvalidResponsesAndInputs(t *testing.T) {
	for _, body := range []string{`{"node_id":"","protocol_version":"1.0"}`, `{"node_id":"node","protocol_version":"2.0"}`, `{"node_id":"node","protocol_version":"1.0","extra":true}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
			defer server.Close()
			store, err := state.Open(filepath.Join(t.TempDir(), "state"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			c := &Client{cfg: config.Local{PanelURL: server.URL}, store: store, http: server.Client()}
			if err = c.Register(context.Background(), strings.Repeat("a", 64), "name"); err == nil {
				t.Fatal("invalid response accepted")
			}
			id, err := store.Identity(server.URL, false)
			if err != nil || id.NodeID != "" {
				t.Fatal("invalid response saved", err)
			}
			for _, name := range []string{strings.Repeat("中", 43), "bad\x00name"} {
				if err = c.Register(context.Background(), strings.Repeat("a", 64), name); err == nil {
					t.Fatal("invalid name accepted")
				}
			}
		})
	}
}
