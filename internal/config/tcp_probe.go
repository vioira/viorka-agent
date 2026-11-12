package config

import (
	"fmt"
	"net/netip"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

// TCPProbe supplies local timing and targets for Panels without managed settings.
type TCPProbe struct {
	Targets []string `json:"targets"`
	// Deprecated: timing is fixed at 20s / 120s; accepted for existing local config.
	IntervalSeconds uint64 `json:"interval_seconds"`
	TimeoutMS       uint64 `json:"timeout_ms"`
	WindowSeconds   uint64 `json:"window_seconds"`
}

func (p TCPProbe) Validate() error {
	if len(p.Targets) > 3 || p.TimeoutMS < 100 || p.TimeoutMS > 1000 {
		return fmt.Errorf("tcp_probe requires at most 3 targets and timeout 100–1000ms")
	}
	return v1.ValidateTCPProbeTargets(p.Targets)
}

// PublicProbeIP reuses inventory public-address rules and excludes additional
// IPv6 special-purpose ranges before any outbound connection.
func PublicProbeIP(ip netip.Addr) bool {
	return v1.PublicProbeIP(ip)
}
