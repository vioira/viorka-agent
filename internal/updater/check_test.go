package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fixture(tag string) release {
	r := release{Tag: tag}
	for _, name := range []string{"viorka-agent-linux-amd64", "viorka-agent-linux-amd64.sha256"} {
		r.Assets = append(r.Assets, struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		}{name, "https://github.com/vioira/viorka-agent/releases/download/" + tag + "/" + name})
	}
	return r
}
func TestVersions(t *testing.T) {
	for _, s := range []string{"1", "01.0.0", "1.0.0-01", "1.0.0-beta.01", "1.0.0-", "1.0.0;id"} {
		if _, err := parseVersion(s); err == nil {
			t.Errorf("accepted %s", s)
		}
	}
	ordered := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "2.0.0", "10.0.0", "9999999999999999999999999.0.0"}
	for i := 1; i < len(ordered); i++ {
		a, _ := parseVersion(ordered[i-1])
		b, _ := parseVersion(ordered[i])
		if a.compare(b) >= 0 || b.compare(a) <= 0 {
			t.Fatal(ordered[i])
		}
	}
	a, _ := parseVersion("v1.0.0+one")
	b, _ := parseVersion("1.0.0+two")
	if a.compare(b) != 0 {
		t.Fatal("build metadata changes precedence")
	}
}
func TestSelection(t *testing.T) {
	for _, tc := range []struct {
		name, current, os, arch, status, candidate string
		preview                                    bool
		releases                                   []release
	}{
		{"stable", "1.0.0", "linux", "amd64", "update-available", "v1.10.0", false, []release{fixture("v1.2.0"), fixture("v1.10.0"), fixture("v2.0.0-rc.1"), fixture("v01.0.0")}},
		{"preview", "1.0.0", "linux", "amd64", "update-available", "v2.0.0-rc.1", true, []release{fixture("v1.9.0"), fixture("v2.0.0-rc.1")}},
		{"equal", "1.0.0+local", "linux", "amd64", "up-to-date", "v1.0.0", false, []release{fixture("v1.0.0")}},
		{"ahead", "2.0.0", "linux", "amd64", "ahead", "v1.0.0", false, []release{fixture("v1.0.0")}},
		{"missing", "0.1.0", "linux", "arm64", "asset-missing", "v1.0.0", false, []release{fixture("v1.0.0")}},
		{"platform", "0.1.0", "darwin", "arm64", "unsupported-platform", "", false, nil},
		{"empty", "0.1.0", "linux", "amd64", "no-release", "", false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, _ := json.Marshal(tc.releases)
			client := jsonClient(string(data))
			r, err := check(context.Background(), client, tc.current, tc.os, tc.arch, tc.preview)
			if err != nil || r.Status != tc.status || r.Candidate != tc.candidate {
				t.Fatalf("%+v %v", r, err)
			}
			if r.Upgrade != "" && !strings.HasSuffix(r.Upgrade, "--version="+tc.candidate) {
				t.Fatal(r)
			}
		})
	}
	for _, mode := range []string{"draft", "flag", "checksum", "source"} {
		t.Run(mode, func(t *testing.T) {
			r := fixture("v1.0.0")
			want := "no-release"
			switch mode {
			case "draft":
				r.Draft = true
			case "flag":
				r.Prerelease = true
			case "checksum":
				r.Assets = r.Assets[:1]
				want = "checksum-missing"
			case "source":
				r.Assets[0].URL = "https://evil.example/binary"
				want = "asset-missing"
			}
			data, _ := json.Marshal([]release{r})
			got, err := check(context.Background(), jsonClient(string(data)), "0.1.0", "linux", "amd64", false)
			if err != nil || got.Status != want {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}
func jsonClient(body string) *http.Client {
	return &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
		if !strings.HasPrefix(req.URL.String(), releasesURL+"?") {
			panic("wrong source")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
}
func TestFailures(t *testing.T) {
	for _, body := range []string{"{", strings.Repeat(" ", maxResponse+1)} {
		if _, err := check(context.Background(), jsonClient(body), "0.1.0", "linux", "amd64", false); err == nil {
			t.Fatal("accepted invalid response")
		}
	}
	for _, status := range []int{403, 404, 500} {
		client := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("[]"))}, nil
		})}
		if _, err := check(context.Background(), client, "0.1.0", "linux", "amd64", false); err == nil {
			t.Fatal(status)
		}
	}
	dnsErr := &net.DNSError{Err: "fixture", Name: "api.github.com", IsNotFound: true}
	client := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) { return nil, dnsErr })}
	if _, err := check(context.Background(), client, "0.1.0", "linux", "amd64", false); !errors.Is(err, dnsErr) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() })
	if _, err := check(ctx, client, "0.1.0", "linux", "amd64", false); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
	if _, err := check(ctx, client, "0.1.0", "linux", "amd64", false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
func TestTLSValidation(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "[]") }))
	defer server.Close()
	tr := http.DefaultTransport.(*http.Transport).Clone()
	defer tr.CloseIdleConnections()
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	if _, err := check(context.Background(), &http.Client{Transport: tr}, "0.1.0", "linux", "amd64", false); err == nil {
		t.Fatal("accepted untrusted TLS")
	}
}
func TestPagination(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		rels := make([]release, 100)
		if calls == 2 {
			rels = []release{fixture("v2.0.0")}
		}
		data, _ := json.Marshal(rels)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(data)))}, nil
	})}
	r, err := check(context.Background(), client, "1.0.0", "linux", "amd64", false)
	if err != nil || calls != 2 || r.Candidate != "v2.0.0" {
		t.Fatalf("%+v %v calls=%d", r, err, calls)
	}
}
