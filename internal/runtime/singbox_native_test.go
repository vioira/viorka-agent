package runtime

import (
	v1 "github.com/vioira/viorka-agent/protocol/v1"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Optional actual pinned-engine syntax verification, separate from supervisor mocks.
func TestSingboxNativeEgressCheck(t *testing.T) {
	binary := os.Getenv("VIORKA_TEST_SINGBOX")
	if binary == "" {
		t.Skip("set VIORKA_TEST_SINGBOX to the pinned sing-box binary")
	}
	c := renderCommand(t, "sing-box")
	c.Outbounds = []*v1.EngineOutbound{
		{ID: "proxy", Name: "VLESS", Type: "vless", Server: "example.com", ServerPort: 443, Uuid: c.Services[0].Uuid, Tls: true, ServerName: "example.com", PublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", ShortID: "aabb", Fingerprint: "chrome", Flow: "xtls-rprx-vision"},
		{ID: "socks", Name: "SOCKS", Type: "socks", Server: "127.0.0.1", ServerPort: 1080},
		{ID: "http", Name: "HTTP", Type: "http", Server: "example.com", ServerPort: 443, Tls: true},
	}
	c.Routing = &v1.EngineRouting{OutboundIds: []string{"proxy", "socks", "http"}, Final: "reject", Sniff: true, Rules: []*v1.EngineRouteRule{{ID: "domain", Name: "Domain", Enabled: true, DomainSuffix: []string{"example.com"}, PortRange: []string{"80:443"}, Network: []string{"tcp"}, Outbound: "http"}, {ID: "ip", Name: "IP", Enabled: true, IpCidr: []string{"192.0.2.0/24"}, Outbound: "socks"}}}
	files, err := renderSingboxFiles(c)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, data := range files {
		if err = os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if output, err := exec.Command(binary, "check", "-C", dir).CombinedOutput(); err != nil {
		t.Fatalf("native check: %v %s", err, output)
	}
}
