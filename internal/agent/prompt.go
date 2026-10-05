package agent

import (
	_ "embed"
	"fmt"
	"strings"
	"text/template"
	"time"
)

//go:embed prompt/system.md
var systemSource string

//go:embed prompt/school.md
var school string

var systemPrompt = template.Must(template.New("system.md").Parse(systemSource))

// Russian names of days and months: time.Format knows only English ones.
var (
	weekdays      = [...]string{"воскресенье", "понедельник", "вторник", "среда", "четверг", "пятница", "суббота"}
	weekdaysShort = [...]string{"вс", "пн", "вт", "ср", "чт", "пт", "сб"}
	months        = [...]string{"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}
)

type promptData struct {
	School  string
	Now     string // «суббота, 3 октября 2026, 15:04»
	Days    string // «сб 03.10 (сегодня), вс 04.10 (завтра), пн 05.10, …»
	Profile string
}

// renderPrompt builds the system prompt for the moment now, which must already
// be in the school's time zone.
func renderPrompt(now time.Time, profile string) (string, error) {
	data := promptData{
		School:  strings.TrimSpace(school),
		Now:     fmt.Sprintf("%s %d, %s", day(now), now.Year(), now.Format("15:04")),
		Days:    week(now),
		Profile: profile,
	}
	var b strings.Builder
	if err := systemPrompt.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

// day names a date the way people say it: «суббота, 3 октября».
func day(t time.Time) string {
	return fmt.Sprintf("%s, %d %s", weekdays[t.Weekday()], t.Day(), months[t.Month()-1])
}

// week lists the dates of the week ahead, today first.
func week(now time.Time) string {
	days := make([]string, 7)
	for i := range days {
		d := now.AddDate(0, 0, i)
		days[i] = weekdaysShort[d.Weekday()] + " " + d.Format("02.01")
		switch i {
		case 0:
			days[i] += " (сегодня)"
		case 1:
			days[i] += " (завтра)"
		}
	}
	return strings.Join(days, ", ")
}
