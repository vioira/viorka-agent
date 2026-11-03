package v1

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func schemaFixture(t *testing.T) map[string]any {
	t.Helper()
	b, e := os.ReadFile("testdata/node.metrics.v3.json")
	if os.IsNotExist(e) {
		b, e = os.ReadFile("../../api/agent/v1/examples/node.metrics.v3.json")
	}
	if e != nil {
		t.Fatal(e)
	}
	var envelope struct {
		Payload json.RawMessage `json:"payload"`
	}
	if e = json.Unmarshal(b, &envelope); e != nil {
		t.Fatal(e)
	}
	var value map[string]any
	if e = DecodeTelemetry(envelope.Payload, &value, true); e != nil {
		t.Fatal(e)
	}
	return value
}
func TestTelemetryRuntimeSchemaBoundaries(t *testing.T) {
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	original := schemaFixture(t)
	raw, _ := json.Marshal(original)
	if _, e := DecodeTelemetryBatch(raw, "node-demo", now, 60, true); e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(map[string]any){
		func(v map[string]any) { delete(v, "run_id") },
		func(v map[string]any) { v["node_id"] = "other" },
		func(v map[string]any) { v["fast"] = nil },
		func(v map[string]any) { v["fast"] = []any{}; v["full"] = []any{} },
		func(v map[string]any) {
			v["fast"].([]any)[0].(map[string]any)["sequence"] = json.Number("9223372036854775808")
		},
		func(v map[string]any) { delete(v["fast"].([]any)[0].(map[string]any), "elapsed_seconds") },
		func(v map[string]any) { v["fast"].([]any)[0].(map[string]any)["cpu_percent"] = nil },
		func(v map[string]any) { v["fast"].([]any)[0].(map[string]any)["cpu_percent"] = 101 },
		func(v map[string]any) { v["fast"].([]any)[0].(map[string]any)["errors"] = []string{"arbitrary_error"} },
		func(v map[string]any) {
			v["fast"].([]any)[0].(map[string]any)["errors"] = []string{"cpu_unavailable", "cpu_unavailable"}
		},
		func(v map[string]any) { v["fast"].([]any)[0].(map[string]any)["sampled_at"] = "2026-02-30T00:00:00Z" },
		func(v map[string]any) { v["fast"] = make([]any, 301) },
		func(v map[string]any) { v["full"].([]any)[0].(map[string]any)["filesystems"] = make([]any, 33) },
	} {
		v := schemaFixture(t)
		mutate(v)
		raw, _ := json.Marshal(v)
		if _, e := DecodeTelemetryBatch(raw, "node-demo", now, 60, true); e == nil {
			t.Fatal("invalid schema/context admitted", string(raw))
		}
	}
}
func TestTelemetryRuntimeSchemaConditionals(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{"TelemetryDiskIO", `{"quality":[],"read_bytes_per_second":0,"write_bytes_per_second":5,"read_iops":0,"write_iops":1,"await_ms":2,"util_percent":10}`, true},
		{"TelemetryDiskIO", `[]`, false},
		{"TelemetryDiskIO", `{"device":"sda","quality":[]}`, false},
		{"TelemetryDiskIO", `{"quality":[],"read_bytes_per_second":null}`, false},
		{"TelemetryDemand", `{"session_generation":"s","demand_revision":1,"lease_id":"l","enabled":true,"ttl_seconds":90}`, true},
		{"TelemetryDemand", `{"session_generation":"s","demand_revision":1,"lease_id":"l","enabled":false,"ttl_seconds":90}`, false},
		{"TelemetryReceipt", `{"batch_id":"b","state":"accepted","accepted_count":1,"duplicate_count":0}`, true},
		{"TelemetryReceipt", `{"batch_id":"b","state":"rejected","accepted_count":1,"duplicate_count":0,"error_code":"overloaded"}`, false},
		{"TelemetrySettings", `{"revision":1,"collection_seconds":60,"http_report_seconds":30,"wss_report_seconds":1,"connection_mode":"auto_http_wss"}`, true},
		{"TelemetrySettings", `{"revision":1,"collection_seconds":0,"http_report_seconds":30,"wss_report_seconds":1,"connection_mode":"auto_http_wss"}`, false},
	} {
		if e := ValidateTelemetryJSON([]byte(tc.raw), tc.name, true); (e == nil) != tc.valid {
			t.Fatal(tc, e)
		}
	}
	if ValidateTelemetryJSON([]byte(`{}`), "NodeSettings", true) == nil {
		t.Fatal("legacy schema exposed through telemetry validator")
	}
}
