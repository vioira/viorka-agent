package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRejectInsecurePanelURL(t *testing.T) {
	for _, raw := range []string{"http://example.com", "https://alice:pass@example.com", "https://example.com?a=b", "https://example.com/v1"} {
		if err := (Local{PanelURL: raw, NodeName: "node", StateDir: "/var/lib/viorka-agent"}).Validate(); err == nil {
			t.Fatalf("unexpected valid URL: %q", raw)
		}
	}
}

func TestAcceptBaseURL(t *testing.T) {
	if err := (Local{PanelURL: "https://panel.example.com:8443", NodeName: "vps-01", StateDir: "/var/lib/viorka-agent"}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestTelemetryStartupGate(t *testing.T) {
	for _, value := range []struct {
		field   string
		enabled bool
		valid   bool
	}{
		{"", true, true}, {`,"telemetry_v3":false`, false, true}, {`,"telemetry_v3":true`, true, true}, {`,"telemetry_v3":"true"`, false, false},
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		raw := `{"panel_url":"https://panel.example.com","node_name":"test","state_dir":"state"` + value.field + `}`
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadFile(path)
		if (err == nil) != value.valid || cfg.TelemetryV3 != value.enabled {
			t.Fatalf("gate %q: %v %v", value.field, cfg.TelemetryV3, err)
		}
		if value.valid {
			encoded, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, encoded, 0600); err != nil {
				t.Fatal(err)
			}
			reloaded, err := LoadFile(path)
			if err != nil || reloaded.TelemetryV3 != value.enabled {
				t.Fatalf("gate changed after saving: %v %v", reloaded.TelemetryV3, err)
			}
		}
	}
}

func TestRetiredMetricsBootstrapRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := os.WriteFile(path, []byte(`{"panel_url":"https://panel.test","node_name":"node","state_dir":"state","metrics_seconds":30}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err == nil {
		t.Fatal("retired monitoring setting accepted")
	}
}

func TestLogLevelConfiguration(t *testing.T) {
	for _, value := range []string{"", "debug", "info", "warn", "error", "trace"} {
		raw := `{"panel_url":"https://panel.test","node_name":"node","state_dir":"state"`
		if value != "" {
			raw += `,"log_level":"` + value + `"`
		}
		raw += `}`
		path := filepath.Join(t.TempDir(), "agent.json")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadFile(path)
		if value == "trace" {
			if err == nil {
				t.Fatal("invalid log level accepted")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		want := value
		if want == "" {
			want = "warn"
		}
		if cfg.LogLevel != want {
			t.Fatalf("got %q want %q", cfg.LogLevel, want)
		}
	}
}

func TestDNSFallbackServers(t *testing.T) {
	for _, tc := range []struct {
		servers []string
		valid   bool
	}{
		{nil, true}, {[]string{"127.0.0.1:53", "[::1]:5353"}, true},
		{[]string{"dns.example.com:53"}, false}, {[]string{"1.1.1.1"}, false},
		{[]string{"1.1.1.1:0"}, false}, {[]string{"1.1.1.1:65536"}, false},
		{[]string{"0.0.0.0:53"}, false}, {[]string{"[::ffff:0.0.0.0]:53"}, false}, {[]string{"255.255.255.255:53"}, false}, {[]string{"224.0.0.1:53"}, false},
		{[]string{"[fe80::1%en0]:53"}, false}, {[]string{"1.1.1.1:53", "1.1.1.1:53"}, false},
		{[]string{"1.1.1.1:53", "1.1.1.2:53", "1.1.1.3:53", "1.1.1.4:53"}, false},
	} {
		cfg := Local{PanelURL: "https://panel.example.com", NodeName: "test", StateDir: "/unused", DNSFallbackServers: tc.servers}
		if err := cfg.Validate(); (err == nil) != tc.valid {
			t.Fatalf("%v: %v", tc.servers, err)
		}
	}
}
