package collector

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
