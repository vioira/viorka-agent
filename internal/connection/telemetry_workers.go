package connection

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/vioira/viorka-agent/internal/collector"
	"github.com/vioira/viorka-agent/internal/config"
	"github.com/vioira/viorka-agent/internal/state"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

type telemetryWorkResult struct {
	kind  int
	value any
	err   error
}
type telemetrySources struct {
	configureProbe func(*v1.TelemetryTCPProbeSettings)
	validate       func(v1.TelemetryTrafficSettings) error
	configure      func(v1.TelemetryTrafficSettings) error
	calibrate      func(context.Context, string, v1.TrafficCalibration, time.Time) (*v1.TrafficCalibration, error)
	trafficError   error // startup diagnostic; a blocked ledger does not disable other groups
	fast, full, ip func(context.Context, time.Time) (any, error)
	save           func(context.Context, time.Time, bool) error
}

// Production sources are instantiated once per Agent run. Reconnecting or
// changing cadence never reconstructs CPU/network/disk/traffic baselines.
func newTelemetrySources(cfg config.Local, store *state.Store, id state.Identity, probeEnabled ...func() bool) (telemetrySources, error) {
	fast, err := collector.NewTelemetryFast(cfg.NetworkFilter)
	if err != nil {
		return telemetrySources{}, err
	}
	ledger, ledgerErr := state.OpenTrafficLedger(store, id.PanelURL, id.NodeID)
	// Open returns a blocked ledger for unsafe/corrupt state. Keep it attached so
	// fast reports unknown traffic instead of starting a false zero ledger.
	if ledger == nil {
		return telemetrySources{}, fmt.Errorf("open traffic ledger: %w", ledgerErr)
	}
	fast.SetTrafficLedger(ledger)
	full, ip := collector.NewTelemetryFull(), collector.NewTelemetryPublicIP(cfg.DNSFallbackServers...)
	probes := collector.NewTCPProbes(cfg.TCPProbe, cfg.DNSFallbackServers)
	return telemetrySources{
		configureProbe: func(next *v1.TelemetryTCPProbeSettings) {
			if next == nil {
				probes.Configure(cfg.TCPProbe)
				return
			}
			local := config.TCPProbe{IntervalSeconds: 60, TimeoutMS: 1000, WindowSeconds: 600}
			if cfg.TCPProbe != nil {
				local = *cfg.TCPProbe
			}
			local.Targets = next.Targets
			probes.Configure(&local)
		},
		trafficError: ledgerErr,
		configure:    fast.ApplyTraffic,
		validate:     fast.ValidateTraffic,
		calibrate:    ledger.CalibrateWithReceipt,
		fast:         func(ctx context.Context, at time.Time) (any, error) { v, _, err := fast.Sample(ctx, at); return v, err },
		full: func(ctx context.Context, at time.Time) (any, error) {
			value, err := full.Sample(ctx, at)
			if err == nil && len(probeEnabled) > 0 && probeEnabled[0]() {
				value.TcpProbes, err = probes.Sample(ctx, at)
			}
			return value, err
		},
		ip:   func(ctx context.Context, at time.Time) (any, error) { return ip.Sample(ctx, at) },
		save: ledger.Save,
	}, nil
}

// Four fixed workers, one outstanding operation each. The scheduler owns busy
// and must consume complete before submitting the next operation of that kind.
// Slow full/IP/disk persistence cannot queue or block fast sampling.
type telemetryWorkers struct {
	requests [4]chan time.Time
	results  chan telemetryWorkResult
	busy     [4]bool
	cancel   context.CancelFunc
	done     chan struct{}
	finalErr error
	stopping <-chan struct{}
}

func startTelemetryWorkers(parent context.Context, s telemetrySources) *telemetryWorkers {
	ctx, cancel := context.WithCancel(parent)
	w := &telemetryWorkers{results: make(chan telemetryWorkResult, 4), cancel: cancel, done: make(chan struct{}), stopping: ctx.Done()}
	var collectors sync.WaitGroup
	collectors.Add(3)
	functions := []func(context.Context, time.Time) (any, error){s.fast, s.full, s.ip}
	for kind := 0; kind < 4; kind++ {
		w.requests[kind] = make(chan time.Time, 1)
	}
	for kind, fn := range functions {
		go func() {
			defer collectors.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case at := <-w.requests[kind]:
					if ctx.Err() != nil {
						return
					}
					timeout := 5 * time.Second
					if kind == 2 {
						timeout = 24 * time.Second
					}
					probe, stop := context.WithTimeout(ctx, timeout)
					value, err := fn(probe, at)
					stop()
					select {
					case w.results <- telemetryWorkResult{kind, value, err}:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}
	go func() {
		defer close(w.done)
		for {
			select {
			case <-ctx.Done():
				// No final write races a last fast observation. Save has its own bounded
				// context because the sampling context is already canceled.
				collectors.Wait()
				final, stop := context.WithTimeout(context.Background(), 5*time.Second)
				err := s.save(final, time.Now(), true)
				stop()
				w.finalErr = err
				return
			case at := <-w.requests[3]:
				if ctx.Err() != nil {
					continue
				}
				save, stop := context.WithTimeout(ctx, 5*time.Second)
				err := s.save(save, at, false)
				stop()
				select {
				case w.results <- telemetryWorkResult{kind: 3, err: err}:
				case <-ctx.Done():
				}
			}
		}
	}()
	return w
}
func (w *telemetryWorkers) submit(kind int, at time.Time) bool {
	if kind < 0 || kind >= len(w.requests) || w.busy[kind] || at.IsZero() {
		return false
	}
	select {
	case <-w.stopping:
		return false
	default:
	}
	select {
	case w.requests[kind] <- at:
		w.busy[kind] = true
		return true
	default:
		return false
	}
}
func (w *telemetryWorkers) complete(result telemetryWorkResult) {
	if result.kind >= 0 && result.kind < len(w.busy) {
		w.busy[result.kind] = false
	}
}

// stop is bounded even on a kernel filesystem call that ignores cancellation.
// A timeout is an explicit shutdown failure, not a successful final save.
func (w *telemetryWorkers) stop(ctx context.Context) error {
	w.cancel()
	select {
	case <-w.done:
		return w.finalErr
	case <-ctx.Done():
		return fmt.Errorf("telemetry worker shutdown: %w", ctx.Err())
	}
}
