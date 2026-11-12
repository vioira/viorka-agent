package collector

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseRelease(t *testing.T) {
	for _, tc := range []struct{ data, id, name, version string }{
		{"ID=debian\nPRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nVERSION_ID=\"12\"", "debian", "Debian GNU/Linux 12 (bookworm)", "12"},
		{"ID=ubuntu\nNAME='Ubuntu'\nVERSION_ID=24.04", "ubuntu", "Ubuntu 24.04", "24.04"},
		{"ID=alpine\nPRETTY_NAME=\"Alpine Linux v3.20\"\nVERSION_ID=3.20", "alpine", "Alpine Linux v3.20", "3.20"},
		{"PRETTY_NAME=\"bad\\nname\"", "", "", ""},
	} {
		id, name, version := parseRelease(tc.data)
		if id != tc.id || name != tc.name || version != tc.version {
			t.Fatalf("%q: %s/%s/%s", tc.data, id, name, version)
		}
	}
}
func TestPublicIPResponse(t *testing.T) {
	for _, tc := range []struct {
		body     string
		status   int
		ipv4, ok bool
	}{
		{"8.8.8.8\n", 200, true, true}, {"2606:4700:4700::1111", 200, false, true},
		{"127.0.0.1", 200, true, false}, {"10.0.0.1", 200, true, false}, {"100.64.0.1", 200, true, false},
		{"192.0.2.1", 200, true, false}, {"8.8.8.8", 200, false, false}, {"fe80::1", 200, false, false},
		{strings.Repeat("a", 65), 200, true, false}, {"8.8.8.8", 500, true, false}, {"8.8.8.8", 302, true, false},
	} {
		t.Run(tc.body, func(t *testing.T) {
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer s.Close()
			_, err := fetchPublicIP(context.Background(), s.Client(), s.URL, tc.ipv4)
			if (err == nil) != tc.ok {
				t.Fatalf("%v", err)
			}
		})
	}
}
func TestInventoryFailureAndCache(t *testing.T) {
	s := NewInventory()
	calls := 0
	s.lookup = func(context.Context, bool) (string, error) { calls++; return "", fmt.Errorf("unavailable") }
	v := s.Sample(context.Background())
	s.Sample(context.Background())
	if calls != 2 || v.PublicIpv4 != "" || v.PublicIpv6 != "" || len(v.Errors) < 2 {
		t.Fatalf("%+v calls %d", v, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := fetchPublicIP(ctx, http.DefaultClient, "https://api.ipify.org", true)
	if err == nil {
		t.Fatal("cancel ignored")
	}
}

func TestInventoryLongIntervalReconnectRefreshesExpiredSnapshot(t *testing.T) {
	s := NewInventory()
	calls := 0
	s.lookup = func(context.Context, bool) (string, error) { calls++; return "", fmt.Errorf("unavailable") }
	s.SampleEvery(context.Background(), time.Hour)
	s.cached.SampledAt = time.Now().Add(-20 * time.Minute)
	v := s.SampleEvery(context.Background(), time.Hour)
	if calls != 4 || time.Since(v.SampledAt) > time.Minute {
		t.Fatal("stale inventory reused on reconnect")
	}
}

func TestPublicIPTraceResponse(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		ipv4, ok   bool
	}{
		{"ipv4", "fl=123\nip=8.8.8.8\nloc=US\n", true, true},
		{"ipv6", "ip=2606:4700:4700::1111\n", false, true},
		{"wrong_family", "ip=8.8.8.8\n", false, false},
		{"private", "ip=10.0.0.1\n", true, false},
		{"unrelated_ip", "error: upstream 8.8.8.8 unavailable", true, false},
		{"oversized", "ip=8.8.8.8\n" + strings.Repeat("x", 4096), true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, tc.body)
			}))
			defer s.Close()
			_, err := fetchPublicIP(context.Background(), s.Client(), s.URL+"/cdn-cgi/trace", tc.ipv4)
			if (err == nil) != tc.ok {
				t.Fatalf("unexpected result: %v", err)
			}
		})
	}
}

func TestPublicIPSourceFallback(t *testing.T) {
	for _, failure := range []string{"status", "invalid", "timeout", "redirect"} {
		t.Run(failure, func(t *testing.T) {
			var paths []string
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/first" && failure == "timeout" {
					<-r.Context().Done()
					return
				}
				paths = append(paths, r.URL.Path)
				if r.URL.Path == "/first" {
					switch failure {
					case "status":
						w.WriteHeader(http.StatusServiceUnavailable)
					case "invalid":
						fmt.Fprint(w, "10.0.0.1")
					case "redirect":
						http.Redirect(w, r, "/unexpected", http.StatusFound)
					}
					return
				}
				fmt.Fprint(w, "8.8.8.8")
			}))
			defer s.Close()
			client := s.Client()
			client.Timeout = 200 * time.Millisecond
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			ip, err := lookupPublicIPFromEndpoints(context.Background(), client, []string{s.URL + "/first", s.URL + "/second", s.URL + "/third"}, true)
			if err != nil || ip != "8.8.8.8" {
				t.Fatal(ip, err)
			}
			want := []string{"/first", "/second"}
			if failure == "timeout" {
				want = []string{"/second"}
			}
			if !reflect.DeepEqual(paths, want) {
				t.Fatal(paths)
			}
		})
	}
}

func TestPublicIPAllSourcesFailAndCancellation(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer s.Close()
	endpoints := []string{s.URL, s.URL}
	if ip, err := lookupPublicIPFromEndpoints(context.Background(), s.Client(), endpoints, true); err == nil || ip != "" {
		t.Fatal(ip, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := lookupPublicIPFromEndpoints(ctx, s.Client(), endpoints, true); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
