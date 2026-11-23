package connection

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"

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

// Private management WSS close code defined by Panel api/agent/v1.
const agentStopping = websocket.StatusCode(4001)

type Client struct {
	activeOrigin          atomic.Pointer[string]
	activeSocket          atomic.Pointer[websocket.Conn]
	dailyPersistFailed    bool // owned by the telemetry sender
	slowUptimeSupported   atomic.Bool
	dailyTrafficSupported atomic.Bool
	dailyPending          func(context.Context, bool) ([]*v1.TelemetryDailyTraffic, error)
	dailyAck              func([]*v1.TelemetryDailyTrafficAck)
	tcpProbeSupported     atomic.Bool
	trafficCalibrate      func(context.Context, string, v1.TrafficCalibration, time.Time) (*v1.TrafficCalibration, error)
	lifecycleLogger       *slog.Logger
	logger                *slog.Logger
	logLevel              *slog.LevelVar
	telemetrySource       func() (telemetrySources, error)
	telemetryEnabled      bool // local telemetry control
	telemetry             *telemetryRunner
	telemetryBridge       telemetryWSS
	sessions              sessionCoordinator
	cfg                   config.Local
	http                  *http.Client
	store                 *state.Store
	inventory             func(context.Context, time.Duration) v1.NodeInventory
	runCtx                context.Context
	assets                *assetSampling
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
	return c.postAt(ctx, c.origin(), path, v, out)
}
func (c *Client) postAt(ctx context.Context, origin, path string, v, out any) error {
	ctx, stop := context.WithTimeout(ctx, 30*time.Second)
	defer stop()
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	r, err := http.NewRequestWithContext(ctx, "POST", origin+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	if path == "/api/v1/agents/challenge" {
		r.Header.Set("Viorka-Agent-Origin", origin)
	}
	res, err := c.http.Do(r)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		failure := &connectionFailure{phase: "authentication", reason: "http_error", status: res.StatusCode}
		if res.StatusCode == 401 || res.StatusCode == 403 {
			failure.reason, failure.cause = "identity_rejected", ErrIdentityRejected
		}
		// Only expose the bounded error code, never the remote message/body.
		body, readErr := io.ReadAll(io.LimitReader(res.Body, 16385))
		var response struct {
			Code string `json:"code"`
		}
		if readErr == nil && len(body) <= 16384 && json.Unmarshal(body, &response) == nil &&
			len(response.Code) > 0 && len(response.Code) <= 64 &&
			strings.Trim(response.Code, "abcdefghijklmnopqrstuvwxyz0123456789_") == "" {
			failure.reason = response.Code
		}
		return failure
	}
	b, err = io.ReadAll(io.LimitReader(res.Body, 16385))
	if err != nil {
		return err
	}
	if len(b) > 16384 {
		return connectionError("authentication", "response_too_large", nil)
	}
	if err := v1.Decode(b, out); err != nil {
		return connectionError("authentication", "invalid_response", err)
	}
	return nil
}
func (c *Client) credentials(ctx context.Context, id state.Identity) (sessionCredential, error) {
	binding := sha256.Sum256(append([]byte(c.cfg.PanelURL+"\x00"+id.NodeID+"\x00"), id.PrivateKey...))
	return c.sessions.Get(ctx, hex.EncodeToString(binding[:]), func(ctx context.Context) (sessionCredential, error) { return c.issueSession(ctx, id) })
}
func (c *Client) session(ctx context.Context, id state.Identity) (string, error) {
	v, err := c.credentials(ctx, id)
	return v.Token, err
}
func ID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func (c *Client) Run(ctx context.Context) (runErr error) {
	c.lifecycle().Info("Agent starting; connecting to Panel")
	defer func() {
		if runErr != nil {
			c.lifecycle().Error("Agent run failed")
		} else {
			c.lifecycle().Info("Agent stopped")
		}
	}()
	id, err := c.store.Identity(c.cfg.PanelURL, false)
	if err != nil {
		return err
	}
	if id.NodeID == "" {
		return fmt.Errorf("enrollment incomplete")
	}
	if err = c.restoreOrigin(id); err != nil {
		return err
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
	// Keep socket I/O alive until the graceful close frame is sent. Canceling
	// an in-flight websocket.Read first would tear down the transport.
	runCtx, stopSampling := context.WithCancel(context.WithoutCancel(ctx))
	shutdownDone := make(chan struct{})
	stopShutdown := context.AfterFunc(ctx, func() {
		defer close(shutdownDone)
		defer stopSampling()
		if conn := c.activeSocket.Load(); conn != nil && ctx.Err() == context.Canceled {
			if err := conn.Close(agentStopping, "agent stopping"); err != nil {
				c.log().Debug("graceful stop notification unavailable")
			}
		}
	})
	defer func() {
		if !stopShutdown() {
			<-shutdownDone
		}
	}()
	c.runCtx = runCtx
	defer func() { stopSampling(); runErr = errors.Join(runErr, c.waitCollectors()) }()
	delay := time.Second
	for {
		started := time.Now()
		err = c.connect(runCtx, id, exec)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, ErrIdentityRejected) || errors.Is(err, errTelemetryStopped) {
			c.logConnectionEnd(err, time.Since(started), 0, true)
			return err
		}
		var certErr *tls.CertificateVerificationError
		if errors.As(err, &certErr) {
			c.logConnectionEnd(err, time.Since(started), 0, true)
			return fmt.Errorf("Panel TLS verification failed: %w", err)
		}
		if time.Since(started) > time.Minute {
			delay = time.Second
		}
		c.logConnectionEnd(err, time.Since(started), delay, false)
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
		return connectionError("authentication", "", err)
	}
	ctx, endSession := context.WithDeadlineCause(ctx, credential.ExpiresAt.Add(-30*time.Second), errSessionRotation)
	defer endSession()
	sessionCtx := ctx
	defer func() {
		if errors.Is(context.Cause(sessionCtx), errSessionRotation) {
			connectErr = connectionError("session", "session_rotation", errSessionRotation)
		}
	}()
	// A failed trial must rediscover the latest target through the last
	// confirmed origin, even if only the WSS upgrade or hello failed.
	if credential.Origin != "" && credential.Origin != c.origin() {
		defer func() {
			if credential.Origin != c.origin() {
				c.sessions.retire(credential.Generation, nil)
			}
		}()
	}
	dialCtx, stop := context.WithTimeout(ctx, 15*time.Second)
	conn, res, err := websocket.Dial(dialCtx, "wss"+strings.TrimPrefix(c.credentialOrigin(credential), "https")+"/api/v1/agents/ws", &websocket.DialOptions{HTTPClient: c.http, HTTPHeader: http.Header{"Authorization": []string{"Bearer " + token}}})
	stop()
	if err != nil {
		status := 0
		if res != nil {
			status = res.StatusCode
		}
		if credential.Origin != "" && credential.Origin != c.origin() {
			return connectionError("migration", "target_unavailable", nil)
		}
		cause := err
		if status == 401 || status == 403 {
			cause = ErrIdentityRejected
			if !c.sessions.Reject(credential.Generation, false) {
				cause = errSessionChanged
			}
		}
		return &connectionFailure{phase: "dial", status: status, cause: cause}
	}

	defer conn.CloseNow()
	conn.SetReadLimit(65536)
	ctx, cancelCause := context.WithCancelCause(ctx)
	cancel := func() { cancelCause(context.Canceled) }
	defer cancel()
	send := func(e v1.Envelope) error {
		b, err := json.Marshal(e)
		if err != nil {
			return err
		}
		c, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		if err := conn.Write(c, websocket.MessageText, b); err != nil {
			return connectionError("write", "", err)
		}
		return nil
	}
	capabilities := []string{"node.inspect", "node.inventory.v1", v1.OriginMigrationCapability}
	if managed.Supported() {
		capabilities = append(capabilities, v1.EngineCapability, v1.EngineEndpointsCapability, v1.EngineRecoveryCapability)
	}
	if c.telemetryEnabled {
		capabilities = append(capabilities, v1.TrafficCapability, v1.DailyTrafficCapability)
	}
	if c.telemetryEnabled {
		capabilities = append(capabilities, v1.TelemetryCapability, v1.TelemetryActivationCapability, v1.SlowUptimeCapability, v1.TCPProbeCapability, v1.TCPProbeSettingsCapability)
	}
	if err = send(v1.Message("system.hello", id.NodeID, ID(), v1.Hello{AgentVersion: version.Version, ProtocolVersions: []string{v1.Version}, Capabilities: capabilities, OS: runtime.GOOS, Arch: runtime.GOARCH})); err != nil {
		return err
	}
	hctx, hstop := context.WithTimeout(ctx, 10*time.Second)
	_, b, err := conn.Read(hctx)
	hstop()
	if err != nil {
		if websocket.CloseStatus(err) == v1.OriginRefreshCloseCode {
			c.sessions.retire(credential.Generation, nil)
		}
		return connectionError("handshake", "", err)
	}
	envelope, err := v1.Parse(b, id.NodeID)
	if err != nil {
		return connectionError("handshake", "invalid_envelope", err)
	}
	var ack v1.HelloAck
	if envelope.Type != "system.hello_ack" || v1.Decode(envelope.Payload, &ack) != nil || ack.ProtocolVersion != v1.Version || ack.HeartbeatSeconds < 5 || ack.HeartbeatSeconds > 60 {
		return connectionError("handshake", "unsupported_hello", nil)
	}
	if ack.OriginMigration {
		origin := c.credentialOrigin(credential)
		if origin != c.origin() {
			id.ActiveOrigin = origin
			if err = c.store.Write("identity.json", id); err != nil {
				return fmt.Errorf("persist migrated Panel origin: %w", err)
			}
			c.activeOrigin.Store(&origin)
		}
		if err = send(v1.Message("node.heartbeat", id.NodeID, ID(), v1.Heartbeat{AppliedOrigin: origin})); err != nil {
			return err
		}
	} else if credential.Origin != "" && credential.Origin != c.origin() {
		return connectionError("migration", "capability_missing", nil)
	}
	if c.runCtx == nil {
		c.runCtx = ctx
		defer func() { cancel(); connectErr = errors.Join(connectErr, c.waitCollectors()) }()
	}
	// Assets have a bounded independent worker, started after negotiation.
	var inventory <-chan struct{}
	if ack.InventoryVersion != nil {
		if *ack.InventoryVersion != 1 {
			return connectionError("handshake", "unsupported_inventory", nil)
		}
		if c.assets == nil {
			c.assets = startAssets(c.runCtx, c.inventory)
		} else {
			c.assets.notify()
		}
		inventory = c.assets.wake
	}
	c.activeSocket.Store(conn)
	defer c.activeSocket.CompareAndSwap(conn, nil)
	c.lifecycle().Info("Panel connected; session established", "protocol_version", ack.ProtocolVersion, "heartbeat_seconds", ack.HeartbeatSeconds)
	incoming := make(chan v1.Envelope, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer cancel()
		for {
			typ, b, err := conn.Read(ctx)
			if err != nil {
				if websocket.CloseStatus(err) == v1.OriginRefreshCloseCode {
					c.sessions.retire(credential.Generation, nil)
				}
				cancelCause(connectionError("read", "", err))
				return
			}
			if typ != websocket.MessageText {
				cancelCause(connectionError("read", "unexpected_message_type", nil))
				return
			}
			e, err := v1.Parse(b, id.NodeID)
			if err != nil {
				cancelCause(connectionError("read", "invalid_envelope", err))
				return
			}
			select {
			case incoming <- e:
			case <-ctx.Done():
				return
			default:
				cancelCause(connectionError("read", "incoming_queue_full", nil))
				return
			}
		}
	}()
	defer func() {
		if cause := context.Cause(ctx); cause != nil && errors.Is(connectErr, context.Canceled) {
			connectErr = cause
		}
		cancel()
		conn.CloseNow()
		<-done
	}()
	tick := time.NewTicker(time.Duration(ack.HeartbeatSeconds) * time.Second)
	defer tick.Stop()
	telemetry := &telemetryClientSession{client: c, identity: id, credential: credential}
	defer telemetry.close()
	// Keep the negotiation deadline until the Panel confirms activation when
	// the capability was negotiated.
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
			return connectionError("heartbeat", "", err)
		}
		h := v1.Heartbeat{}
		if ack.OriginMigration {
			h.AppliedOrigin = c.credentialOrigin(credential)
		}
		return send(v1.Message("node.heartbeat", id.NodeID, ID(), h))
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
				if err := conn.Write(bounded, websocket.MessageText, raw); err != nil {
					return connectionError("write", "", err)
				}
				return nil
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
			if telemetry.generation == "" || telemetry.awaitingActivation {
				reason := "ready_timeout"
				if telemetry.awaitingActivation {
					reason = "activation_timeout"
				}
				return connectionError("telemetry_negotiation", reason, nil)
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
			return context.Cause(ctx)
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
				return connectionError("read", "message_rate_exceeded", nil)
			}
			if handled, handleErr := telemetry.handle(e, send); handled {
				if telemetry.generation != "" && !telemetry.awaitingActivation && negotiationTimer != nil {
					negotiationTimer.Stop()
					negotiationExpired = nil
				}
				if handleErr != nil {
					return connectionError("telemetry_control", "invalid_control", handleErr)
				}
				continue
			}
			if e.Type == "engine.endpoints" {
				var b v1.EngineEndpointBackup
				if !managed.Supported() || e.Revision != nil || e.CorrelationID != "" || v1.Decode(e.Payload, &b) != nil {
					return errors.New("invalid engine endpoint backup")
				}
				if err := managed.NewManaged(c.log()).StoreEndpointBackup(ctx, b); err != nil {
					c.log().Error("Published endpoints could not be saved", "error", err)
				}
				continue
			}
			var d v1.TaskDispatch
			if e.Type != "task.dispatch" || e.Revision != nil || v1.Decode(e.Payload, &d) != nil || e.CorrelationID != d.TaskID {
				return connectionError("task", "invalid_dispatch", nil)
			}
			if v1.EngineOperation(d.Operation) {
				if engineBusy {
					continue
				}
				if d.Engine == nil || d.Engine.Validate(d.Operation) != nil || d.Attempt == nil || d.Operation == v1.EngineRecoverOperation && d.ResourceID != id.NodeID {
					return connectionError("task", "invalid_engine_task", nil)
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
