package connection

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

// WSS delivery uses the sole management writer and waits for the matching
// metrics receipt; write success alone is not acceptance. The owner fences
// generation/batch correlation before completing this callback.
type telemetryFrameDelivery func(context.Context, []byte, string, uint64) error

func (c *Client) telemetryTransport(node, run string, delivery func() telemetryFrameDelivery) telemetryTransmit {
	return func(ctx context.Context, request telemetryTransmission) error {
		flight := request.send.flight
		// Buffered observations can survive a reconnect to an older Panel. Strip
		// only the optional extension from a private copy, preserving sample identity.
		slow, tcp := c.slowUptimeSupported.Load(), c.tcpProbeSupported.Load()
		if !tcp || !slow {
			flight = &telemetryFlight{samples: slices.Clone(flight.samples)}
			for i := range flight.samples {
				if err := flight.samples[i].adaptCapabilities(slow, tcp); err != nil {
					return err
				}
			}
		}
		if c.dailyTrafficSupported.Load() && c.dailyPending != nil {
			daily, err := c.dailyPending(ctx, request.send.attempt.HTTP)
			if err == nil {
				c.dailyPersistFailed = false
				flight = &telemetryFlight{samples: flight.samples, daily: daily}
			} else if !c.dailyPersistFailed {
				c.dailyPersistFailed = true
				c.logger.Warn("日流量持久化失败，保留待报记录", "error", err)
			}
		}
		if request.send.attempt.HTTP {
			body, err := encodeTelemetryWindow(flight.samples, node, run, ID(), flight.daily...)
			if err != nil {
				return &telemetryUploadError{Code: "invalid_batch", Cause: err}
			}
			_, err = c.uploadTelemetryBody(ctx, request.credential, body, node, request.report)
			if err == nil {
				request.send.flight.acknowledged = len(flight.samples)
			}
			return err
		}
		var deliver telemetryFrameDelivery
		if delivery != nil {
			deliver = delivery()
		}
		if deliver == nil {
			return &telemetryUploadError{Code: "network_error"}
		}
		for offset := 0; offset < len(flight.samples); {
			select {
			case <-request.credential.Changed:
				return &telemetryUploadError{Code: "stale_session"}
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			id := ID()
			frame, next, err := flight.frame(offset, node, run, id, time.Now())
			if err != nil {
				return &telemetryUploadError{Code: "invalid_batch", Cause: err}
			}
			if err = deliver(ctx, frame, id, uint64(next-offset)); err != nil {
				var failure *telemetryUploadError
				if errors.As(err, &failure) {
					switch failure.Code {
					case "session_expired", "session_invalid", "identity_revoked", "node_disabled":
						if !c.sessions.Reject(request.credential.Generation, failure.Code == "session_expired") {
							return &telemetryUploadError{Code: "stale_session"}
						}
					}
				}
				return err
			}
			request.send.flight.acknowledged = next
			offset = next
		}
		return nil
	}
}

// Adapt buffered observations on downgrade without changing the original cache.
func (sample *telemetrySample) adaptCapabilities(slowUptime, tcpProbe bool) error {
	var value any
	switch {
	case sample.kind == 0 && sample.slowUptime && !slowUptime:
		var f v1.TelemetryFast
		if err := json.Unmarshal(sample.raw, &f); err != nil {
			return err
		}
		f.Errors = append(f.Errors, "uptime_unavailable")
		value = &f
	case sample.kind == 2 && sample.slowUptime && !slowUptime:
		var ip v1.TelemetryPublicIP
		if err := json.Unmarshal(sample.raw, &ip); err != nil {
			return err
		}
		ip.Uptime = nil
		value = &ip
	case sample.kind == 1 && !tcpProbe:
		var f v1.TelemetryFull
		if err := json.Unmarshal(sample.raw, &f); err != nil {
			return err
		}
		f.TcpProbes = nil
		value = &f
	default:
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	sample.raw = raw
	return nil
}
