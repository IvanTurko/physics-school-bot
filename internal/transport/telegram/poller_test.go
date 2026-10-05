package telegram

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"
	"time"

	tg "github.com/IvanTurko/physics-school-bot/internal/provider/telegram"
)

// batches answers getUpdates with the queued batches, then stops the poll.
type batches struct {
	queue   [][]tg.Update
	offsets []int64
	stop    context.CancelFunc
}

func (b *batches) GetUpdates(ctx context.Context, offset int64) ([]tg.Update, error) {
	b.offsets = append(b.offsets, offset)
	if len(b.queue) == 0 {
		b.stop()
		return nil, errors.New("stopped")
	}
	next := b.queue[0]
	b.queue = b.queue[1:]
	return next, nil
}

func TestPollAsksPastLastUpdate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	src := &batches{queue: [][]tg.Update{{{UpdateID: 10}, {UpdateID: 11}}, nil}, stop: cancel}

	var pushed []int64
	Poll(ctx, src, func(u tg.Update) { pushed = append(pushed, u.UpdateID) }, quiet)

	if len(pushed) != 2 || pushed[0] != 10 || pushed[1] != 11 {
		t.Errorf("pushed %v, want [10 11]", pushed)
	}
	if want := []int64{0, 12, 12}; !slices.Equal(src.offsets, want) {
		t.Errorf("offsets %v, want %v", src.offsets, want)
	}
}

// failing refuses every call and counts them.
type failing struct{ calls int }

func (f *failing) GetUpdates(context.Context, int64) ([]tg.Update, error) {
	f.calls++
	return nil, errors.New("unauthorized")
}

func TestPollPausesAfterFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	src := &failing{}

	Poll(ctx, src, func(tg.Update) {}, quiet)
	if src.calls != 1 {
		t.Fatalf("calls = %d, want one within the pause", src.calls)
	}
}

func TestPollStopsWithoutLoggingAnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var logs bytes.Buffer

	Poll(ctx, &batches{stop: cancel}, func(tg.Update) {}, slog.New(slog.NewTextHandler(&logs, nil)))
	if logs.Len() != 0 {
		t.Fatalf("logged %q while stopping", logs.String())
	}
}
