package connection

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

func waitFrame(t *testing.T, p *telemetryWSSPort) *telemetryFrameRequest {
	t.Helper()
	select {
	case request := <-p.requests:
		return request
	case <-time.After(2 * time.Second):
		t.Fatal("frame not queued")
		return nil
	}
}
func waitDelivery(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("delivery did not finish")
		return nil
	}
}
func TestTelemetryWSSRealSocketReceiptAndControl(t *testing.T) {
	fixture := uploadFixture(t)
	var window telemetryWindow
	if err := window.add(fixture.Fast[0]); err != nil {
		t.Fatal(err)
	}
	flight := window.take(false)
	frame, _, err := flight.frame(0, fixture.NodeID, fixture.RunID, "batch-wss", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		_, raw, err := conn.Read(ctx)
		if err != nil {
			t.Error(err)
			return
		}
		e, err := v1.Parse(raw, fixture.NodeID)
		if err != nil {
			t.Error(err)
			return
		}
		if e.Type != "node.metrics.v3" {
			t.Error("missing telemetry frame")
		}
		if _, err = v1.DecodeTelemetryBatch(e.Payload, fixture.NodeID, time.Now(), 60, false); err != nil {
			t.Error(err)
			return
		}
		// Deliberately withhold metric acceptance until another control message
		// arrives. Receipt waiting must not hold the management writer hostage.
		_, raw, err = conn.Read(ctx)
		if err != nil {
			t.Error(err)
			return
		}
		heartbeat, err := v1.Parse(raw, fixture.NodeID)
		if err != nil || heartbeat.Type != "node.heartbeat" {
			t.Error("management blocked by metric ACK", err)
			return
		}
		reply := v1.Message("node.metrics.receipt", fixture.NodeID, "receipt-1", v1.TelemetryReceipt{BatchID: "batch-wss", State: "accepted", AcceptedCount: 1})
		reply.CorrelationID = e.MessageID
		raw, err = json.Marshal(reply)
		if err != nil {
			t.Error(err)
			return
		}
		if err = conn.Write(ctx, websocket.MessageText, raw); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "wss"+strings.TrimPrefix(server.URL, "https"), &websocket.DialOptions{HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	var bridge telemetryWSS
	port := bridge.attach()
	defer port.close()
	result := make(chan error, 1)
	go func() { result <- bridge.delivery()(ctx, frame, "batch-wss", 1) }()
	request := waitFrame(t, port)
	if err = port.write(request, func(ctx context.Context, raw []byte) error { return conn.Write(ctx, websocket.MessageText, raw) }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-result:
		t.Fatal("socket write falsely accepted metrics")
	default:
	}
	heartbeat, _ := json.Marshal(v1.Message("node.heartbeat", fixture.NodeID, "heartbeat-1", v1.Heartbeat{}))
	if err = conn.Write(ctx, websocket.MessageText, heartbeat); err != nil {
		t.Fatal(err)
	}
	_, raw, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := v1.Parse(raw, fixture.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if !port.receipt(receipt) {
		t.Fatal("receipt not matched")
	}
	if err = waitDelivery(t, result); err != nil {
		t.Fatal(err)
	}
	window.release(flight)
}
func TestTelemetryWSSPortGenerationAndLateReceipt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var bridge telemetryWSS
	old := bridge.attach()
	oldDelivery := bridge.delivery()
	result := make(chan error, 1)
	go func() { result <- oldDelivery(ctx, []byte("{}"), "old", 1) }()
	request := waitFrame(t, old)
	if err := old.write(request, func(context.Context, []byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	next := bridge.attach()
	defer next.close()
	if err := waitDelivery(t, result); err == nil {
		t.Fatal("reconnect accepted old frame")
	}
	if err := oldDelivery(ctx, []byte("{}"), "next-fragment", 1); err == nil {
		t.Fatal("old window crossed new connection")
	}
	go func() { result <- bridge.delivery()(ctx, []byte("{}"), "new", 1) }()
	request = waitFrame(t, next)
	if err := next.write(request, func(context.Context, []byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	reply := v1.Message("node.metrics.receipt", "node", "reply", v1.TelemetryReceipt{BatchID: "old", State: "accepted", AcceptedCount: 1})
	reply.CorrelationID = "old"
	if old.receipt(reply) || next.receipt(reply) {
		t.Fatal("late receipt consumed")
	}
	select {
	case <-result:
		t.Fatal("late receipt completed new window")
	default:
	}
	cancel()
	if err := waitDelivery(t, result); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}
func TestTelemetryWSSMalformedAndRejectedReceipt(t *testing.T) {
	for _, tc := range []struct{ body, code string }{
		{`{"batch_id":"batch","state":"accepted","accepted_count":2,"duplicate_count":0}`, "invalid_response"},
		{`{"batch_id":"batch","state":"rejected","accepted_count":0,"duplicate_count":0,"error_code":"rate_limited"}`, "rate_limited"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		var bridge telemetryWSS
		port := bridge.attach()
		result := make(chan error, 1)
		go func() { result <- bridge.delivery()(ctx, []byte("{}"), "batch", 1) }()
		request := waitFrame(t, port)
		if err := port.write(request, func(context.Context, []byte) error { return nil }); err != nil {
			t.Fatal(err)
		}
		e := v1.Envelope{Type: "node.metrics.receipt", CorrelationID: "batch", Payload: json.RawMessage(tc.body)}
		if !port.receipt(e) {
			t.Fatal("matched invalid receipt ignored")
		}
		var failure *telemetryUploadError
		if err := waitDelivery(t, result); !errors.As(err, &failure) || failure.Code != tc.code {
			t.Fatalf("classification: %v", err)
		}
		cancel()
		port.close()
	}
}

func TestTelemetryWSSSplitWindowCannotCrossReconnect(t *testing.T) {
	fixture := uploadFixture(t)
	var window telemetryWindow
	for i := 1; i <= 300; i++ {
		fixture.Fast[0].Sequence = uint64(i)
		if err := window.add(fixture.Fast[0]); err != nil {
			t.Fatal(err)
		}
	}
	flight := window.take(false)
	var bridge telemetryWSS
	port := bridge.attach()
	defer port.close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client := &Client{}
	transmit := client.telemetryTransport(fixture.NodeID, fixture.RunID, bridge.delivery)
	result := make(chan error, 1)
	go func() {
		result <- transmit(ctx, telemetryTransmission{send: &telemetrySend{flight: flight}, credential: sessionCredential{Changed: make(chan struct{})}, report: 60})
	}()
	request := waitFrame(t, port)
	if request.count >= 300 {
		t.Fatal("fixture did not split")
	}
	if err := port.write(request, func(context.Context, []byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	receipt := v1.Message("node.metrics.receipt", fixture.NodeID, "receipt", v1.TelemetryReceipt{BatchID: request.id, State: "accepted", AcceptedCount: request.count})
	receipt.CorrelationID = request.id
	if !port.receipt(receipt) {
		t.Fatal("first receipt not matched")
	}
	next := bridge.attach()
	defer next.close()
	if err := waitDelivery(t, result); err == nil {
		t.Fatal("remaining window accepted after reconnect")
	}
	select {
	case <-next.requests:
		t.Fatal("remaining frames migrated to new generation")
	default:
	}
	window.release(flight)
}

func TestTelemetryWSSIdentityRejectionBlocksSharedSession(t *testing.T) {
	fixture := uploadFixture(t)
	var window telemetryWindow
	if err := window.add(fixture.Fast[0]); err != nil {
		t.Fatal(err)
	}
	flight := window.take(false)
	defer window.release(flight)
	client := &Client{}
	credential, err := client.sessions.Get(context.Background(), "node", func(context.Context) (sessionCredential, error) {
		return sessionCredential{Token: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(15 * time.Minute)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	transmit := client.telemetryTransport(fixture.NodeID, fixture.RunID, func() telemetryFrameDelivery {
		return func(context.Context, []byte, string, uint64) error {
			return &telemetryUploadError{Code: "identity_revoked"}
		}
	})
	err = transmit(context.Background(), telemetryTransmission{send: &telemetrySend{flight: flight}, credential: credential, report: 60})
	var failure *telemetryUploadError
	if !errors.As(err, &failure) || failure.Code != "identity_revoked" {
		t.Fatalf("wrong rejection: %v", err)
	}
	select {
	case <-credential.Changed:
	default:
		t.Fatal("management session not invalidated")
	}
	_, err = client.sessions.Get(context.Background(), "node", func(context.Context) (sessionCredential, error) {
		t.Error("revoked identity challenged again")
		return sessionCredential{}, nil
	})
	if !errors.Is(err, ErrIdentityRejected) {
		t.Fatalf("shared session not blocked: %v", err)
	}
}
