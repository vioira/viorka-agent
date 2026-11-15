package connection

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

// Steady full-buffer sampling with failed sends. Allocations include encoding
// and schema validation; encoded cache bytes are not process RSS.
func BenchmarkTelemetryReplayBuffer(b *testing.B) {
	for _, interfaces := range []int{1, 64} {
		b.Run(fmt.Sprintf("interfaces_%d", interfaces), func(b *testing.B) {
			raw, err := os.ReadFile("../../protocol/v1/testdata/node.metrics.v3.json")
			if err != nil {
				b.Fatal(err)
			}
			var envelope struct {
				Payload v1.TelemetryBatch `json:"payload"`
			}
			if err = json.Unmarshal(raw, &envelope); err != nil {
				b.Fatal(err)
			}
			sample := envelope.Payload.Fast[0]
			base := sample.Interfaces[0]
			sample.Interfaces = sample.Interfaces[:1]
			for len(sample.Interfaces) < interfaces {
				nic := *base
				nic.Name = fmt.Sprintf("eth%d", len(sample.Interfaces))
				sample.Interfaces = append(sample.Interfaces, &nic)
			}
			sample.SampledAt = time.Now().UTC()
			var window telemetryWindow
			sample.Sequence = 0
			peak := 0
			b.ReportAllocs()
			for b.Loop() {
				sample.Sequence++
				if err := window.add(sample); err != nil {
					b.Fatal(err)
				}
				if sample.Sequence%30 == 0 {
					window.complete(window.take(), false)
				}
				size, _ := window.usage()
				peak = max(peak, size)
				if size > telemetryBufferBytes {
					b.Fatal("buffer exceeded", size)
				}
			}
			b.ReportMetric(float64(peak), "peak_buffer_B")
		})
	}
}
