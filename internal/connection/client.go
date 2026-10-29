package connection

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/coder/websocket"
	"github.com/vioira/viorka-agent/internal/config"
	"github.com/vioira/viorka-agent/internal/executor"
	"github.com/vioira/viorka-agent/internal/state"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

var ErrIdentityRejected = errors.New("agent identity rejected; contact administrator")

type Client struct {
	cfg   config.Local
	http  *http.Client
	store *state.Store
}

func New(cfg config.Local, s *state.Store) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.CAFile != "" {
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		b, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, err
		}
		if !pool.AppendCertsFromPEM(b) {
			return nil, fmt.Errorf("invalid CA certificate")
		}
		tlsConfig.RootCAs = pool
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	return &Client{cfg, &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, s}, nil
}
func (c *Client) Close() { c.http.CloseIdleConnections() }
func (c *Client) post(ctx context.Context, path string, v, out any) error {
	ctx, stop := context.WithTimeout(ctx, 15*time.Second)
	defer stop()
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	r, err := http.NewRequestWithContext(ctx, "POST", c.cfg.PanelURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(r)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return ErrIdentityRejected
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("Panel returned HTTP %d", res.StatusCode)
	}
	b, err = io.ReadAll(io.LimitReader(res.Body, 16385))
	if err != nil {
		return err
	}
	if len(b) > 16384 {
		return fmt.Errorf("response too large")
	}
	return v1.Decode(b, out)
}
func (c *Client) Enroll(ctx context.Context, token string) error {
	id, err := c.store.Identity(c.cfg.PanelURL, true)
	if err != nil {
		return err
	}
	if id.NodeID != "" {
		return fmt.Errorf("already enrolled; existing identity preserved")
	}
	key := ed25519.PrivateKey(id.PrivateKey).Public().(ed25519.PublicKey)
	var result struct {
		NodeID          string `json:"node_id"`
		ProtocolVersion string `json:"protocol_version"`
	}
	if err = c.post(ctx, "/api/v1/agents/enroll", map[string]string{"token": token, "public_key": base64.StdEncoding.EncodeToString(key), "protocol_version": v1.Version}, &result); err != nil {
		return err
	}
	if result.NodeID == "" || len(result.NodeID) > 128 || result.ProtocolVersion != v1.Version {
		return fmt.Errorf("invalid enrollment response")
	}
	id.NodeID = result.NodeID
	return c.store.Write("identity.json", id)
}
func (c *Client) session(ctx context.Context, id state.Identity) (string, error) {
	var ch struct {
		ID        string    `json:"id"`
		Message   string    `json:"message"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := c.post(ctx, "/api/v1/agents/challenge", map[string]string{"node_id": id.NodeID}, &ch); err != nil {
		return "", err
	}
	expected := fmt.Sprintf("viorka-agent-session-v1\n%s\n%s\n%s\n%s", c.cfg.PanelURL, id.NodeID, ch.ID, ch.ExpiresAt.Format(time.RFC3339Nano))
	if ch.Message != expected || !ch.ExpiresAt.After(time.Now()) || ch.ExpiresAt.After(time.Now().Add(2*time.Minute)) {
		return "", fmt.Errorf("invalid challenge context")
	}
	sig := ed25519.Sign(ed25519.PrivateKey(id.PrivateKey), []byte(ch.Message))
	var result struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := c.post(ctx, "/api/v1/agents/session", map[string]string{"challenge_id": ch.ID, "signature": base64.StdEncoding.EncodeToString(sig)}, &result); err != nil {
		return "", err
	}
	if len(result.Token) != 64 || !result.ExpiresAt.After(time.Now()) {
		return "", fmt.Errorf("invalid session")
	}
	return result.Token, nil
}
func ID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func (c *Client) Run(ctx context.Context) error {
	id, err := c.store.Identity(c.cfg.PanelURL, false)
	if err != nil {
		return err
	}
	if id.NodeID == "" {
		return fmt.Errorf("enrollment incomplete")
	}
	exec, err := executor.New(c.store, id.NodeID)
	if err != nil {
		return err
	}
	delay := time.Second
	for {
		started := time.Now()
		err = c.connect(ctx, id, exec)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, ErrIdentityRejected) {
			return err
		}
		var certErr *tls.CertificateVerificationError
		if errors.As(err, &certErr) {
			return fmt.Errorf("Panel TLS verification failed: %w", err)
		}
		if time.Since(started) > time.Minute {
			delay = time.Second
		}
		slog.Warn("Panel connection lost; retrying", "retry_seconds", delay.Seconds())
		j, _ := rand.Int(rand.Reader, big.NewInt(int64(delay/2)))
		wait := time.NewTimer(delay + time.Duration(j.Int64()))
		select {
		case <-ctx.Done():
			wait.Stop()
			return nil
		case <-wait.C:
		}
		delay = min(delay*2, 30*time.Second)
	}
}
func (c *Client) connect(ctx context.Context, id state.Identity, exec *executor.Executor) error {
	token, err := c.session(ctx, id)
	if err != nil {
		return err
	}
	dialCtx, stop := context.WithTimeout(ctx, 15*time.Second)
	conn, res, err := websocket.Dial(dialCtx, "wss"+strings.TrimPrefix(c.cfg.PanelURL, "https")+"/api/v1/agents/ws", &websocket.DialOptions{HTTPClient: c.http, HTTPHeader: http.Header{"Authorization": []string{"Bearer " + token}}})
	stop()
	if err != nil {
		if res != nil && (res.StatusCode == 401 || res.StatusCode == 403) {
			return ErrIdentityRejected
		}
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(65536)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	send := func(e v1.Envelope) error {
		b, err := json.Marshal(e)
		if err != nil {
			return err
		}
		c, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		return conn.Write(c, websocket.MessageText, b)
	}
	if err = send(v1.Message("system.hello", id.NodeID, ID(), v1.Hello{AgentVersion: "0.1.0-m0", ProtocolVersions: []string{v1.Version}, Capabilities: []string{"node.inspect"}, OS: runtime.GOOS, Arch: runtime.GOARCH})); err != nil {
		return err
	}
	hctx, hstop := context.WithTimeout(ctx, 10*time.Second)
	_, b, err := conn.Read(hctx)
	hstop()
	if err != nil {
		return err
	}
	envelope, err := v1.Parse(b, id.NodeID)
	if err != nil {
		return err
	}
	var ack v1.HelloAck
	if envelope.Type != "system.hello_ack" || v1.Decode(envelope.Payload, &ack) != nil || ack.ProtocolVersion != v1.Version || ack.HeartbeatSeconds < 5 || ack.HeartbeatSeconds > 60 {
		return fmt.Errorf("unsupported hello response")
	}
	incoming := make(chan v1.Envelope, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer cancel()
		for {
			typ, b, err := conn.Read(ctx)
			if err != nil || typ != websocket.MessageText {
				return
			}
			e, err := v1.Parse(b, id.NodeID)
			if err != nil {
				return
			}
			select {
			case incoming <- e:
			case <-ctx.Done():
				return
			default:
				return
			}
		}
	}()
	defer func() { cancel(); conn.CloseNow(); <-done }()
	tick := time.NewTicker(time.Duration(ack.HeartbeatSeconds) * time.Second)
	defer tick.Stop()
	window := time.Now()
	count := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			// Ping checks liveness even when no task is pending. Reader handles control frames.
			pctx, pstop := context.WithTimeout(ctx, 10*time.Second)
			err = conn.Ping(pctx)
			pstop()
			if err != nil {
				return err
			}
			if err = send(v1.Message("node.heartbeat", id.NodeID, ID(), v1.Heartbeat{})); err != nil {
				return err
			}
		case e := <-incoming:
			count++
			if time.Since(window) >= time.Minute {
				window = time.Now()
				count = 1
			}
			if count > 120 {
				return fmt.Errorf("message rate exceeded")
			}
			var d v1.TaskDispatch
			if e.Type != "task.dispatch" || e.Revision != nil || v1.Decode(e.Payload, &d) != nil || e.CorrelationID != d.TaskID {
				return fmt.Errorf("invalid dispatch")
			}
			result, err := exec.Execute(d)
			if err != nil {
				return err
			}
			accept := v1.TaskReply{TaskID: d.TaskID, Attempt: *d.Attempt, ResourceID: id.NodeID, State: "accepted"}
			msg := v1.Message("task.ack", id.NodeID, ID(), accept)
			msg.CorrelationID = d.TaskID
			if err = send(msg); err != nil {
				return err
			}
			msg = v1.Message("task.result", id.NodeID, ID(), result)
			msg.CorrelationID = d.TaskID
			if err = send(msg); err != nil {
				return err
			}
		}
	}
}
