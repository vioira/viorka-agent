package runtime

import (
	v1 "github.com/vioira/viorka-agent/protocol/v1"
	"testing"
)

func TestPlainVLESSEgress(t *testing.T) {
	o := &v1.EngineOutbound{ID: "plain", Name: "Plain", Type: "vless", Server: "example.com", ServerPort: 443, Uuid: "12345678-1234-4234-8234-123456789012"}
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	c := renderCommand(t, "sing-box")
	c.Outbounds = []*v1.EngineOutbound{o}
	out := singboxEgress(c)["outbounds"].([]any)[1].(map[string]any)
	if out["uuid"] != o.Uuid || out["type"] != "vless" || out["server"] != o.Server {
		t.Fatalf("incorrect outbound: %v", out)
	}
	for _, key := range []string{"tls", "flow", "transport"} {
		if _, ok := out[key]; ok {
			t.Fatalf("unexpected %s", key)
		}
	}
}
