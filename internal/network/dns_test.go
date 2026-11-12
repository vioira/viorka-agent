package network

import (
	"context"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"github.com/coder/websocket"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A real TCP DNS fixture: reads the length-prefixed query and returns an A
// answer, NXDOMAIN, or no response. No public DNS/network is involved.
func dnsServer(t *testing.T, mode string) (string, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Go(func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Go(func() {
				defer c.Close()
				c.SetDeadline(time.Now().Add(2 * time.Second))
				var size [2]byte
				if _, err := io.ReadFull(c, size[:]); err != nil {
					return
				}
				q := make([]byte, binary.BigEndian.Uint16(size[:]))
				if _, err := io.ReadFull(c, q); err != nil {
					return
				}
				calls.Add(1)
				if mode == "silent" {
					<-stop
					return
				}
				end := 12
				for q[end] != 0 {
					end += int(q[end]) + 1
				}
				end += 5
				answer := append([]byte{}, q[:end]...)
				answer[2], answer[3] = 0x81, 0x80
				answer[6], answer[7], answer[10], answer[11] = 0, 0, 0, 0
				if mode == "fail" {
					answer[3] = 0x83
				} else if binary.BigEndian.Uint16(q[end-4:end-2]) == 1 {
					answer[7] = 1
					answer = append(answer, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 127, 0, 0, 1)
				}
				binary.BigEndian.PutUint16(size[:], uint16(len(answer)))
				c.Write(append(size[:], answer...))
			})
		}
	})
	t.Cleanup(func() { ln.Close(); close(stop); wg.Wait() })
	return ln.Addr().String(), &calls
}

func fixtureDNS(t *testing.T, first string, backups ...string) *DNS {
	t.Helper()
	d := NewDNS(backups)
	d.resolvers[0] = NewDNS([]string{first}).resolvers[1]
	d.attemptTimeout = 50 * time.Millisecond
	return d
}

func TestSystemDefault(t *testing.T) {
	d := NewDNS(nil)
	if len(d.resolvers) != 1 || d.resolvers[0] != net.DefaultResolver {
		t.Fatal("system resolver changed")
	}
	ips, err := d.LookupIPAddr(context.Background(), "127.0.0.1")
	if err != nil || len(ips) != 1 {
		t.Fatal(ips, err)
	}
}

func TestDNSFallback(t *testing.T) {
	for _, mode := range []string{"fail", "silent", "ok"} {
		t.Run(mode, func(t *testing.T) {
			first, firstCalls := dnsServer(t, mode)
			backup, backupCalls := dnsServer(t, "ok")
			d := fixtureDNS(t, first, backup)
			ips, err := d.LookupIPAddr(context.Background(), "panel.test.")
			if err != nil || len(ips) != 1 || ips[0].IP.String() != "127.0.0.1" {
				t.Fatal(ips, err)
			}
			if firstCalls.Load() == 0 || (backupCalls.Load() > 0) != (mode != "ok") {
				t.Fatal("incorrect query fallback")
			}
		})
	}
}

func TestDNSFailureAndCancellation(t *testing.T) {
	first, _ := dnsServer(t, "fail")
	second, _ := dnsServer(t, "fail")
	d := fixtureDNS(t, first, second)
	_, err := d.LookupIPAddr(context.Background(), "panel.test.")
	var dnsErr *net.DNSError
	if !errors.As(err, &dnsErr) {
		t.Fatal(err)
	}
	silent, _ := dnsServer(t, "silent")
	d = fixtureDNS(t, silent, second)
	for _, dial := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		if dial {
			_, err = d.DialContext(ctx, "tcp", "panel.test.:443")
		} else {
			_, err = d.LookupIPAddr(ctx, "panel.test.")
		}
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		ctx, cancel = context.WithCancel(context.Background())
		cancel()
		_, err = d.DialContext(ctx, "tcp", "panel.test.:443")
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
}

func TestDialFallbackAndTLSHostname(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws" {
			c, err := websocket.Accept(w, r, nil)
			if err == nil {
				c.CloseNow()
			}
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	for _, mode := range []string{"fail", "silent"} {
		first, _ := dnsServer(t, mode)
		backup, calls := dnsServer(t, "ok")
		d := fixtureDNS(t, first, backup)
		transport := server.Client().Transport.(*http.Transport).Clone()
		transport.DialContext = d.DialContext
		transport.TLSClientConfig.RootCAs = roots
		client := &http.Client{Transport: transport, Timeout: time.Second}
		// httptest's certificate includes example.com, but not wrong.test.
		res, err := client.Get("https://example.com:" + port)
		if err != nil {
			t.Fatal(mode, err)
		}
		res.Body.Close()
		ws, _, err := websocket.Dial(context.Background(), "wss://example.com:"+port+"/ws", &websocket.DialOptions{HTTPClient: client})
		if err != nil {
			t.Fatal(err)
		}
		ws.CloseNow()
		_, err = client.Get("https://wrong.test:" + port)
		var hostname x509.HostnameError
		if !errors.As(err, &hostname) {
			t.Fatalf("hostname validation lost: %v", err)
		}
		if calls.Load() == 0 {
			t.Fatal("no backup queries")
		}
		transport.CloseIdleConnections()
	}
}

func TestConnectionFailureDoesNotChangeDNS(t *testing.T) {
	first, _ := dnsServer(t, "ok")
	backup, calls := dnsServer(t, "ok")
	d := fixtureDNS(t, first, backup)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := strings.Replace(ln.Addr().String(), "127.0.0.1", "panel.test.", 1)
	ln.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if c, err := d.DialContext(ctx, "tcp4", address); err == nil {
		c.Close()
		t.Fatal("expected refusal")
	}
	if calls.Load() != 0 {
		t.Fatal("connection refusal retried DNS")
	}
}

func TestBackupQueryTimeoutAdvancesServer(t *testing.T) {
	first, _ := dnsServer(t, "fail")
	silent, queried := dnsServer(t, "silent")
	last, answered := dnsServer(t, "ok")
	d := fixtureDNS(t, first, silent, last)
	ips, err := d.LookupIPAddr(context.Background(), "panel.test.")
	if err != nil || len(ips) == 0 || queried.Load() == 0 || answered.Load() == 0 {
		t.Fatal(ips, err)
	}
}
