package connection

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"sync"
	"time"
)

var errSessionChanged = errors.New("shared session changed")

type sessionCredential struct {
	Token      string
	ExpiresAt  time.Time
	Generation uint64
	Changed    <-chan struct{}
}
type sessionCoordinator struct {
	mu         sync.Mutex
	binding    string
	current    sessionCredential
	changed    chan struct{}
	running    chan struct{}
	generation uint64
	blocked    error
	failure    error
	retryAt    time.Time
	now        func() time.Time
}

func (s *sessionCoordinator) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}
func (s *sessionCoordinator) invalidate() {
	if s.changed != nil {
		close(s.changed)
		s.changed = nil
	}
}

// Get serializes challenge/sign/session issuance across all transports. It does
// not create goroutines; waiting callers retain their own cancellation deadline.
func (s *sessionCoordinator) Get(ctx context.Context, binding string, issue func(context.Context) (sessionCredential, error)) (sessionCredential, error) {
	for {
		if err := ctx.Err(); err != nil {
			return sessionCredential{}, err
		}
		s.mu.Lock()
		if s.binding != "" && s.binding != binding {
			s.mu.Unlock()
			return sessionCredential{}, fmt.Errorf("session identity changed during run")
		}
		s.binding = binding
		if s.blocked != nil {
			err := s.blocked
			s.mu.Unlock()
			return sessionCredential{}, err
		}
		if s.running != nil {
			done := s.running
			s.mu.Unlock()
			select {
			case <-ctx.Done():
				return sessionCredential{}, ctx.Err()
			case <-done:
				continue
			}
		}
		now := s.clock()
		if s.current.Token != "" && s.current.ExpiresAt.After(now.Add(30*time.Second)) {
			v := s.current
			s.mu.Unlock()
			return v, nil
		}
		if s.failure != nil && now.Before(s.retryAt) {
			err := s.failure
			s.mu.Unlock()
			return sessionCredential{}, err
		}
		done := make(chan struct{})
		s.running = done
		s.invalidate()
		s.mu.Unlock()
		bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
		v, err := issue(bounded)
		if err == nil {
			err = bounded.Err()
		}
		cancel()
		s.mu.Lock()
		if err == nil && (len(v.Token) != 64 || !v.ExpiresAt.After(s.clock().Add(30*time.Second)) || v.ExpiresAt.After(s.clock().Add(16*time.Minute))) {
			err = fmt.Errorf("invalid shared session lifetime")
		}
		if err == nil {
			s.generation++
			s.changed = make(chan struct{})
			v.Generation = s.generation
			v.Changed = s.changed
			s.current = v
			s.failure = nil
		} else {
			s.current = sessionCredential{}
			s.failure = err
			s.retryAt = s.clock().Add(time.Second)
			var certErr *tls.CertificateVerificationError
			if errors.Is(err, ErrIdentityRejected) || errors.As(err, &certErr) {
				s.blocked = err
			}
		}
		s.running = nil
		close(done)
		s.mu.Unlock()
		return v, err
	}
}

// Reject fences delayed responses: an old request cannot revoke newer credentials.
// expired is true only for an authenticated session_expired classification.
func (s *sessionCoordinator) Reject(generation uint64, expired bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation == 0 || s.current.Generation != generation || s.running != nil {
		return false
	}
	s.invalidate()
	s.current = sessionCredential{}
	if !expired {
		s.blocked = ErrIdentityRejected
	}
	return true
}

// block preserves TLS failure classification and fences stale transport results.
func (s *sessionCoordinator) block(generation uint64, err error) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation == 0 || s.current.Generation != generation || s.running != nil {
		return false
	}
	s.invalidate()
	s.current = sessionCredential{}
	s.blocked = err
	return true
}
