package collector

import (
	"context"
	"runtime"
	"strings"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

func (m *Metrics) sampleUptime(ctx context.Context) *v1.TelemetryUptime {
	sample := &v1.TelemetryUptime{SampledAt: time.Now().UTC()}
	if runtime.GOOS != "linux" && m.root == "/proc" {
		sample.Error = "unsupported_platform"
		return sample
	}
	data, err := m.read(ctx, "uptime")
	fields := strings.Fields(data)
	if err == nil && len(fields) > 0 {
		if seconds, err := number(fields[0]); err == nil && seconds <= 1e20 {
			sample.Seconds = &seconds
			return sample
		}
	}
	sample.Error = "uptime_unavailable"
	return sample
}
