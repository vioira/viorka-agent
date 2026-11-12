package collector

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

// Inventory is reused across reconnects to bound public-address discovery traffic.
type Inventory struct {
	cached   v1.NodeInventory
	readFile func(string) ([]byte, error)
	lookup   func(context.Context, bool) (string, error)
}

func NewInventory() *Inventory { return &Inventory{readFile: readRelease, lookup: lookupPublicIP} }
func readRelease(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 16385))
	if len(b) > 16384 {
		return nil, fmt.Errorf("os-release too large")
	}
	return b, err
}
func (s *Inventory) Sample(ctx context.Context) v1.NodeInventory {
	return s.SampleEvery(ctx, 5*time.Minute)
}
func (s *Inventory) SampleEvery(ctx context.Context, interval time.Duration) v1.NodeInventory {
	if !s.cached.SampledAt.IsZero() && time.Since(s.cached.SampledAt) < min(interval, 10*time.Minute) {
		return s.cached
	}
	v := v1.NodeInventory{Errors: []string{}}
	if runtime.GOOS == "linux" {
		b, err := s.readFile("/etc/os-release")
		if os.IsNotExist(err) {
			b, err = s.readFile("/usr/lib/os-release")
		}
		if err == nil {
			v.OSID, v.OSName, v.OSVersion = parseRelease(string(b))
		}
	}
	if v.OSName == "" {
		v.Errors = append(v.Errors, "os_release_unavailable")
	}
	var err error
	v.PublicIpv4, err = s.lookup(ctx, true)
	if err != nil {
		v.PublicIpv4 = ""
		v.Errors = append(v.Errors, "public_ipv4_unavailable")
	}
	v.PublicIpv6, err = s.lookup(ctx, false)
	if err != nil {
		v.PublicIpv6 = ""
		v.Errors = append(v.Errors, "public_ipv6_unavailable")
	}
	v.SampledAt = time.Now().UTC()
	s.cached = v
	return v
}
func parseRelease(data string) (id, name, version string) {
	values := map[string]string{}
	for _, line := range strings.Split(data, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		if key != "ID" && key != "NAME" && key != "PRETTY_NAME" && key != "VERSION_ID" {
			continue
		}
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, "\"") {
			var err error
			value, err = strconv.Unquote(value)
			if err != nil {
				continue
			}
		} else if strings.HasPrefix(value, "'") {
			if len(value) < 2 || !strings.HasSuffix(value, "'") {
				continue
			}
			value = value[1 : len(value)-1]
		}
		limit := 64
		if key == "NAME" || key == "PRETTY_NAME" {
			limit = 256
		}
		if len(value) > limit || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			continue
		}
		values[key] = value
	}
	name = values["PRETTY_NAME"]
	if name == "" {
		name = strings.TrimSpace(values["NAME"] + " " + values["VERSION_ID"])
	}
	if len(name) > 256 {
		name = ""
	}
	return values["ID"], name, values["VERSION_ID"]
}
func lookupPublicIP(ctx context.Context, ipv4 bool) (string, error) {
	network := "tcp6"
	endpoints := []string{"https://api6.ipify.org", "https://ipv6.icanhazip.com", "https://cloudflare.com/cdn-cgi/trace"}
	if ipv4 {
		network = "tcp4"
		endpoints = []string{"https://www.visa.cn/cdn-cgi/trace", "https://www.qualcomm.cn/cdn-cgi/trace", "https://api.ipify.org"}
	}
	// Direct egress, never a proxy's address. No credentials, redirects or remote URLs.
	transport := &http.Transport{DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, address)
	}, TLSHandshakeTimeout: 3 * time.Second, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return lookupPublicIPFromEndpoints(ctx, client, endpoints, ipv4)
}

// Each address family has its own budget so an unavailable family cannot
// consume the other family's discovery time.
func lookupPublicIPFromEndpoints(ctx context.Context, client *http.Client, endpoints []string, ipv4 bool) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var lastErr error
	for _, endpoint := range endpoints {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		value, err := fetchPublicIP(ctx, client, endpoint, ipv4)
		if err == nil {
			return value, nil
		}
		lastErr = fmt.Errorf("%s: %w", endpoint, err)
	}
	return "", fmt.Errorf("public address sources exhausted: %w", lastErr)
}
func fetchPublicIP(ctx context.Context, client *http.Client, endpoint string, ipv4 bool) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "curl/8.0.1")
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("address discovery HTTP %d", res.StatusCode)
	}
	limit := int64(64)
	trace := strings.HasSuffix(req.URL.Path, "/cdn-cgi/trace")
	if trace {
		limit = 4096
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return "", err
	}
	if int64(len(b)) > limit {
		return "", fmt.Errorf("public address response too large")
	}
	value := strings.TrimSpace(string(b))
	if trace {
		value = ""
		for _, line := range strings.Split(string(b), "\n") {
			if ip, ok := strings.CutPrefix(line, "ip="); ok {
				value = strings.TrimSpace(ip)
				break
			}
		}
	}
	if !v1.PublicAddress(value, ipv4) {
		return "", fmt.Errorf("invalid public address")
	}
	return value, nil
}
