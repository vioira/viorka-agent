package connection

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

func windowFixture(t *testing.T) v1.TelemetryBatch {
	t.Helper()
	raw, err := os.ReadFile("../../protocol/v1/testdata/node.metrics.v3.json")
	if err != nil {
		t.Fatal(err)
	}
	var e v1.Envelope
	if err = json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	var b v1.TelemetryBatch
	if err = json.Unmarshal(e.Payload, &b); err != nil {
		t.Fatal(err)
	}
	return b
}
func TestTelemetryWindowOwnershipAndLimit(t *testing.T) {
	b := windowFixture(t)
	var w telemetryWindow
	for i := 1; i <= 301; i++ {
		b.Fast[0].Sequence = uint64(i)
		if err := w.add(b.Fast[0]); err != nil {
			t.Fatal(err)
		}
	}
	if len(w.pending) != 300 || w.pending[0].sequence != 2 || w.droppedLimit != 1 {
		t.Fatalf("limit: %+v", w)
	}
	f := w.take(false)
	if f == nil || w.take(false) != nil || len(w.pending) != 0 {
		t.Fatal("flight ownership")
	}
	b.Fast[0].Sequence = 302
	if err := w.add(b.Fast[0]); err != nil {
		t.Fatal(err)
	}
	b.Fast[0].Sequence = 999
	var first v1.TelemetryFast
	if err := json.Unmarshal(f.samples[0].raw, &first); err != nil {
		t.Fatal(err)
	}
	if first.Sequence != 2 {
		t.Fatal("collector mutated flight")
	}
	if !w.release(f) || w.release(f) || f.samples != nil {
		t.Fatal("release")
	}
	if len(w.pending) != 1 || w.pending[0].sequence != 302 {
		t.Fatal("release lost new sample")
	}
	w.collapse()
	if len(w.pending) != 0 || w.bytes != 0 || w.droppedCooldown != 1 {
		t.Fatal("cooldown")
	}
	f = w.take(false)
	if len(f.samples) != 1 || f.samples[0].sequence != 302 {
		t.Fatal("latest identity")
	}
	w.release(f)
	if err := w.add(&v1.TelemetryFast{Sequence: 302}); err == nil {
		t.Fatal("duplicate accepted")
	}
}
func TestTelemetryWindowFramesRoundTrip(t *testing.T) {
	b := windowFixture(t)
	var w telemetryWindow
	for i := 1; i <= 300; i++ {
		b.Fast[0].Sequence = uint64(i)
		if err := w.add(b.Fast[0]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.add(b.Full[0]); err != nil {
		t.Fatal(err)
	}
	f := w.take(false)
	body, err := encodeTelemetryWindow(f.samples, b.NodeID, b.RunID, "http-batch")
	if err != nil {
		t.Fatal(err)
	}
	now := b.Fast[0].SampledAt
	if _, err = v1.DecodeTelemetryBatch(body, b.NodeID, now, 60, true); err != nil {
		t.Fatal(err)
	}
	count, frames, offset := 0, 0, 0
	for offset < len(f.samples) {
		raw, next, err := f.frame(offset, b.NodeID, b.RunID, fmt.Sprintf("frame-%d", frames), now)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) > v1.TelemetryMaxFrameBytes || next <= offset {
			t.Fatal("frame bound/progress")
		}
		var e v1.Envelope
		if err = json.Unmarshal(raw, &e); err != nil {
			t.Fatal(err)
		}
		decoded, err := v1.DecodeTelemetryBatch(e.Payload, b.NodeID, now, 60, true)
		if err != nil {
			t.Fatal(err)
		}
		count += len(decoded.Fast) + len(decoded.Full)
		frames++
		offset = next
	}
	if frames < 2 || count != 301 {
		t.Fatalf("frames=%d count=%d", frames, count)
	}
	w.release(f)
	// Only the latest snapshot may be retried, never the detached 301 samples.
	fallback := w.take(true)
	if len(fallback.samples) != 2 {
		t.Fatal("failed window replayed")
	}
}
func TestTelemetryWindowByteBoundAndOversizedFrame(t *testing.T) {
	b := windowFixture(t)
	var w telemetryWindow
	// Many valid interface observations make the byte limit tighter than count.
	base := b.Fast[0].Interfaces[0]
	for len(b.Fast[0].Interfaces) < 64 {
		c := *base
		c.Name = fmt.Sprintf("eth%03d", len(b.Fast[0].Interfaces))
		b.Fast[0].Interfaces = append(b.Fast[0].Interfaces, &c)
	}
	for i := 1; i <= 300; i++ {
		b.Fast[0].Sequence = uint64(i)
		if err := w.add(b.Fast[0]); err != nil {
			t.Fatal(err)
		}
	}
	if w.bytes > v1.TelemetryMaxBodyBytes-telemetryWindowReserve || w.droppedLimit == 0 || len(w.pending) >= 300 {
		t.Fatal("byte bound not enforced")
	}
	f := w.take(false)
	raw, err := encodeTelemetryWindow(f.samples, b.NodeID, b.RunID, "batch")
	if err != nil || len(raw) > v1.TelemetryMaxBodyBytes {
		t.Fatal("body bound", err)
	}
	w.release(f)
	huge := &telemetryFlight{samples: []telemetrySample{{raw: json.RawMessage(`"` + strings.Repeat("x", v1.TelemetryMaxFrameBytes) + `"`)}}}
	if _, _, err := huge.frame(0, b.NodeID, b.RunID, "batch", time.Now()); err == nil {
		t.Fatal("oversized single group accepted")
	}
}

func TestTelemetryWindowExactFrameBoundary(t *testing.T) {
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	f := &telemetryFlight{samples: []telemetrySample{{raw: json.RawMessage(`""`)}}}
	raw, _, err := f.frame(0, "node", "11111111-1111-4111-8111-111111111111", "batch", now)
	if err != nil {
		t.Fatal(err)
	}
	spare := v1.TelemetryMaxFrameBytes - len(raw)
	f.samples[0].raw = json.RawMessage(`"` + strings.Repeat("x", spare) + `"`)
	raw, next, err := f.frame(0, "node", "11111111-1111-4111-8111-111111111111", "batch", now)
	if err != nil || len(raw) != v1.TelemetryMaxFrameBytes || next != 1 {
		t.Fatalf("exact frame %d %d %v", len(raw), next, err)
	}
	f.samples[0].raw = json.RawMessage(`"` + strings.Repeat("x", spare+1) + `"`)
	if _, _, err = f.frame(0, "node", "11111111-1111-4111-8111-111111111111", "batch", now); err == nil {
		t.Fatal("one byte overflow accepted")
	}
}
