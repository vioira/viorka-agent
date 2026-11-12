package connection

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/vioira/viorka-agent/internal/state"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

var errTelemetryStopped = errors.New("telemetry scheduler stopped")

type telemetryClientSession struct {
	client              *Client
	identity            state.Identity
	credential          sessionCredential
	port                *telemetryWSSPort
	generation, readyID string
	applied             bool
}

func (t *telemetryClientSession) enqueue(event telemetryEvent) error {
	runner := t.client.telemetry
	if runner == nil {
		return fmt.Errorf("telemetry not negotiated")
	}
	select {
	case <-runner.done:
		return fmt.Errorf("%w: %v", errTelemetryStopped, runner.err)
	default:
	}
	select {
	case runner.events <- event:
		return nil
	default:
		return fmt.Errorf("telemetry control queue full")
	}
}
func (t *telemetryClientSession) close() {
	if t.port != nil {
		t.port.close()
	}
	if t.client.telemetry != nil {
		if err := t.enqueue(telemetryEvent{kind: "disconnected"}); err != nil {
			t.client.telemetry.cancel()
		}
	}
}
func (t *telemetryClientSession) control(control telemetryControl, send func(v1.Envelope) error) error {
	if control.generation != t.generation {
		return nil
	}
	var message v1.Envelope
	if control.settings != nil {
		message = v1.Message("node.settings.v3_result", t.identity.NodeID, ID(), control.settings)
	} else if control.demand != nil {
		message = v1.Message("node.metrics.demand_result", t.identity.NodeID, ID(), control.demand)
	} else {
		return fmt.Errorf("empty telemetry control result")
	}
	message.CorrelationID = control.correlation
	if err := send(message); err != nil {
		return err
	}
	if control.settings != nil && control.correlation == t.readyID {
		t.applied = control.settings.State == "applied"
	}
	return nil
}
func (t *telemetryClientSession) handle(e v1.Envelope, send func(v1.Envelope) error) (bool, error) {
	switch e.Type {
	case "node.telemetry.ready", "node.settings.v3", "node.metrics.demand", "node.metrics.receipt":
	default:
		return false, nil
	}
	if !t.client.telemetryEnabled || e.Revision != nil {
		return true, fmt.Errorf("unnegotiated telemetry message")
	}
	if e.Type == "node.metrics.receipt" {
		if !t.applied || t.port == nil {
			return true, fmt.Errorf("telemetry receipt before ready")
		}
		t.port.receipt(e)
		return true, nil
	}
	if e.CorrelationID != "" {
		return true, fmt.Errorf("unexpected telemetry correlation")
	}
	switch e.Type {
	case "node.telemetry.ready":
		if t.generation != "" {
			return true, fmt.Errorf("duplicate telemetry ready")
		}
		var ready v1.TelemetryReady
		if v1.ValidateTelemetryJSON(e.Payload, "TelemetryReady", false) != nil || v1.DecodeTelemetry(e.Payload, &ready, false) != nil || ready.Settings == nil || ready.Settings.Validate() != nil {
			return true, fmt.Errorf("invalid telemetry ready")
		}
		t.client.tcpProbeSupported.Store(ready.TcpProbe)
		t.generation = ready.SessionGeneration
		t.readyID = e.MessageID
		t.port = t.client.telemetryBridge.attach()
		if t.client.telemetry != nil {
			return true, t.enqueue(telemetryEvent{kind: "ready", correlation: e.MessageID, settings: *ready.Settings, credential: t.credential, serverGeneration: ready.SessionGeneration})
		}
		return true, t.start(ready, send)
	case "node.settings.v3":
		if t.generation == "" {
			return true, fmt.Errorf("settings before telemetry ready")
		}
		var settings v1.TelemetrySettings
		if v1.ValidateTelemetryJSON(e.Payload, "TelemetrySettings", false) != nil || v1.DecodeTelemetry(e.Payload, &settings, false) != nil {
			return true, fmt.Errorf("invalid telemetry settings")
		}
		if !t.applied {
			t.readyID = e.MessageID
			if t.client.telemetry == nil {
				return true, t.start(v1.TelemetryReady{SessionGeneration: t.generation, Settings: &settings}, send)
			}
			return true, t.enqueue(telemetryEvent{kind: "ready", correlation: e.MessageID, settings: settings, credential: t.credential, serverGeneration: t.generation})
		}
		return true, t.enqueue(telemetryEvent{kind: "settings", correlation: e.MessageID, settings: settings})
	case "node.metrics.demand":
		if !t.applied {
			return true, fmt.Errorf("demand before telemetry ready applied")
		}
		var demand v1.TelemetryDemand
		if v1.ValidateTelemetryJSON(e.Payload, "TelemetryDemand", false) != nil || v1.DecodeTelemetry(e.Payload, &demand, false) != nil {
			return true, fmt.Errorf("invalid telemetry demand")
		}
		return true, t.enqueue(telemetryEvent{kind: "demand", correlation: e.MessageID, demand: demand})
	}
	return true, nil
}
func (t *telemetryClientSession) start(ready v1.TelemetryReady, send func(v1.Envelope) error) error {
	c := t.client
	settings, err := loadTelemetrySettings(c.store, t.identity)
	if err != nil {
		return err
	}
	settings.setLogLevel = c.applyLogLevel
	settings.logger = c.logger
	var sources telemetrySources
	if c.telemetrySource != nil {
		sources, err = c.telemetrySource()
	} else {
		sources, err = newTelemetrySources(c.cfg, c.store, t.identity, c.tcpProbeSupported.Load)
	}
	if err != nil {
		return err
	}
	settings.validate = sources.validate
	settings.configure = sources.configure
	settings.configureProbe = sources.configureProbe
	result := settings.apply(*ready.Settings)
	if result.State != "applied" {
		return t.control(telemetryControl{generation: t.generation, correlation: t.readyID, settings: &result}, send)
	}
	c.trafficCalibrate = sources.calibrate
	var run [16]byte
	if _, err = rand.Read(run[:]); err != nil {
		return fmt.Errorf("telemetry run identity: %w", err)
	}
	run[6] = (run[6] & 15) | 64
	run[8] = (run[8] & 63) | 128
	runID := fmt.Sprintf("%x-%x-%x-%x-%x", run[:4], run[4:6], run[6:8], run[8:10], run[10:])
	schedule, err := newTelemetrySchedule(c.runCtx, *ready.Settings, t.credential.Generation, t.generation, time.Now(), sources)
	if err != nil {
		return err
	}
	if err = t.control(telemetryControl{generation: t.generation, correlation: t.readyID, settings: &result}, send); err != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return errors.Join(err, schedule.workers.stop(ctx))
	}
	c.telemetry = startTelemetryRunner(c.runCtx, schedule, t.credential, settings, c.telemetryTransport(t.identity.NodeID, runID, c.telemetryBridge.delivery), time.Now)
	return nil
}
