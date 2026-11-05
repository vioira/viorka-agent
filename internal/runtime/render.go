package runtime

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

// Render accepts only the bounded structured protocol, never native config text.
func Render(c v1.EngineCommand) ([]byte, error) {
	if err := c.Validate("engine.apply"); err != nil {
		return nil, err
	}
	if c.Name == "realm" {
		var b strings.Builder
		b.WriteString("[log]\nlevel = \"off\"\n[network]\nno_tcp = false\nuse_udp = false\n")
		for _, s := range c.Services {
			if s.Enabled {
				fmt.Fprintf(&b, "\n[[endpoints]]\nlisten = %q\nremote = %q\n", net.JoinHostPort(s.ListenHost, strconv.FormatUint(s.ListenPort, 10)), net.JoinHostPort(s.TargetHost, strconv.FormatUint(s.TargetPort, 10)))
			}
		}
		return []byte(b.String()), nil
	}
	inbounds := []any{}
	for _, s := range c.Services {
		if !s.Enabled {
			continue
		}
		if c.Name == "sing-box" {
			inbounds = append(inbounds, map[string]any{"type": "vless", "tag": s.ID, "listen": s.ListenHost, "listen_port": s.ListenPort, "users": []any{map[string]any{"uuid": s.Uuid, "flow": "xtls-rprx-vision"}}, "tls": map[string]any{"enabled": true, "server_name": s.ServerName, "reality": map[string]any{"enabled": true, "private_key": s.PrivateKey, "short_id": s.ShortIds, "handshake": map[string]any{"server": s.TargetHost, "server_port": s.TargetPort}}}})
		} else {
			inbounds = append(inbounds, map[string]any{"tag": s.ID, "listen": s.ListenHost, "port": s.ListenPort, "protocol": "vless", "settings": map[string]any{"decryption": "none", "clients": []any{map[string]any{"id": s.Uuid, "flow": "xtls-rprx-vision"}}}, "streamSettings": map[string]any{"network": "raw", "security": "reality", "realitySettings": map[string]any{"show": false, "target": net.JoinHostPort(s.TargetHost, strconv.FormatUint(s.TargetPort, 10)), "serverNames": []string{s.ServerName}, "privateKey": s.PrivateKey, "shortIds": s.ShortIds}}})
		}
	}
	if c.Name == "sing-box" {
		config := singboxEgress(c)
		config["log"] = map[string]any{"disabled": true}
		config["inbounds"] = inbounds
		return json.Marshal(config)
	}
	return json.Marshal(map[string]any{"log": map[string]any{"access": "none", "error": "none", "loglevel": "none"}, "inbounds": inbounds, "outbounds": []any{map[string]any{"protocol": "freedom", "tag": "direct"}}})
}
