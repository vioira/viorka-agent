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
	for i := 1; i <= 600; i++ {
		b.Fast[0].Sequence = uint64(i)
		if err := w.add(b.Fast[0]); err != nil {
			t.Fatal(err)
		}
	}
	size, count := w.usage()
	if size > telemetryBufferBytes || count > 512 || w.droppedLimit == 0 {
		t.Fatal("unbounded queue", size, count)
	}
	firstSequence := w.pending[0].sequence
	f := w.take()
	if f == nil || w.take() != nil || len(w.pending) == 0 {
		t.Fatal("bounded flight ownership")
	}
	b.Fast[0].Sequence = 601
	if err := w.add(b.Fast[0]); err != nil {
		t.Fatal(err)
	}
	b.Fast[0].Sequence = 999
	var first v1.TelemetryFast
	if err := json.Unmarshal(f.samples[0].raw, &first); err != nil || first.Sequence != firstSequence {
		t.Fatal("collector mutated flight", err)
	}
	unconfirmed := f.samples[1].sequence
	f.acknowledged = 1
	if !w.complete(f, false) || w.release(f) || f.samples != nil {
		t.Fatal("completion ownership")
	}
	if w.pending[0].sequence != unconfirmed {
		t.Fatal("retry not oldest first")
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
	count, batches := 0, 0
	var previous uint64
	for len(w.pending) > 0 {
		f := w.take()
		body, err := encodeTelemetryWindow(f.samples, b.NodeID, b.RunID, "http-batch")
		if err != nil || len(body) > telemetryBatchBytes {
			t.Fatal("HTTP batch bound", err)
		}
		decoded, err := v1.DecodeTelemetryBatch(body, b.NodeID, b.Fast[0].SampledAt, 60, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range decoded.Fast {
			if s.Sequence <= previous {
				t.Fatal("latest skipped backlog")
			}
			previous = s.Sequence
			count++
		}
		raw, next, err := f.frame(0, b.NodeID, b.RunID, fmt.Sprintf("frame-%d", batches), b.Fast[0].SampledAt)
		if err != nil || len(raw) > telemetryBatchBytes || next != len(f.samples) {
			t.Fatal("frame bound", err)
		}
		batches++
		w.release(f)
	}
	if batches < 2 || count != 300 || previous != 300 {
		t.Fatal("lost samples", batches, count, previous)
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
	if w.bytes > telemetryBufferBytes || w.droppedLimit == 0 || len(w.pending) >= 300 {
		t.Fatal("byte bound not enforced")
	}
	f := w.take()
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
