package v1

import "testing"

func TestRetiredHelloFieldsRejected(t *testing.T) {
	for _, field := range []string{`"metrics_version":2`, `"settings_version":1`, `"collection_settings":{}`, `"node_settings":{}`, `"host_metrics_version":1`, `"filesystems_version":1`} {
		var ack HelloAck
		if err := Decode([]byte(`{"protocol_version":"1.0","heartbeat_seconds":30,`+field+`}`), &ack); err == nil {
			t.Fatal("retired hello field accepted", field)
		}
	}
}
