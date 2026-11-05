package runtime

import v1 "github.com/vioira/viorka-agent/protocol/v1"

func singboxEgress(c v1.EngineCommand) map[string]any {
	tag := func(id string) string {
		if id == "direct" {
			return "sb-egress-direct"
		}
		return "egress-" + id
	}
	outbounds := []any{map[string]any{"type": "direct", "tag": "sb-egress-direct", "domain_resolver": "sb-egress-dns"}}
	for _, o := range c.Outbounds {
		out := map[string]any{"type": o.Type, "tag": tag(o.ID), "server": o.Server, "server_port": o.ServerPort, "domain_resolver": "sb-egress-dns"}
		if o.Type == "socks" {
			out["version"] = "5"
		}
		if o.Username != "" {
			out["username"] = o.Username
			out["password"] = o.Password
		}
		if o.Type == "vless" {
			out["uuid"] = o.Uuid
			if o.Flow != "" {
				out["flow"] = o.Flow
			}
		}
		if o.Tls {
			tls := map[string]any{"enabled": true}
			if o.ServerName != "" {
				tls["server_name"] = o.ServerName
			}
			if o.Type == "vless" {
				tls["utls"] = map[string]any{"enabled": true, "fingerprint": o.Fingerprint}
				tls["reality"] = map[string]any{"enabled": true, "public_key": o.PublicKey, "short_id": o.ShortID}
			}
			out["tls"] = tls
		}
		outbounds = append(outbounds, out)
	}
	rules := []any{}
	enabled := map[string]bool{}
	tags := []string{}
	for _, s := range c.Services {
		if s.Enabled {
			tags = append(tags, s.ID)
			enabled[s.ID] = true
		}
	}
	if len(tags) > 0 && (c.Routing == nil || c.Routing.Sniff) {
		rules = append(rules, map[string]any{"inbound": tags, "action": "sniff", "timeout": "300ms"})
	}
	final := "direct"
	if c.Routing != nil {
		final = c.Routing.Final
		for _, r := range c.Routing.Rules {
			if r.Enabled && len(r.IpCidr) > 0 {
				rules = append(rules, map[string]any{"action": "resolve", "server": "sb-egress-dns"})
				break
			}
		}

		for _, r := range c.Routing.Rules {
			if !r.Enabled {
				continue
			}
			rule := map[string]any{}
			if len(r.Inbound) > 0 {
				in := []string{}
				for _, id := range r.Inbound {
					if enabled[id] {
						in = append(in, id)
					}
				}
				if len(in) == 0 {
					continue
				}
				rule["inbound"] = in
			}
			if len(r.Domain) > 0 {
				rule["domain"] = r.Domain
			}
			if len(r.DomainSuffix) > 0 {
				rule["domain_suffix"] = r.DomainSuffix
			}
			if len(r.IpCidr) > 0 {
				rule["ip_cidr"] = r.IpCidr
			}
			if len(r.Port) > 0 {
				rule["port"] = r.Port
			}
			if len(r.PortRange) > 0 {
				rule["port_range"] = r.PortRange
			}
			if len(r.Network) > 0 {
				rule["network"] = r.Network
			}
			// sing-box default rules group some address fields with OR. Explicit
			// logical AND keeps the visual editor's different-field semantics.
			conditions := []any{}
			for _, key := range []string{"inbound", "domain", "domain_suffix", "ip_cidr", "port", "port_range", "network"} {
				if value, ok := rule[key]; ok {
					conditions = append(conditions, map[string]any{key: value})
				}
			}
			if len(conditions) > 1 {
				rule = map[string]any{"type": "logical", "mode": "and", "rules": conditions}
			}
			if r.Outbound == "reject" {
				rule["action"] = "reject"
			} else {
				rule["action"] = "route"
				rule["outbound"] = tag(r.Outbound)
			}
			rules = append(rules, rule)
		}
	}
	if final == "reject" {
		rules = append(rules, map[string]any{"action": "reject"})
		final = "direct"
	}
	return map[string]any{"outbounds": outbounds, "dns": map[string]any{"servers": []any{map[string]any{"type": "local", "tag": "sb-egress-dns"}}}, "route": map[string]any{"rules": rules, "final": tag(final), "default_domain_resolver": "sb-egress-dns"}}
}
