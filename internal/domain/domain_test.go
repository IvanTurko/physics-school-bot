package domain_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/IvanTurko/physics-school-bot/internal/domain"
)

func TestNormalizePhone(t *testing.T) {
	for _, tc := range []struct{ name, in string }{
		{"leading 8", "8 999 123 45 67"},
		{"FormatPhone output", "+7 999 123-45-67"},
		{"no country code", "999 123 45 67"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := domain.NormalizePhone(tc.in); err != nil || got != "+79991234567" {
				t.Fatalf("got %q, %v; want +79991234567", got, err)
			}
		})
	}
}

func TestNormalizePhoneRejects(t *testing.T) {
	for _, tc := range []struct{ name, in string }{
		{"landline", "+7 495 123-45-67"},
		{"too short", "999 123 45 6"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := domain.NormalizePhone(tc.in); !errors.Is(err, domain.ErrInvalidPhone) {
				t.Fatalf("err = %v, want ErrInvalidPhone", err)
			}
		})
	}
}

func TestFormatPhone(t *testing.T) {
	if got := domain.FormatPhone("+79991234567"); got != "+7 999 123-45-67" {
		t.Fatalf("got %q, want +7 999 123-45-67", got)
	}
}

func TestFormatPhonesIn(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"spaced with 8", "звоним на 8 999 123 45 67.", "звоним на +7 999 123-45-67."},
		{"no separators", "89991234567", "+7 999 123-45-67"},
		{"brackets", "8 (999) 123-45-67", "+7 999 123-45-67"},
		{"already formatted", "+7 999 123-45-67", "+7 999 123-45-67"},
		{"no country code", "999 123 45 67", "+7 999 123-45-67"},
		{"landline", "8 495 123 45 67", "8 495 123 45 67"},
		{"one digit short", "8 999 123 45 6", "8 999 123 45 6"},
		{"digits before", "189991234567", "189991234567"},
		{"digits after", "99912345678", "99912345678"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.FormatPhonesIn(tc.in); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// FuzzFormatPhonesIn holds that formatting never panics and a second pass
// changes nothing.
func FuzzFormatPhonesIn(f *testing.F) {
	for _, s := range []string{"8 999 123 45 67", "8 (999) 123-45-67", "+7 999 123-45-67", "8 495 123 45 67", "8 999 123 45 6"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		once := domain.FormatPhonesIn(s)
		if twice := domain.FormatPhonesIn(once); twice != once {
			t.Fatalf("%q formats to %q, then to %q", s, once, twice)
		}
	})
}

func TestCheckGrade(t *testing.T) {
	for _, tc := range []struct {
		name    string
		grade   int
		refused bool
	}{
		{"lowest", 7, false},
		{"below lowest", 6, true},
		{"highest", 11, false},
		{"above highest", 12, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := domain.CheckGrade(tc.grade); errors.Is(err, domain.ErrInvalidGrade) != tc.refused {
				t.Fatalf("err = %v, want refused = %v", err, tc.refused)
			}
		})
	}
}

func TestParseGoal(t *testing.T) {
	if got, err := domain.ParseGoal("огэ"); err != nil || got != domain.GoalOGE {
		t.Errorf("огэ: got %q, %v", got, err)
	}
	if _, err := domain.ParseGoal("химия"); !errors.Is(err, domain.ErrInvalidGoal) {
		t.Errorf("химия: err = %v, want ErrInvalidGoal", err)
	}
}

func TestParseSlotTime(t *testing.T) {
	msk := time.FixedZone("MSK", 3*60*60)
	want := time.Date(2026, 10, 7, 16, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ name, in string }{
		{"slot layout", "2026-10-07 19:00"},
		{"ISO without offset", "2026-10-07T19:00:00"},
		{"Russian date", "07.10.2026 19:00"},
		{"with offset", "2026-10-07T16:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := domain.ParseSlotTime(tc.in, msk); err != nil || !got.Equal(want) {
				t.Fatalf("got %v, %v; want %v", got, err, want)
			}
		})
	}
	if _, err := domain.ParseSlotTime("завтра вечером", msk); !errors.Is(err, domain.ErrInvalidSlotTime) {
		t.Errorf("err = %v, want ErrInvalidSlotTime", err)
	}
}

func TestParseSlotDay(t *testing.T) {
	msk := time.FixedZone("MSK", 3*60*60)
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, msk)
	at := func(year int, month time.Month, day, hour int) time.Time {
		return time.Date(year, month, day, hour, 0, 0, 0, msk)
	}
	for _, tc := range []struct {
		name, in string
		now      time.Time
		want     []time.Time
		err      error
	}{
		{"times of a day", "07.10 18:00 19:00", now, []time.Time{at(2026, 10, 7, 18), at(2026, 10, 7, 19)}, nil},
		{"date over half a year behind", "05.01 18:00", at(2026, 12, 30, 12), []time.Time{at(2027, 1, 5, 18)}, nil},
		{"time now", "05.10 12:00", now, nil, domain.ErrPastSlot},
		{"no times", "07.10", now, nil, domain.ErrInvalidSlotTime},
		{"29 February of a common year", "29.02 18:00", now, nil, domain.ErrInvalidSlotTime},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domain.ParseSlotDay(tc.in, tc.now, msk)
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
			if !slices.EqualFunc(got, tc.want, time.Time.Equal) {
				t.Fatalf("starts = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCheckMove(t *testing.T) {
	start := time.Date(2026, 10, 7, 19, 0, 0, 0, time.UTC)
	before, after := start.Add(-time.Hour), start.Add(time.Hour)
	for _, tc := range []struct {
		name     string
		from, to domain.Status
		now      time.Time
		want     error
	}{
		{"new to confirmed, before the start", domain.StatusNew, domain.StatusConfirmed, before, nil},
		{"new to cancelled, before the start", domain.StatusNew, domain.StatusCancelled, before, nil},
		{"confirmed to cancelled, before the start", domain.StatusConfirmed, domain.StatusCancelled, before, nil},
		{"new to done, after the start", domain.StatusNew, domain.StatusDone, after, nil},
		{"new to no_show, after the start", domain.StatusNew, domain.StatusNoShow, after, nil},
		{"confirmed to done, after the start", domain.StatusConfirmed, domain.StatusDone, after, nil},
		{"confirmed to no_show, after the start", domain.StatusConfirmed, domain.StatusNoShow, after, nil},
		{"new to confirmed, at the start", domain.StatusNew, domain.StatusConfirmed, start, domain.ErrAlreadyStarted},
		{"cancelled to cancelled, after the start", domain.StatusCancelled, domain.StatusCancelled, after, domain.ErrAlreadyHandled},
		{"new to done, before the start", domain.StatusNew, domain.StatusDone, before, domain.ErrNotStarted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := domain.BookingView{Booking: domain.Booking{Status: tc.from}, Start: start}
			if err := domain.CheckMove(v, tc.to, tc.now); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestFilterOf(t *testing.T) {
	start := time.Date(2026, 10, 7, 19, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		status domain.Status
		now    time.Time
		want   domain.Filter
	}{
		{"new ahead", domain.StatusNew, start.Add(-time.Hour), domain.FilterNew},
		{"new at the start", domain.StatusNew, start, domain.FilterAwaiting},
		{"confirmed after the start", domain.StatusConfirmed, start.Add(time.Hour), domain.FilterAwaiting},
		{"done", domain.StatusDone, start.Add(time.Hour), domain.FilterDone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := domain.BookingView{Booking: domain.Booking{Status: tc.status}, Start: start}
			if got := domain.FilterOf(v, tc.now); got != tc.want {
				t.Fatalf("filter = %s, want %s", got, tc.want)
			}
		})
	}
}
