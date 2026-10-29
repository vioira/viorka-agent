package v1

import (
	"encoding/json"
	"testing"
	"time"
)

func TestEnvelopeRoundtrip(t *testing.T) {
	msg := Envelope{Version: Version, MessageID: "id-01", Type: "node.heartbeat", SentAt: time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC), Payload: json.RawMessage(`{"healthy":true}`)}
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var got Envelope
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "node.heartbeat" || got.Version != Version {
		t.Fatalf("unexpected decoded envelope: %+v", got)
	}
}
