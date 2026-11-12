package connection

import (
	"context"
	"sync"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

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
			probeCtx, cancel := context.WithTimeout(ctx, 24*time.Second)
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
