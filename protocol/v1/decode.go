package v1

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"
)

// Decode rejects unknown fields, excessive nesting, trailing JSON and oversized frames.
func Decode(data []byte, v any) error {
	if len(data) > 65536 {
		return errors.New("message too large")
	}
	depth := 0
	quoted, escaped := false, false
	for _, b := range data {
		if quoted {
			if escaped {
				escaped = false
			} else if b == '\\' {
				escaped = true
			} else if b == '"' {
				quoted = false
			}
			continue
		}
		if b == '"' {
			quoted = true
		} else if b == '{' || b == '[' {
			depth++
			if depth > 16 {
				return errors.New("message too deep")
			}
		} else if b == '}' || b == ']' {
			depth--
		}
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var rest any
	if err := d.Decode(&rest); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
func Parse(data []byte, node string) (Envelope, error) {
	var e Envelope
	if err := Decode(data, &e); err != nil {
		return e, err
	}
	if e.Version != Version || e.NodeID != node || len(e.MessageID) == 0 || len(e.MessageID) > 128 || len(e.CorrelationID) > 128 || e.SentAt.IsZero() || len(e.Payload) == 0 || e.Payload[0] != '{' {
		return e, errors.New("invalid envelope")
	}
	return e, nil
}
func Message(kind, node, id string, payload any) Envelope {
	b, _ := json.Marshal(payload)
	return Envelope{Version: Version, MessageID: id, Type: kind, NodeID: node, SentAt: time.Now().UTC(), Payload: b}
}
