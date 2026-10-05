package service_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/IvanTurko/physics-school-bot/internal/clock"
	"github.com/IvanTurko/physics-school-bot/internal/domain"
	"github.com/IvanTurko/physics-school-bot/internal/service"
	"github.com/IvanTurko/physics-school-bot/internal/storage/sqlite"
	"github.com/IvanTurko/physics-school-bot/internal/testkit"
)

var (
	ctx = context.Background()
	msk = time.FixedZone("MSK", 3*60*60)
	now = time.Date(2026, 10, 5, 12, 0, 0, 0, msk)
)

// events records the notifications as «created 07.10 19:00».
type events struct{ list []string }

func (e *events) addf(format string, args ...any) {
	e.list = append(e.list, fmt.Sprintf(format, args...))
}

func (e *events) BookingCreated(v domain.BookingView) { e.addf("created %s", at(v.Start)) }

func (e *events) BookingRescheduled(v domain.BookingView, old time.Time) {
	e.addf("rescheduled %s → %s", at(old), at(v.Start))
}

func (e *events) BookingCancelled(v domain.BookingView) { e.addf("cancelled %s", at(v.Start)) }

func (e *events) BookingConfirmed(v domain.BookingView) { e.addf("confirmed %s", at(v.Start)) }

func (e *events) BookingCancelledByAdmin(v domain.BookingView) {
	e.addf("cancelled by admin %s", at(v.Start))
}

func (e *events) BookingMarked(v domain.BookingView) { e.addf("marked %s", at(v.Start)) }

func at(t time.Time) string { return t.In(msk).Format("02.01 15:04") }

type fixture struct {
	svc    *service.Bookings
	store  *sqlite.Store
	clock  *clock.Fake
	events *events
}

func setup(t *testing.T) fixture {
	c := clock.NewFake(now)
	f := fixture{store: sqlite.New(testkit.NewDB(t), c), clock: c, events: &events{}}
	f.svc = service.New(f.store, c, msk, f.events)
	return f
}

// slots adds slots at the given offsets from now.
func (f fixture) slots(t *testing.T, after ...time.Duration) {
	starts := make([]time.Time, len(after))
	for i, d := range after {
		starts[i] = now.Add(d)
	}
	if _, err := f.store.AddSlots(ctx, starts); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) user(t *testing.T, tgID int64) int64 {
	u, err := f.store.EnsureUser(ctx, tgID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func slotTime(after time.Duration) string { return now.Add(after).Format(domain.SlotTimeLayout) }

// input books the slot that far after now.
func input(after time.Duration) service.BookInput {
	return service.BookInput{SlotTime: slotTime(after), Grade: 9, Goal: "ОГЭ", Phone: "8 999 123 45 67"}
}

func (f fixture) freeSlots(t *testing.T) []string {
	slots, err := f.svc.FreeSlots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(slots))
	for i, s := range slots {
		got[i] = at(s.Start)
	}
	return got
}

func TestFreeSlotsOffersOpenSlotsInOrder(t *testing.T) {
	f := setup(t)
	f.slots(t, 5*time.Hour, 4*time.Hour, time.Hour, 2*time.Hour, 3*time.Hour, 15*24*time.Hour)
	if _, err := f.svc.Book(ctx, f.user(t, 1), input(3*time.Hour)); err != nil {
		t.Fatal(err)
	}

	want := []string{"05.10 14:00", "05.10 16:00", "05.10 17:00"}
	if got := f.freeSlots(t); !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v: from two hours ahead, free, within the horizon, in order", got, want)
	}
}

func TestFreeSlotsLimit(t *testing.T) {
	f := setup(t)
	after := make([]time.Duration, service.SlotLimit+1)
	for i := range after {
		after[i] = time.Duration(3+i) * time.Hour
	}
	f.slots(t, after...)

	if got := f.freeSlots(t); len(got) != service.SlotLimit {
		t.Fatalf("got %d slots, want %d", len(got), service.SlotLimit)
	}
}

func TestBookSlotExactlyTwoHoursAhead(t *testing.T) {
	f := setup(t)
	f.slots(t, domain.MinLead)
	if _, err := f.svc.Book(ctx, f.user(t, 1), input(domain.MinLead)); err != nil {
		t.Fatal(err)
	}
}

func TestBookSavesNormalizedBooking(t *testing.T) {
	f := setup(t)
	f.slots(t, 31*time.Hour)
	in := input(31 * time.Hour)
	in.Goal = "огэ"

	v, err := f.svc.Book(ctx, f.user(t, 1), in)
	if err != nil {
		t.Fatal(err)
	}
	if v.Phone != "+79991234567" || v.Goal != domain.GoalOGE || at(v.Start) != "06.10 19:00" {
		t.Errorf("booking = %+v, want +79991234567, ОГЭ, 06.10 19:00", v)
	}
	if want := []string{"created 06.10 19:00"}; !slices.Equal(f.events.list, want) {
		t.Errorf("events = %v, want %v", f.events.list, want)
	}
}

func TestBookRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*service.BookInput)
		want error
	}{
		{"grade out of range", func(in *service.BookInput) { in.Grade = 6 }, domain.ErrInvalidGrade},
		{"unknown goal", func(in *service.BookInput) { in.Goal = "химия" }, domain.ErrInvalidGoal},
		{"invalid phone", func(in *service.BookInput) { in.Phone = "123" }, domain.ErrInvalidPhone},
		{"unreadable time", func(in *service.BookInput) { in.SlotTime = "завтра" }, domain.ErrInvalidSlotTime},
		{"no slot at that time", func(in *service.BookInput) { in.SlotTime = slotTime(4 * time.Hour) }, domain.ErrSlotNotFound},
		{"slot within two hours", func(in *service.BookInput) { in.SlotTime = slotTime(time.Hour) }, domain.ErrTooSoon},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			f.slots(t, time.Hour, 3*time.Hour)
			in := input(3 * time.Hour)
			tc.edit(&in)

			if _, err := f.svc.Book(ctx, f.user(t, 1), in); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if len(f.events.list) != 0 {
				t.Fatalf("events = %v, want none after a refusal", f.events.list)
			}
		})
	}
}

func TestBookRejectsSecondActiveBooking(t *testing.T) {
	f := setup(t)
	f.slots(t, 3*time.Hour, 4*time.Hour)
	user := f.user(t, 1)
	if _, err := f.svc.Book(ctx, user, input(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Book(ctx, user, input(4*time.Hour)); !errors.Is(err, domain.ErrAlreadyBooked) {
		t.Fatalf("err = %v, want ErrAlreadyBooked", err)
	}
}

func TestBookRejectsTakenSlot(t *testing.T) {
	f := setup(t)
	f.slots(t, 3*time.Hour)
	if _, err := f.svc.Book(ctx, f.user(t, 1), input(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Book(ctx, f.user(t, 2), input(3*time.Hour)); !errors.Is(err, domain.ErrSlotTaken) {
		t.Fatalf("err = %v, want ErrSlotTaken", err)
	}
}

// meeting holds each ActiveBooking call until a second one arrives or 100 ms
// pass, so two bookings outside a transaction both see no active booking.
type meeting struct {
	*sqlite.Store

	mu      sync.Mutex
	callers int
	met     chan struct{}
}

func (m *meeting) ActiveBooking(ctx context.Context, userID int64) (domain.BookingView, error) {
	v, err := m.Store.ActiveBooking(ctx, userID)
	m.mu.Lock()
	if m.callers++; m.callers == 2 {
		close(m.met)
	}
	m.mu.Unlock()
	select {
	case <-m.met:
	case <-time.After(100 * time.Millisecond):
	}
	return v, err
}

// TestBookRaceForOneUser also fails without immediate transactions: the second
// write gets SQLITE_BUSY instead of ErrAlreadyBooked.
func TestBookRaceForOneUser(t *testing.T) {
	f := setup(t)
	f.slots(t, 3*time.Hour, 4*time.Hour)
	user := f.user(t, 1)
	svc := service.New(&meeting{Store: f.store, met: make(chan struct{})}, f.clock, msk, f.events)

	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, d := range []time.Duration{3 * time.Hour, 4 * time.Hour} {
		wg.Go(func() { _, errs[i] = svc.Book(ctx, user, input(d)) })
	}
	wg.Wait()

	if ok, booked := countErrs(errs, nil), countErrs(errs, domain.ErrAlreadyBooked); ok != 1 || booked != 1 {
		t.Fatalf("errs = %v, want one booking and one ErrAlreadyBooked", errs)
	}
}

func countErrs(errs []error, target error) int {
	n := 0
	for _, err := range errs {
		if errors.Is(err, target) {
			n++
		}
	}
	return n
}

func TestMyBookingIsPerUser(t *testing.T) {
	f := setup(t)
	f.slots(t, 3*time.Hour)
	if _, err := f.svc.Book(ctx, f.user(t, 1), input(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.MyBooking(ctx, f.user(t, 2)); !errors.Is(err, domain.ErrNoActiveBooking) {
		t.Fatalf("err = %v, want ErrNoActiveBooking", err)
	}
}

func TestBookAgainAfterLessonPassed(t *testing.T) {
	f := setup(t)
	f.slots(t, 3*time.Hour, 30*time.Hour)
	user := f.user(t, 1)
	if _, err := f.svc.Book(ctx, user, input(3*time.Hour)); err != nil {
		t.Fatal(err)
	}

	f.clock.Advance(4 * time.Hour)
	if _, err := f.svc.Book(ctx, user, input(30*time.Hour)); err != nil {
		t.Fatalf("err = %v, want a new booking once the lesson has passed", err)
	}
}

func TestRescheduleMovesBooking(t *testing.T) {
	f := setup(t)
	f.slots(t, 3*time.Hour, 30*time.Hour)
	user := f.user(t, 1)
	if _, err := f.svc.Book(ctx, user, input(3*time.Hour)); err != nil {
		t.Fatal(err)
	}

	v, err := f.svc.Reschedule(ctx, user, slotTime(30*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if at(v.Start) != "06.10 18:00" {
		t.Errorf("start = %s, want 06.10 18:00", at(v.Start))
	}
	if got := f.freeSlots(t); !slices.Equal(got, []string{"05.10 15:00"}) {
		t.Errorf("free = %v, want the old slot back", got)
	}
	if want := []string{"created 05.10 15:00", "rescheduled 05.10 15:00 → 06.10 18:00"}; !slices.Equal(f.events.list, want) {
		t.Errorf("events = %v, want %v", f.events.list, want)
	}
}

func TestRescheduleRefusals(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after time.Duration
		want  error
	}{
		{"same slot", 3 * time.Hour, domain.ErrSameSlot},
		{"slot within two hours", 3*time.Hour + 30*time.Minute, domain.ErrTooSoon},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			f.slots(t, 3*time.Hour, 3*time.Hour+30*time.Minute)
			user := f.user(t, 1)
			if _, err := f.svc.Book(ctx, user, input(3*time.Hour)); err != nil {
				t.Fatal(err)
			}
			f.clock.Advance(2 * time.Hour) // an hour before the lesson

			if _, err := f.svc.Reschedule(ctx, user, slotTime(tc.after)); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestCancelFreesSlot(t *testing.T) {
	f := setup(t)
	f.slots(t, 3*time.Hour)
	user := f.user(t, 1)
	if _, err := f.svc.Book(ctx, user, input(3*time.Hour)); err != nil {
		t.Fatal(err)
	}

	if err := f.svc.Cancel(ctx, user); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.MyBooking(ctx, user); !errors.Is(err, domain.ErrNoActiveBooking) {
		t.Errorf("err = %v, want ErrNoActiveBooking", err)
	}
	if _, err := f.svc.Book(ctx, f.user(t, 2), input(3*time.Hour)); err != nil {
		t.Errorf("err = %v, want the freed slot booked again", err)
	}
	if want := "cancelled 05.10 15:00"; !slices.Contains(f.events.list, want) {
		t.Errorf("events = %v, want %q", f.events.list, want)
	}
}

func TestCancelWithoutBooking(t *testing.T) {
	f := setup(t)
	if err := f.svc.Cancel(ctx, f.user(t, 1)); !errors.Is(err, domain.ErrNoActiveBooking) {
		t.Fatalf("err = %v, want ErrNoActiveBooking", err)
	}
}

// booked books a slot 3 hours after now for a new user and returns the booking.
func (f fixture) booked(t *testing.T) domain.BookingView {
	f.slots(t, 3*time.Hour)
	v, err := f.svc.Book(ctx, f.user(t, 1), input(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (f fixture) status(t *testing.T, id int64) domain.Status {
	v, err := f.store.Booking(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return v.Status
}

func TestConfirmBooking(t *testing.T) {
	f := setup(t)
	v := f.booked(t)

	if err := f.svc.Confirm(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t, v.ID); got != domain.StatusConfirmed {
		t.Errorf("status = %s, want confirmed", got)
	}
	if want := []string{"created 05.10 15:00", "confirmed 05.10 15:00"}; !slices.Equal(f.events.list, want) {
		t.Errorf("events = %v, want %v", f.events.list, want)
	}
}

func TestAdminCancelConfirmedBooking(t *testing.T) {
	f := setup(t)
	v := f.booked(t)
	if err := f.svc.Confirm(ctx, v.ID); err != nil {
		t.Fatal(err)
	}

	if err := f.svc.AdminCancel(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t, v.ID); got != domain.StatusCancelled {
		t.Errorf("status = %s, want cancelled", got)
	}
	if last := f.events.list[len(f.events.list)-1]; last != "cancelled by admin 05.10 15:00" {
		t.Errorf("last event = %q, want the cancel by the admin", last)
	}
}

func TestMarkLesson(t *testing.T) {
	for _, tc := range []struct {
		name string
		mark func(f fixture, id int64) error
		want domain.Status
	}{
		{"done", func(f fixture, id int64) error { return f.svc.MarkDone(ctx, id) }, domain.StatusDone},
		{"no show", func(f fixture, id int64) error { return f.svc.MarkNoShow(ctx, id) }, domain.StatusNoShow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			v := f.booked(t)
			f.clock.Advance(3 * time.Hour)

			if err := tc.mark(f, v.ID); err != nil {
				t.Fatal(err)
			}
			if got := f.status(t, v.ID); got != tc.want {
				t.Errorf("status = %s, want %s", got, tc.want)
			}
			if last := f.events.list[len(f.events.list)-1]; last != "marked 05.10 15:00" {
				t.Errorf("last event = %q, want the mark", last)
			}
		})
	}
}

func TestDecisionRefusals(t *testing.T) {
	confirm := func(f fixture, id int64) error { return f.svc.Confirm(ctx, id) }
	cancel := func(f fixture, id int64) error { return f.svc.AdminCancel(ctx, id) }
	lessonStarts := func(f fixture, _ int64) error { f.clock.Advance(3 * time.Hour); return nil }
	for _, tc := range []struct {
		name           string
		before, decide func(f fixture, id int64) error
		want           error
	}{
		{"cancel a cancelled booking", cancel, cancel, domain.ErrAlreadyHandled},
		{"confirm as the lesson starts", lessonStarts, confirm, domain.ErrAlreadyStarted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			v := f.booked(t)
			if err := tc.before(f, v.ID); err != nil {
				t.Fatal(err)
			}
			events := len(f.events.list)

			if err := tc.decide(f, v.ID); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if len(f.events.list) != events {
				t.Errorf("events = %v, want none for a refusal", f.events.list[events:])
			}
		})
	}
}

func TestRescheduleAsksConfirmationAgain(t *testing.T) {
	f := setup(t)
	v := f.booked(t)
	f.slots(t, 30*time.Hour)
	if err := f.svc.Confirm(ctx, v.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := f.svc.Reschedule(ctx, v.UserID, slotTime(30*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t, v.ID); got != domain.StatusNew {
		t.Fatalf("status = %s, want new", got)
	}
}
