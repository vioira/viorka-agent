package connection

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

type telemetryUploadError struct {
	Code       string
	Status     int
	RetryAfter time.Duration
	Cause      error
}

func (e *telemetryUploadError) Error() string {
	return fmt.Sprintf("telemetry upload %s (HTTP %d)", e.Code, e.Status)
}
func (e *telemetryUploadError) Unwrap() error { return e.Cause }
func retryAfter(value string, now time.Time) (time.Duration, error) {
	if value == "" {
		return 0, nil
	}
	if seconds, err := strconv.ParseUint(value, 10, 64); err == nil {
		if seconds > uint64(math.MaxInt64/int64(time.Second)) {
			return 0, fmt.Errorf("retry duration overflow")
		}
		return time.Duration(seconds) * time.Second, nil
	}
	date, err := http.ParseTime(value)
	if err != nil {
		return 0, fmt.Errorf("invalid Retry-After")
	}
	if date.Before(now) {
		return 0, nil
	}
	return date.Sub(now), nil
}

// uploadTelemetry makes exactly one bounded request with the credential that
// completed ready negotiation. It neither issues a challenge nor replays a batch.
// The scheduler owns retry windows and decides when to renegotiate after expiry.
func (c *Client) uploadTelemetry(ctx context.Context, credential sessionCredential, batch v1.TelemetryBatch, reportSeconds uint64) (v1.TelemetryReceipt, error) {
	if len(batch.Fast) > 300 || len(batch.Full) > 8 || len(batch.PublicIp) > 2 {
		return v1.TelemetryReceipt{}, &telemetryUploadError{Code: "invalid_batch"}
	}
	raw, err := json.Marshal(batch)
	if err != nil {
		return v1.TelemetryReceipt{}, &telemetryUploadError{Code: "invalid_batch", Cause: err}
	}
	return c.uploadTelemetryBody(ctx, credential, raw, batch.NodeID, reportSeconds)
}

// uploadTelemetryBody accepts the scheduler's immutable encoded window, avoiding
// a decode/re-encode cycle and a second full-sized request buffer.
func (c *Client) uploadTelemetryBody(ctx context.Context, credential sessionCredential, raw []byte, node string, reportSeconds uint64) (v1.TelemetryReceipt, error) {
	empty := v1.TelemetryReceipt{}
	failed := func(code string, status int, retry time.Duration, cause error) (v1.TelemetryReceipt, error) {
		return empty, &telemetryUploadError{Code: code, Status: status, RetryAfter: retry, Cause: cause}
	}
	if len(credential.Token) != 64 || credential.Generation == 0 {
		return failed("session_invalid", 0, 0, nil)
	}
	select {
	case <-credential.Changed:
		return failed("stale_session", 0, 0, nil)
	default:
	}
	if !credential.ExpiresAt.After(time.Now()) {
		if !c.sessions.Reject(credential.Generation, true) {
			return failed("stale_session", 0, 0, nil)
		}
		return failed("session_expired", 0, 0, nil)
	}
	batch, err := v1.DecodeTelemetryBatch(raw, node, time.Now(), reportSeconds, true)
	if err != nil {
		return failed("invalid_batch", 0, 0, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.PanelURL+"/api/v1/agents/metrics", bytes.NewReader(raw))
	if err != nil {
		return failed("invalid_batch", 0, 0, err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+credential.Token)
	response, err := c.http.Do(request)
	if err != nil {
		var certificate *tls.CertificateVerificationError
		if errors.As(err, &certificate) {
			if !c.sessions.block(credential.Generation, err) {
				return failed("stale_session", 0, 0, err)
			}
			return failed("tls_error", 0, 0, err)
		}
		return failed("network_error", 0, 0, err)
	}
	defer response.Body.Close()
	select {
	case <-credential.Changed:
		return failed("stale_session", response.StatusCode, 0, nil)
	default:
	}
	delay, delayErr := retryAfter(response.Header.Get("Retry-After"), time.Now())
	if delayErr != nil {
		if response.StatusCode == 429 {
			return failed("rate_limited", 429, 0, delayErr)
		}
		return failed("invalid_response", response.StatusCode, 0, delayErr)
	}
	// Missing endpoints are a capability fallback, never a reason to refresh identity.
	if response.StatusCode == 404 || response.StatusCode == 405 {
		return failed("unsupported_protocol", response.StatusCode, delay, nil)
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, 16385))
	if err != nil {
		return failed("network_error", response.StatusCode, delay, err)
	}
	if len(raw) > 16384 {
		return failed("invalid_response", response.StatusCode, delay, nil)
	}
	select {
	case <-credential.Changed:
		return failed("stale_session", response.StatusCode, 0, nil)
	default:
	}
	media, parameters, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	validJSON := mediaErr == nil && media == "application/json" && (parameters["charset"] == "" || strings.EqualFold(parameters["charset"], "utf-8"))
	total := uint64(len(batch.Fast) + len(batch.Full) + len(batch.PublicIp))
	if response.StatusCode == 200 {
		var receipt v1.TelemetryReceipt
		if !validJSON || v1.ValidateTelemetryJSON(raw, "TelemetryReceipt", true) != nil || v1.DecodeTelemetry(raw, &receipt, true) != nil || receipt.Validate(total) != nil || receipt.BatchID != batch.BatchID || receipt.State != "accepted" {
			return failed("invalid_response", 200, delay, nil)
		}
		return receipt, nil
	}
	code := ""
	if validJSON {
		var apiError struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if v1.DecodeTelemetry(raw, &apiError, true) == nil {
			code = apiError.Code
		} else {
			var receipt v1.TelemetryReceipt
			if v1.ValidateTelemetryJSON(raw, "TelemetryReceipt", true) == nil && v1.DecodeTelemetry(raw, &receipt, true) == nil && receipt.Validate(total) == nil && receipt.BatchID == batch.BatchID && receipt.State == "rejected" {
				code = receipt.ErrorCode
			}
		}
	}
	switch response.StatusCode {
	case 401, 403:
		switch code {
		case "session_expired":
			if response.StatusCode != 401 {
				code = "session_invalid"
			}
		case "identity_revoked", "node_disabled", "session_invalid":
		default:
			code = "session_invalid"
		}
		if !c.sessions.Reject(credential.Generation, code == "session_expired") {
			return failed("stale_session", response.StatusCode, delay, nil)
		}
	case 429:
		code = "rate_limited"
	case 400:
		if code != "invalid_batch" {
			code = "invalid_response"
		}
	case 409:
		if code != "stale_session" && code != "unsupported_protocol" {
			code = "invalid_response"
		}
	default:
		if response.StatusCode >= 500 && response.StatusCode <= 599 {
			code = "overloaded"
		} else {
			code = "invalid_response"
		}
	}
	return failed(code, response.StatusCode, delay, nil)
}
