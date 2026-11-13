package connection

import (
	"encoding/json"
	"fmt"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

// All methods belong to the scheduler. Encoded samples are immutable and owned
// here, so collector reuse cannot mutate an in-flight window. The one sender
// owns a detached window until release, including after partial WSS failure.
// Limits: pending and inflight each <=1MiB; three latest samples each <=1MiB;
// a body/frame encoder may additionally allocate <=1MiB/64KiB respectively.
const telemetryWindowReserve = 8192

type telemetrySample struct {
	kind     int
	sequence uint64
	raw      json.RawMessage
}
type telemetryWindow struct {
	pending                       []telemetrySample
	latest                        [3]*telemetrySample
	bytes                         int
	inflight                      *telemetryFlight
	droppedLimit, droppedCooldown uint64
}
type telemetryFlight struct {
	samples []telemetrySample
	daily   []*v1.TelemetryDailyTraffic
}

func (w *telemetryWindow) add(value any) error {
	var kind int
	var sequence uint64
	var schema string
	switch s := value.(type) {
	case *v1.TelemetryFast:
		if s == nil {
			return fmt.Errorf("nil fast sample")
		}
		kind, sequence, schema = 0, s.Sequence, "TelemetryFast"
	case *v1.TelemetryFull:
		if s == nil {
			return fmt.Errorf("nil full sample")
		}
		kind, sequence, schema = 1, s.Sequence, "TelemetryFull"
	case *v1.TelemetryPublicIP:
		if s == nil {
			return fmt.Errorf("nil IP sample")
		}
		kind, sequence, schema = 2, s.Sequence, "TelemetryPublicIP"
	default:
		return fmt.Errorf("unknown telemetry sample")
	}
	if old := w.latest[kind]; old != nil && sequence <= old.sequence {
		return fmt.Errorf("non-increasing telemetry sequence")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(raw)+1 > v1.TelemetryMaxBodyBytes-telemetryWindowReserve {
		return fmt.Errorf("telemetry sample exceeds window")
	}
	if err := v1.ValidateTelemetryJSON(raw, schema, true); err != nil {
		return err
	}
	sample := telemetrySample{kind: kind, sequence: sequence, raw: raw}
	w.latest[kind] = &sample
	w.pending = append(w.pending, sample)
	w.bytes += len(raw) + 1
	limit := [3]int{300, 8, 2}[kind]
	count := 0
	for _, s := range w.pending {
		if s.kind == kind {
			count++
		}
	}
	for count > limit {
		for i, s := range w.pending {
			if s.kind == kind {
				w.remove(i)
				w.droppedLimit++
				count--
				break
			}
		}
	}
	for w.bytes > v1.TelemetryMaxBodyBytes-telemetryWindowReserve {
		w.remove(0)
		w.droppedLimit++
	}
	return nil
}
func (w *telemetryWindow) remove(i int) {
	w.bytes -= len(w.pending[i].raw) + 1
	copy(w.pending[i:], w.pending[i+1:])
	w.pending[len(w.pending)-1] = telemetrySample{}
	w.pending = w.pending[:len(w.pending)-1]
}

// collapse is called at each R boundary without a sending opportunity. Cached
// observations retain their original sequence/time; no offline replay queue.
func (w *telemetryWindow) collapse() {
	w.droppedCooldown += uint64(len(w.pending))
	w.pending = nil
	w.bytes = 0
}
func (w *telemetryWindow) take(latestOnly bool) *telemetryFlight {
	if w.inflight != nil {
		return nil
	}
	var samples []telemetrySample
	if latestOnly || len(w.pending) == 0 {
		for _, s := range w.latest {
			if s != nil {
				samples = append(samples, *s)
			}
		}
		// Latest groups together must fit one logical window as well. Prefer fast,
		// then full, then IP, without changing their observation identities.
		size := 0
		kept := samples[:0]
		for _, s := range samples {
			if size+len(s.raw)+1 <= v1.TelemetryMaxBodyBytes-telemetryWindowReserve {
				kept = append(kept, s)
				size += len(s.raw) + 1
			}
		}
		samples = kept
	} else {
		samples = w.pending
		w.pending = nil
		w.bytes = 0
	}
	// Slow caches accompany fast-only windows, retaining original identities.
	// Leave space bounded rather than evicting unsent fast observations.
	var present [3]bool
	size := 0
	for _, s := range samples {
		present[s.kind] = true
		size += len(s.raw) + 1
	}
	for kind, s := range w.latest {
		if s != nil && !present[kind] && size+len(s.raw)+1 <= v1.TelemetryMaxBodyBytes-telemetryWindowReserve {
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
func (w *telemetryWindow) release(f *telemetryFlight) bool {
	if f == nil || w.inflight != f {
		return false
	}
	f.samples = nil
	w.inflight = nil
	return true
}

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
// offset forward. A failure discards the entire flight, including its remainder.
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
