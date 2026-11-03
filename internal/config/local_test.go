package config

import (
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

func TestMetricsIntervalBounds(t *testing.T) {
	for _, n := range []int{-1, 1, 9, 301} {
		c := Local{PanelURL: "https://panel.example.com", NodeName: "node", StateDir: "state", MetricsSeconds: n}
		if c.Validate() == nil {
			t.Fatalf("accepted interval %d", n)
		}
	}
	for _, n := range []int{0, 10, 30, 300} {
		c := Local{PanelURL: "https://panel.example.com", NodeName: "node", StateDir: "state", MetricsSeconds: n}
		if c.Validate() != nil {
			t.Fatalf("rejected interval %d", n)
		}
		if n == 0 && c.MetricsIntervalSeconds() != 30 {
			t.Fatal("default")
		}
	}
}

func TestTelemetryStartupGate(t *testing.T) {
	for _, value := range []struct {
		field   string
		enabled bool
		valid   bool
	}{
		{"", false, true}, {`,"telemetry_v3":false`, false, true}, {`,"telemetry_v3":true`, true, true}, {`,"telemetry_v3":"true"`, false, false},
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
	}
}
