// Package service runs the booking operations and the admins' access, and keeps their rules.
package service

import (
	"context"
	"errors"
	"time"

	"github.com/IvanTurko/physics-school-bot/internal/clock"
	"github.com/IvanTurko/physics-school-bot/internal/domain"
)

// Store keeps slots and bookings.
type Store interface {
	// InTx runs fn in one transaction; store calls made with fn's ctx join it.
	InTx(ctx context.Context, fn func(ctx context.Context) error) error
	FreeSlots(ctx context.Context, from, to time.Time, limit int) ([]domain.Slot, error)
	SlotAt(ctx context.Context, start time.Time) (domain.Slot, error)
	ActiveBooking(ctx context.Context, userID int64) (domain.BookingView, error)
	InsertBooking(ctx context.Context, b domain.Booking) (domain.BookingView, error)
	UpdateBooking(ctx context.Context, b domain.Booking) (domain.BookingView, error)
	Booking(ctx context.Context, id int64) (domain.BookingView, error)
}

// Notifier passes the booking changes on without making the caller wait.
type Notifier interface {
	BookingCreated(v domain.BookingView)
	BookingRescheduled(v domain.BookingView, oldStart time.Time)
	BookingCancelled(v domain.BookingView)
	BookingConfirmed(v domain.BookingView)
	BookingCancelledByAdmin(v domain.BookingView)
	BookingMarked(v domain.BookingView)
}

// Limits of the free slots offered at once.
const (
	Horizon   = 14 * 24 * time.Hour
	SlotLimit = 30
)

// BookInput is a booking request as the model passes it.
type BookInput struct {
	SlotTime string
	Grade    int
	Goal     string
	Phone    string
}

// Bookings runs the operations on trial lessons, the student's and the admin's.
type Bookings struct {
	store  Store
	clock  clock.Clock
	tz     *time.Location
	notify Notifier
}

// New creates Bookings that read slot times in tz.
func New(store Store, c clock.Clock, tz *time.Location, n Notifier) *Bookings {
	return &Bookings{store: store, clock: c, tz: tz, notify: n}
}

// FreeSlots returns the slots open for booking, earliest first.
func (b *Bookings) FreeSlots(ctx context.Context) ([]domain.Slot, error) {
	now := b.clock.Now()
	return b.store.FreeSlots(ctx, now.Add(domain.MinLead), now.Add(Horizon), SlotLimit)
}

// Book creates the user's booking, the only active one they may have.
func (b *Bookings) Book(ctx context.Context, userID int64, in BookInput) (domain.BookingView, error) {
	if err := domain.CheckGrade(in.Grade); err != nil {
		return domain.BookingView{}, err
	}
	goal, err := domain.ParseGoal(in.Goal)
	if err != nil {
		return domain.BookingView{}, err
	}
	phone, err := domain.NormalizePhone(in.Phone)
	if err != nil {
		return domain.BookingView{}, err
	}
	start, err := domain.ParseSlotTime(in.SlotTime, b.tz)
	if err != nil {
		return domain.BookingView{}, err
	}

	var v domain.BookingView
	err = b.store.InTx(ctx, func(ctx context.Context) error {
		_, err := b.store.ActiveBooking(ctx, userID)
		if err == nil {
			return domain.ErrAlreadyBooked
		}
		if !errors.Is(err, domain.ErrNoActiveBooking) {
			return err
		}
		slot, err := b.openSlot(ctx, start)
		if err != nil {
			return err
		}
		v, err = b.store.InsertBooking(ctx, domain.Booking{
			UserID: userID, SlotID: slot.ID, Grade: in.Grade, Goal: goal, Phone: phone, Status: domain.StatusNew,
		})
		return err
	})
	if err != nil {
		return domain.BookingView{}, err
	}
	b.notify.BookingCreated(v)
	return v, nil
}

// MyBooking returns the user's active booking: one whose lesson has not started.
func (b *Bookings) MyBooking(ctx context.Context, userID int64) (domain.BookingView, error) {
	return b.store.ActiveBooking(ctx, userID)
}

// Reschedule moves the user's active booking to the slot at slotTime.
func (b *Bookings) Reschedule(ctx context.Context, userID int64, slotTime string) (domain.BookingView, error) {
	start, err := domain.ParseSlotTime(slotTime, b.tz)
	if err != nil {
		return domain.BookingView{}, err
	}

	var old, v domain.BookingView
	err = b.store.InTx(ctx, func(ctx context.Context) error {
		var err error
		if old, err = b.store.ActiveBooking(ctx, userID); err != nil {
			return err
		}
		if start.Equal(old.Start) {
			return domain.ErrSameSlot
		}
		slot, err := b.openSlot(ctx, start)
		if err != nil {
			return err
		}
		moved := old.Booking
		moved.SlotID, moved.Status = slot.ID, domain.StatusNew // a new time waits for the admin again
		v, err = b.store.UpdateBooking(ctx, moved)
		return err
	})
	if err != nil {
		return domain.BookingView{}, err
	}
	b.notify.BookingRescheduled(v, old.Start)
	return v, nil
}

// Cancel withdraws the user's active booking and frees its slot.
func (b *Bookings) Cancel(ctx context.Context, userID int64) error {
	var v domain.BookingView
	err := b.store.InTx(ctx, func(ctx context.Context) error {
		active, err := b.store.ActiveBooking(ctx, userID)
		if err != nil {
			return err
		}
		active.Status = domain.StatusCancelled
		v, err = b.store.UpdateBooking(ctx, active.Booking)
		return err
	})
	if err != nil {
		return err
	}
	b.notify.BookingCancelled(v)
	return nil
}

// Confirm records the admin's confirmation of the booking.
func (b *Bookings) Confirm(ctx context.Context, id int64) error {
	v, err := b.decide(ctx, id, domain.StatusConfirmed)
	if err != nil {
		return err
	}
	b.notify.BookingConfirmed(v)
	return nil
}

// AdminCancel records the admin's cancel of the booking and frees its slot.
func (b *Bookings) AdminCancel(ctx context.Context, id int64) error {
	v, err := b.decide(ctx, id, domain.StatusCancelled)
	if err != nil {
		return err
	}
	b.notify.BookingCancelledByAdmin(v)
	return nil
}

// MarkDone records that the lesson took place.
func (b *Bookings) MarkDone(ctx context.Context, id int64) error {
	return b.mark(ctx, id, domain.StatusDone)
}

// MarkNoShow records that the student did not come.
func (b *Bookings) MarkNoShow(ctx context.Context, id int64) error {
	return b.mark(ctx, id, domain.StatusNoShow)
}

// mark records how the lesson went.
func (b *Bookings) mark(ctx context.Context, id int64, to domain.Status) error {
	v, err := b.decide(ctx, id, to)
	if err != nil {
		return err
	}
	b.notify.BookingMarked(v)
	return nil
}

// decide moves the booking to the status to, if the admin may now.
func (b *Bookings) decide(ctx context.Context, id int64, to domain.Status) (domain.BookingView, error) {
	var v domain.BookingView
	err := b.store.InTx(ctx, func(ctx context.Context) error {
		var err error
		if v, err = b.store.Booking(ctx, id); err != nil {
			return err
		}
		if err := domain.CheckMove(v, to, b.clock.Now()); err != nil {
			return err
		}
		v.Status = to
		v, err = b.store.UpdateBooking(ctx, v.Booking)
		return err
	})
	return v, err
}

// openSlot returns the slot at start if it exists and is far enough ahead.
func (b *Bookings) openSlot(ctx context.Context, start time.Time) (domain.Slot, error) {
	if start.Before(b.clock.Now().Add(domain.MinLead)) {
		return domain.Slot{}, domain.ErrTooSoon
	}
	return b.store.SlotAt(ctx, start)
}
