package connection

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

type telemetryEvent struct {
	kind             string
	correlation      string
	demand           v1.TelemetryDemand
	settings         v1.TelemetrySettings
	credential       sessionCredential
	serverGeneration string
}
type telemetryControl struct {
	generation  string
	correlation string
	demand      *v1.TelemetryDemandResult
	settings    *v1.TelemetrySettingsResult
}
type telemetryTransmission struct {
	send       *telemetrySend
	credential sessionCredential
	report     uint64
}
type telemetryTransmissionResult struct {
	send *telemetrySend
	err  error
}

// transmit executes one logical window, using the existing management writer
// for WSS. It must not retry or retain a flight after returning. The runner
// supplies a 15-second deadline and owns the sole sending worker.
type telemetryTransmit func(context.Context, telemetryTransmission) error

type telemetryDiagnostics struct {
	Completed [4]uint64
	Errors    [4]error
}
type telemetryRunner struct {
	diagnostics chan telemetryDiagnostics
	events      chan telemetryEvent
	controls    chan telemetryControl
	done        chan struct{}
	cancel      context.CancelFunc
	err         error // published by closing done
}

func startTelemetryRunner(parent context.Context, s *telemetrySchedule, credential sessionCredential, settings *telemetrySettings, transmit telemetryTransmit, now func() time.Time) *telemetryRunner {
	ctx, cancel := context.WithCancel(parent)
	r := &telemetryRunner{diagnostics: make(chan telemetryDiagnostics, 1), events: make(chan telemetryEvent, 8), controls: make(chan telemetryControl, 8), done: make(chan struct{}), cancel: cancel}
	go r.run(ctx, s, credential, settings, transmit, now)
	return r
}
func (r *telemetryRunner) run(ctx context.Context, s *telemetrySchedule, credential sessionCredential, settings *telemetrySettings, transmit telemetryTransmit, now func() time.Time) {
	logger := settings.logger
	if logger == nil {
		logger = slog.Default()
	}
	defer close(r.done)
	senderCtx, cancelSender := context.WithCancel(ctx)
	requests := make(chan telemetryTransmission, 1)
	results := make(chan telemetryTransmissionResult, 1)
	senderDone := make(chan struct{})
	go func() {
		defer close(senderDone)
		for {
			select {
			case <-senderCtx.Done():
				return
			case request := <-requests:
				if senderCtx.Err() != nil {
					return
				}
				attempt, stop := context.WithTimeout(senderCtx, 15*time.Second)
				err := transmit(attempt, request)
				stop()
				select {
				case results <- telemetryTransmissionResult{request.send, err}:
				case <-senderCtx.Done():
					return
				}
			}
		}
	}()
	defer func() {
		cancelSender()
		s.workers.cancel()
		shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		select {
		case <-senderDone:
		case <-shutdown.Done():
			r.err = errors.Join(r.err, fmt.Errorf("telemetry sender shutdown: %w", shutdown.Err()))
		}
		workerErr := s.workers.stop(shutdown)
		r.err = errors.Join(r.err, workerErr)
		level := slog.LevelInfo
		if r.err != nil {
			level = slog.LevelError
		}
		logger.Log(context.Background(), level, "Telemetry shutdown", "worker_shutdown_and_final_save_ok", workerErr == nil, "shutdown_ok", r.err == nil)
	}()
	control := func(c telemetryControl) bool {
		if c.generation == "" {
			c.generation = s.lease.generation
		}
		select {
		case r.controls <- c:
			return true
		default:
			r.err = fmt.Errorf("telemetry control consumer stalled")
			return false
		}
	}
	var operational telemetryLogState
	operational.failed[3] = s.trafficError != nil
	var diagnostics telemetryDiagnostics
	diagnostics.Errors[3] = s.trafficError
	publish := func() {
		select {
		case r.diagnostics <- diagnostics:
		default:
			select {
			case <-r.diagnostics:
			default:
			}
			select {
			case r.diagnostics <- diagnostics:
			default:
			}
		}
	}
	publish()
	correlation := ""
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		at := now()
		if result := s.advance(at); result != nil {
			if !control(telemetryControl{correlation: correlation, demand: result}) {
				return
			}
		}
		if send := s.take(at); send != nil {
			operational.send = at
			// Only take can create active ownership, so this fixed queue is never full.
			select {
			case requests <- telemetryTransmission{send, credential, s.cadence.settings.HttpReportSeconds}:
			case <-ctx.Done():
				return
			}
		}
		operational.record(ctx, logger, s, at)
		next := s.nextWake(at)
		delay := next.Sub(at)
		if delay < 0 {
			delay = 0
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(delay)
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case result := <-s.workers.results:
			// Group errors are already represented in observations where possible.
			// A bad local sample is dropped; it never tears down management or tasks.
			diagnostics.Errors[result.kind] = s.collected(result)
			diagnostics.Completed[result.kind]++
			operational.collection(result, diagnostics.Errors[result.kind], now())
			publish()
		case result := <-results:
			code := ""
			var retry time.Duration
			if result.err != nil {
				var upload *telemetryUploadError
				if errors.As(result.err, &upload) {
					code = upload.Code
					retry = upload.RetryAfter
				} else {
					code = "network_error"
				}
			}
			operational.sendFailed = code != ""
			if code == "" {
				operational.accepted = now()
			} else {
				operational.droppedFailure += uint64(len(result.send.flight.samples))
			}
			if receipt := s.finish(result.send, now(), code, retry); receipt != nil {
				if !control(telemetryControl{correlation: correlation, demand: receipt}) {
					return
				}
			}
		case event := <-r.events:
			at = now()
			switch event.kind {
			case "demand":
				receipt := s.demand(event.demand, at)
				if receipt == nil {
					correlation = event.correlation
				} else if !control(telemetryControl{correlation: event.correlation, demand: receipt}) {
					return
				}
			case "settings":
				result := settings.apply(event.settings)
				if result.State == "applied" {
					if err := s.settings(event.settings, at); err != nil {
						r.err = err
						return
					}
				}
				if !control(telemetryControl{correlation: event.correlation, settings: &result}) {
					return
				}
			case "disconnected":
				s.disconnect(at)
			case "ready":
				result := settings.apply(event.settings)
				if result.State == "applied" {
					if err := s.settings(event.settings, at); err != nil {
						r.err = err
						return
					}
					if err := s.reconnected(event.credential.Generation, event.serverGeneration, at); err != nil {
						r.err = err
						return
					}
					credential = event.credential
				}
				if !control(telemetryControl{generation: event.serverGeneration, correlation: event.correlation, settings: &result}) {
					return
				}
			case "wake": // injectable-clock tests; no state mutation
			default:
				r.err = fmt.Errorf("unknown local telemetry event")
				return
			}
		}
	}
}
func (r *telemetryRunner) stop(ctx context.Context) error {
	r.cancel()
	select {
	case <-r.done:
		return r.err
	case <-ctx.Done():
		return fmt.Errorf("telemetry runner shutdown: %w", ctx.Err())
	}
}
