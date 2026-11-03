package collector

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTelemetryPublicIPIndependentCache(t *testing.T) {
	s := NewTelemetryPublicIP()
	calls := 0
	fail := false
	s.lookup = func(ctx context.Context, v4 bool) (string, error) {
		calls++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded lookup")
		}
		if fail {
			return "", errors.New("unavailable")
		}
		if v4 {
			return "8.8.8.8", nil
		}
		return "2606:4700:4700::1111", nil
	}
	at := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	a, err := s.Sample(context.Background(), at)
	if err != nil || calls != 2 || a.Sequence != 1 {
		t.Fatal(a, err, calls)
	}
	a.Ipv4 = "mutated"
	a.Errors = append(a.Errors, "unknown")
	b, err := s.Sample(context.Background(), at.Add(599*time.Second))
	if err != nil || calls != 2 || b.Sequence != 1 || !b.SampledAt.Equal(at) || b.Ipv4 != "8.8.8.8" || len(b.Errors) != 0 {
		t.Fatal(b, err, calls)
	}
	fail = true
	b, err = s.Sample(context.Background(), at.Add(600*time.Second))
	if err != nil || calls != 4 || b.Sequence != 2 || b.Ipv4 != "" || b.Ipv6 != "" || len(b.Errors) != 2 {
		t.Fatal(b, err, calls)
	}
	b, err = s.Sample(context.Background(), at.Add(601*time.Second))
	if err != nil || calls != 4 || b.Sequence != 2 {
		t.Fatal(b, err, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.Sample(ctx, at.Add(1200*time.Second)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
