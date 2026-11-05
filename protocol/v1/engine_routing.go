package v1

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/netip"
	"strconv"
	"strings"
	"unicode"
)

func routingID(s string) bool {
	if len(s) < 1 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
func routingName(s string) bool {
	return len(s) > 0 && len(s) <= 128 && strings.TrimSpace(s) == s && strings.IndexFunc(s, unicode.IsControl) < 0
}
func (o EngineOutbound) Validate() error {
	bad := errors.New("invalid outbound configuration")
	if !routingID(o.ID) || o.ID == "direct" || o.ID == "reject" || !routingName(o.Name) || !engineHost(o.Server) || o.ServerPort < 1 || o.ServerPort > 65535 || len(o.Username) > 256 || len(o.Password) > 1024 {
		return bad
	}
	switch o.Type {
	case "vless":
		if len(o.Uuid) != 36 || o.Uuid[8] != '-' || o.Uuid[13] != '-' || o.Uuid[18] != '-' || o.Uuid[23] != '-' {
			return bad
		}
		if _, e := hex.DecodeString(strings.ReplaceAll(o.Uuid, "-", "")); e != nil {
			return bad
		}
		key, e := base64.RawURLEncoding.DecodeString(o.PublicKey)
		if e != nil || len(key) != 32 {
			return bad
		}
		sid, e := hex.DecodeString(o.ShortID)
		if e != nil || len(sid) > 8 {
			return bad
		}
		if !engineHost(o.ServerName) || o.Username != "" || o.Password != "" || !o.Tls || o.Flow != "" && o.Flow != "xtls-rprx-vision" {
			return bad
		}
		switch o.Fingerprint {
		case "chrome", "firefox", "safari", "edge", "randomized":
		default:
			return bad
		}
	case "socks", "http":
		if o.Uuid != "" || o.PublicKey != "" || o.ShortID != "" || o.Flow != "" || o.Fingerprint != "" || o.Type == "socks" && o.Tls || o.ServerName != "" && (!o.Tls || !engineHost(o.ServerName)) || (o.Username == "") != (o.Password == "") {
			return bad
		}
	default:
		return bad
	}
	return nil
}
func (r EngineRouting) Validate(services []*EngineService) error {
	bad := errors.New("invalid routing configuration")
	if len(r.OutboundIds) > 32 || len(r.Rules) > 128 {
		return bad
	}
	exits := map[string]bool{"direct": true, "reject": true}
	for _, id := range r.OutboundIds {
		if !routingID(id) || exits[id] {
			return bad
		}
		exits[id] = true
	}
	if !exits[r.Final] {
		return bad
	}
	ins := map[string]bool{}
	for _, s := range services {
		if s != nil {
			ins[s.ID] = true
		}
	}
	seen := map[string]bool{}
	for _, rule := range r.Rules {
		if rule == nil || !routingID(rule.ID) || seen[rule.ID] || !routingName(rule.Name) || !exits[rule.Outbound] {
			return bad
		}
		seen[rule.ID] = true
		if len(rule.Inbound) > 32 || len(rule.Domain) > 64 || len(rule.DomainSuffix) > 64 || len(rule.IpCidr) > 64 || len(rule.Port) > 64 || len(rule.PortRange) > 64 || len(rule.Network) > 2 {
			return bad
		}
		for _, id := range rule.Inbound {
			if !ins[id] {
				return bad
			}
		}
		for _, a := range append(append([]string{}, rule.Domain...), rule.DomainSuffix...) {
			if !engineHost(a) {
				return bad
			}
			if _, e := netip.ParseAddr(a); e == nil {
				return bad
			}
		}
		for _, a := range rule.IpCidr {
			if _, e := netip.ParsePrefix(a); e != nil {
				return bad
			}
		}
		for _, p := range rule.Port {
			if p < 1 || p > 65535 {
				return bad
			}
		}
		for _, p := range rule.PortRange {
			v := strings.Split(p, ":")
			if len(v) != 2 {
				return bad
			}
			a, e := strconv.Atoi(v[0])
			b, f := strconv.Atoi(v[1])
			if e != nil || f != nil || a < 1 || b > 65535 || a > b {
				return bad
			}
		}
		for _, n := range rule.Network {
			if n != "tcp" && n != "udp" {
				return bad
			}
		}
		conditions := len(rule.Domain) + len(rule.DomainSuffix) + len(rule.IpCidr) + len(rule.Port) + len(rule.PortRange) + len(rule.Network)
		if conditions == 0 && !rule.MatchAll || conditions > 0 && rule.MatchAll {
			return bad
		}
	}
	return nil
}
func (c EngineCommand) ValidateRouting(operation string) error {
	bad := errors.New("invalid engine routing")
	if len(c.Outbounds) == 0 && c.Routing == nil {
		return nil
	}
	if c.Name != "sing-box" || operation != "engine.apply" || c.Routing == nil || len(c.Outbounds) != len(c.Routing.OutboundIds) {
		return bad
	}
	if err := c.Routing.Validate(c.Services); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, o := range c.Outbounds {
		if o == nil || seen[o.ID] {
			return bad
		}
		if err := o.Validate(); err != nil {
			return err
		}
		seen[o.ID] = true
	}
	for _, id := range c.Routing.OutboundIds {
		if !seen[id] {
			return bad
		}
	}
	// HTTP CONNECT cannot carry UDP. Reject ambiguous UDP routes rather than dropping traffic silently.
	http := map[string]bool{}
	for _, o := range c.Outbounds {
		http[o.ID] = o.Type == "http"
	}
	if http[c.Routing.Final] {
		return bad
	}
	for _, r := range c.Routing.Rules {
		if r.Enabled && http[r.Outbound] && (len(r.Network) != 1 || r.Network[0] != "tcp") {
			return bad
		}
	}
	return nil
}
