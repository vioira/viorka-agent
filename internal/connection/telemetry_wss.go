package connection

import (
	"context"
	"fmt"
	"sync"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

type telemetryFrameRequest struct {
	ctx    context.Context
	raw    []byte
	id     string
	count  uint64
	result chan error
}

// telemetryWSS switches immutable connection ports. A logical transmission
// snapshots one port, so reconnecting cannot move remaining frames into a new
// management generation. It never owns or writes a socket.
type telemetryWSS struct {
	mu      sync.Mutex
	current *telemetryWSSPort
}
type telemetryWSSPort struct {
	requests  chan *telemetryFrameRequest
	done      chan struct{}
	closeOnce sync.Once
	pending   *telemetryFrameRequest // accessed only by the management writer
}

func (b *telemetryWSS) attach() *telemetryWSSPort {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.current != nil {
		b.current.close()
	}
	p := &telemetryWSSPort{requests: make(chan *telemetryFrameRequest, 1), done: make(chan struct{})}
	b.current = p
	return p
}
func (p *telemetryWSSPort) close() { p.closeOnce.Do(func() { close(p.done) }) }
func (b *telemetryWSS) delivery() telemetryFrameDelivery {
	b.mu.Lock()
	p := b.current
	b.mu.Unlock()
	if p == nil {
		return nil
	}
	return p.deliver
}
func (p *telemetryWSSPort) deliver(ctx context.Context, raw []byte, id string, count uint64) error {
	if len(raw) == 0 || len(raw) > v1.TelemetryMaxFrameBytes || id == "" || count == 0 || count > 310 {
		return &telemetryUploadError{Code: "invalid_batch"}
	}
	request := &telemetryFrameRequest{ctx: ctx, raw: raw, id: id, count: count, result: make(chan error, 1)}
	select {
	case <-p.done:
		return &telemetryUploadError{Code: "network_error"}
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	select {
	case p.requests <- request:
	case <-p.done:
		return &telemetryUploadError{Code: "network_error"}
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-request.result:
		return err
	case <-p.done:
		return &telemetryUploadError{Code: "network_error"}
	case <-ctx.Done():
		return ctx.Err()
	}
}

// write runs on the existing management writer. Successful Write leaves a
// pending receipt while heartbeats/tasks/control messages continue normally.
func (p *telemetryWSSPort) write(request *telemetryFrameRequest, send func(context.Context, []byte) error) error {
	select {
	case <-p.done:
		return nil
	default:
	}
	if request.ctx.Err() != nil {
		return nil
	}
	if p.pending != nil && p.pending.ctx.Err() == nil {
		err := fmt.Errorf("multiple telemetry frames in flight")
		request.result <- &telemetryUploadError{Code: "invalid_response", Cause: err}
		return err
	}
	p.pending = request
	if err := send(request.ctx, request.raw); err != nil {
		p.pending = nil
		request.result <- &telemetryUploadError{Code: "network_error", Cause: err}
		return err
	}
	return nil
}

// receipt ignores unmatched/late frames, never applying them to the next
// attempt. The caller already authenticates/parses the envelope's node/session.
func (p *telemetryWSSPort) receipt(e v1.Envelope) bool {
	pending := p.pending
	if pending == nil || e.CorrelationID != pending.id {
		return false
	}
	p.pending = nil
	if pending.ctx.Err() != nil {
		return false
	}
	select {
	case <-p.done:
		return false
	default:
	}
	var receipt v1.TelemetryReceipt
	var err error
	if e.Type != "node.metrics.receipt" || e.Revision != nil || v1.ValidateTelemetryJSON(e.Payload, "TelemetryReceipt", false) != nil || v1.DecodeTelemetry(e.Payload, &receipt, false) != nil || receipt.Validate(pending.count) != nil || receipt.BatchID != pending.id {
		err = &telemetryUploadError{Code: "invalid_response"}
	} else if receipt.State == "rejected" {
		err = &telemetryUploadError{Code: receipt.ErrorCode}
	}
	pending.result <- err
	return true
}
