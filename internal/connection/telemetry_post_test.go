package connection

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vioira/viorka-agent/internal/config"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

func uploadFixture(t *testing.T) v1.TelemetryBatch {
	t.Helper()
	raw, err := os.ReadFile("../../protocol/v1/testdata/node.metrics.v3.json")
	if err != nil {
		t.Fatal(err)
	}
	var e struct {
		Payload v1.TelemetryBatch `json:"payload"`
	}
	if err = json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	e.Payload.Fast[0].SampledAt = now
	e.Payload.Full[0].SampledAt = now
	return e.Payload
}
func TestTelemetryPOSTResponsesAndIdentityPolicy(t *testing.T) {
	for _, tc := range []struct {
		name              string
		status            int
		body, code, retry string
		blocked           bool
	}{
		{"accepted", 200, `{"batch_id":"batch-1","state":"accepted","accepted_count":2,"duplicate_count":0}`, "", "", false},
		{"duplicate", 200, `{"batch_id":"batch-1","state":"accepted","accepted_count":0,"duplicate_count":2}`, "", "", false},
		{"wrong batch", 200, `{"batch_id":"other","state":"accepted","accepted_count":2,"duplicate_count":0}`, "invalid_response", "", false},
		{"wrong count", 200, `{"batch_id":"batch-1","state":"accepted","accepted_count":1,"duplicate_count":0}`, "invalid_response", "", false},
		{"duplicate key", 200, `{"batch_id":"batch-1","batch_id":"batch-1","state":"accepted","accepted_count":2,"duplicate_count":0}`, "invalid_response", "", false},
		{"expiry", 401, `{"code":"session_expired","message":"expired"}`, "session_expired", "", false},
		{"revoked", 401, `{"code":"identity_revoked","message":"revoked"}`, "identity_revoked", "", true},
		{"invalid", 401, `{"code":"session_invalid","message":"invalid"}`, "session_invalid", "", true},
		{"ambiguous auth", 401, `not json`, "session_invalid", "", true},
		{"forbidden expiry", 403, `{"code":"session_expired","message":"expired"}`, "session_invalid", "", true},
		{"rate", 429, `{"code":"rate_limited","message":"slow"}`, "rate_limited", "17", false},
		{"overload", 503, `{"batch_id":"batch-1","state":"rejected","accepted_count":0,"duplicate_count":0,"error_code":"overloaded"}`, "overloaded", "1", false},
		{"proxy failure", 502, `bad gateway`, "overloaded", "", false},
		{"missing", 404, `not found`, "unsupported_protocol", "", false},
		{"malformed retry", 429, `{}`, "rate_limited", "many", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/api/v1/agents/metrics" || r.Method != "POST" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 64) || r.Header.Get("Cookie") != "" {
					t.Error("invalid upload request")
				}
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if _, err = v1.DecodeTelemetryBatch(raw, "node-demo", time.Now(), 60, true); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				if tc.retry != "" {
					w.Header().Set("Retry-After", tc.retry)
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			c := &Client{cfg: config.Local{PanelURL: server.URL}, http: server.Client()}
			defer c.Close()
			issue := func(context.Context) (sessionCredential, error) {
				return sessionCredential{Token: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(15 * time.Minute)}, nil
			}
			credential, err := c.sessions.Get(context.Background(), "node", issue)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := c.uploadTelemetry(context.Background(), credential, uploadFixture(t), 60)
			if tc.code == "" {
				if err != nil || receipt.Validate(2) != nil {
					t.Fatal(receipt, err)
				}
			} else {
				var failure *telemetryUploadError
				if !errors.As(err, &failure) || failure.Code != tc.code {
					t.Fatal(err)
				}
				if tc.retry == "17" && failure.RetryAfter != 17*time.Second {
					t.Fatal(failure.RetryAfter)
				}
			}
			if calls != 1 {
				t.Fatal("upload retried", calls)
			}
			_, err = c.sessions.Get(context.Background(), "node", issue)
			if errors.Is(err, ErrIdentityRejected) != tc.blocked {
				t.Fatal("identity policy", err)
			}
		})
	}
}
func TestRetryAfterDateAndOverflow(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	delay, err := retryAfter(now.Add(time.Minute).Format(http.TimeFormat), now)
	if err != nil || delay != time.Minute {
		t.Fatal(delay, err)
	}
	if _, err = retryAfter("18446744073709551615", now); err == nil {
		t.Fatal("overflow")
	}
	if d, err := retryAfter(now.Add(-time.Minute).Format(http.TimeFormat), now); err != nil || d != 0 {
		t.Fatal(d, err)
	}
}

func TestTelemetryPOSTLateSuccessCannotAcknowledgeNewSession(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"batch_id":"batch-1","state":"accepted","accepted_count":2,"duplicate_count":0}`))
	}))
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		server.Close()
	}()
	c := &Client{cfg: config.Local{PanelURL: server.URL}, http: server.Client()}
	defer c.Close()
	issue := func(context.Context) (sessionCredential, error) {
		return sessionCredential{Token: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(15 * time.Minute)}, nil
	}
	first, err := c.sessions.Get(context.Background(), "node", issue)
	if err != nil {
		t.Fatal(err)
	}
	batch := uploadFixture(t)
	done := make(chan error, 1)
	go func() { _, err := c.uploadTelemetry(context.Background(), first, batch, 60); done <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("upload not started")
	}
	if !c.sessions.Reject(first.Generation, true) {
		t.Fatal("expiry not applied")
	}
	next, err := c.sessions.Get(context.Background(), "node", issue)
	if err != nil || next.Generation == first.Generation {
		t.Fatal(err)
	}
	close(release)
	select {
	case err = <-done:
		var failure *telemetryUploadError
		if !errors.As(err, &failure) || failure.Code != "stale_session" {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("late response not completed")
	}
}
