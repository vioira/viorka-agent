package connection

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

// The scheduler owns this bounded queue. Raw observations are immutable and
// shared with latest snapshots; the sender only publishes acknowledged progress
// before returning its result. Never send a group's latest value ahead of backlog.
const (
	telemetryWindowReserve = 8192
	telemetryBufferBytes   = 512 << 10
	telemetryBufferSamples = 512
	telemetryBatchBytes    = 64 << 10
	telemetryRetention     = 5 * time.Minute
)

type telemetrySample struct {
	slowUptime bool
	kind       int
	sequence   uint64
	at         time.Time
	raw        json.RawMessage
}
type telemetryWindow struct {
	pending  []telemetrySample
	latest   [3]*telemetrySample
	sequence [3]uint64
	bytes    int // pending only; usage includes flight and unshared latest snapshots
	maxBytes int
	inflight *telemetryFlight

	droppedLimit, droppedExpired, droppedOversized, retried, acknowledged uint64
}
type telemetryFlight struct {
	samples []telemetrySample
	daily   []*v1.TelemetryDailyTraffic
	// Written only by the sender; read only after the result channel handoff.
	acknowledged int
}

func (w *telemetryWindow) limits() (int, int) {
	if w.maxBytes == 1<<20 {
		return 1 << 20, 1024
	}
	return telemetryBufferBytes, telemetryBufferSamples
}
func (w *telemetryWindow) usage() (bytes, count int) {
	var shared [3]bool
	add := func(s telemetrySample) {
		bytes += len(s.raw) + 1
		count++
		if latest := w.latest[s.kind]; latest != nil && latest.sequence == s.sequence {
			shared[s.kind] = true
		}
	}
	for _, s := range w.pending {
		add(s)
	}
	if w.inflight != nil {
		for _, s := range w.inflight.samples {
			add(s)
		}
	}
	for kind, s := range w.latest {
		if s != nil && !shared[kind] {
			bytes += len(s.raw) + 1
			count++
		}
	}
	return
}
func (w *telemetryWindow) trim() {
	maxBytes, maxCount := w.limits()
	for {
		bytes, count := w.usage()
		if bytes <= maxBytes && count <= maxCount {
			return
		}
		if len(w.pending) == 0 {
			return
		} // flight <=56 KiB plus three bounded snapshots
		w.remove(0)
		w.droppedLimit++
	}
}
func (w *telemetryWindow) add(value any) error {
	var kind int
	var slowUptime bool
	var sequence uint64
	var at time.Time
	var schema string
	switch s := value.(type) {
	case *v1.TelemetryFast:
		if s == nil {
			return fmt.Errorf("nil fast sample")
		}
		slowUptime = s.UptimeSeconds == nil && !slices.Contains(s.Errors, "uptime_unavailable") && !slices.Contains(s.Errors, "unsupported_platform")
		kind, sequence, at, schema = 0, s.Sequence, s.SampledAt, "TelemetryFast"
	case *v1.TelemetryFull:
		if s == nil {
			return fmt.Errorf("nil full sample")
		}
		kind, sequence, at, schema = 1, s.Sequence, s.SampledAt, "TelemetryFull"
	case *v1.TelemetryPublicIP:
		if s == nil {
			return fmt.Errorf("nil IP sample")
		}
		slowUptime = s.Uptime != nil
		kind, sequence, at, schema = 2, s.Sequence, s.SampledAt, "TelemetryPublicIP"
	default:
		return fmt.Errorf("unknown telemetry sample")
	}
	if sequence <= w.sequence[kind] {
		return fmt.Errorf("non-increasing telemetry sequence")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(raw)+1 > telemetryBatchBytes-telemetryWindowReserve {
		w.droppedOversized++
		return fmt.Errorf("telemetry sample exceeds batch")
	}
	if err := v1.ValidateTelemetryJSON(raw, schema, true); err != nil {
		return err
	}
	sample := telemetrySample{slowUptime: slowUptime, kind: kind, sequence: sequence, at: at, raw: raw}
	w.sequence[kind] = sequence
	w.latest[kind] = &sample
	w.pending = append(w.pending, sample)
	w.bytes += len(raw) + 1
	w.trim()
	return nil
}
func (w *telemetryWindow) remove(i int) {
	w.bytes -= len(w.pending[i].raw) + 1
	copy(w.pending[i:], w.pending[i+1:])
	w.pending[len(w.pending)-1] = telemetrySample{}
	w.pending = w.pending[:len(w.pending)-1]
}

// expire uses observation time, never retimestamps replay. Leave at least 30s
// of protocol admission headroom for the bounded 15s upload. IP snapshots are
// refreshed every 600s and have a separate 915s protocol age allowance.
func (w *telemetryWindow) expire(now time.Time) {
	for i := 0; i < len(w.pending); {
		if now.Sub(w.pending[i].at) > telemetryRetention {
			w.remove(i)
			w.droppedExpired++
		} else {
			i++
		}
	}
	for kind, s := range w.latest {
		age := telemetryRetention
		if kind == 2 {
			age = 900 * time.Second
		}
		if s != nil && now.Sub(s.at) > age {
			w.latest[kind] = nil
		}
	}
}
func (w *telemetryWindow) take() *telemetryFlight {
	if w.inflight != nil {
		return nil
	}
	var samples []telemetrySample
	var counts [3]int
	size, end := 0, 0
	for _, s := range w.pending {
		if size+len(s.raw)+1 > telemetryBatchBytes-telemetryWindowReserve || counts[s.kind] == [3]int{300, 8, 2}[s.kind] {
			break
		}
		samples = append(samples, s)
		counts[s.kind]++
		size += len(s.raw) + 1
		end++
	}
	copy(w.pending, w.pending[end:])
	clear(w.pending[len(w.pending)-end:])
	w.pending = w.pending[:len(w.pending)-end]
	w.bytes -= size
	// Only attach a cached group when none of that group's unsent observations
	// remain. Otherwise a higher watermark would make subsequent replay disappear.
	var waiting [3]bool
	for _, s := range w.pending {
		waiting[s.kind] = true
	}
	for kind, s := range w.latest {
		if s != nil && counts[kind] == 0 && !waiting[kind] && size+len(s.raw)+1 <= telemetryBatchBytes-telemetryWindowReserve {
			samples = append(samples, *s)
			size += len(s.raw) + 1
		}
	}
	if len(samples) == 0 {
		return nil
	}
	w.inflight = &telemetryFlight{samples: samples}
	return w.inflight
}

// complete is called only after the sole sender returns. An unconfirmed suffix
// goes before new observations, including when acceptance's receipt was lost.
func (w *telemetryWindow) complete(f *telemetryFlight, success bool) bool {
	if f == nil || w.inflight != f {
		return false
	}
	ack := f.acknowledged
	if success {
		ack = len(f.samples)
	}
	w.acknowledged += uint64(ack)
	if ack < len(f.samples) {
		remaining := f.samples[ack:]
		pending := make([]telemetrySample, 0, len(remaining)+len(w.pending))
		pending = append(pending, remaining...)
		pending = append(pending, w.pending...)
		w.pending = pending
		for _, s := range remaining {
			w.bytes += len(s.raw) + 1
		}
		w.retried += uint64(len(remaining))
	}
	f.samples = nil
	w.inflight = nil
	w.trim()
	return true
}
func (w *telemetryWindow) release(f *telemetryFlight) bool { return w.complete(f, true) }

type telemetryWireBatch struct {
	NodeID   string                      `json:"node_id"`
	RunID    string                      `json:"run_id"`
	BatchID  string                      `json:"batch_id"`
	Fast     []json.RawMessage           `json:"fast"`
	Full     []json.RawMessage           `json:"full"`
	PublicIP []json.RawMessage           `json:"public_ip"`
	Daily    []*v1.TelemetryDailyTraffic `json:"daily_traffic,omitempty"`
}

func encodeTelemetryWindow(samples []telemetrySample, node, run, id string, daily ...*v1.TelemetryDailyTraffic) ([]byte, error) {
	if len(node) > 128 || len(run) > 128 || len(id) > 128 {
		return nil, fmt.Errorf("telemetry identity exceeds bound")
	}
	b := telemetryWireBatch{node, run, id, []json.RawMessage{}, []json.RawMessage{}, []json.RawMessage{}, daily}
	for _, s := range samples {
		switch s.kind {
		case 0:
			b.Fast = append(b.Fast, s.raw)
		case 1:
			b.Full = append(b.Full, s.raw)
		case 2:
			b.PublicIP = append(b.PublicIP, s.raw)
		}
	}
	raw, err := json.Marshal(b)
	if err == nil && len(raw) > v1.TelemetryMaxBodyBytes {
		return nil, fmt.Errorf("telemetry body exceeds bound")
	}
	return raw, err
}

// frame encodes just the next frame, never a second full-window copy or an
// unbounded list of encoded frames. The caller awaits its receipt before moving
// offset forward. A failure retains the unconfirmed suffix for the scheduler.
func (f *telemetryFlight) frame(offset int, node, run, id string, now time.Time) ([]byte, int, error) {
	if offset < 0 || offset >= len(f.samples) {
		return nil, offset, fmt.Errorf("invalid telemetry frame offset")
	}
	// Measure the actual empty envelope once. Samples are already canonical
	// json.Marshal output, so their bytes and per-array commas give exact size.
	empty, err := encodeTelemetryWindow(nil, node, run, id, f.daily...)
	if err != nil {
		return nil, offset, err
	}
	envelope := v1.Envelope{Version: v1.Version, MessageID: id, NodeID: node, Type: "node.metrics.v3", SentAt: now, Payload: empty}
	header, err := json.Marshal(envelope)
	if err != nil {
		return nil, offset, err
	}
	end, size := offset, len(header)
	var counts [3]int
	for end < len(f.samples) {
		s := f.samples[end]
		cost := len(s.raw)
		if counts[s.kind] > 0 {
			cost++
		}
		if size+cost > v1.TelemetryMaxFrameBytes {
			break
		}
		size += cost
		counts[s.kind]++
		end++
	}
	if end == offset {
		return nil, offset, fmt.Errorf("single telemetry sample exceeds WSS frame")
	}
	body, err := encodeTelemetryWindow(f.samples[offset:end], node, run, id, f.daily...)
	if err != nil {
		return nil, offset, err
	}
	envelope.Payload = body
	raw, err := json.Marshal(envelope)
	if err != nil {
		return nil, offset, err
	}
	if len(raw) > v1.TelemetryMaxFrameBytes {
		return nil, offset, fmt.Errorf("telemetry envelope exceeds frame bound")
	}
	return raw, end, nil
}
