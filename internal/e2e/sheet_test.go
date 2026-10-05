package e2e

import (
	"slices"
	"testing"
)

func TestSheetShowsBooking(t *testing.T) {
	h := start(t)
	bookSlot(h)
	h.TG.WaitMessage(admin) // the card: the booking is saved

	h.Sync()
	want := []string{"1", "06.10 12:00", "Ученик", "", "+7 999 123-45-67", "9", "ОГЭ", "08.10 19:00", "🆕 Ждёт подтверждения"}
	if got := h.Sheet.Row(2); !slices.Equal(got, want) {
		t.Errorf("row 2 = %q, want %q", got, want)
	}
}
