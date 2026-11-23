package collector

import (
	"context"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/vioira/viorka-agent/internal/config"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

func TestSlowUptimeAndIPPartialFailures(t *testing.T) {
	m, write := fixtureMetrics(t)
	write("uptime", "1047600.50 99999.75\n")
	slow := NewTelemetryPublicIP()
	slow.readUptime = m.sampleUptime
	slow.lookup = func(context.Context, bool) (string, error) { return "", context.DeadlineExceeded }
	at := time.Now()
	ip, err := slow.SampleWithUptime(t.Context(), at, true)
	if err != nil || len(ip.Errors) != 2 || ip.Uptime.Seconds == nil || *ip.Uptime.Seconds != 1047600.5 || ip.Uptime.SampledAt.Before(at) {
		t.Fatal(ip, err)
	}
	*ip.Uptime.Seconds = 0 // A caller cannot mutate the retained observation.
	write("uptime", "bad")
	fast, err := NewTelemetryFast(config.NetworkFilter{})
	if err != nil {
		t.Fatal(err)
	}
	fast.source = m
	fast.network.source = m
	for i := 1; i <= 20; i++ {
		f, _, err := fast.SampleWithUptime(t.Context(), at.Add(time.Duration(i)*time.Second), false)
		if err != nil || f.UptimeSeconds != nil || slices.Contains(f.Errors, "uptime_unavailable") {
			t.Fatal(f, err)
		}
	}
	cached, err := slow.SampleWithUptime(t.Context(), at.Add(599*time.Second), true)
	if err != nil || cached.Sequence != ip.Sequence || !cached.Uptime.SampledAt.Equal(ip.Uptime.SampledAt) || *cached.Uptime.Seconds != 1047600.5 {
		t.Fatal(cached, err)
	}
	slow.lookup = func(_ context.Context, ipv4 bool) (string, error) {
		if ipv4 {
			return "8.8.8.8", nil
		}
		return "2606:4700:4700::1111", nil
	}
	next, err := slow.SampleWithUptime(t.Context(), at.Add(10*time.Minute), true)
	if err != nil || next.Ipv4 != "8.8.8.8" || next.Uptime.Error != "uptime_unavailable" || next.Uptime.Seconds != nil {
		t.Fatal(next, err)
	}
	if runtime.GOOS != "linux" && NewMetrics().sampleUptime(t.Context()).Error != "unsupported_platform" {
		t.Fatal("non-Linux uptime")
	}
}

func TestSlowUptimeSkippedWithoutCapability(t *testing.T) {
	slow := NewTelemetryPublicIP()
	slow.readUptime = func(context.Context) *v1.TelemetryUptime { t.Fatal("unexpected uptime read"); return nil }
	slow.lookup = func(context.Context, bool) (string, error) { return "", context.DeadlineExceeded }
	ip, err := slow.Sample(t.Context(), time.Now())
	if err != nil || ip.Uptime != nil {
		t.Fatal(ip, err)
	}
}
