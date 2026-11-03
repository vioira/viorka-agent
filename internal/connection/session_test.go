package connection

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionCoordinatorCoalescesRefreshAndFencesLateResponses(t *testing.T) {
	at := time.Now()
	s := sessionCoordinator{now: func() time.Time { return at }}
	var calls atomic.Int32
	issue := func(ctx context.Context) (sessionCredential, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded issuance")
		}
		calls.Add(1)
		return sessionCredential{Token: strings.Repeat("a", 64), ExpiresAt: at.Add(15 * time.Minute)}, nil
	}
	var wg sync.WaitGroup
	results := make(chan sessionCredential, 32)
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := s.Get(context.Background(), "node", issue)
			results <- v
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first sessionCredential
	for v := range results {
		if v.Generation != 1 {
			t.Fatal(v.Generation)
		}
		first = v
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	at = at.Add(14*time.Minute + 31*time.Second)
	second, err := s.Get(context.Background(), "node", issue)
	if err != nil || second.Generation != 2 || calls.Load() != 2 {
		t.Fatal(second.Generation, err, calls.Load())
	}
	select {
	case <-first.Changed:
	default:
		t.Fatal("old connection not notified")
	}
	if s.Reject(first.Generation, false) {
		t.Fatal("late rejection affected new session")
	}
	if _, err = s.Get(context.Background(), "other-node", issue); err == nil {
		t.Fatal("cross identity reuse")
	}
	if !s.Reject(second.Generation, false) {
		t.Fatal("current rejection ignored")
	}
	if _, err = s.Get(context.Background(), "node", issue); !errors.Is(err, ErrIdentityRejected) || calls.Load() != 2 {
		t.Fatal(err, calls.Load())
	}
}
func TestSessionCoordinatorWaitCancellationAndRefreshFence(t *testing.T) {
	at := time.Now()
	s := sessionCoordinator{now: func() time.Time { return at }}
	first, err := s.Get(context.Background(), "node", func(context.Context) (sessionCredential, error) {
		return sessionCredential{Token: strings.Repeat("a", 64), ExpiresAt: at.Add(time.Minute)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	at = at.Add(31 * time.Second)
	started, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	done := make(chan error, 1)
	go func() {
		_, err := s.Get(context.Background(), "node", func(context.Context) (sessionCredential, error) {
			close(started)
			<-release
			return sessionCredential{Token: strings.Repeat("b", 64), ExpiresAt: at.Add(15 * time.Minute)}, nil
		})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}
	if s.Reject(first.Generation, false) {
		t.Fatal("in-flight old session rejection blocked refresh")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.Get(ctx, "node", nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("refresh did not finish")
	}
}
func TestSessionCoordinatorTemporaryFailureBudgetAndExpiry(t *testing.T) {
	at := time.Now()
	s := sessionCoordinator{now: func() time.Time { return at }}
	calls := 0
	failure := errors.New("network failure")
	issue := func(context.Context) (sessionCredential, error) {
		calls++
		if calls == 1 {
			return sessionCredential{}, failure
		}
		return sessionCredential{Token: strings.Repeat("a", 64), ExpiresAt: at.Add(15 * time.Minute)}, nil
	}
	if _, err := s.Get(context.Background(), "node", issue); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if _, err := s.Get(context.Background(), "node", issue); !errors.Is(err, failure) || calls != 1 {
		t.Fatal(err, calls)
	}
	at = at.Add(time.Second)
	v, err := s.Get(context.Background(), "node", issue)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Reject(v.Generation, true) {
		t.Fatal("expiry ignored")
	}
	if _, err = s.Get(context.Background(), "node", issue); err != nil || calls != 3 {
		t.Fatal(err, calls)
	}
}
