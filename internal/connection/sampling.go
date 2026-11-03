package connection

import (
	"context"
	"sync"
	"time"

	"github.com/vioira/viorka-agent/internal/collector"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

// sampling lives for Run, not a WSS session. Samples never reach disk.
// Offline storage is one snapshot; an online window is capped at ten samples.
type sampling struct {
	cancel   context.CancelFunc
	mu       sync.Mutex
	latest   v1.NodeMetrics
	pending  []*v1.NodeMetrics
	online   bool
	interval time.Duration
	changed  chan struct{}
	ready    chan struct{}
	done     chan struct{}
}

func startSampling(ctx context.Context, interval time.Duration) *sampling {
	ctx, cancel := context.WithCancel(ctx)
	s := &sampling{cancel: cancel, interval: interval, changed: make(chan struct{}, 1), ready: make(chan struct{}), done: make(chan struct{})}
	go s.run(ctx, collector.NewMetrics().Sample)
	return s
}
func (s *sampling) run(ctx context.Context, sample func(context.Context) v1.NodeMetrics) {
	defer close(s.done)
	collect := func() {
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		m := sample(c)
		cancel()
		s.mu.Lock()
		s.latest = m
		if s.online {
			s.pending = append(s.pending, &m)
			if len(s.pending) > 10 {
				s.pending = s.pending[len(s.pending)-10:]
			}
		} else {
			s.pending = nil
		}
		s.mu.Unlock()
	}
	collect()
	close(s.ready)
	s.mu.Lock()
	interval := s.interval
	s.mu.Unlock()
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.changed:
		case <-timer.C:
			collect()
		}
		s.mu.Lock()
		interval = s.interval
		s.mu.Unlock()
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(interval)
	}
}
func (s *sampling) setInterval(d time.Duration) {
	s.mu.Lock()
	if s.interval == d {
		s.mu.Unlock()
		return
	}
	s.interval = d
	s.mu.Unlock()
	select {
	case s.changed <- struct{}{}:
	default:
	}
}
func (s *sampling) setOnline(online bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.online = online
	s.pending = nil
}
func (s *sampling) take() []*v1.NodeMetrics {
	s.mu.Lock()
	defer s.mu.Unlock()
	samples := s.pending
	s.pending = nil
	if len(samples) == 0 && !s.latest.SampledAt.IsZero() {
		m := s.latest
		samples = []*v1.NodeMetrics{&m}
	}
	return samples
}

// Asset discovery starts only after capability negotiation, then survives reconnects.
type assetSampling struct {
	mu     sync.Mutex
	latest v1.NodeInventory
	wake   chan struct{}
	done   chan struct{}
}

func startAssets(ctx context.Context, sample func(context.Context, time.Duration) v1.NodeInventory) *assetSampling {
	s := &assetSampling{wake: make(chan struct{}, 1), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		timer := time.NewTicker(5 * time.Minute)
		defer timer.Stop()
		for {
			probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			value := sample(probeCtx, 5*time.Minute)
			cancel()
			s.mu.Lock()
			s.latest = value
			s.mu.Unlock()
			s.notify()
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
		}
	}()
	return s
}
func (s *assetSampling) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *assetSampling) snapshot() v1.NodeInventory {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latest
}
