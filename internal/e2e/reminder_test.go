package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/IvanTurko/physics-school-bot/internal/testkit"
)

func TestRemindsADayBefore(t *testing.T) {
	h := start(t)
	bookSlot(h)
	h.TG.WaitMessage(user) // «Готово!»: the booking is saved

	h.Clock.Advance(31*time.Hour - time.Minute) // a minute before 07.10 19:00, when the reminder is due
	h.Remind()
	if sent := h.TG.Sent(user); len(sent) != 1 {
		t.Fatalf("sent %q a minute early, want only «Готово!»", sent)
	}
	h.Clock.Advance(time.Minute)
	h.Remind()
	if sent := h.TG.Sent(user); len(sent) != 2 || !strings.Contains(sent[1], "08.10 19:00") {
		t.Errorf("sent %q, want «Готово!» and then the reminder of 08.10 19:00", sent)
	}
}

func TestDemoRemindsAMinuteAfterBooking(t *testing.T) {
	h := testkit.Start(t, testkit.Options{Now: now, Demo: true})
	h.AddSlot(slot)
	bookSlot(h)
	h.TG.WaitMessage(user) // «Готово!»: the booking is saved

	h.Clock.Advance(time.Minute)
	h.Remind()
	if sent := h.TG.Sent(user); len(sent) != 2 {
		t.Errorf("sent %q, want «Готово!» and then the reminder", sent)
	}
}
