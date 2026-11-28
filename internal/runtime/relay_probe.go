package runtime

import (
	"context"
	"errors"
	"net"
	"strconv"
	"syscall"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

// The caller has verified the active managed file and the requested revision.
// Both destinations come from that file. The request contains no dial address.
func probeManagedRelay(ctx context.Context, p *v1.RelayProbe, rule *v1.EngineService) *v1.RelayProbeResult {
	host := rule.ListenHost
	switch host {
	case "::":
		host = "::1"
	case "0.0.0.0":
		host = "127.0.0.1"
	}
	return &v1.RelayProbeResult{RuleID: p.RuleID, AppliedRevision: p.AppliedRevision, Listen: relayTCPCheck(ctx, host, rule.ListenPort), Dial: relayTCPCheck(ctx, rule.TargetHost, rule.TargetPort)}
}
func relayTCPCheck(ctx context.Context, host string, port uint64) string {
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(bounded, "tcp", net.JoinHostPort(host, strconv.FormatUint(port, 10)))
	if err == nil {
		conn.Close()
		return "passed"
	}
	var dns *net.DNSError
	var timeout net.Error
	switch {
	case errors.As(err, &timeout) && timeout.Timeout():
		return "timeout"
	case errors.As(err, &dns):
		return "dns"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "refused"
	}
	return "unreachable"
}
