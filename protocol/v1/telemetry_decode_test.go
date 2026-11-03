package v1

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestTelemetryStrictTransportDecode(t *testing.T) {
	for _, raw := range []string{
		`{"node_id":"n","node_id":"other"}`,
		`{"node_id":"n","node_\u0069d":"other"}`,
		`{"fast":[{"sequence":1,"sequence":2}]}`,
		`{"fast":[]} {"fast":[]}`,
		`{"fast":[],"command":"anything"}`,
		`{"fast":[{"sequence":1.1}]}`,
		`{"fast":[{"sequence":18446744073709551616}]}`,
	} {
		var b TelemetryBatch
		if DecodeTelemetry([]byte(raw), &b, true) == nil {
			t.Fatal("accepted ambiguous/invalid JSON", raw)
		}
	}
	// Size enforcement includes insignificant whitespace, with exact boundaries.
	raw := append([]byte(`{"fast":[],"full":[],"public_ip":[]}`), bytes.Repeat([]byte(" "), TelemetryMaxFrameBytes)...)
	var b TelemetryBatch
	if DecodeTelemetry(raw, &b, false) == nil {
		t.Fatal("WSS accepted HTTP-sized payload")
	}
	if err := DecodeTelemetry(raw, &b, true); err != nil {
		t.Fatal(err)
	}
	atLimit := append([]byte(`{}`), bytes.Repeat([]byte(" "), TelemetryMaxBodyBytes-2)...)
	if err := DecodeTelemetry(atLimit, &b, true); err != nil {
		t.Fatal("exact body bound", err)
	}
	if DecodeTelemetry(append(atLimit, ' '), &b, true) == nil {
		t.Fatal("body limit ignored")
	}
	nested := append(bytes.Repeat([]byte("["), 17), bytes.Repeat([]byte("]"), 17)...)
	var value any
	if DecodeTelemetry(nested, &value, true) == nil {
		t.Fatal("depth limit ignored")
	}
	raw = []byte(`{"fast":[{"sequence":9007199254740993}]}`)
	if err := DecodeTelemetry(raw, &b, true); err != nil || b.Fast[0].Sequence != 9007199254740993 {
		t.Fatal("integer precision lost", err)
	}
	encoded, err := json.Marshal(b)
	if err != nil || !bytes.Contains(encoded, []byte("9007199254740993")) {
		t.Fatal(err, string(encoded))
	}
}
