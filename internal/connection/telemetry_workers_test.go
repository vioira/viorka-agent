package connection

import (
	"bytes"
	"context"
	"errors"
	"github.com/vioira/viorka-agent/internal/config"
	"github.com/vioira/viorka-agent/internal/state"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func receiveWork(t *testing.T, w *telemetryWorkers) telemetryWorkResult {
	t.Helper()
	select {
	case r := <-w.results:
		w.complete(r)
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("worker result timeout")
		return telemetryWorkResult{}
	}
}
func TestTelemetryWorkersIsolationAndFinalSave(t *testing.T) {
	b := windowFixture(t)
	started := make(chan int, 3)
	var sequence uint64
	var canceled atomic.Int32
	var final atomic.Int32
	blocked := func(kind int) func(context.Context, time.Time) (any, error) {
		return func(ctx context.Context, _ time.Time) (any, error) {
			started <- kind
			<-ctx.Done()
			canceled.Add(1)
			return nil, ctx.Err()
		}
	}
	sources := telemetrySources{
		fast: func(context.Context, time.Time) (any, error) {
			sequence++
			s := *b.Fast[0]
			s.Sequence = sequence
			return &s, nil
		}, full: blocked(1), ip: blocked(2),
		save: func(ctx context.Context, _ time.Time, force bool) error {
			if force {
				if canceled.Load() != 3 {
					return errors.New("final save before collector completion")
				}
				final.Add(1)
				return nil
			}
			started <- 3
			<-ctx.Done()
			canceled.Add(1)
			return ctx.Err()
		},
	}
	w := startTelemetryWorkers(context.Background(), sources)
	defer w.cancel()
	now := time.Now()
	for _, kind := range []int{1, 2, 3} {
		if !w.submit(kind, now) {
			t.Fatal("submit")
		}
	}
	for i := 0; i < 3; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("worker did not start")
		}
	}
	var window telemetryWindow
	for i := 0; i < 3; i++ {
		if !w.submit(0, now) || w.submit(0, now) {
			t.Fatal("single outstanding operation violated")
		}
		r := receiveWork(t, w)
		if r.kind != 0 || r.err != nil {
			t.Fatalf("fast blocked: %+v", r)
		}
		if err := window.add(r.value); err != nil {
			t.Fatal(err)
		}
	}
	if len(window.pending) != 3 {
		t.Fatal("fast observations lost")
	}
	for _, kind := range []int{1, 2, 3} {
		if w.submit(kind, now) {
			t.Fatal("queued duplicate slow operation")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := w.stop(ctx); err != nil {
		t.Fatal(err)
	}
	if final.Load() != 1 {
		t.Fatal("final save missing")
	}
	if err := w.stop(ctx); err != nil {
		t.Fatal("repeat stop", err)
	}
	if w.submit(0, now) {
		t.Fatal("accepted after shutdown")
	}
}
func TestTelemetryWorkersShutdownTimeoutIsVisible(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	noop := func(context.Context, time.Time) (any, error) { return nil, nil }
	finalErr := errors.New("disk sync failed")
	w := startTelemetryWorkers(context.Background(), telemetrySources{fast: noop, full: func(context.Context, time.Time) (any, error) { close(entered); <-release; return nil, nil }, ip: noop, save: func(context.Context, time.Time, bool) error { return finalErr }})
	defer func() {
		close(release)
		select {
		case <-w.done:
		case <-time.After(2 * time.Second):
			t.Error("worker did not exit after release")
		}
	}()
	w.submit(1, time.Now())
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("full not started")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.stop(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown falsely succeeded: %v", err)
	}
	// Release occurs in defer; no stuck test goroutine is left behind.
}
func TestTelemetryWorkersFinalSaveFailure(t *testing.T) {
	noop := func(context.Context, time.Time) (any, error) { return nil, nil }
	failure := errors.New("final write failed")
	w := startTelemetryWorkers(context.Background(), telemetrySources{fast: noop, full: noop, ip: noop, save: func(context.Context, time.Time, bool) error { return failure }})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := w.stop(ctx); !errors.Is(err, failure) {
		t.Fatalf("lost final failure: %v", err)
	}
}

func TestTelemetrySourcesPreserveCorruptTraffic(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	store, err := state.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	original := []byte("corrupt ledger must be preserved")
	path := filepath.Join(dir, "traffic.dat")
	if err = os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	sources, err := newTelemetrySources(config.Local{}, store, state.Identity{PanelURL: "https://panel.test", NodeID: "node-test"})
	if err != nil {
		t.Fatal("corrupt traffic disabled all collection", err)
	}
	if sources.trafficError == nil {
		t.Fatal("missing startup diagnostic")
	}
	now := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if value, err := sources.fast(ctx, now); value == nil || err != nil {
		t.Fatal("fast group unavailable", err)
	}
	if err = sources.save(ctx, now, true); err == nil {
		t.Fatal("blocked ledger reported save success")
	}
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, actual) {
		t.Fatal("corrupt ledger overwritten")
	}
}
