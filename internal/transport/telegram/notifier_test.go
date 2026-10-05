package telegram

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IvanTurko/physics-school-bot/internal/clock"
	"github.com/IvanTurko/physics-school-bot/internal/domain"
	tg "github.com/IvanTurko/physics-school-bot/internal/provider/telegram"
)

// sender records the messages, numbered from 1, and the edits; it holds each
// message until release is closed, and with err set fails every message at once.
type sender struct {
	release chan struct{}
	err     error
	sent    []tg.SendMessage
	edits   []string // "chat/message title buttons"
	refuse  int64    // the chat whose edits fail
}

func (s *sender) SendMessage(ctx context.Context, m tg.SendMessage) (tg.Message, error) {
	if s.err != nil {
		return tg.Message{}, s.err
	}
	select {
	case <-s.release:
	case <-ctx.Done():
		return tg.Message{}, ctx.Err()
	}
	s.sent = append(s.sent, m)
	return tg.Message{MessageID: int64(len(s.sent))}, nil
}

func (s *sender) EditMessageText(_ context.Context, m tg.EditMessageText) error {
	title, _, _ := strings.Cut(m.Text, "\n")
	s.edits = append(s.edits, fmt.Sprintf("%d/%d %s %s", m.ChatID, m.MessageID, title, describeButtons(m.ReplyMarkup)))
	if m.ChatID == s.refuse {
		return errors.New("message to edit not found")
	}
	return nil
}

// team is a fixed list of admins.
type team []int64

func (t team) Admins(context.Context) ([]int64, error) { return t, nil }

// diary records the messages saved to each user's history.
type diary map[int64][]domain.Message

func (d diary) Append(_ context.Context, userID int64, msgs ...domain.Message) error {
	d[userID] = append(d[userID], msgs...)
	return nil
}

// album keeps the cards in memory; Booking gives the test booking with this
// ID, in status, or err.
type album struct {
	cards  []domain.Card
	status domain.Status
	err    error
}

func (a *album) AddCard(_ context.Context, c domain.Card) error {
	a.cards = append(a.cards, c)
	return nil
}

func (a *album) Cards(_ context.Context, bookingID int64) ([]domain.Card, error) {
	return slices.DeleteFunc(slices.Clone(a.cards), func(c domain.Card) bool { return c.BookingID != bookingID }), nil
}

func (a *album) Booking(_ context.Context, id int64) (domain.BookingView, error) {
	v := booking
	v.ID, v.Status = id, a.status
	return v, a.err
}

// today is the notifier's time in the tests: a day before the booking's lesson.
var today = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

var msk = time.FixedZone("MSK", 3*60*60)

var booking = domain.BookingView{
	Booking: domain.Booking{ID: 7, UserID: 3, Grade: 9, Goal: domain.GoalOGE, Phone: "+79991234567", Status: domain.StatusNew},
	Start:   time.Date(2026, 10, 7, 16, 0, 0, 0, time.UTC),
	User:    domain.User{TelegramID: 1001},
}

// sentBy runs notify on a Notifier for admins 1 and 2 over s, letting every
// message through at once, with the cards in a, and returns the history.
func sentBy(s *sender, a *album, notify func(n *Notifier)) diary {
	s.release = make(chan struct{})
	close(s.release)
	saved := diary{}
	n := NewNotifier(context.Background(), s, saved, a, team{1, 2}, clock.NewFake(today), msk, quiet)
	notify(n)
	n.Shutdown(time.Now().Add(time.Second))
	return saved
}

func TestNotifierSendsCardToEveryAdmin(t *testing.T) {
	s := &sender{}
	sentBy(s, &album{}, func(n *Notifier) { n.BookingCreated(booking) })
	if len(s.sent) != 2 || s.sent[0].ChatID != 1 || s.sent[1].ChatID != 2 {
		t.Fatalf("sent %+v, want the card to both admins", s.sent)
	}
	if card := s.sent[0].Text; !strings.Contains(card, "07.10 19:00") || !strings.Contains(card, "+7 999 123-45-67") {
		t.Fatalf("card = %q, want the school's time and the phone", card)
	}
}

func TestNotifierDoesNotWaitForTelegram(t *testing.T) {
	n := NewNotifier(context.Background(), &sender{release: make(chan struct{})}, diary{}, &album{}, team{1}, clock.NewFake(today), time.UTC, quiet)

	pushed := make(chan struct{})
	go func() {
		for range notifyQueue + 2 {
			n.BookingCreated(booking)
		}
		close(pushed)
	}()
	select {
	case <-pushed:
	case <-time.After(time.Second):
		t.Fatal("a stuck Telegram blocks the caller")
	}
	n.Shutdown(time.Now())
}

func TestNotifierCardsCarryButtons(t *testing.T) {
	for _, tc := range []struct {
		name string
		send func(n *Notifier)
	}{
		{"new booking", func(n *Notifier) { n.BookingCreated(booking) }},
		{"rescheduled", func(n *Notifier) { n.BookingRescheduled(booking, booking.Start.Add(-time.Hour)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &sender{}
			if sentBy(s, &album{}, tc.send); len(s.sent) != 2 || s.sent[0].ReplyMarkup == nil {
				t.Fatalf("sent %+v, want a card with buttons", s.sent)
			}
		})
	}
}

func TestNotifierTellsStudent(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		send       func(n *Notifier)
	}{
		{"confirmed", "подтвердил вашу запись на пробный урок: 07.10 19:00", func(n *Notifier) { n.BookingConfirmed(booking) }},
		{"cancelled by admin", "отменил вашу запись на пробный урок 07.10 19:00", func(n *Notifier) { n.BookingCancelledByAdmin(booking) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &sender{}
			saved := sentBy(s, &album{}, tc.send)
			if len(s.sent) != 1 || s.sent[0].ChatID != 1001 || !strings.Contains(s.sent[0].Text, tc.want) {
				t.Fatalf("sent %+v, want %q to chat 1001 only", s.sent, tc.want)
			}
			if h := saved[3]; len(saved) != 1 || len(h) != 1 || h[0].Role != domain.RoleAssistant || h[0].Content != s.sent[0].Text {
				t.Fatalf("history %+v, want the sent text as the assistant's, for user 3 only", saved)
			}
		})
	}
}

func TestNotifierSavesOnlyWhatWasSent(t *testing.T) {
	saved, a := diary{}, &album{}
	n := NewNotifier(context.Background(), &sender{err: errors.New("bot blocked")}, saved, a, team{1}, clock.NewFake(today), time.UTC, quiet)
	n.BookingConfirmed(booking)
	n.BookingCreated(booking)
	n.Shutdown(time.Now().Add(time.Second))
	if len(saved) != 0 {
		t.Fatalf("history %+v, want nothing: the message was not sent", saved)
	}
	if len(a.cards) != 0 {
		t.Fatalf("cards %+v, want nothing: the card was not sent", a.cards)
	}
}

func TestNotifierKeepsAdminCardsOutOfHistory(t *testing.T) {
	if saved := sentBy(&sender{}, &album{}, func(n *Notifier) { n.BookingCreated(booking) }); len(saved) != 0 {
		t.Fatalf("history %+v, want nothing: cards go to the admins", saved)
	}
}

func TestNotifierRemembersCards(t *testing.T) {
	s, a := &sender{}, &album{}
	sentBy(s, a, func(n *Notifier) { n.BookingCreated(booking) })
	if want := []domain.Card{{BookingID: 7, ChatID: 1, MessageID: 1}, {BookingID: 7, ChatID: 2, MessageID: 2}}; !slices.Equal(a.cards, want) {
		t.Fatalf("cards = %v, want %v", a.cards, want)
	}
	if len(s.edits) != 0 {
		t.Fatalf("edits %q, want nothing: the cards are new", s.edits)
	}
}

func TestNotifierRedrawsCards(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		status     domain.Status
		send       func(n *Notifier)
	}{
		{"news to the student", "1/5 ✅ Подтверждена [Отменить=bk:cancel:7 💬 Диалог=dg:3]", domain.StatusConfirmed, func(n *Notifier) { n.BookingConfirmed(booking) }},
		{"card to the admins", "1/5 🆕 Ждёт подтверждения [Подтвердить=bk:confirm:7 Отменить=bk:cancel:7 💬 Диалог=dg:3]", domain.StatusNew, func(n *Notifier) { n.BookingRescheduled(booking, booking.Start.Add(-time.Hour)) }},
		{"lesson marked", "1/5 🎓 Урок проведён [💬 Диалог=dg:3]", domain.StatusDone, func(n *Notifier) { n.BookingMarked(booking) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &sender{}
			sentBy(s, &album{cards: []domain.Card{{BookingID: 7, ChatID: 1, MessageID: 5}}, status: tc.status}, tc.send)
			if want := []string{tc.want}; !slices.Equal(s.edits, want) {
				t.Fatalf("edits = %q, want %q", s.edits, want)
			}
		})
	}
}

func TestNotifierRedrawFailures(t *testing.T) {
	first, second := domain.Card{BookingID: 7, ChatID: 1, MessageID: 5}, domain.Card{BookingID: 7, ChatID: 2, MessageID: 6}
	for _, tc := range []struct {
		name   string
		cards  []domain.Card
		refuse int64
		err    error
		want   []string
	}{
		{"edit refused", []domain.Card{first, second}, 1, nil, []string{"1/5 ✅ Подтверждена [Отменить=bk:cancel:7 💬 Диалог=dg:3]", "2/6 ✅ Подтверждена [Отменить=bk:cancel:7 💬 Диалог=dg:3]"}},
		{"booking not read", []domain.Card{first}, 0, errors.New("disk is full"), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &sender{refuse: tc.refuse}
			sentBy(s, &album{cards: tc.cards, status: domain.StatusConfirmed, err: tc.err}, func(n *Notifier) { n.BookingConfirmed(booking) })
			if !slices.Equal(s.edits, tc.want) {
				t.Fatalf("edits = %q, want %q", s.edits, tc.want)
			}
		})
	}
}

func TestNotifierShowsCard(t *testing.T) {
	old := domain.Card{BookingID: 7, ChatID: 1, MessageID: 5}
	for _, tc := range []struct {
		name  string
		err   error
		sent  string // the chat, the title and the buttons
		cards []domain.Card
	}{
		{"booking read", nil, "2 ✅ Подтверждена [Отменить=bk:cancel:7 💬 Диалог=dg:3]", []domain.Card{old, {BookingID: 7, ChatID: 2, MessageID: 1}}},
		{"booking not read", errors.New("disk is full"), "", []domain.Card{old}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, a := &sender{}, &album{cards: []domain.Card{old}, status: domain.StatusConfirmed, err: tc.err}
			sentBy(s, a, func(n *Notifier) { n.ShowCard(2, 7) })
			var sent string
			for _, m := range s.sent {
				title, _, _ := strings.Cut(m.Text, "\n")
				sent += fmt.Sprintf("%d %s %s", m.ChatID, title, describeButtons(m.ReplyMarkup))
			}
			if sent != tc.sent {
				t.Fatalf("sent %q, want %q", sent, tc.sent)
			}
			if !slices.Equal(a.cards, tc.cards) {
				t.Fatalf("cards = %v, want %v", a.cards, tc.cards)
			}
			if len(s.edits) != 0 {
				t.Fatalf("edits %q, want nothing: the booking did not change", s.edits)
			}
		})
	}
}

func TestCurrentCard(t *testing.T) {
	n := &Notifier{clock: clock.NewFake(today), tz: msk}
	for _, tc := range []struct {
		name   string
		status domain.Status
		start  time.Time
		want   string // the title and the buttons
	}{
		{"new", domain.StatusNew, today.Add(time.Hour), "🆕 Ждёт подтверждения [Подтвердить=bk:confirm:7 Отменить=bk:cancel:7 💬 Диалог=dg:3]"},
		{"confirmed", domain.StatusConfirmed, today.Add(time.Hour), "✅ Подтверждена [Отменить=bk:cancel:7 💬 Диалог=dg:3]"},
		{"awaiting", domain.StatusNew, today.Add(-time.Hour), "⏳ Ждёт отметки [Провели=bk:done:7 Не пришёл=bk:noshow:7 💬 Диалог=dg:3]"},
		{"done", domain.StatusDone, today.Add(-time.Hour), "🎓 Урок проведён [💬 Диалог=dg:3]"},
		{"no_show", domain.StatusNoShow, today.Add(-time.Hour), "🚫 Не пришёл [💬 Диалог=dg:3]"},
		{"cancelled", domain.StatusCancelled, today.Add(time.Hour), "❌ Отменена [💬 Диалог=dg:3]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := domain.BookingView{Booking: domain.Booking{ID: 7, UserID: 3, Phone: "+79991234567", Status: tc.status}, Start: tc.start}
			text, buttons := n.current(v)
			title, _, _ := strings.Cut(text, "\n")
			if got := title + " " + describeButtons(buttons); got != tc.want {
				t.Fatalf("card = %s, want %s", got, tc.want)
			}
		})
	}
}
