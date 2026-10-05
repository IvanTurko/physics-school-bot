package domain

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Status is where a booking is in its life.
type Status string

const (
	StatusNew       Status = "new" // waits for the admin
	StatusConfirmed Status = "confirmed"
	StatusCancelled Status = "cancelled"
	StatusDone      Status = "done"
	StatusNoShow    Status = "no_show"
)

// next lists the statuses the admin may move a booking to from each status.
var next = map[Status][]Status{
	StatusNew:       {StatusConfirmed, StatusCancelled, StatusDone, StatusNoShow},
	StatusConfirmed: {StatusCancelled, StatusDone, StatusNoShow},
}

// isOutcome reports whether s says how the lesson went.
func (s Status) isOutcome() bool { return s == StatusDone || s == StatusNoShow }

// Goal is what the student studies physics for.
type Goal string

const (
	GoalOGE      Goal = "ОГЭ"
	GoalEGE      Goal = "ЕГЭ"
	GoalGrades   Goal = "успеваемость"
	GoalOlympiad Goal = "олимпиада"
)

// Goals lists every goal the school takes.
var Goals = []Goal{GoalOGE, GoalEGE, GoalGrades, GoalOlympiad}

// MinLead is how long before its start a slot can still be booked.
const MinLead = 2 * time.Hour

// Slot is a time a trial lesson can start.
type Slot struct {
	ID    int64
	Start time.Time
}

// SlotView is a slot with the booking that holds it; a free slot has BookingID 0.
type SlotView struct {
	Slot

	BookingID int64
	Name      string // who booked it
}

// Booking is a request for a trial lesson.
type Booking struct {
	ID     int64
	UserID int64
	SlotID int64
	Grade  int
	Goal   Goal
	Phone  string // +79991234567
	Status Status
}

// BookingView is a booking with its slot's time and the user's names.
type BookingView struct {
	Booking

	Start time.Time
	User  User
}

// Lesson is an active booking with the times its reminders count from.
type Lesson struct {
	BookingView

	Booked   time.Time // when the booking was made
	Reminded time.Time // reminders due by then are sent or skipped
}

// Lead is a booking with when it was made and its version.
type Lead struct {
	BookingView

	Booked  time.Time
	Version int
}

// started reports whether the booking's lesson has started by now.
func (v BookingView) started(now time.Time) bool { return !v.Start.After(now) }

// CheckMove checks that the admin may move v to status to at now; how the
// lesson went is set only after it starts, any other move only before.
func CheckMove(v BookingView, to Status, now time.Time) error {
	started := v.started(now)
	switch {
	case !slices.Contains(next[v.Status], to):
		return ErrAlreadyHandled
	case started && !to.isOutcome():
		return ErrAlreadyStarted
	case !started && to.isOutcome():
		return ErrNotStarted
	}
	return nil
}

// Filter is a list of bookings the admin sees: a status, or awaiting for a new
// or confirmed booking whose lesson has started.
type Filter string

const (
	FilterNew       = Filter(StatusNew)
	FilterConfirmed = Filter(StatusConfirmed)
	FilterAwaiting  = Filter("awaiting")
	FilterDone      = Filter(StatusDone)
	FilterNoShow    = Filter(StatusNoShow)
	FilterCancelled = Filter(StatusCancelled)
)

// FilterOf returns the list booking v is in at now.
func FilterOf(v BookingView, now time.Time) Filter {
	if (v.Status == StatusNew || v.Status == StatusConfirmed) && v.started(now) {
		return FilterAwaiting
	}
	return Filter(v.Status)
}

// Card is where a booking card went to an admin; its text is drawn from the booking.
type Card struct {
	BookingID int64
	ChatID    int64
	MessageID int64
}

// Profile is what the bot knows about a user; nil means nothing.
type Profile struct {
	Active    *BookingView
	Last      *Booking // the latest booking of any status: grade, goal and phone
	TrialHeld bool     // whether an admin marked one of the user's lessons held
}

// Refusals of the booking rules.
var (
	ErrInvalidPhone    = errors.New("invalid phone")
	ErrInvalidGrade    = errors.New("invalid grade")
	ErrInvalidGoal     = errors.New("invalid goal")
	ErrInvalidSlotTime = errors.New("invalid slot time")
	ErrPastSlot        = errors.New("past slot")
	ErrSlotNotFound    = errors.New("unknown slot")
	ErrSlotTaken       = errors.New("slot already taken")
	ErrTooSoon         = errors.New("slot starts too soon")
	ErrSameSlot        = errors.New("booking already at this slot")
	ErrAlreadyBooked   = errors.New("active booking exists")
	ErrNoActiveBooking = errors.New("missing active booking")
	ErrAlreadyHandled  = errors.New("booking already handled")
	ErrAlreadyStarted  = errors.New("lesson already started")
	ErrNotStarted      = errors.New("lesson not started yet")
)

// CheckGrade accepts the grades the school teaches.
func CheckGrade(grade int) error {
	if grade < 7 || grade > 11 {
		return fmt.Errorf("%w %d (want 7 to 11)", ErrInvalidGrade, grade)
	}
	return nil
}

// ParseGoal finds the goal named s in any letter case.
func ParseGoal(s string) (Goal, error) {
	for _, g := range Goals {
		if strings.EqualFold(s, string(g)) {
			return g, nil
		}
	}
	return "", fmt.Errorf("%w %q (want ОГЭ, ЕГЭ, успеваемость or олимпиада)", ErrInvalidGoal, s)
}
