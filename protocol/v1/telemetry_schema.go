package v1

import (
	"encoding/json"
	"errors"
	"math"
	"net/netip"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Only the telemetry subset is generated into the binary. These rules come
// from Panel's authoritative Schema; legacy admission remains unchanged.
type telemetryRule struct {
	Type       string                    `json:"type"`
	Ref        string                    `json:"$ref"`
	Properties map[string]*telemetryRule `json:"properties"`
	Required   []string                  `json:"required"`
	Additional *bool                     `json:"additionalProperties"`
	Items      *telemetryRule            `json:"items"`
	MinItems   int                       `json:"minItems"`
	MaxItems   int                       `json:"maxItems"`
	Unique     bool                      `json:"uniqueItems"`
	MinLength  int                       `json:"minLength"`
	MaxLength  int                       `json:"maxLength"`
	Pattern    string                    `json:"pattern"`
	Format     string                    `json:"format"`
	Minimum    *json.Number              `json:"minimum"`
	Maximum    *json.Number              `json:"maximum"`
	Enum       []any                     `json:"enum"`
	Const      json.RawMessage           `json:"const"`
	All        []*telemetryRule          `json:"allOf"`
	Any        []*telemetryRule          `json:"anyOf"`
	If         *telemetryRule            `json:"if"`
	Then       *telemetryRule            `json:"then"`
	Else       *telemetryRule            `json:"else"`
	Not        *telemetryRule            `json:"not"`
	pattern    *regexp.Regexp
}

var telemetryRules = loadTelemetryRules()
var ErrTelemetryPayload = errors.New("invalid telemetry payload")

func loadTelemetryRules() map[string]*telemetryRule {
	rules := map[string]*telemetryRule{}
	if err := json.Unmarshal([]byte(telemetryDefinitionsJSON), &rules); err != nil {
		panic("invalid generated telemetry schema")
	}
	var compile func(*telemetryRule)
	compile = func(r *telemetryRule) {
		if r == nil {
			return
		}
		if r.Pattern != "" {
			r.pattern = regexp.MustCompile(r.Pattern)
		}
		for _, p := range r.Properties {
			compile(p)
		}
		compile(r.Items)
		compile(r.If)
		compile(r.Then)
		compile(r.Else)
		compile(r.Not)
		for _, p := range r.All {
			compile(p)
		}
		for _, p := range r.Any {
			compile(p)
		}
	}
	for _, r := range rules {
		compile(r)
	}
	return rules
}

// ValidateTelemetryJSON validates structure, bounds, required/non-null fields,
// enums and Schema conditionals. Authorization and cross-field admission remain
// separate. Byte limits apply before allocating the generic validation tree.
func ValidateTelemetryJSON(data []byte, definition string, http bool) error {
	rule := telemetryRules[definition]
	if rule == nil {
		return ErrTelemetryPayload
	}
	var value any
	if err := DecodeTelemetry(data, &value, http); err != nil {
		return err
	}
	if !rule.matches(value) {
		return ErrTelemetryPayload
	}
	return nil
}
func (r *telemetryRule) matches(v any) bool {
	if r == nil {
		return true
	}
	if r.Ref != "" {
		ref := telemetryRules[strings.TrimPrefix(r.Ref, "#/$defs/")]
		return ref != nil && ref.matches(v)
	}
	if r.Const != nil {
		var constant any
		d := json.NewDecoder(strings.NewReader(string(r.Const)))
		d.UseNumber()
		if d.Decode(&constant) != nil || !reflect.DeepEqual(constant, v) {
			return false
		}
	}
	if len(r.Enum) > 0 {
		found := false
		for _, e := range r.Enum {
			if reflect.DeepEqual(e, v) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	switch r.Type {
	case "object":
		if _, ok := v.(map[string]any); !ok {
			return false
		}
	case "array":
		if _, ok := v.([]any); !ok {
			return false
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return false
		}
	case "string":
		s, ok := v.(string)
		if !ok || utf8.RuneCountInString(s) < r.MinLength || (r.MaxLength > 0 && utf8.RuneCountInString(s) > r.MaxLength) {
			return false
		}
		if r.pattern != nil && !r.pattern.MatchString(s) {
			return false
		}
		switch r.Format {
		case "date-time":
			if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
				return false
			}
		case "uuid":
			if !telemetryRunID.MatchString(s) {
				return false
			}
		case "ipv4":
			a, e := netip.ParseAddr(s)
			if e != nil || !a.Is4() {
				return false
			}
		case "ipv6":
			a, e := netip.ParseAddr(s)
			if e != nil || !a.Is6() || a.Is4In6() || a.Zone() != "" {
				return false
			}
		}
	case "integer":
		n, ok := v.(json.Number)
		if !ok {
			return false
		}
		value, e := strconv.ParseUint(n.String(), 10, 64)
		if e != nil {
			return false
		}
		if r.Minimum != nil {
			lo, e := strconv.ParseUint(r.Minimum.String(), 10, 64)
			if e != nil || value < lo {
				return false
			}
		}
		if r.Maximum != nil {
			hi, e := strconv.ParseUint(r.Maximum.String(), 10, 64)
			if e != nil || value > hi {
				return false
			}
		}
	case "number":
		n, ok := v.(json.Number)
		if !ok {
			return false
		}
		value, e := n.Float64()
		if e != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
		if r.Minimum != nil {
			lo, e := r.Minimum.Float64()
			if e != nil || value < lo {
				return false
			}
		}
		if r.Maximum != nil {
			hi, e := r.Maximum.Float64()
			if e != nil || value > hi {
				return false
			}
		}
	}
	if object, ok := v.(map[string]any); ok {
		for _, key := range r.Required {
			if _, exists := object[key]; !exists {
				return false
			}
		}
		for key, value := range object {
			if prop, exists := r.Properties[key]; exists {
				if !prop.matches(value) {
					return false
				}
			} else if r.Additional != nil && !*r.Additional {
				return false
			}
		}
	}
	if array, ok := v.([]any); ok {
		if len(array) < r.MinItems || (r.MaxItems > 0 && len(array) > r.MaxItems) {
			return false
		}
		for i, item := range array {
			if !r.Items.matches(item) {
				return false
			}
			if r.Unique {
				for _, previous := range array[:i] {
					if reflect.DeepEqual(previous, item) {
						return false
					}
				}
			}
		}
	}
	for _, child := range r.All {
		if !child.matches(v) {
			return false
		}
	}
	if len(r.Any) > 0 {
		matched := false
		for _, child := range r.Any {
			if child.matches(v) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if r.Not != nil && r.Not.matches(v) {
		return false
	}
	if r.If != nil {
		if r.If.matches(v) {
			if !r.Then.matches(v) {
				return false
			}
		} else if !r.Else.matches(v) {
			return false
		}
	}
	return true
}

// DecodeTelemetryBatch performs both structural and contextual checks before
// callers mutate receiver state.
func DecodeTelemetryBatch(data []byte, node string, now time.Time, reportSeconds uint64, http bool) (TelemetryBatch, error) {
	var b TelemetryBatch
	if err := ValidateTelemetryJSON(data, "TelemetryBatch", http); err != nil {
		return b, err
	}
	if err := json.Unmarshal(data, &b); err != nil {
		return b, err
	}
	if err := b.ValidateIdentity(node, now, reportSeconds); err != nil {
		return b, err
	}
	if err := b.ValidateSemantics(); err != nil {
		return b, err
	}
	return b, nil
}
