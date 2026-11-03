package runtime

import (
	"context"
	"crypto/tls"
	"errors"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
	"net"
	"net/netip"
	"strconv"
	"time"
)

func preflight(ctx context.Context, c, old v1.EngineCommand, running bool) error {
	listeners := []net.Listener{}
	defer func() {
		for _, l := range listeners {
			l.Close()
		}
	}()
	for _, s := range c.Services {
		if !s.Enabled {
			continue
		}
		reused := false
		if running {
			for _, previous := range old.Services {
				if previous.Enabled && s.ListenHost == previous.ListenHost && s.ListenPort == previous.ListenPort {
					reused = true
				}
			}
		}
		if !reused {
			lc := net.ListenConfig{}
			l, err := lc.Listen(ctx, "tcp", net.JoinHostPort(s.ListenHost, strconv.FormatUint(s.ListenPort, 10)))
			if err != nil {
				return errors.New("engine_port_conflict")
			}
			listeners = append(listeners, l)
		}
		check, cancel := context.WithTimeout(ctx, 5*time.Second)
		ips, err := net.DefaultResolver.LookupNetIP(check, "ip", s.TargetHost)
		if err == nil {
			local, _ := net.InterfaceAddrs()
			for _, ip := range ips {
				for _, a := range local {
					prefix, e := netip.ParsePrefix(a.String())
					if e == nil && prefix.Addr().Unmap() == ip.Unmap() && s.TargetPort == s.ListenPort {
						cancel()
						return errors.New("engine_invalid_config")
					}
				}
			}
		}
		if err == nil {
			addr := net.JoinHostPort(s.TargetHost, strconv.FormatUint(s.TargetPort, 10))
			dialer := net.Dialer{Timeout: 3 * time.Second}
			if c.Name == "realm" {
				conn, e := dialer.DialContext(check, "tcp", addr)
				err = e
				if conn != nil {
					conn.Close()
				}
			} else {
				td := tls.Dialer{NetDialer: &dialer, Config: &tls.Config{MinVersion: tls.VersionTLS13, ServerName: s.ServerName}}
				conn, e := td.DialContext(check, "tcp", addr)
				err = e
				if conn != nil {
					conn.Close()
				}
			}
		}
		cancel()
		if err != nil && !(c.Name == "realm" && c.AllowUnreachable) {
			return errors.New("engine_target_unreachable")
		}
	}
	return nil
}
