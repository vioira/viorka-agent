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

// IsUnauthorized identifies a terminal Panel HTTP 401 for interactive recovery.
func IsUnauthorized(err error) bool {
	var failure *connectionFailure
	return errors.As(err, &failure) && failure.status == 401 && errors.Is(failure, ErrIdentityRejected)
}

func (e *connectionFailure) Error() string {
	detail := connectionReasonChinese(e.reason)
	if e.status != 0 {
		return fmt.Sprintf("Panel connection: %s: %s (HTTP %d)：%s", e.phase, e.reason, e.status, detail)
	}
	return "Panel connection: " + e.phase + ": " + e.reason + "：" + detail
}

// Use local descriptions so remote error messages cannot expose credentials.
func connectionReasonChinese(reason string) string {
	switch reason {
	case "engine_conflict":
		return "引擎恢复冲突：请核对 Panel 上原引擎归属、目标节点同名记录和端口占用"
	case "engine_recovery_owned":
		return "原引擎 ID 仍绑定另一节点或另一种引擎，请核对 Panel 记录"
	case "engine_recovery_occupied":
		return "目标节点已有同名引擎及业务或任务记录，无法自动替换"
	case "engine_recovery_ports":
		return "原配置的监听地址或端口已被目标节点中的服务保留"
	case "conflict":
		return "身份或资源状态冲突；注册时表示该公钥已绑定节点"
	case "unauthorized", "identity_rejected":
		return "身份认证失败：注册令牌或节点凭证无效、过期或已被撤销"
	case "engine_forbidden":
		return "没有目标节点的引擎管理权限"
	case "registration_limit_reached":
		return "批量注册令牌配额已用尽"
	case "invalid_request", "engine_invalid":
		return "请求参数无效"
	case "not_found":
		return "请求的节点或资源不存在"
	case "unavailable":
		return "Panel 服务暂不可用"
	case "http_error":
		return "Panel 或反向代理返回错误，响应中没有可识别的错误码"
	case "tls_verification_failed":
		return "Panel TLS 证书校验失败"
	case "dns_failed":
		return "Panel 域名解析失败"
	case "timeout":
		return "连接或请求超时"
	case "connection_refused":
		return "Panel 拒绝连接"
	case "connection_reset", "eof", "remote_close":
		return "与 Panel 的连接已断开"
	case "network_error":
		return "连接 Panel 时发生网络错误"
	case "invalid_response":
		return "Panel 响应格式无效"
	case "response_too_large":
		return "Panel 响应超过大小限制"
	case "activation_timeout":
		return "等待 Panel 确认遥测会话激活超时"
	case "session_rotation", "session_changed":
		return "会话已更新，正在重新连接"
	case "invalid_target":
		return "Panel 返回的迁移目标不是有效的 HTTPS 地址"
	case "target_unavailable":
		return "新地址的 TLS 或管理接口尚不可用，将从原地址重试"
	case "session_unconfirmed":
		return "原 Panel 未确认新地址的认证挑战，请检查两个地址的回源"
	case "capability_missing":
		return "新地址尚未协商地址迁移能力"
	case "invalid_challenge":
		return "挑战与连接地址、节点身份或有效期不匹配"
	case "canceled":
		return "连接操作已取消"
	default:
		return "请根据错误码定位具体原因"
	}
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
	attrs = append(attrs, slog.String("原因", connectionReasonChinese(reason)))
	if code := websocket.CloseStatus(err); code != -1 {
		attrs = append(attrs, slog.Int("close_code", int(code)))
	}
	if status != 0 {
		attrs = append(attrs, slog.Int("http_status", status))
	}
	c.lifecycle().LogAttrs(context.Background(), level, message, attrs...)
}
