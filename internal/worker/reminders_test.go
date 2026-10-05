package worker_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/IvanTurko/physics-school-bot/internal/clock"
	"github.com/IvanTurko/physics-school-bot/internal/domain"
	"github.com/IvanTurko/physics-school-bot/internal/worker"
)

// calendar keeps the lessons and the IDs marked reminded; err fails the read.
type calendar struct {
	lessons []domain.Lesson
	marked  []int64
	err     error
}

func (c *calendar) Upcoming(context.Context) ([]domain.Lesson, error) { return c.lessons, c.err }

func (c *calendar) MarkReminded(_ context.Context, id int64) error {
	c.marked = append(c.marked, id)
	return nil
}

// postman records the bookings it reminded and fails booking refuse.
type postman struct {
	sent   []int64
	refuse int64
}

func (p *postman) Remind(_ context.Context, v domain.BookingView) error {
	if v.ID == p.refuse {
		return errors.New("telegram is down")
	}
	p.sent = append(p.sent, v.ID)
	return nil
}

// start is when the test lessons start.
var start = time.Date(2026, 10, 8, 16, 0, 0, 0, time.UTC)

// lesson is the lesson of booking id at start.
func lesson(id int64, booked, reminded time.Time) domain.Lesson {
	v := domain.BookingView{Booking: domain.Booking{ID: id}, Start: start}
	return domain.Lesson{BookingView: v, Booked: booked, Reminded: reminded}
}

// tick runs one pass of the reminders at now.
func tick(t *testing.T, cal *calendar, post *postman, now time.Time, demo bool) {
	t.Helper()
	r := worker.NewReminders(cal, post, clock.NewFake(now), demo, slog.New(slog.DiscardHandler))
	if err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRemindersFollowSchedule(t *testing.T) {
	demoNow := start.Add(-48 * time.Hour) // away from the lesson's moments
	for _, tc := range []struct {
		name             string
		demo             bool
		booked, reminded time.Time
		now              time.Time
		want             []int64
	}{
		{"24h ahead", false, time.Time{}, start.Add(-48 * time.Hour), start.Add(-24 * time.Hour), []int64{7}},
		{"24h1m ahead", false, time.Time{}, start.Add(-48 * time.Hour), start.Add(-24*time.Hour - time.Minute), nil},
		{"1h ahead", false, time.Time{}, start.Add(-24 * time.Hour), start.Add(-time.Hour), []int64{7}},
		{"1h1m ahead", false, time.Time{}, start.Add(-24 * time.Hour), start.Add(-time.Hour - time.Minute), nil},
		{"demo 1m after booking", true, demoNow.Add(-time.Minute), demoNow.Add(-time.Minute), demoNow, []int64{7}},
		{"demo 59s after booking", true, demoNow.Add(-59 * time.Second), demoNow.Add(-59 * time.Second), demoNow, nil},
		{"demo already reminded", true, demoNow.Add(-10 * time.Minute), demoNow.Add(-9 * time.Minute), demoNow, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cal, post := &calendar{lessons: []domain.Lesson{lesson(7, tc.booked, tc.reminded)}}, &postman{}
			tick(t, cal, post, tc.now, tc.demo)
			if !slices.Equal(post.sent, tc.want) || !slices.Equal(cal.marked, tc.want) {
				t.Fatalf("reminded %v, marked %v; want %v", post.sent, cal.marked, tc.want)
			}
		})
	}
}

func TestRemindersRetryFailed(t *testing.T) {
	reminded := start.Add(-48 * time.Hour)
	cal := &calendar{lessons: []domain.Lesson{lesson(1, time.Time{}, reminded), lesson(2, time.Time{}, reminded)}}
	tick(t, cal, &postman{refuse: 1}, start.Add(-30*time.Minute), false) // both moments passed
	if !slices.Equal(cal.marked, []int64{2}) {
		t.Fatalf("marked %v, want only the second lesson", cal.marked)
	}
}

func TestRemindersReportUnreadLessons(t *testing.T) {
	cal := &calendar{err: errors.New("disk is full")}
	r := worker.NewReminders(cal, &postman{}, clock.NewFake(start), false, slog.New(slog.DiscardHandler))
	if err := r.Tick(context.Background()); !errors.Is(err, cal.err) {
		t.Fatalf("err = %v, want the read failure", err)
	}
}
