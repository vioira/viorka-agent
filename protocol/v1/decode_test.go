package v1

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStrictEnvelope(t *testing.T) {
	good := Message("node.heartbeat", "node", "message", Heartbeat{})
	b, _ := json.Marshal(good)
	if _, err := Parse(b, "node"); err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{append(b, []byte(`{}`)...), []byte(strings.Repeat(" ", 65537)), []byte(`{"version":"2.0"}`), []byte(strings.Replace(string(b), `"node_id":"node"`, `"node_id":"forged"`, 1)), []byte(strings.Replace(string(b), `"payload":{`, `"extra":true,"payload":{`, 1)), []byte(`{"payload":` + strings.Repeat(`[`, 17) + strings.Repeat(`]`, 17) + `}`)} {
		if _, err := Parse(data, "node"); err == nil {
			t.Fatalf("accepted invalid data")
		}
	}
}
