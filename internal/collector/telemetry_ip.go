package collector

import (
	"context"
	"fmt"
	"math"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

// TelemetryPublicIP owns the slow address cache independently of OS/full
// collection. Call from its own bounded worker, never the fast sampler/writer.
type TelemetryPublicIP struct {
	lookup   func(context.Context, bool) (string, error)
	cached   *v1.TelemetryPublicIP
	sequence uint64
}

func NewTelemetryPublicIP(servers ...string) *TelemetryPublicIP {
	return &TelemetryPublicIP{lookup: publicIPLookup(servers)}
}
func (t *TelemetryPublicIP) Sample(ctx context.Context, at time.Time) (*v1.TelemetryPublicIP, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if at.IsZero() {
		return nil, fmt.Errorf("missing public IP sample time")
	}
	copyResult := func() *v1.TelemetryPublicIP { v := *t.cached; v.Errors = append([]string{}, v.Errors...); return &v }
	if t.cached != nil && at.Sub(t.cached.SampledAt) >= 0 && at.Sub(t.cached.SampledAt) < 600*time.Second {
		return copyResult(), nil
	}
	if t.sequence >= math.MaxInt64 {
		return nil, fmt.Errorf("public IP sequence exhausted")
	}
	// The existing fixed-source HTTP probes have TLS validation and timeouts.
	// Bound the whole two-family refresh as well, including cancellation.
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, 22*time.Second)
	defer cancel()
	v := &v1.TelemetryPublicIP{SampledAt: at.UTC(), Errors: []string{}}
	for _, ipv4 := range []bool{true, false} {
		address, err := t.lookup(ctx, ipv4)
		if err != nil || !v1.PublicAddress(address, ipv4) {
			if ipv4 {
				v.Errors = append(v.Errors, "ipv4_unavailable")
			} else {
				v.Errors = append(v.Errors, "ipv6_unavailable")
			}
			continue
		}
		if ipv4 {
			v.Ipv4 = address
		} else {
			v.Ipv6 = address
		}
	}
	if err := parent.Err(); err != nil {
		return nil, err
	}
	t.sequence++
	v.Sequence = t.sequence
	t.cached = v
	return copyResult(), nil
}
