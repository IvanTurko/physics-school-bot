package worker

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/IvanTurko/physics-school-bot/internal/clock"
	"github.com/IvanTurko/physics-school-bot/internal/domain"
)

// Lessons keeps the upcoming lessons and when their students were reminded.
type Lessons interface {
	Upcoming(ctx context.Context) ([]domain.Lesson, error)
	MarkReminded(ctx context.Context, bookingID int64) error
}

// Messenger reminds a student of the lesson; an error means to try again later.
type Messenger interface {
	Remind(ctx context.Context, v domain.BookingView) error
}

// Reminders decides when to remind the students of their lessons.
type Reminders struct {
	lessons Lessons
	send    Messenger
	clock   clock.Clock
	moments func(l domain.Lesson) []time.Time
	log     *slog.Logger
}

// NewReminders creates Reminders that remind a day and an hour before the lesson,
// or in demo mode once, a minute after the booking.
func NewReminders(lessons Lessons, send Messenger, c clock.Clock, demo bool, log *slog.Logger) *Reminders {
	r := &Reminders{lessons: lessons, send: send, clock: c, moments: beforeLesson, log: log}
	if demo {
		r.moments = afterBooking
	}
	return r
}

func beforeLesson(l domain.Lesson) []time.Time {
	return []time.Time{l.Start.Add(-24 * time.Hour), l.Start.Add(-time.Hour)}
}

func afterBooking(l domain.Lesson) []time.Time { return []time.Time{l.Booked.Add(time.Minute)} }

// Tick sends the reminders that are due and records each one sent.
func (r *Reminders) Tick(ctx context.Context) error {
	lessons, err := r.lessons.Upcoming(ctx)
	if err != nil {
		return fmt.Errorf("cannot read lessons: %w", err)
	}
	now := r.clock.Now()
	for _, l := range lessons {
		if !due(r.moments(l), l.Reminded, now) {
			continue
		}
		if err := r.send.Remind(ctx, l.BookingView); err != nil {
			r.log.Error("cannot remind", "booking", l.ID, "err", err)
			continue
		}
		if err := r.lessons.MarkReminded(ctx, l.ID); err != nil {
			r.log.Error("cannot mark reminded", "booking", l.ID, "err", err)
		}
	}
	return nil
}

func due(moments []time.Time, reminded, now time.Time) bool {
	return slices.ContainsFunc(moments, func(m time.Time) bool { return m.After(reminded) && !m.After(now) })
}

// Run sends the due reminders at every tick until ctx ends.
func (r *Reminders) Run(ctx context.Context, ticks <-chan time.Time) {
	every(ctx, ticks, r.Tick, r.log, "reminders")
}

// every runs tick at each of ticks until ctx ends and logs its failures under name.
func every(ctx context.Context, ticks <-chan time.Time, tick func(context.Context) error, log *slog.Logger, name string) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			if err := tick(ctx); err != nil {
				log.Error(name, "err", err)
			}
		}
	}
}
