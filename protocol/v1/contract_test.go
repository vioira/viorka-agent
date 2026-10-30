package v1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Canonical examples include legacy proxy.apply. Structural compatibility is
// independent of the executor's node.inspect-only authorization policy.
func TestCanonicalExamples(t *testing.T) {
	files, err := filepath.Glob("testdata/*.json")
	if err != nil || len(files) < 5 {
		t.Fatal("canonical fixtures missing", err)
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var e Envelope
			if err = json.Unmarshal(b, &e); err != nil {
				t.Fatal(err)
			}
			if _, err = Parse(b, e.NodeID); err != nil {
				t.Fatal(err)
			}
			var payload any
			switch e.Type {
			case "node.metrics":
				payload = &NodeMetrics{}
			case "system.hello":
				payload = &Hello{}
			case "task.dispatch":
				payload = &TaskDispatch{}
			case "task.result":
				payload = &TaskReply{}
			default:
				return
			}
			if err = Decode(e.Payload, payload); err != nil {
				t.Fatal(err)
			}
		})
	}
}
