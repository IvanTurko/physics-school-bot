package worker_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/IvanTurko/physics-school-bot/internal/clock"
	"github.com/IvanTurko/physics-school-bot/internal/worker"
)

type recorded struct{ starts []time.Time }

func (r *recorded) AddSlots(_ context.Context, starts []time.Time) (int, error) {
	r.starts = starts
	return len(starts), nil
}

func TestDemoSlotsCoverWeekAhead(t *testing.T) {
	msk := time.FixedZone("MSK", 3*60*60)
	slots := &recorded{}
	now := clock.NewFake(time.Date(2026, 10, 5, 17, 30, 0, 0, msk))
	d := worker.NewDemoSlots(slots, now, msk, slog.New(slog.DiscardHandler))

	if err := d.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := slots.starts
	first, last := time.Date(2026, 10, 5, 18, 0, 0, 0, msk), time.Date(2026, 10, 11, 19, 0, 0, 0, msk)
	if len(got) != 2+6*4 || !got[0].Equal(first) || !got[len(got)-1].Equal(last) {
		t.Fatalf("got %d slots from %v to %v; want today's 18:00 and 19:00, then 16:00–19:00 for six days", len(got), got[0], got[len(got)-1])
	}
}
