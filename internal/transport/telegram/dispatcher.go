// Package telegram receives the bot's updates and writes everything the bot
// says in Telegram.
package telegram

import (
	"context"
	"log/slog"
	"sync"
	"time"

	tg "github.com/IvanTurko/physics-school-bot/internal/provider/telegram"
)

// Dispatcher runs updates of one user strictly in order and updates of
// different users in parallel.
type Dispatcher struct {
	handle func(ctx context.Context, u tg.Update)
	log    *slog.Logger

	// ctx is cancelled at the shutdown deadline, not together with the poller's.
	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	queues map[int64][]tg.Update // a key present means its worker is running
	wg     sync.WaitGroup
}

// NewDispatcher creates a Dispatcher that passes each update to handle; ctx
// bounds the handlers, and Shutdown cancels it at its deadline.
func NewDispatcher(ctx context.Context, handle func(ctx context.Context, u tg.Update), log *slog.Logger) *Dispatcher {
	ctx, cancel := context.WithCancel(ctx)
	return &Dispatcher{handle: handle, log: log, ctx: ctx, cancel: cancel, queues: make(map[int64][]tg.Update)}
}

// Push queues the update behind the user's earlier ones and returns at once.
func (d *Dispatcher) Push(user int64, u tg.Update) {
	d.mu.Lock()
	defer d.mu.Unlock()
	q, running := d.queues[user]
	d.queues[user] = append(q, u)
	if !running {
		d.wg.Add(1)
		go d.work(user)
	}
}

func (d *Dispatcher) work(user int64) {
	defer d.wg.Done()
	for {
		d.mu.Lock()
		q := d.queues[user]
		if len(q) == 0 {
			delete(d.queues, user)
			d.mu.Unlock()
			return
		}
		u := q[0]
		d.queues[user] = q[1:]
		d.mu.Unlock()

		d.run(u)
	}
}

// run keeps a panic in one handler from taking the bot down.
func (d *Dispatcher) run(u tg.Update) {
	defer func() {
		if r := recover(); r != nil {
			d.log.Error("handler panicked", "update_id", u.UpdateID, "panic", r)
		}
	}()
	d.handle(d.ctx, u)
}

// Shutdown waits for the queues to drain until deadline, then cancels the
// handlers still running and waits for them to return.
func (d *Dispatcher) Shutdown(deadline time.Time) {
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	if !drain(done, deadline, d.cancel) {
		d.log.Warn("queues not drained by the deadline, handlers cancelled")
	}
}

// drain waits for done until deadline, then cancels the work and waits for it
// to stop; it reports whether the work finished in time.
func drain(done <-chan struct{}, deadline time.Time, cancel context.CancelFunc) bool {
	select {
	case <-done:
		cancel()
		return true
	case <-time.After(time.Until(deadline)):
		cancel()
		<-done
		return false
	}
}
