package agent

import (
	"strings"
	"testing"
	"time"
)

func TestRenderPrompt(t *testing.T) {
	msk := time.FixedZone("MSK", 3*60*60)
	got, err := renderPrompt(time.Date(2026, 10, 3, 15, 4, 0, 0, msk), "Пока ничего не известно.")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"суббота, 3 октября 2026, 15:04",
		"сб 03.10 (сегодня), вс 04.10 (завтра), пн 05.10",
		"пт 09.10.",
		"23 500 ₽",
		"Пока ничего не известно.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if !strings.HasPrefix(got, "Ты — ") {
		t.Errorf("prompt starts with %.40q, want the role with the template comment stripped", got)
	}
}
