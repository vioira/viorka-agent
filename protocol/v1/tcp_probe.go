package v1

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// ValidateTCPProbeTargets validates both local bootstrap and Panel settings.
func ValidateTCPProbeTargets(targets []string) error {
	if len(targets) > 3 {
		return fmt.Errorf("tcp_probe allows at most 3 targets")
	}
	seen := map[string]bool{}
	for _, target := range targets {
		host, port, err := net.SplitHostPort(target)
		n, e := strconv.Atoi(port)
		if err != nil || e != nil || n < 1 || n > 65535 || len(target) > 259 || host == "" || strings.ContainsAny(host, "/%@\\ \t\r\n") {
			return fmt.Errorf("tcp_probe target must be host:port")
		}
		if ip, err := netip.ParseAddr(host); err == nil {
			if !PublicProbeIP(ip) {
				return fmt.Errorf("tcp_probe target must be a public address")
			}
			host = ip.Unmap().String()
		} else {
			if len(host) > 253 || !strings.Contains(host, ".") {
				return fmt.Errorf("tcp_probe requires a public DNS hostname")
			}
			for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
				if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
					return fmt.Errorf("invalid tcp_probe hostname")
				}
				for _, c := range label {
					if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
						return fmt.Errorf("invalid tcp_probe hostname")
					}
				}
			}
		}
		key := net.JoinHostPort(strings.ToLower(strings.TrimSuffix(host, ".")), strconv.Itoa(n))
		if seen[key] {
			return fmt.Errorf("duplicate tcp_probe target")
		}
		seen[key] = true
	}
	return nil
}

// PublicProbeIP reuses inventory public-address rules and excludes additional
// IPv6 special-purpose ranges before any outbound connection.
func PublicProbeIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	return PublicAddress(ip.String(), ip.Is4()) &&
		!netip.MustParsePrefix("2001::/23").Contains(ip) &&
		!netip.MustParsePrefix("3fff::/20").Contains(ip) &&
		!netip.MustParsePrefix("192.88.99.0/24").Contains(ip)
}
