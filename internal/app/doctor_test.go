package app

import (
	"bytes"
	"context"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vioira/viorka-agent/internal/collector"
	"github.com/vioira/viorka-agent/internal/config"
)

func TestDoctorExplicitChecks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := os.WriteFile(path, []byte(`{"panel_url":"https://panel.example.com","node_name":"node","state_dir":"/unused"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args      []string
		ip, panel bool
		code      int
	}{
		{nil, false, false, 0},
		{[]string{"--config", path}, false, false, 2},
		{[]string{"unexpected"}, false, false, 2},
		{[]string{"--unknown"}, false, false, 2},
		{[]string{"--public-ip"}, true, false, 1},
		{[]string{"--panel", "--config", path}, false, true, 0},
		{[]string{"--panel", "--config", path, "--public-ip"}, true, true, 1},
	} {
		var ipCalled, panelCalled bool
		var out, errOut bytes.Buffer
		checks := doctorChecks{
			publicIP: func(ctx context.Context) []collector.IPDiagnostic {
				ipCalled = true
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("missing budget")
				}
				return []collector.IPDiagnostic{{Status: "failed", Failure: "dns"}}
			},
			panel: func(context.Context, config.Local) ([]panelDiagnostic, error) {
				panelCalled = true
				return []panelDiagnostic{{Stage: "tls", Status: "ok"}}, nil
			},
		}
		code := doctorWithChecks(tc.args, &out, &errOut, checks)
		if code != tc.code || ipCalled != tc.ip || panelCalled != tc.panel {
			t.Fatalf("%v: code=%d ip=%v panel=%v", tc.args, code, ipCalled, panelCalled)
		}
	}
}

func TestDoctorRedactsInvalidConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	os.WriteFile(path, []byte(`{"secret-token":"sensitive"}`), 0600)
	var out, errOut bytes.Buffer
	if code := doctor([]string{"--panel", "--config", path}, &out, &errOut); code != 2 {
		t.Fatal(code)
	}
	if strings.Contains(errOut.String(), "secret-token") || strings.Contains(errOut.String(), "sensitive") {
		t.Fatal(errOut.String())
	}
}

func TestPanelTLSChecks(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Local{PanelURL: server.URL, NodeName: "test", StateDir: "/unused", CAFile: path}
	results, err := diagnosePanel(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range results {
		if d.Status != "ok" {
			t.Fatal(results)
		}
	}
	cfg.CAFile = ""
	results, err = diagnosePanel(context.Background(), cfg)
	if err != nil || results[2].Status != "failed" {
		t.Fatal(results, err)
	}
	if requests.Load() != 0 {
		t.Fatal("Panel received HTTP request")
	}
	cfg.CAFile = path
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results, err = diagnosePanel(ctx, cfg)
	if err != nil || results[2].Status == "ok" {
		t.Fatal(results, err)
	}
}

func TestPanelTLSDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var b [4096]byte
		for {
			if _, err := conn.Read(b[:]); err != nil {
				return
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	results, err := diagnosePanel(ctx, config.Local{PanelURL: "https://" + listener.Addr().String(), NodeName: "test", StateDir: "/unused"})
	if err != nil || results[1].Status != "ok" || results[2].Status != "failed" || time.Since(start) > time.Second {
		t.Fatal(results, err)
	}
	<-done
}
