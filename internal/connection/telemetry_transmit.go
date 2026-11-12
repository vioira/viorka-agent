package connection

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"
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
		if !c.tcpProbeSupported.Load() {
			flight = &telemetryFlight{samples: slices.Clone(flight.samples)}
			for i := range flight.samples {
				sample := &flight.samples[i]
				if sample.kind != 1 {
					continue
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(sample.raw, &fields); err != nil {
					return err
				}
				delete(fields, "tcp_probes")
				raw, err := json.Marshal(fields)
				if err != nil {
					return err
				}
				sample.raw = raw
			}
		}
		if request.send.attempt.HTTP {
			body, err := encodeTelemetryWindow(flight.samples, node, run, ID())
			if err != nil {
				return &telemetryUploadError{Code: "invalid_batch", Cause: err}
			}
			_, err = c.uploadTelemetryBody(ctx, request.credential, body, node, request.report)
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
			offset = next
		}
		return nil
	}
}
