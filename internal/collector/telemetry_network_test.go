package collector

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vioira/viorka-agent/internal/config"
)

func TestTelemetryNetworkIdentityAndRecovery(t *testing.T) {
	m, write := fixtureMetrics(t)
	if err := os.MkdirAll(filepath.Join(m.root, "sys/kernel/random"), 0700); err != nil {
		t.Fatal(err)
	}
	write("sys/kernel/random/boot_id", "11111111-1111-4111-8111-111111111111\n")
	filter := config.NetworkFilter{}
	sampler, err := NewTelemetryNetwork(filter)
	if err != nil {
		t.Fatal(err)
	}
	sampler.source = m
	index := 2
	sampler.interfaces = func() ([]net.Interface, error) {
		return []net.Interface{{Name: "eth0", Index: index}, {Name: "lo", Index: 1}}, nil
	}
	at := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	ctx := context.Background()
	sample := func(delta time.Duration) NetworkObservation {
		t.Helper()
		at = at.Add(delta)
		v, err := sampler.Sample(ctx, at)
		if err != nil || len(v.Interfaces) != 1 {
			t.Fatal(v, err)
		}
		return v
	}
	first := sample(0)
	if first.Interfaces[0].RXBytesPerSecond != nil || first.Interfaces[0].Quality[0] != "baseline" {
		t.Fatal(first)
	}
	write("net/dev", netFixture(1060, 2120))
	next := sample(30 * time.Second)
	if *next.Interfaces[0].RXBytesPerSecond != 2 || *next.Interfaces[0].TXBytesPerSecond != 4 {
		t.Fatal(next)
	}
	write("net/dev", netFixture(1064, 2128))
	next = sample(2 * time.Second)
	if *next.Interfaces[0].RXBytesPerSecond != 2 {
		t.Fatal("period change reused configured denominator")
	}
	index = 3
	next = sample(time.Second)
	if next.Interfaces[0].Quality[0] != "identity_changed" || next.Interfaces[0].RXBytesPerSecond != nil {
		t.Fatal(next)
	}
	write("net/dev", netFixture(1, 2))
	next = sample(time.Second)
	if next.Interfaces[0].Quality[0] != "counter_reset" {
		t.Fatal(next)
	}
	write("sys/kernel/random/boot_id", "22222222-2222-4222-8222-222222222222")
	next = sample(time.Second)
	if next.Interfaces[0].Quality[0] != "identity_changed" {
		t.Fatal(next)
	}
	next = sample(601 * time.Second)
	if next.Interfaces[0].Quality[0] != "baseline" {
		t.Fatal(next)
	}
	next = sample(-time.Second)
	if next.Interfaces[0].Quality[0] != "baseline" {
		t.Fatal(next)
	}
	write("net/dev", "broken")
	v, err := sampler.Sample(ctx, at.Add(time.Second))
	if err == nil || len(v.Interfaces) != 0 {
		t.Fatal("invalid sample accepted", v, err)
	}
	write("net/dev", netFixture(100, 200))
	next = sample(2 * time.Second)
	if next.Interfaces[0].Quality[0] != "baseline" {
		t.Fatal("failure recovery invented rate")
	}
	if first.Interfaces[0].RXBytes != 1000 {
		t.Fatal("returned snapshot mutated")
	}
}
func TestTelemetryNetworkExplicitLoopbackAndMissingIdentity(t *testing.T) {
	m, write := fixtureMetrics(t)
	if err := os.MkdirAll(filepath.Join(m.root, "sys/kernel/random"), 0700); err != nil {
		t.Fatal(err)
	}
	write("sys/kernel/random/boot_id", "11111111-1111-4111-8111-111111111111")
	filter := config.NetworkFilter{Include: []string{"lo"}, Exclude: []string{"eth*"}}
	s, err := NewTelemetryNetwork(filter)
	if err != nil {
		t.Fatal(err)
	}
	s.source = m
	filter.Include[0] = "eth*" // Constructor must own configuration, not caller slices.
	s.interfaces = func() ([]net.Interface, error) {
		return []net.Interface{{Name: "lo", Index: 1}, {Name: "eth0", Index: 2}}, nil
	}
	v, err := s.Sample(context.Background(), time.Now())
	if err != nil || len(v.Interfaces) != 1 || v.Interfaces[0].Name != "lo" {
		t.Fatal(v, err)
	}
	s.interfaces = func() ([]net.Interface, error) { return nil, nil }
	v, err = s.Sample(context.Background(), time.Now())
	if err == nil || len(v.Interfaces) != 0 {
		t.Fatal("missing identity reported as usable", v)
	}
}

func TestTelemetryNetworkFiltersBeforeSelectedLimit(t *testing.T) {
	m, write := fixtureMetrics(t)
	if err := os.MkdirAll(filepath.Join(m.root, "sys/kernel/random"), 0700); err != nil {
		t.Fatal(err)
	}
	write("sys/kernel/random/boot_id", "11111111-1111-4111-8111-111111111111")
	raw := netFixture(100, 200)
	interfaces := []net.Interface{{Name: "lo", Index: 1}, {Name: "eth0", Index: 2}}
	for i := 0; i < 70; i++ {
		name := fmt.Sprintf("veth%d", i)
		raw += fmt.Sprintf(" %s: 1 0 0 0 0 0 0 0 2 0 0 0 0 0 0 0\n", name)
		interfaces = append(interfaces, net.Interface{Name: name, Index: i + 3})
	}
	write("net/dev", raw)
	s, err := NewTelemetryNetwork(config.NetworkFilter{})
	if err != nil {
		t.Fatal(err)
	}
	s.source = m
	s.interfaces = func() ([]net.Interface, error) { return interfaces, nil }
	observation, err := s.Sample(context.Background(), time.Now())
	if err != nil || len(observation.Interfaces) != 1 {
		t.Fatal(observation, err)
	}
	if _, err = parseNetwork(raw); err == nil {
		t.Fatal("legacy bound unexpectedly changed")
	}
	s, err = NewTelemetryNetwork(config.NetworkFilter{Include: []string{"veth*"}})
	if err != nil {
		t.Fatal(err)
	}
	s.source = m
	s.interfaces = func() ([]net.Interface, error) { return interfaces, nil }
	observation, err = s.Sample(context.Background(), time.Now())
	if err == nil || len(observation.Interfaces) != 0 {
		t.Fatal("selected limit not enforced", observation)
	}
}

func TestRemoteTrafficSelectsOnlyNamedInterfaces(t *testing.T) {
	m, write := fixtureMetrics(t)
	if err := os.MkdirAll(filepath.Join(m.root, "sys/kernel/random"), 0700); err != nil {
		t.Fatal(err)
	}
	write("sys/kernel/random/boot_id", "11111111-1111-4111-8111-111111111111\n")
	network, err := NewTelemetryNetwork(config.NetworkFilter{})
	if err != nil {
		t.Fatal(err)
	}
	network.source = m
	network.interfaces = func() ([]net.Interface, error) {
		return []net.Interface{{Name: "eth0", Index: 2}, {Name: "lo", Index: 1}}, nil
	}
	network.selected = "lo"
	v, err := network.Sample(context.Background(), time.Now())
	if err != nil || len(v.Interfaces) != 1 || v.Interfaces[0].Name != "lo" {
		t.Fatal(v, err)
	}
	network.selected = "missing"
	if _, err = network.Sample(context.Background(), time.Now().Add(time.Second)); err == nil {
		t.Fatal("missing interface silently replaced")
	}
}
