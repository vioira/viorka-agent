package connection

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vioira/viorka-agent/internal/config"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

func TestTelemetryReplayRetentionAndSnapshotOrdering(t *testing.T) {
	b := uploadFixture(t)
	now := b.Fast[0].SampledAt
	var w telemetryWindow
	// An older full group behind a large fast backlog must not be skipped by
	// attaching its latest snapshot to an earlier batch.
	for i := 1; i <= 300; i++ {
		b.Fast[0].Sequence = uint64(i)
		b.Fast[0].SampledAt = now.Add(time.Duration(i-300) * time.Second)
		if err := w.add(b.Fast[0]); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 10; i++ {
		b.Full[0].Sequence = uint64(i)
		if err := w.add(b.Full[0]); err != nil {
			t.Fatal(err)
		}
	}
	var high [3]uint64
	for len(w.pending) > 0 {
		f := w.take()
		for _, s := range f.samples {
			if s.sequence == high[s.kind] {
				continue
			} // already acknowledged snapshot
			if s.sequence != high[s.kind]+1 {
				t.Fatal("skipped sequence", s.kind, s.sequence, high)
			}
			high[s.kind] = s.sequence
		}
		w.release(f)
	}
	if high[0] != 300 || high[1] != 10 {
		t.Fatal(high)
	}
	b.Fast[0].Sequence = 301
	b.Fast[0].SampledAt = now.Add(-301 * time.Second)
	if err := w.add(b.Fast[0]); err != nil {
		t.Fatal(err)
	}
	w.expire(now)
	if w.droppedExpired != 1 || w.latest[0] != nil {
		t.Fatal("stale cache retained")
	}
	ip := &v1.TelemetryPublicIP{Sequence: 1, SampledAt: now.Add(-600 * time.Second), Ipv4: "8.8.8.8", Errors: []string{}}
	if err := w.add(ip); err != nil {
		t.Fatal(err)
	}
	w.expire(now)
	if w.latest[2] == nil {
		t.Fatal("IP refresh interval mistaken for offline history")
	}
	w.expire(now.Add(301 * time.Second))
	if w.latest[2] != nil {
		t.Fatal("protocol-expired IP retained")
	}
}

func TestTelemetryReplayBudgetDuringFailure(t *testing.T) {
	for _, limit := range []int{512 << 10, 1 << 20} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			b := uploadFixture(t)
			// Real schema-valid 64-interface samples exercise the byte budget first.
			base := b.Fast[0].Interfaces[0]
			for len(b.Fast[0].Interfaces) < 64 {
				n := *base
				n.Name = fmt.Sprintf("eth%d", len(b.Fast[0].Interfaces))
				b.Fast[0].Interfaces = append(b.Fast[0].Interfaces, &n)
			}
			w := telemetryWindow{maxBytes: limit}
			for i := 1; i <= 500; i++ {
				b.Fast[0].Sequence = uint64(i)
				if err := w.add(b.Fast[0]); err != nil {
					t.Fatal(err)
				}
				if i == 2 {
					w.take()
				}
				bytes, count := w.usage()
				if bytes > limit || count > limit/(1<<10) {
					t.Fatal("budget overflow", bytes, count)
				}
			}
			if w.droppedLimit == 0 {
				t.Fatal("fixture never reached byte bound")
			}
			f := w.inflight
			first := f.samples[0].sequence
			w.complete(f, false)
			bytes, count := w.usage()
			if bytes > limit || count > limit/(1<<10) {
				t.Fatal("requeue exceeded budget")
			}
			// Capacity pressure may explicitly evict oldest data, never reorder retained data.
			last := first - 1
			for _, s := range w.pending {
				if s.sequence <= last {
					t.Fatal("reordered retry")
				}
				last = s.sequence
			}
		})
	}
}

// Real TLS HTTP response loss: the receiver has accepted a prefix, but its
// response is lost. Retry must preserve sequence/time; duplicates count once.
func TestTelemetryHTTPReplayLostReceipt(t *testing.T) {
	b := uploadFixture(t)
	var highest uint64
	accepted, duplicates, calls := 0, 0, 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		raw, err := io.ReadAll(io.LimitReader(r.Body, telemetryBatchBytes+1))
		batch, decodeErr := v1.DecodeTelemetryBatch(raw, b.NodeID, time.Now(), 30, true)
		if err != nil || decodeErr != nil || len(raw) > telemetryBatchBytes {
			t.Error("invalid bounded replay", err, decodeErr)
			w.WriteHeader(400)
			return
		}
		receipt := v1.TelemetryReceipt{BatchID: batch.BatchID, State: "accepted"}
		for _, s := range batch.Fast {
			if s.Sequence <= highest {
				duplicates++
				receipt.DuplicateCount++
				continue
			}
			if s.Sequence != highest+1 {
				t.Error("sequence gap", highest, s.Sequence)
			}
			if s.SampledAt != b.Fast[0].SampledAt {
				t.Error("retimestamped replay")
			}
			highest = s.Sequence
			accepted++
			receipt.AcceptedCount++
		}
		if calls == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(receipt)
	}))
	defer server.Close()
	client := &Client{cfg: config.Local{PanelURL: server.URL}, http: server.Client()}
	defer client.Close()
	credential := sessionCredential{Token: strings.Repeat("a", 64), Generation: 1, ExpiresAt: time.Now().Add(time.Hour), Changed: make(chan struct{})}
	transmit := client.telemetryTransport(b.NodeID, b.RunID, nil)
	var window telemetryWindow
	for i := 1; i <= 250; i++ {
		b.Fast[0].Sequence = uint64(i)
		if err := window.add(b.Fast[0]); err != nil {
			t.Fatal(err)
		}
	}
	for len(window.pending) > 0 {
		f := window.take()
		err := transmit(context.Background(), telemetryTransmission{send: &telemetrySend{attempt: telemetryAttempt{HTTP: true}, flight: f}, credential: credential, report: 30})
		if calls == 1 && err == nil {
			t.Fatal("lost response acknowledged")
		}
		if calls > 1 && err != nil {
			t.Fatal(err)
		}
		window.complete(f, err == nil)
	}
	if accepted != 250 || duplicates == 0 || highest != 250 || calls < 3 {
		t.Fatal("replay loss/dedup", accepted, duplicates, highest, calls)
	}
}

func TestTelemetryReplayAfterThreeMinuteDisconnect(t *testing.T) {
	settings := v1.DefaultTelemetrySettings()
	settings.CollectionSeconds = 2
	s, start := scheduleFixture(t, settings)
	s.advance(start)
	drainSchedule(t, s)
	s.postWanted = true
	first := s.take(start)
	s.finish(first, start, "network_error", 0)
	s.disconnect(start)
	for seconds := 2; seconds <= 180; seconds += 2 {
		s.advance(start.Add(time.Duration(seconds) * time.Second))
		drainSchedule(t, s)
	}
	if err := s.reconnected(2, "next", start.Add(180*time.Second)); err != nil {
		t.Fatal(err)
	}
	send := s.take(start.Add(180 * time.Second))
	if send == nil || send.flight.samples[0].sequence != 1 {
		t.Fatal("reconnect skipped oldest")
	}
	last := uint64(0)
	at := start.Add(180 * time.Second)
	for send != nil {
		for _, sample := range send.flight.samples {
			if sample.sequence != last+1 {
				t.Fatal("lost ordered sample")
			}
			last = sample.sequence
		}
		s.finish(send, at, "", 0)
		if len(s.window.pending) == 0 {
			break
		}
		at = at.Add(time.Duration(settings.HttpReportSeconds) * time.Second)
		s.postWanted = true
		send = s.take(at)
	}
	if last != 91 {
		t.Fatal("missing offline observations", last)
	}
}

func TestTelemetryReplayOversizedSampleDoesNotPoisonQueue(t *testing.T) {
	batch := uploadFixture(t)
	var w telemetryWindow
	if err := w.add(batch.Fast[0]); err != nil {
		t.Fatal(err)
	}
	original := w.latest[0]
	batch.Fast[0].Sequence++
	batch.Fast[0].Interfaces[0].Name = strings.Repeat("x", telemetryBatchBytes)
	if err := w.add(batch.Fast[0]); err == nil {
		t.Fatal("oversized observation accepted")
	}
	if w.latest[0] != original || len(w.pending) != 1 || w.droppedOversized != 1 {
		t.Fatal("oversize displaced valid history")
	}
	f := w.take()
	if len(f.samples) != 1 || f.samples[0].sequence != original.sequence {
		t.Fatal("valid sample poisoned")
	}
}
