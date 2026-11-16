package connection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/vioira/viorka-agent/internal/config"
	"github.com/vioira/viorka-agent/internal/state"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestDisconnectClassificationAndRedaction(t *testing.T) {
	for _, tc := range []struct {
		err    error
		reason string
		level  string
	}{
		{connectionError("read", "", websocket.CloseError{Code: websocket.StatusGoingAway, Reason: "secret-token"}), "remote_close", "WARN"},
		{connectionError("read", "invalid_envelope", errors.New("secret-payload")), "invalid_envelope", "WARN"},
		{connectionError("session", "session_rotation", errSessionRotation), "session_rotation", "INFO"},
		{errSessionChanged, "session_changed", "INFO"},
		{io.EOF, "eof", "WARN"},
		{context.DeadlineExceeded, "timeout", "WARN"},
		{&net.DNSError{Name: "secret-host", Err: "secret-value"}, "dns_failed", "WARN"},
		{&tls.CertificateVerificationError{Err: errors.New("secret-certificate")}, "tls_verification_failed", "WARN"},
		{&connectionFailure{phase: "dial", status: 403, cause: ErrIdentityRejected}, "identity_rejected", "WARN"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			var output bytes.Buffer
			c := &Client{logger: slog.New(slog.NewTextHandler(&output, nil))}
			c.logConnectionEnd(tc.err, 10*time.Second, time.Second, false)
			got := output.String()
			if !strings.Contains(got, "reason="+tc.reason) || !strings.Contains(got, "level="+tc.level) || strings.Contains(got, "secret") {
				t.Fatal(got)
			}
			if tc.reason == "remote_close" && !strings.Contains(got, "close_code=1001") {
				t.Fatal(got)
			}
			if tc.reason == "identity_rejected" && !strings.Contains(got, "http_status=403") {
				t.Fatal(got)
			}
		})
	}
}

func TestConnectionFailureChineseReason(t *testing.T) {
	for _, tc := range []struct{ code, detail string }{
		{"engine_conflict", "引擎恢复冲突"},
		{"conflict", "该公钥已绑定节点"},
		{"unauthorized", "身份认证失败"},
		{"http_error", "响应中没有可识别的错误码"},
		{"future_error", "请根据错误码定位具体原因"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			err := &connectionFailure{phase: "authentication", reason: tc.code, status: 409}
			if got := err.Error(); !strings.Contains(got, tc.detail) || !strings.Contains(got, tc.code+" (HTTP 409)") {
				t.Fatal(got)
			}
			var output bytes.Buffer
			c := &Client{logger: slog.New(slog.NewTextHandler(&output, nil))}
			c.logConnectionEnd(err, time.Second, time.Second, true)
			if got := output.String(); !strings.Contains(got, tc.detail) || !strings.Contains(got, "reason="+tc.code) {
				t.Fatal(got)
			}
		})
	}
}

// Exercise the actual single reader: its close/protocol error must survive
// cancellation of the connection owner, instead of becoming context.Canceled.
func TestConnectionReaderPreservesReason(t *testing.T) {
	for _, mode := range []string{"close", "binary", "malformed", "rotation"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.CloseNow()
				if _, _, err = conn.Read(ctx); err != nil {
					t.Error(err)
					return
				}
				raw, _ := json.Marshal(v1.Message("system.hello_ack", "node", ID(), v1.HelloAck{ProtocolVersion: v1.Version, HeartbeatSeconds: 30}))
				if err = conn.Write(ctx, websocket.MessageText, raw); err != nil {
					t.Error(err)
					return
				}
				switch mode {
				case "close":
					conn.Close(websocket.StatusGoingAway, "secret-close-text")
				case "binary":
					conn.Write(ctx, websocket.MessageBinary, []byte("secret-binary"))
				case "malformed":
					conn.Write(ctx, websocket.MessageText, []byte("secret-malformed"))
				case "rotation":
					conn.Read(ctx)
				}
			}))
			defer server.Close()
			c := &Client{cfg: config.Local{PanelURL: server.URL}, http: server.Client(), runCtx: ctx}
			defer c.Close()
			expiry := time.Now().Add(15 * time.Minute)
			if mode == "rotation" {
				expiry = time.Now().Add(30*time.Second + 150*time.Millisecond)
			}
			binding := sha256.Sum256([]byte(server.URL + "\x00node\x00"))
			if _, err := c.sessions.Get(ctx, hex.EncodeToString(binding[:]), func(context.Context) (sessionCredential, error) {
				return sessionCredential{Token: strings.Repeat("a", 64), ExpiresAt: expiry}, nil
			}); err != nil {
				t.Fatal(err)
			}
			err := c.connect(ctx, state.Identity{NodeID: "node"}, nil)
			var failure *connectionFailure
			if !errors.As(err, &failure) {
				t.Fatalf("reader lost cause: %v", err)
			}
			expected := map[string]string{"close": "remote_close", "binary": "unexpected_message_type", "malformed": "invalid_envelope", "rotation": "session_rotation"}[mode]
			var output bytes.Buffer
			c.logger = slog.New(slog.NewTextHandler(&output, nil))
			c.logConnectionEnd(err, time.Second, time.Second, false)
			if got := output.String(); !strings.Contains(got, "reason="+expected) || strings.Contains(got, "secret") {
				t.Fatal(got)
			}
		})
	}
}
