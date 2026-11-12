// Package config provides strictly local non-secret bootstrap configuration.
package config

import (
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"strings"
)

type Local struct {
	TCPProbe           *TCPProbe     `json:"tcp_probe,omitempty"`
	DNSFallbackServers []string      `json:"dns_fallback_servers,omitempty"`
	TelemetryV3        bool          `json:"telemetry_v3"`
	NetworkFilter      NetworkFilter `json:"network_filter,omitempty"`
	PanelURL           string        `json:"panel_url"`
	NodeName           string        `json:"node_name"`
	StateDir           string        `json:"state_dir"`
	LogLevel           string        `json:"log_level"`
	CAFile             string        `json:"ca_file,omitempty"`
}

func LoadFile(path string) (Local, error) {
	f, err := os.Open(path)
	if err != nil {
		return Local{}, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	cfg := Local{TelemetryV3: true, LogLevel: "warn"}
	if err := dec.Decode(&cfg); err != nil {
		return Local{}, err
	}
	var rest any
	if err := dec.Decode(&rest); err != io.EOF {
		return Local{}, fmt.Errorf("unexpected trailing JSON content")
	}
	return cfg, cfg.Validate()
}

func (c Local) Validate() error {
	if c.TCPProbe != nil {
		if err := c.TCPProbe.Validate(); err != nil {
			return err
		}
	}
	if len(c.DNSFallbackServers) > 3 {
		return fmt.Errorf("dns_fallback_servers allows at most 3 servers")
	}
	seen := make(map[netip.AddrPort]bool)
	for _, server := range c.DNSFallbackServers {
		addr, err := netip.ParseAddrPort(server)
		ip := addr.Addr().Unmap()
		if err != nil || addr.Port() == 0 || ip.IsUnspecified() || ip.IsMulticast() || ip == netip.AddrFrom4([4]byte{255, 255, 255, 255}) || ip.Zone() != "" {
			return fmt.Errorf("dns_fallback_servers requires unicast IP:port addresses with ports 1–65535")
		}
		addr = netip.AddrPortFrom(addr.Addr().Unmap(), addr.Port())
		if seen[addr] {
			return fmt.Errorf("duplicate dns_fallback_servers address")
		}
		seen[addr] = true
	}

	if err := c.NetworkFilter.Validate(); err != nil {
		return err
	}
	u, err := url.Parse(c.PanelURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return fmt.Errorf("panel_url must be an HTTPS base origin without credentials, query, path or fragment")
	}
	if u.Scheme != "https" {
		return fmt.Errorf("panel_url must use HTTPS")
	}
	if strings.TrimSpace(c.NodeName) == "" || strings.TrimSpace(c.StateDir) == "" {
		return fmt.Errorf("node_name and state_dir are required")
	}
	if c.LogLevel != "" && c.LogLevel != "debug" && c.LogLevel != "info" && c.LogLevel != "warn" && c.LogLevel != "error" {
		return fmt.Errorf("unsupported log_level")
	}
	return nil
}
