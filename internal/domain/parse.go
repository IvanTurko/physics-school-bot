package domain

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// NormalizePhone reads a Russian mobile number written any usual way and
// returns it as +79991234567.
func NormalizePhone(s string) (string, error) {
	digits := strings.Map(func(r rune) rune {
		if r < '0' || r > '9' {
			return -1
		}
		return r
	}, s)
	if len(digits) == 11 && (digits[0] == '7' || digits[0] == '8') {
		digits = digits[1:]
	}
	if len(digits) != 10 || digits[0] != '9' {
		return "", fmt.Errorf("%w %q (want a Russian mobile number)", ErrInvalidPhone, s)
	}
	return "+7" + digits, nil
}

// FormatPhone writes a normalized number as +7 999 123-45-67.
func FormatPhone(p string) string {
	return fmt.Sprintf("+7 %s %s-%s-%s", p[2:5], p[5:8], p[8:10], p[10:])
}

// phoneLike matches a Russian mobile number inside text, never a part of a
// longer run of digits.
var phoneLike = regexp.MustCompile(`(?:\+7[\s\-(]*|\b[78][\s\-(]*|\b)9\d{2}[\s\-)]*\d{3}[\s\-]*\d{2}[\s\-]*\d{2}\b`)

// FormatPhonesIn rewrites every Russian mobile number in text as +7 999 123-45-67.
func FormatPhonesIn(text string) string {
	return phoneLike.ReplaceAllStringFunc(text, func(s string) string {
		p, _ := NormalizePhone(s) // never fails: the pattern admits only valid numbers
		return FormatPhone(p)
	})
}

// SlotTimeLayout is how the bot writes a slot's time, in the school's zone.
const SlotTimeLayout = "2006-01-02 15:04"

// localLayouts are the ways a model writes a slot's time without a zone.
var localLayouts = []string{SlotTimeLayout, "2006-01-02T15:04:05", "02.01.2006 15:04"}

// ParseSlotTime reads a slot's time; one without a zone offset is in tz.
func ParseSlotTime(s string, tz *time.Location) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	for _, layout := range localLayouts {
		if t, err := time.ParseInLocation(layout, s, tz); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("%w %q (want %s)", ErrInvalidSlotTime, s, SlotTimeLayout)
}

// ParseSlotDay reads a day of slots, like 07.10 18:00 19:00, in tz; a date over half a year
// behind now is next year's, and a time not after now gives ErrPastSlot.
func ParseSlotDay(s string, now time.Time, tz *time.Location) ([]time.Time, error) {
	fields := strings.Fields(s)
	if len(fields) < 2 {
		return nil, ErrInvalidSlotTime
	}
	year := now.Year()
	// No error check: the same date fails below, parsed with its year.
	if date, _ := time.Parse("02.01", fields[0]); time.Date(year, date.Month(), date.Day(), 0, 0, 0, 0, tz).Before(now.AddDate(0, -6, 0)) {
		year++
	}
	starts := make([]time.Time, 0, len(fields)-1)
	for _, hm := range fields[1:] {
		t, err := time.ParseInLocation("02.01.2006 15:04", fmt.Sprintf("%s.%d %s", fields[0], year, hm), tz)
		if err != nil {
			return nil, ErrInvalidSlotTime
		}
		if !t.After(now) {
			return nil, ErrPastSlot
		}
		starts = append(starts, t)
	}
	return starts, nil
}
