package collector

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vioira/viorka-agent/internal/config"
	"github.com/vioira/viorka-agent/internal/network"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

const TCPProbeInterval = 20 * time.Second
const TCPProbeWindow = 120 * time.Second
const tcpProbeSamples = 6

type tcpObservation struct {
	at     time.Time
	ms     float64
	failed bool
}
type rollingProbeHistory struct{ observations []tcpObservation }

type TCPProbes struct {
	mu        sync.Mutex
	settings  config.TCPProbe
	histories map[string]*rollingProbeHistory
	next      time.Time
	snapshot  atomic.Pointer[v1.TelemetryTCPProbeBatch]
	changed   chan struct{}
	lookup    func(context.Context, string) ([]net.IPAddr, error)
	dial      func(context.Context, string, string) (net.Conn, error)
	now       func() time.Time
}

func NewTCPProbes(settings *config.TCPProbe, servers []string) *TCPProbes {
	p := &TCPProbes{lookup: network.NewDNS(servers).LookupIPAddr, dial: (&net.Dialer{}).DialContext, now: time.Now, changed: make(chan struct{}, 1)}
	p.Configure(settings)
	return p
}
func (p *TCPProbes) Changes() <-chan struct{} { return p.changed }
func (p *TCPProbes) Configure(settings *config.TCPProbe) {
	p.mu.Lock()
	defer p.mu.Unlock()
	next := config.TCPProbe{TimeoutMS: 1000}
	if settings != nil {
		next = *settings
		next.Targets = slices.Clone(settings.Targets)
	}
	next.IntervalSeconds, next.WindowSeconds = 20, 120
	if slices.Equal(p.settings.Targets, next.Targets) && p.settings.TimeoutMS == next.TimeoutMS {
		return
	}
	histories := map[string]*rollingProbeHistory{}
	for _, target := range next.Targets {
		if h := p.histories[target]; h != nil {
			histories[target] = h
		}
	}
	p.settings, p.histories = next, histories
	p.snapshot.Store(nil)
	p.next = time.Time{}
	select {
	case p.changed <- struct{}{}:
	default:
	}
}

// Snapshot never changes the original batch time or performs network I/O.
func (p *TCPProbes) Snapshot(at time.Time) *v1.TelemetryTCPProbeBatch {
	snapshot := p.snapshot.Load()
	if snapshot == nil || snapshot.SampledAt.After(at) || at.Sub(snapshot.SampledAt) >= TCPProbeWindow {
		return nil
	}
	return snapshot // Published batches and their targets are immutable.
}

// Sample performs one serial round and publishes all targets atomically.
func (p *TCPProbes) Sample(ctx context.Context, at time.Time) (*v1.TelemetryTCPProbeBatch, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if at.Before(p.next) {
		return p.snapshot.Load(), nil
	}
	if len(p.settings.Targets) == 0 {
		return nil, nil
	}
	histories := make(map[string]*rollingProbeHistory, len(p.settings.Targets))
	for _, target := range p.settings.Targets {
		h := &rollingProbeHistory{}
		if previous := p.histories[target]; previous != nil {
			h.observations = slices.Clone(previous.observations)
		}
		ms, ok := p.measure(ctx, target)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		h.observations = append(h.observations, tcpObservation{p.now(), ms, !ok})
		histories[target] = h
	}
	completed := p.now()
	batch := &v1.TelemetryTCPProbeBatch{SampledAt: completed.UTC(), Targets: make([]*v1.TelemetryTCPProbe, 0, len(p.settings.Targets))}
	for _, target := range p.settings.Targets {
		h := histories[target]
		cutoff := completed.Add(-TCPProbeWindow)
		h.observations = slices.DeleteFunc(h.observations, func(o tcpObservation) bool { return !o.at.After(cutoff) })
		if len(h.observations) > tcpProbeSamples {
			h.observations = slices.Clone(h.observations[len(h.observations)-tcpProbeSamples:])
		}
		values := make([]float64, 0, len(h.observations))
		for _, o := range h.observations {
			if !o.failed {
				values = append(values, o.ms)
			}
		}
		result := &v1.TelemetryTCPProbe{Target: target, Ping: -1, Loss: uint64((len(h.observations) - len(values)) * 100 / len(h.observations))}
		if len(values) > 0 {
			slices.Sort(values)
			mid := len(values) / 2
			result.Ping = values[mid]
			if len(values)%2 == 0 {
				result.Ping = (values[mid-1] + values[mid]) / 2
			}
		}
		batch.Targets = append(batch.Targets, result)
	}
	p.histories = histories
	p.snapshot.Store(batch)
	p.next = at.Add(TCPProbeInterval)
	return batch, nil
}
func (p *TCPProbes) measure(parent context.Context, target string) (float64, bool) {
	host, port, _ := net.SplitHostPort(target)
	ctx, cancel := context.WithTimeout(parent, time.Duration(p.settings.TimeoutMS)*time.Millisecond)
	defer cancel()
	addresses, err := p.lookup(ctx, host)
	if err != nil || len(addresses) == 0 {
		return 0, false
	}
	// Reject mixed public/private answers too; never silently probe an internal IP.
	for _, address := range addresses {
		ip, ok := netip.AddrFromSlice(address.IP)
		if !ok || address.Zone != "" || !config.PublicProbeIP(ip) {
			return 0, false
		}
	}
	// One resolved address and one connection attempt per target per tick. DNS is
	// outside the timer; no TLS/HTTP/application payload or retry biases the sample.
	if ctx.Err() != nil {
		return 0, false
	}
	start := p.now()
	conn, err := p.dial(ctx, "tcp", net.JoinHostPort(addresses[0].IP.String(), port))
	ms := p.now().Sub(start).Seconds() * 1000
	if err != nil {
		return 0, false
	}
	conn.Close()
	return ms, true
}
