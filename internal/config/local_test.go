package config

import "testing"

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
