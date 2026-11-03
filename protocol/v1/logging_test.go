package v1

import (
	"encoding/json"
	"testing"
)

func TestTelemetryLogLevels(t *testing.T) {
	if DefaultTelemetrySettings().LogLevel != "warn" {
		t.Fatal("default must be warn")
	}
	for _, value := range []string{"debug", "info", "warn", "error", "trace", "WARN"} {
		s := DefaultTelemetrySettings()
		s.LogLevel = value
		raw, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		valid := value != "trace" && value != "WARN"
		if (s.Validate() == nil) != valid || (ValidateTelemetryJSON(raw, "TelemetrySettings", false) == nil) != valid {
			t.Fatal(value)
		}
	}
}
