package testkit

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"
)

// wait bounds every wait for the bot, in a test and in a fake.
const wait = 5 * time.Second

// state guards a fake's data and wakes its waiters on every change.
type state struct {
	mu      sync.Mutex
	changed chan struct{} // closed and replaced on every change
}

func newState() *state { return &state{changed: make(chan struct{})} }

// update changes the data under the lock and wakes the waiters.
func (s *state) update(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn()
	close(s.changed)
	s.changed = make(chan struct{})
}

// await checks ok under the lock after every change until it holds, and
// reports false when ctx ends or wait passes first.
func (s *state) await(ctx context.Context, ok func() bool) bool {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	for {
		s.mu.Lock()
		done, changed := ok(), s.changed
		s.mu.Unlock()
		if done {
			return true
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return false
		}
	}
}

func answer(t testing.TB, w http.ResponseWriter, v any) {
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("cannot write fake answer: %v", err)
	}
}
