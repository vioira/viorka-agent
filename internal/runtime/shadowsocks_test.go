package runtime

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

// Distinct SS peers accept TCP and UDP respectively. Only the server-side routes
// know the echo ports, so a direct fallback or a wrong outbound cannot pass.
func TestSingboxNativeShadowsocksTraffic(t *testing.T) {
	binary := os.Getenv("VIORKA_TEST_SINGBOX")
	if binary == "" {
		t.Skip("set VIORKA_TEST_SINGBOX to the pinned sing-box binary")
	}
	tcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "tcp via SS") }))
	defer tcp.Close()
	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		var buf [128]byte
		n, peer, e := udp.ReadFrom(buf[:])
		if e == nil {
			_, _ = udp.WriteTo(buf[:n], peer)
		}
	}()
	defer func() { udp.Close(); <-done }()
	// Reserve distinct ports while assembling the config.
	var held []net.Listener
	defer func() {
		for _, l := range held {
			l.Close()
		}
	}()
	port := func() int {
		l, e := net.Listen("tcp4", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		held = append(held, l)
		return l.Addr().(*net.TCPAddr).Port
	}
	tcpPort, udpPort, tcpIn, udpIn := port(), port(), port(), port()
	c := renderCommand(t, "sing-box")
	c.Services = nil
	c.Outbounds = []*v1.EngineOutbound{
		{ID: "tcp", Name: "TCP peer", Type: "shadowsocks", Server: "127.0.0.1", ServerPort: uint64(tcpPort), Method: "aes-128-gcm", Password: "test passphrase"},
		{ID: "udp", Name: "UDP peer", Type: "shadowsocks", Server: "127.0.0.1", ServerPort: uint64(udpPort), Method: "2022-blake3-aes-128-gcm", Password: "AAAAAAAAAAAAAAAAAAAAAA=="},
	}
	c.Routing = &v1.EngineRouting{OutboundIds: []string{"tcp", "udp"}, Final: "tcp", Rules: []*v1.EngineRouteRule{{ID: "udp-rule", Name: "UDP", Enabled: true, Network: []string{"udp"}, Outbound: "udp"}}}
	config := singboxEgress(c)
	inbounds := []any{}
	route := config["route"].(map[string]any)
	rules := []any{}
	for i, network := range []string{"tcp", "udp"} {
		o := c.Outbounds[i]
		tag := "server-" + network
		inbounds = append(inbounds, map[string]any{"type": "shadowsocks", "tag": tag, "listen": "127.0.0.1", "listen_port": o.ServerPort, "network": network, "method": o.Method, "password": o.Password})
		inbounds = append(inbounds, map[string]any{"type": "direct", "tag": "client-" + network, "listen": "127.0.0.1", "listen_port": []int{tcpIn, udpIn}[i], "network": network, "override_address": "127.0.0.1", "override_port": 1})
		destination := []int{tcp.Listener.Addr().(*net.TCPAddr).Port, udp.LocalAddr().(*net.UDPAddr).Port}[i]
		rules = append(rules, map[string]any{"inbound": tag, "action": "route", "outbound": "sb-egress-direct", "override_port": destination})
	}
	route["rules"] = append(rules, route["rules"].([]any)...)
	config["inbounds"] = inbounds
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(binary, "check", "-c", path).CombinedOutput(); err != nil {
		t.Fatalf("check: %s %v", out, err)
	}
	for _, l := range held {
		l.Close()
	}
	held = nil
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "run", "-c", path)
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = cmd.Wait() }()
	client := http.Client{Timeout: 500 * time.Millisecond}
	defer client.CloseIdleConnections()
	var resp *http.Response
	for i := 0; i < 60; i++ {
		resp, err = client.Get("http://127.0.0.1:" + strconv.Itoa(tcpIn))
		if err == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "tcp via SS" {
		t.Fatal("TCP default route failed", err)
	}
	conn, err := net.Dial("udp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(udpIn)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = conn.Write([]byte("udp via SS")); err != nil {
		t.Fatal(err)
	}
	var buf [128]byte
	n, err := conn.Read(buf[:])
	if err != nil || string(buf[:n]) != "udp via SS" {
		t.Fatal("UDP rule failed", err)
	}
}
