// Package clock tells business code the time, real or fixed.
package clock

import (
	"sync"
	"time"
)

// Clock tells the current time.
type Clock interface {
	Now() time.Time
}

// Real is the wall clock.
type Real struct{}

// Now returns time.Now.
func (Real) Now() time.Time { return time.Now() }

// Fake stands still until it is told to move; safe for concurrent use.
type Fake struct {
	mu  sync.Mutex
	now time.Time
}

// NewFake creates a Fake showing t.
func NewFake(t time.Time) *Fake { return &Fake{now: t} }

// Now returns the time the Fake shows.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Advance moves the Fake forward by d.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}
