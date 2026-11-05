package v1

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/netip"
	"strings"
)

const EngineCapability = "engine.manage.v3"

func EngineOperation(op string) bool {
	switch op {
	case "engine.install", "engine.upgrade", "engine.start", "engine.stop", "engine.uninstall", "engine.apply", "engine.inspect":
		return true
	}
	return false
}
func (c EngineCommand) Validate(operation string) error {
	bad := errors.New("invalid engine command")
	if err := c.ValidateRouting(operation); err != nil {
		return err
	}
	if !EngineOperation(operation) || (c.Name != "sing-box" && c.Name != "xray" && c.Name != "realm") || len(c.Version) < 1 || len(c.Version) > 64 || c.Revision > 1<<53-1 || (c.RunIntent != "running" && c.RunIntent != "stopped") || len(c.Services) > 32 {
		return bad
	}
	if operation != "engine.apply" && len(c.Services) != 0 {
		return bad
	}
	if operation == "engine.apply" && c.Revision == 0 {
		return bad
	}
	seen := map[string]bool{}
	for index, s := range c.Services {
		if s == nil || len(s.ID) < 1 || len(s.ID) > 64 || seen[s.ID] || len(s.Name) < 1 || len(s.Name) > 128 {
			return bad
		}
		seen[s.ID] = true
		for _, ch := range s.ID {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
				return bad
			}
		}
		a, e := netip.ParseAddr(s.ListenHost)
		if e != nil || a.Zone() != "" || a.Is4In6() || a.IsMulticast() || s.ListenPort < 1 || s.ListenPort > 65535 || s.TargetPort < 1 || s.TargetPort > 65535 || !engineHost(s.TargetHost) {
			return bad
		}
		for _, other := range c.Services[:index] {
			if other == nil {
				return bad
			}
			if s.ListenPort == other.ListenPort {
				b, _ := netip.ParseAddr(other.ListenHost)
				if a == b || a == netip.IPv6Unspecified() || b == netip.IPv6Unspecified() || a.Is4() == b.Is4() && (a.IsUnspecified() || b.IsUnspecified()) {
					return bad
				}
			}
		}
		if c.Name == "realm" {
			if s.Uuid != "" || s.PrivateKey != "" || len(s.ShortIds) != 0 || s.ServerName != "" {
				return bad
			}
		} else {
			if !engineHost(s.ServerName) {
				return bad
			}
			if _, err := netip.ParseAddr(s.ServerName); err == nil {
				return bad
			}
			if len(s.Uuid) != 36 {
				return bad
			}
			raw := strings.ReplaceAll(s.Uuid, "-", "")
			if len(raw) != 32 {
				return bad
			}
			if _, err := hex.DecodeString(raw); err != nil {
				return bad
			}
			key, err := base64.RawURLEncoding.DecodeString(s.PrivateKey)
			if err != nil {
				return bad
			}
			if _, err = ecdh.X25519().NewPrivateKey(key); err != nil {
				return bad
			}
			if len(s.ShortIds) < 1 || len(s.ShortIds) > 8 {
				return bad
			}
			seenIDs := map[string]bool{}
			for _, id := range s.ShortIds {
				short, err := hex.DecodeString(id)
				if err != nil || len(short) < 1 || len(short) > 8 || seenIDs[strings.ToLower(id)] {
					return bad
				}
				seenIDs[strings.ToLower(id)] = true
			}
		}
	}
	return nil
}
func engineHost(s string) bool {
	if a, e := netip.ParseAddr(s); e == nil {
		return a.Zone() == "" && !a.IsUnspecified() && !a.IsMulticast()
	}
	if len(s) < 1 || len(s) > 253 {
		return false
	}
	for _, l := range strings.Split(s, ".") {
		if len(l) < 1 || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for _, c := range l {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func EngineErrorCode(code string) bool {
	switch code {
	case "engine_unsupported", "engine_external", "engine_download_failed", "engine_checksum", "engine_invalid_config", "engine_target_unreachable", "engine_port_conflict", "engine_start_failed", "engine_rollback_failed", "engine_io_failed", "engine_result_unknown", "deadline_exceeded":
		return true
	}
	return false
}
