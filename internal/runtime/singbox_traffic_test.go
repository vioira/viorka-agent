package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Exercise the generated egress with real sing-box traffic on loopback. A test
// HTTP inbound replaces VLESS only for this harness; systemd and remote TLS are
// deliberately outside this test's claim.
func TestSingboxNativeRoutingTraffic(t *testing.T) {
	binaryPath := os.Getenv("VIORKA_TEST_SINGBOX")
	if binaryPath == "" {
		t.Skip("set VIORKA_TEST_SINGBOX to the pinned sing-box binary")
	}
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "direct") }))
	defer direct.Close()
	targetURL, _ := url.Parse(direct.URL)
	_, targetPortText, _ := net.SplitHostPort(targetURL.Host)
	targetPort, _ := strconv.Atoi(targetPortText)
	socks, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer socks.Close()
	socksDone := make(chan struct{})
	defer func() { socks.Close(); <-socksDone }()
	go func() {
		defer close(socksDone)
		for {
			conn, e := socks.Accept()
			if e != nil {
				return
			}
			func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				reader := bufio.NewReader(conn)
				head := make([]byte, 2)
				if _, e = io.ReadFull(reader, head); e != nil {
					return
				}
				methods := make([]byte, int(head[1]))
				if _, e = io.ReadFull(reader, methods); e != nil {
					return
				}
				conn.Write([]byte{5, 0})
				request := make([]byte, 4)
				if _, e = io.ReadFull(reader, request); e != nil {
					return
				}
				size := 0
				switch request[3] {
				case 1:
					size = 4
				case 4:
					size = 16
				case 3:
					n, e := reader.ReadByte()
					if e != nil {
						return
					}
					size = int(n)
				default:
					return
				}
				if _, e = io.CopyN(io.Discard, reader, int64(size+2)); e != nil {
					return
				}
				conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
				if _, e = http.ReadRequest(reader); e != nil {
					return
				}
				io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 5\r\nConnection: close\r\n\r\nproxy")
			}()
		}
	}()
	port := socks.Addr().(*net.TCPAddr).Port
	c := renderCommand(t, "sing-box")
	c.Services = nil
	c.Outbounds = []*v1.EngineOutbound{{ID: "proxy", Name: "Proxy", Type: "socks", Server: "127.0.0.1", ServerPort: uint64(port)}}
	c.Routing = &v1.EngineRouting{OutboundIds: []string{"proxy"}, Final: "reject", Rules: []*v1.EngineRouteRule{
		{ID: "domain", Name: "Domain and port", Enabled: true, Domain: []string{"localhost"}, Port: []uint64{uint64(targetPort)}, Outbound: "proxy"},
		{ID: "direct", Name: "Local direct", Enabled: true, Port: []uint64{uint64(targetPort)}, Outbound: "direct"},
	}}
	raw, err := Render(c)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err = json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	inPort := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	config["inbounds"] = []any{map[string]any{"type": "http", "tag": "test", "listen": "127.0.0.1", "listen_port": inPort}}
	raw, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath, "run", "-c", path)
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = cmd.Wait() }()
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(inPort))
	ready := false
	for i := 0; i < 100; i++ {
		conn, e := net.DialTimeout("tcp", address, 50*time.Millisecond)
		if e == nil {
			conn.Close()
			ready = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("native listener did not start")
	}
	proxyURL, _ := url.Parse("http://" + address)
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	for _, tc := range []struct{ url, want string }{{"http://localhost:" + targetPortText + "/", "proxy"}, {direct.URL, "direct"}} {
		res, e := client.Get(tc.url)
		if e != nil {
			t.Fatal(e)
		}
		body, e := io.ReadAll(res.Body)
		res.Body.Close()
		if e != nil || string(body) != tc.want {
			t.Fatal("wrong route", tc.want, string(body), e)
		}
	}
	socks.Close()
	failed, failErr := client.Get("http://localhost:" + targetPortText + "/")
	if failErr == nil {
		defer failed.Body.Close()
		if failed.StatusCode < 400 {
			t.Fatal("failed proxy silently fell back to direct")
		}
	}
	// A different port matches neither rule and must be rejected, never fall back.
	res, e := client.Get("http://127.0.0.1:1/")
	if e == nil {
		defer res.Body.Close()
		if res.StatusCode < 400 {
			t.Fatal("default reject allowed traffic")
		}
	}
}
