package config

import (
	"fmt"
	"net/netip"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

// TCPProbe supplies local timing and targets for Panels without managed settings.
type TCPProbe struct {
	Targets         []string `json:"targets"`
	IntervalSeconds uint64   `json:"interval_seconds"`
	TimeoutMS       uint64   `json:"timeout_ms"`
	WindowSeconds   uint64   `json:"window_seconds"`
}

func (p TCPProbe) Validate() error {
	if len(p.Targets) > 3 || p.IntervalSeconds < 30 || p.IntervalSeconds > 3600 || p.TimeoutMS < 100 || p.TimeoutMS > 1000 || p.WindowSeconds < 60 || p.WindowSeconds > 3600 || p.WindowSeconds < p.IntervalSeconds {
		return fmt.Errorf("tcp_probe requires at most 3 targets, interval 30–3600s, timeout 100–1000ms, window 60–3600s and window >= interval")
	}
	return v1.ValidateTCPProbeTargets(p.Targets)
}

// PublicProbeIP reuses inventory public-address rules and excludes additional
// IPv6 special-purpose ranges before any outbound connection.
func PublicProbeIP(ip netip.Addr) bool {
	return v1.PublicProbeIP(ip)
}
