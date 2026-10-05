package testkit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path"
	"slices"
	"strconv"
	"testing"

	tg "github.com/IvanTurko/physics-school-bot/internal/provider/telegram"
)

// FakeTelegram is a Bot API server where the test plays the users.
type FakeTelegram struct {
	*state

	t        testing.TB
	srv      *httptest.Server
	updates  []tg.Update
	sent     map[int64][]message
	messages int64           // sent to all chats so far: the last message ID
	read     map[int64]int   // per chat, how many sent messages WaitMessage returned
	answered map[string]bool // button presses the bot answered, by ID
	polling  int             // getUpdates calls in flight
}

// message is a message the bot sent, as its latest edit left it.
type message struct {
	id      int64
	text    string
	buttons *tg.InlineKeyboardMarkup
}

func newFakeTelegram(t testing.TB) *FakeTelegram {
	f := &FakeTelegram{
		state: newState(), t: t,
		sent: make(map[int64][]message), read: make(map[int64]int), answered: make(map[string]bool),
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// UserSays sends text from the user's private chat.
func (f *FakeTelegram) UserSays(userID int64, text string) {
	f.update(func() {
		f.updates = append(f.updates, tg.Update{
			UpdateID: int64(len(f.updates)) + 1,
			Message: &tg.Message{
				From: &tg.User{ID: userID, FirstName: "Ученик"},
				Chat: tg.Chat{ID: userID, Type: tg.ChatPrivate},
				Text: text,
			},
		})
	})
}

// WaitMessage returns the next message to the chat that the test has not read.
func (f *FakeTelegram) WaitMessage(chatID int64) string {
	f.t.Helper()
	var text string
	if !f.await(context.Background(), func() bool {
		if f.read[chatID] == len(f.sent[chatID]) {
			return false
		}
		text = f.sent[chatID][f.read[chatID]].text
		f.read[chatID]++
		return true
	}) {
		f.t.Fatalf("no message to chat %d within %v", chatID, wait)
	}
	return text
}

// Sent returns the text of every message sent to the chat so far, read or
// not, as edited.
func (f *FakeTelegram) Sent(chatID int64) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	texts := make([]string, len(f.sent[chatID]))
	for i, m := range f.sent[chatID] {
		texts[i] = m.text
	}
	return texts
}

// UserPresses presses the button with this label on the latest message in the
// user's private chat that has one, and waits until the bot answers the press.
func (f *FakeTelegram) UserPresses(userID int64, label string) {
	f.t.Helper()
	msgID, data, found := f.button(userID, label)
	if !found {
		f.t.Fatalf("missing button %q in chat %d", label, userID)
	}
	var id string
	f.update(func() {
		n := int64(len(f.updates)) + 1
		id = strconv.FormatInt(n, 10)
		f.updates = append(f.updates, tg.Update{
			UpdateID: n,
			CallbackQuery: &tg.CallbackQuery{
				ID:      id,
				From:    tg.User{ID: userID, FirstName: "Админ"},
				Message: &tg.Message{MessageID: msgID, Chat: tg.Chat{ID: userID, Type: tg.ChatPrivate}},
				Data:    data,
			},
		})
	})
	if !f.await(context.Background(), func() bool { return f.answered[id] }) {
		f.t.Fatalf("press of %q in chat %d not answered within %v", label, userID, wait)
	}
}

func (f *FakeTelegram) button(chatID int64, label string) (msgID int64, data string, found bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range slices.Backward(f.sent[chatID]) {
		if m.buttons == nil {
			continue
		}
		for _, row := range m.buttons.InlineKeyboard {
			for _, b := range row {
				if b.Text == label {
					return m.id, b.CallbackData, true
				}
			}
		}
	}
	return 0, "", false
}

func (f *FakeTelegram) serve(w http.ResponseWriter, r *http.Request) {
	var params struct {
		Offset          int64                    `json:"offset"`
		ChatID          int64                    `json:"chat_id"`
		MessageID       int64                    `json:"message_id"`
		Text            string                   `json:"text"`
		ReplyMarkup     *tg.InlineKeyboardMarkup `json:"reply_markup"`
		CallbackQueryID string                   `json:"callback_query_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		f.t.Errorf("cannot read Telegram call: %v", err)
		return
	}
	switch method := path.Base(r.URL.Path); method {
	case "getMe":
		answer(f.t, w, ok(tg.User{ID: 777, FirstName: "Бот", Username: "test_bot"}))
	case "getUpdates":
		answer(f.t, w, ok(f.updatesFrom(r.Context(), params.Offset)))
	case "sendMessage":
		var id int64
		f.update(func() {
			f.messages++
			id = f.messages
			f.sent[params.ChatID] = append(f.sent[params.ChatID], message{id, params.Text, params.ReplyMarkup})
		})
		answer(f.t, w, ok(tg.Message{MessageID: id, Chat: tg.Chat{ID: params.ChatID, Type: tg.ChatPrivate}, Text: params.Text}))
	case "editMessageText":
		f.update(func() {
			i := slices.IndexFunc(f.sent[params.ChatID], func(m message) bool { return m.id == params.MessageID })
			if i < 0 {
				f.t.Errorf("unknown message %d in chat %d", params.MessageID, params.ChatID)
				return
			}
			f.sent[params.ChatID][i] = message{params.MessageID, params.Text, params.ReplyMarkup}
		})
		answer(f.t, w, ok(true))
	case "sendChatAction":
		answer(f.t, w, ok(true))
	case "answerCallbackQuery":
		f.update(func() { f.answered[params.CallbackQueryID] = true })
		answer(f.t, w, ok(true))
	default:
		f.t.Errorf("unknown Telegram method %q", method)
	}
}

// updatesFrom long-polls like getUpdates: it waits for an update at offset or
// later until the caller gives up or wait passes.
func (f *FakeTelegram) updatesFrom(ctx context.Context, offset int64) []tg.Update {
	f.update(func() { f.polling++ })
	defer f.update(func() { f.polling-- })
	var ups []tg.Update
	f.await(ctx, func() bool {
		ups = slices.DeleteFunc(slices.Clone(f.updates), func(u tg.Update) bool { return u.UpdateID < offset })
		return len(ups) > 0
	})
	return ups
}

func ok(result any) map[string]any { return map[string]any{"ok": true, "result": result} }
