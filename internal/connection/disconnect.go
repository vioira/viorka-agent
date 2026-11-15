package connection

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

var errSessionRotation = errors.New("scheduled session rotation")

type connectionFailure struct {
	phase, reason string
	status        int
	cause         error
}

func (e *connectionFailure) Error() string {
	if e.status != 0 {
		return fmt.Sprintf("Panel connection: %s: %s (HTTP %d)", e.phase, e.reason, e.status)
	}
	return "Panel connection: " + e.phase + ": " + e.reason
}
func (e *connectionFailure) Unwrap() error { return e.cause }
func connectionError(phase, reason string, err error) error {
	var existing *connectionFailure
	if reason == "" && errors.As(err, &existing) {
		return err
	}
	return &connectionFailure{phase: phase, reason: reason, cause: err}
}

// Classify into fixed vocabulary. Never log Error(), URLs, remote close text,
// TLS subjects, addresses, response bodies, or credentials from wrapped errors.
func connectionReason(err error) string {
	var cert *tls.CertificateVerificationError
	var dns *net.DNSError
	var network net.Error
	switch {
	case errors.Is(err, errSessionRotation):
		return "session_rotation"
	case errors.Is(err, errSessionChanged):
		return "session_changed"
	case errors.Is(err, ErrIdentityRejected):
		return "identity_rejected"
	case errors.Is(err, errTelemetryStopped):
		return "telemetry_stopped"
	case errors.As(err, &cert):
		return "tls_verification_failed"
	case errors.As(err, &dns):
		return "dns_failed"
	case websocket.CloseStatus(err) != -1:
		return "remote_close"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "eof"
	case errors.Is(err, syscall.ECONNRESET):
		return "connection_reset"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection_refused"
	case errors.As(err, &network):
		if network.Timeout() {
			return "timeout"
		}
		return "network_error"
	default:
		return "operation_failed"
	}
}
func (c *Client) logConnectionEnd(err error, elapsed, retry time.Duration, terminal bool) {
	phase, reason, status := "connection", connectionReason(err), 0
	var failure *connectionFailure
	if errors.As(err, &failure) {
		phase, status = failure.phase, failure.status
		if failure.reason != "" {
			reason = failure.reason
		}
	}
	level := slog.LevelWarn
	message := "Panel connection failed or lost; retrying"
	if reason == "session_rotation" || reason == "session_changed" {
		level = slog.LevelInfo
		message = "Panel session rotating; reconnecting"
	}
	if terminal {
		level = slog.LevelError
		message = "Panel connection stopped"
	}
	attrs := []slog.Attr{slog.String("phase", phase), slog.String("reason", reason), slog.Float64("attempt_seconds", elapsed.Seconds()), slog.Float64("retry_seconds", retry.Seconds())}
	if code := websocket.CloseStatus(err); code != -1 {
		attrs = append(attrs, slog.Int("close_code", int(code)))
	}
	if status != 0 {
		attrs = append(attrs, slog.Int("http_status", status))
	}
	c.lifecycle().LogAttrs(context.Background(), level, message, attrs...)
}
