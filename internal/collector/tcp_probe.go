package collector

import (
	"context"
	"net"
	"net/netip"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/vioira/viorka-agent/internal/config"
	"github.com/vioira/viorka-agent/internal/network"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

type tcpObservation struct {
	at     time.Time
	ms     float64
	failed bool
}
type tcpHistory struct {
	observations []tcpObservation
	at           time.Time
	lastError    string
}

// TCPProbes is owned by the existing full worker. It has no goroutines, retries,
// sockets retained between samples, or state outside the configured window.
type TCPProbes struct {
	mu        sync.Mutex
	settings  config.TCPProbe
	histories map[string]*tcpHistory
	next      time.Time
	lookup    func(context.Context, string) ([]net.IPAddr, error)
	dial      func(context.Context, string, string) (net.Conn, error)
	now       func() time.Time
}

func NewTCPProbes(settings *config.TCPProbe, servers []string) *TCPProbes {
	p := &TCPProbes{lookup: network.NewDNS(servers).LookupIPAddr, dial: (&net.Dialer{}).DialContext, now: time.Now}
	p.Configure(settings)
	return p
}
func (p *TCPProbes) Configure(settings *config.TCPProbe) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if settings != nil && reflect.DeepEqual(p.settings, *settings) {
		return
	}
	p.settings = config.TCPProbe{}
	if settings != nil {
		p.settings = *settings
		p.settings.Targets = slices.Clone(settings.Targets)
	}
	p.histories = map[string]*tcpHistory{}
	p.next = time.Time{}
}
func (p *TCPProbes) Sample(ctx context.Context, at time.Time) ([]*v1.TelemetryTCPProbe, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	due := !at.Before(p.next)
	// Commit a complete tick together; cancellation must not advance cadence or
	// leave partially sampled targets in the next protocol observation.
	histories := make(map[string]*tcpHistory, len(p.settings.Targets))
	out := make([]*v1.TelemetryTCPProbe, 0, len(p.settings.Targets))
	for _, target := range p.settings.Targets {
		h := &tcpHistory{}
		if previous := p.histories[target]; previous != nil {
			*h = *previous
			h.observations = slices.Clone(previous.observations)
		}
		histories[target] = h
		if due {
			ms, attempted, code := p.measure(ctx, target)
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			h.at = at
			h.lastError = code
			if attempted {
				h.observations = append(h.observations, tcpObservation{at, ms, code != ""})
				if len(h.observations) > 120 {
					h.observations = slices.Clone(h.observations[len(h.observations)-120:])
				}
			}
		}
		cutoff := at.Add(-time.Duration(p.settings.WindowSeconds) * time.Second)
		h.observations = slices.DeleteFunc(h.observations, func(o tcpObservation) bool { return !o.at.After(cutoff) })
		result := &v1.TelemetryTCPProbe{Target: target, SampledAt: h.at.UTC(), WindowSeconds: p.settings.WindowSeconds, Samples: uint64(len(h.observations)), LastError: h.lastError}
		values := make([]float64, 0, len(h.observations))
		for _, o := range h.observations {
			if o.failed {
				result.Failures++
			} else {
				values = append(values, o.ms)
			}
		}
		if result.Samples > 0 {
			percent := float64(result.Failures) * 100 / float64(result.Samples)
			result.FailurePercent = &percent
		}
		if len(values) > 0 {
			slices.Sort(values)
			mid := len(values) / 2
			median := values[mid]
			if len(values)%2 == 0 {
				median = (values[mid-1] + median) / 2
			}
			result.MedianMs = &median
		}
		out = append(out, result)
	}
	p.histories = histories
	if due {
		p.next = at.Add(time.Duration(p.settings.IntervalSeconds) * time.Second)
	}
	return out, nil
}
func (p *TCPProbes) measure(parent context.Context, target string) (float64, bool, string) {
	host, port, _ := net.SplitHostPort(target)
	ctx, cancel := context.WithTimeout(parent, time.Duration(p.settings.TimeoutMS)*time.Millisecond)
	defer cancel()
	addresses, err := p.lookup(ctx, host)
	if err != nil || len(addresses) == 0 {
		return 0, false, "dns_failed"
	}
	// Reject mixed public/private answers too; never silently probe an internal IP.
	for _, address := range addresses {
		ip, ok := netip.AddrFromSlice(address.IP)
		if !ok || address.Zone != "" || !config.PublicProbeIP(ip) {
			return 0, false, "address_blocked"
		}
	}
	// One resolved address and one connection attempt per target per tick. DNS is
	// outside the timer; no TLS/HTTP/application payload or retry biases the sample.
	if ctx.Err() != nil {
		return 0, false, "dns_failed"
	}
	start := p.now()
	conn, err := p.dial(ctx, "tcp", net.JoinHostPort(addresses[0].IP.String(), port))
	ms := p.now().Sub(start).Seconds() * 1000
	if err != nil {
		return 0, true, "connect_failed"
	}
	conn.Close()
	return ms, true, ""
}
