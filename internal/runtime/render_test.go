package runtime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
	"strings"
	"testing"
)

func renderCommand(t *testing.T, name string) v1.EngineCommand {
	t.Helper()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s := &v1.EngineService{ID: "service", Name: "service", Enabled: true, ListenHost: "127.0.0.1", ListenPort: 8443, TargetHost: "example.com", TargetPort: 443, ServerName: "example.com", Uuid: "12345678-1234-4234-8234-123456789012", PrivateKey: base64.RawURLEncoding.EncodeToString(key.Bytes()), ShortID: "0123456789abcdef"}
	if name == "realm" {
		s.ServerName = ""
		s.Uuid = ""
		s.PrivateKey = ""
		s.ShortID = ""
	}
	return v1.EngineCommand{Name: name, Version: v1.EngineVersion(name), Revision: 1, RunIntent: "running", Services: []*v1.EngineService{s}}
}
func TestRenderUsesSeparateNativeFormats(t *testing.T) {
	for _, name := range []string{"sing-box", "xray", "realm"} {
		t.Run(name, func(t *testing.T) {
			c := renderCommand(t, name)
			b, err := Render(c)
			if err != nil {
				t.Fatal(err)
			}
			if name == "realm" {
				if !strings.Contains(string(b), `remote = "example.com:443"`) || !strings.Contains(string(b), "use_udp = false") {
					t.Fatal(string(b))
				}
			} else {
				var doc map[string]any
				if json.Unmarshal(b, &doc) != nil || len(doc["inbounds"].([]any)) != 1 {
					t.Fatal(string(b))
				}
				in := doc["inbounds"].([]any)[0].(map[string]any)
				if name == "sing-box" && in["type"] != "vless" || name == "xray" && in["protocol"] != "vless" {
					t.Fatal(in)
				}
			}
			c.Services[0].Enabled = false
			b, err = Render(c)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), "example.com") {
				t.Fatal("disabled service rendered")
			}
		})
	}
}
func TestExtractRejectsLinksAndDuplicateBinaries(t *testing.T) {
	for _, kind := range []string{"valid", "symlink", "duplicate"} {
		var out bytes.Buffer
		gz := gzip.NewWriter(&out)
		tw := tar.NewWriter(gz)
		n := 1
		if kind == "duplicate" {
			n = 2
		}
		for i := 0; i < n; i++ {
			body := []byte{0x7f, 'E', 'L', 'F', 1}
			h := &tar.Header{Name: "release/realm", Mode: 0700, Size: int64(len(body)), Typeflag: tar.TypeReg}
			if kind == "symlink" {
				h.Typeflag = tar.TypeSymlink
				h.Size = 0
				h.Linkname = "/bin/sh"
			}
			if err := tw.WriteHeader(h); err != nil {
				t.Fatal(err)
			}
			if h.Size > 0 {
				tw.Write(body)
			}
		}
		tw.Close()
		gz.Close()
		_, err := extractBinary(out.Bytes(), "realm", false)
		if (err == nil) != (kind == "valid") {
			t.Fatalf("%s %v", kind, err)
		}
	}
}
