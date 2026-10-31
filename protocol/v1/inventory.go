package v1

import (
	"errors"
	"net/netip"
	"strings"
	"time"
	"unicode"
)

// PublicAddress rejects local, shared NAT, documentation and special-use addresses.
func PublicAddress(value string, ipv4 bool) bool {
	a, err := netip.ParseAddr(value)
	if err != nil || a.Is4() != ipv4 || a.Is4In6() || a.Zone() != "" || !a.IsGlobalUnicast() || a.IsPrivate() {
		return false
	}
	for _, prefix := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "2001::/32", "2002::/16"} {
		if netip.MustParsePrefix(prefix).Contains(a) {
			return false
		}
	}
	return ipv4 || netip.MustParsePrefix("2000::/3").Contains(a)
}

func (v NodeInventory) Validate(now time.Time) error {
	invalid := errors.New("invalid node inventory")
	if v.SampledAt.IsZero() || v.SampledAt.After(now.Add(time.Minute)) || v.SampledAt.Before(now.Add(-15*time.Minute)) || v.Errors == nil || len(v.Errors) > 3 {
		return invalid
	}
	for _, field := range []struct {
		value string
		limit int
	}{{v.OSID, 64}, {v.OSName, 256}, {v.OSVersion, 64}} {
		if len(field.value) > field.limit || strings.IndexFunc(field.value, unicode.IsControl) >= 0 {
			return invalid
		}
	}
	seen := map[string]bool{}
	for _, code := range v.Errors {
		if seen[code] || (code != "os_release_unavailable" && code != "public_ipv4_unavailable" && code != "public_ipv6_unavailable") {
			return invalid
		}
		seen[code] = true
	}
	if (v.OSName == "") != seen["os_release_unavailable"] {
		return invalid
	}
	for _, entry := range []struct {
		value, code string
		ipv4        bool
	}{{v.PublicIpv4, "public_ipv4_unavailable", true}, {v.PublicIpv6, "public_ipv6_unavailable", false}} {
		if (entry.value == "") != seen[entry.code] || (entry.value != "" && !PublicAddress(entry.value, entry.ipv4)) {
			return invalid
		}
	}
	return nil
}
