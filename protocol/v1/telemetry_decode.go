package v1

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

// DecodeTelemetry applies transport-specific byte bounds without relaxing the
// legacy WSS Decode limit. It rejects duplicate object members before decoding
// typed fields, so authorization/identity fields have one unambiguous value.
// Semantic validation of the selected payload remains mandatory afterwards.
func DecodeTelemetry(data []byte, v any, http bool) error {
	limit := TelemetryMaxFrameBytes
	if http {
		limit = TelemetryMaxBodyBytes
	}
	if len(data) == 0 || len(data) > limit || !utf8.Valid(data) {
		return errors.New("invalid telemetry message size")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := telemetryJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("trailing telemetry JSON")
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(v); err != nil {
		return err
	}
	return nil
}
func telemetryJSONValue(d *json.Decoder, depth int) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	delimiter, container := token.(json.Delim)
	if !container {
		return nil
	}
	if depth >= 16 {
		return errors.New("telemetry JSON too deep")
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return errors.New("invalid telemetry object")
			}
			if _, ok := seen[name]; ok {
				return errors.New("duplicate telemetry field")
			}
			seen[name] = struct{}{}
			if err = telemetryJSONValue(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err = telemetryJSONValue(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid telemetry delimiter")
	}
	closing, err := d.Token()
	if err != nil {
		return err
	}
	if (delimiter == '{' && closing != json.Delim('}')) || (delimiter == '[' && closing != json.Delim(']')) {
		return errors.New("invalid telemetry closing delimiter")
	}
	return nil
}
