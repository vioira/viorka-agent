package collector

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"
)

type diagnosticTransport func(*http.Request) (*http.Response, error)

func (f diagnosticTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestIPDiagnosticFailureStages(t *testing.T) {
	for _, stage := range []string{"dns", "connection", "tls", "http"} {
		t.Run(stage, func(t *testing.T) {
			client := &http.Client{Transport: diagnosticTransport(func(r *http.Request) (*http.Response, error) {
				trace := httptrace.ContextClientTrace(r.Context())
				switch stage {
				case "dns":
					return nil, &net.DNSError{Err: "sensitive", Name: "secret"}
				case "tls":
					trace.TLSHandshakeStart()
					trace.TLSHandshakeDone(tls.ConnectionState{}, fmt.Errorf("sensitive"))
				case "http":
					trace.GotConn(httptrace.GotConnInfo{})
				}
				return nil, fmt.Errorf("sensitive")
			})}
			d := diagnoseIPFamily(context.Background(), client, []string{"https://example.com"}, true)[0]
			if d.Status != "failed" || d.Failure != stage || d.Address != "" {
				t.Fatal(d)
			}
		})
	}
}

func TestIPDiagnosticsAllSourcesAndBudgets(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			fmt.Fprint(w, "8.8.8.8")
		case "/format":
			fmt.Fprint(w, strings.Repeat("secret", 20))
		case "/redirect":
			http.Redirect(w, r, "/ok", 302)
		case "/timeout":
			<-r.Context().Done()
		default:
			w.WriteHeader(503)
		}
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 50 * time.Millisecond
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	var endpoints []string
	for _, path := range []string{"/ok", "/format", "/redirect", "/status", "/timeout"} {
		endpoints = append(endpoints, server.URL+path)
	}
	results := diagnoseIPFamily(context.Background(), client, endpoints, true)
	for i, want := range []string{"", "format", "http", "http", "http"} {
		if results[i].Failure != want || results[i].Family != "ipv4" {
			t.Fatal(results)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results = diagnoseIPFamily(ctx, client, endpoints, false)
	for _, d := range results {
		if d.Status != "skipped" || d.Failure != "budget" || d.Family != "ipv6" {
			t.Fatal(d)
		}
	}
	results = diagnoseIPFamily(context.Background(), &http.Client{Timeout: time.Second}, []string{server.URL}, true)
	if results[0].Failure != "tls" {
		t.Fatal(results)
	}
}
