package connection

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/coder/websocket"
	"github.com/vioira/viorka-agent/internal/collector"
	"github.com/vioira/viorka-agent/internal/config"
	"github.com/vioira/viorka-agent/internal/executor"
	"github.com/vioira/viorka-agent/internal/network"
	managed "github.com/vioira/viorka-agent/internal/runtime"
	"github.com/vioira/viorka-agent/internal/state"
	"github.com/vioira/viorka-agent/internal/version"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

var ErrIdentityRejected = errors.New("agent identity rejected; contact administrator")

type Client struct {
	tcpProbeSupported atomic.Bool
	trafficCalibrate  func(context.Context, string, v1.TrafficCalibration, time.Time) (*v1.TrafficCalibration, error)
	logger            *slog.Logger
	logLevel          *slog.LevelVar
	telemetrySource   func() (telemetrySources, error)
	telemetryEnabled  bool // local telemetry control
	telemetry         *telemetryRunner
	telemetryBridge   telemetryWSS
	sessions          sessionCoordinator
	cfg               config.Local
	http              *http.Client
	store             *state.Store
	inventory         func(context.Context, time.Duration) v1.NodeInventory
	runCtx            context.Context
	assets            *assetSampling
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
	if len(cfg.DNSFallbackServers) > 0 {
		dns := network.NewDNS(cfg.DNSFallbackServers)
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			return dns.DialContext(ctx, network, address)
		}
	}
	level := new(slog.LevelVar)
	level.Set(parseLogLevel(cfg.LogLevel))
	return &Client{logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})), logLevel: level, cfg: cfg, telemetryEnabled: cfg.TelemetryV3, http: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, store: s, inventory: collector.NewInventory(cfg.DNSFallbackServers...).SampleEvery}, nil
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
	engines, err := managed.NewManaged(c.log()).RecoverySnapshots(ctx)
	if err != nil {
		return err
	}
	request := struct {
		Token           string               `json:"token"`
		PublicKey       string               `json:"public_key"`
		ProtocolVersion string               `json:"protocol_version"`
		Engines         []*v1.EngineRecovery `json:"engines,omitempty"`
	}{token, base64.StdEncoding.EncodeToString(key), v1.Version, engines}
	if err = c.post(ctx, "/api/v1/agents/enroll", request, &result); err != nil {
		return err
	}
	if result.NodeID == "" || len(result.NodeID) > 128 || result.ProtocolVersion != v1.Version {
		return fmt.Errorf("invalid enrollment response")
	}
	id.NodeID = result.NodeID
	return c.store.Write("identity.json", id)
}
func (c *Client) credentials(ctx context.Context, id state.Identity) (sessionCredential, error) {
	binding := sha256.Sum256(append([]byte(c.cfg.PanelURL+"\x00"+id.NodeID+"\x00"), id.PrivateKey...))
	return c.sessions.Get(ctx, hex.EncodeToString(binding[:]), func(ctx context.Context) (sessionCredential, error) { return c.issueSession(ctx, id) })
}
func (c *Client) session(ctx context.Context, id state.Identity) (string, error) {
	v, err := c.credentials(ctx, id)
	return v.Token, err
}
func (c *Client) issueSession(ctx context.Context, id state.Identity) (sessionCredential, error) {
	if len(id.PrivateKey) != ed25519.PrivateKeySize {
		return sessionCredential{}, fmt.Errorf("invalid identity key")
	}
	var ch struct {
		ID        string    `json:"id"`
		Message   string    `json:"message"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := c.post(ctx, "/api/v1/agents/challenge", map[string]string{"node_id": id.NodeID}, &ch); err != nil {
		return sessionCredential{}, err
	}
	expected := fmt.Sprintf("viorka-agent-session-v1\n%s\n%s\n%s\n%s", c.cfg.PanelURL, id.NodeID, ch.ID, ch.ExpiresAt.Format(time.RFC3339Nano))
	if ch.Message != expected || !ch.ExpiresAt.After(time.Now()) || ch.ExpiresAt.After(time.Now().Add(2*time.Minute)) {
		return sessionCredential{}, fmt.Errorf("invalid challenge context")
	}
	sig := ed25519.Sign(ed25519.PrivateKey(id.PrivateKey), []byte(ch.Message))
	var result struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := c.post(ctx, "/api/v1/agents/session", map[string]string{"challenge_id": ch.ID, "signature": base64.StdEncoding.EncodeToString(sig)}, &result); err != nil {
		return sessionCredential{}, err
	}
	if len(result.Token) != 64 || !result.ExpiresAt.After(time.Now()) {
		return sessionCredential{}, fmt.Errorf("invalid session")
	}
	return sessionCredential{Token: result.Token, ExpiresAt: result.ExpiresAt}, nil
}
func ID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func (c *Client) Run(ctx context.Context) (runErr error) {
	id, err := c.store.Identity(c.cfg.PanelURL, false)
	if err != nil {
		return err
	}
	if id.NodeID == "" {
		return fmt.Errorf("enrollment incomplete")
	}
	saved, err := loadTelemetrySettings(c.store, id)
	if err != nil {
		return err
	}
	if saved.current != nil {
		c.applyLogLevel(saved.current.LogLevel)
	}
	exec, err := executor.New(c.store, id.NodeID)
	if err != nil {
		return err
	}
	exec.Logger = c.log()
	runCtx, stopSampling := context.WithCancel(ctx)
	c.runCtx = runCtx
	defer func() { stopSampling(); runErr = errors.Join(runErr, c.waitCollectors()) }()
	delay := time.Second
	for {
		started := time.Now()
		err = c.connect(ctx, id, exec)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, ErrIdentityRejected) || errors.Is(err, errTelemetryStopped) {
			return err
		}
		var certErr *tls.CertificateVerificationError
		if errors.As(err, &certErr) {
			return fmt.Errorf("Panel TLS verification failed: %w", err)
		}
		if time.Since(started) > time.Minute {
			delay = time.Second
		}
		c.log().Warn("Panel connection lost; retrying", "retry_seconds", delay.Seconds())
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
func (c *Client) connect(ctx context.Context, id state.Identity, exec *executor.Executor) (connectErr error) {
	credential, err := c.credentials(ctx, id)
	token := credential.Token
	if err != nil {
		return err
	}
	ctx, endSession := context.WithDeadline(ctx, credential.ExpiresAt.Add(-30*time.Second))
	defer endSession()
	dialCtx, stop := context.WithTimeout(ctx, 15*time.Second)
	conn, res, err := websocket.Dial(dialCtx, "wss"+strings.TrimPrefix(c.cfg.PanelURL, "https")+"/api/v1/agents/ws", &websocket.DialOptions{HTTPClient: c.http, HTTPHeader: http.Header{"Authorization": []string{"Bearer " + token}}})
	stop()
	if err != nil {
		if res != nil && (res.StatusCode == 401 || res.StatusCode == 403) {
			if !c.sessions.Reject(credential.Generation, false) {
				return errSessionChanged
			}
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
	capabilities := []string{"node.inspect", "node.inventory.v1"}
	if managed.Supported() {
		capabilities = append(capabilities, v1.EngineCapability)
	}
	if c.telemetryEnabled {
		capabilities = append(capabilities, v1.TrafficCapability)
	}
	if c.telemetryEnabled {
		capabilities = append(capabilities, v1.TelemetryCapability, v1.TCPProbeCapability)
	}
	if err = send(v1.Message("system.hello", id.NodeID, ID(), v1.Hello{AgentVersion: version.Version, ProtocolVersions: []string{v1.Version}, Capabilities: capabilities, OS: runtime.GOOS, Arch: runtime.GOARCH})); err != nil {
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
	if c.runCtx == nil {
		c.runCtx = ctx
		defer func() { cancel(); connectErr = errors.Join(connectErr, c.waitCollectors()) }()
	}
	// Assets have a bounded independent worker, started after negotiation.
	var inventory <-chan struct{}
	if ack.InventoryVersion != nil {
		if *ack.InventoryVersion != 1 {
			return fmt.Errorf("unsupported inventory version")
		}
		if c.assets == nil {
			c.assets = startAssets(c.runCtx, c.inventory)
		} else {
			c.assets.notify()
		}
		inventory = c.assets.wake
	}
	c.log().Info("Panel connected; session established", "protocol_version", ack.ProtocolVersion, "heartbeat_seconds", ack.HeartbeatSeconds)
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
	telemetry := &telemetryClientSession{client: c, identity: id, credential: credential}
	defer telemetry.close()
	// Only ready confirms telemetry on this management connection; retain HTTP
	// across reconnects while waiting for the new session generation.
	var negotiationTimer *time.Timer
	var negotiationExpired <-chan time.Time
	if c.telemetryEnabled {
		negotiationTimer = time.NewTimer(10 * time.Second)
		negotiationExpired = negotiationTimer.C
		defer negotiationTimer.Stop()
	}

	heartbeat := func() error {
		pingCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		err := conn.Ping(pingCtx)
		stop()
		if err != nil {
			return err
		}
		return send(v1.Message("node.heartbeat", id.NodeID, ID(), v1.Heartbeat{}))
	}
	type engineCompletion struct {
		reply v1.TaskReply
		err   error
	}
	engineResults := make(chan engineCompletion, 1)
	var engineBusy bool
	defer func() {
		if engineBusy {
			<-engineResults
		}
	}()
	var pendingFrame *telemetryFrameRequest
	window := time.Now()
	count := 0
	for {
		var controls <-chan telemetryControl
		var runnerDone <-chan struct{}
		var frames <-chan *telemetryFrameRequest
		if c.telemetry != nil {
			controls = c.telemetry.controls
			runnerDone = c.telemetry.done
		}
		if telemetry.applied && pendingFrame == nil {
			frames = telemetry.port.requests
		}
		// Ready/settings/demand replies and heartbeats precede bulk metrics.
		select {
		case control := <-controls:
			if err = telemetry.control(control, send); err != nil {
				return err
			}
			continue
		default:
		}
		select {
		case <-tick.C:
			if err = heartbeat(); err != nil {
				return err
			}
			continue
		default:
		}
		if pendingFrame != nil && len(incoming) == 0 {
			err = telemetry.port.write(pendingFrame, func(writeCtx context.Context, raw []byte) error {
				bounded, stop := context.WithTimeout(writeCtx, 5*time.Second)
				defer stop()
				return conn.Write(bounded, websocket.MessageText, raw)
			})
			pendingFrame = nil
			if err != nil {
				return err
			}
			continue
		}
		select {
		case <-negotiationExpired:
			negotiationExpired = nil
			if telemetry.generation == "" {
				return fmt.Errorf("Panel does not support telemetry v3")
			}
		case <-runnerDone:
			return fmt.Errorf("%w: %v", errTelemetryStopped, c.telemetry.err)
		case control := <-controls:
			if err = telemetry.control(control, send); err != nil {
				return err
			}
		case frame := <-frames:
			pendingFrame = frame
		case <-credential.Changed:
			return errSessionChanged
		case completed := <-engineResults:
			engineBusy = false
			if completed.err != nil {
				return completed.err
			}
			msg := v1.Message("task.result", id.NodeID, ID(), completed.reply)
			msg.CorrelationID = completed.reply.TaskID
			if err = send(msg); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		case <-inventory:
			value := c.assets.snapshot()
			if value.SampledAt.IsZero() {
				continue
			}
			if err = send(v1.Message("node.inventory", id.NodeID, ID(), value)); err != nil {
				return err
			}
		case <-tick.C:
			if err = heartbeat(); err != nil {
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
			if handled, handleErr := telemetry.handle(e, send); handled {
				if telemetry.generation != "" && negotiationTimer != nil {
					negotiationTimer.Stop()
					negotiationExpired = nil
				}
				if handleErr != nil {
					return handleErr
				}
				continue
			}
			var d v1.TaskDispatch
			if e.Type != "task.dispatch" || e.Revision != nil || v1.Decode(e.Payload, &d) != nil || e.CorrelationID != d.TaskID {
				return fmt.Errorf("invalid dispatch")
			}
			if v1.EngineOperation(d.Operation) {
				if engineBusy {
					continue
				}
				if d.Engine == nil || d.Engine.Validate(d.Operation) != nil || d.Attempt == nil {
					return fmt.Errorf("invalid engine task")
				}
				accept := v1.TaskReply{TaskID: d.TaskID, Attempt: *d.Attempt, ResourceID: d.ResourceID, State: "accepted"}
				msg := v1.Message("task.ack", id.NodeID, ID(), accept)
				msg.CorrelationID = d.TaskID
				if err = send(msg); err != nil {
					return err
				}
				engineBusy = true
				workCtx := c.runCtx
				if workCtx == nil {
					workCtx = ctx
				}
				go func() { reply, err := exec.ExecuteEngine(workCtx, d); engineResults <- engineCompletion{reply, err} }()
				continue
			}
			exec.Calibrate = c.trafficCalibrate
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

func (c *Client) waitCollectors() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var result error
	wait := func(done <-chan struct{}) {
		select {
		case <-done:
		case <-ctx.Done():
			result = errors.Join(result, fmt.Errorf("collector shutdown: %w", ctx.Err()))
		}
	}
	if c.assets != nil {
		wait(c.assets.done)
		c.assets = nil
	}
	if c.telemetry != nil {
		result = errors.Join(result, c.telemetry.stop(ctx))
		c.telemetry = nil
	}
	c.runCtx = nil
	return result
}
