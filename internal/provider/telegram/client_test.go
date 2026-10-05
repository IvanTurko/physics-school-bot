package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IvanTurko/physics-school-bot/pkg/httpx"
)

const token = "123456:SECRET-token"

var ctx = context.Background()

// serve answers every call with body and records the path and JSON of the last
// request.
func serve(t *testing.T, body string) (c *Client, path *string, sent map[string]any) {
	path, sent = new(string), map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*path = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("request body: %v", err)
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, token, httpx.New()), path, sent
}

func TestSendMessagePostsChatAndText(t *testing.T) {
	c, path, sent := serve(t, `{"ok":true,"result":{"chat":{"id":42,"type":"private"},"text":"привет"}}`)

	msg, err := c.SendMessage(ctx, SendMessage{ChatID: 42, Text: "привет"})
	if err != nil {
		t.Fatal(err)
	}
	if *path != "/bot"+token+"/sendMessage" {
		t.Errorf("path = %s, want /bot<token>/sendMessage", *path)
	}
	if _, ok := sent["reply_markup"]; sent["chat_id"] != float64(42) || sent["text"] != "привет" || ok {
		t.Errorf("sent %v, want chat_id 42, the text and no reply_markup", sent)
	}
	if msg.Chat.ID != 42 {
		t.Errorf("msg = %+v, want the sent message decoded", msg)
	}
}

func TestSendTypingPostsChatAndAction(t *testing.T) {
	c, path, sent := serve(t, `{"ok":true,"result":true}`)

	if err := c.SendTyping(ctx, 42); err != nil {
		t.Fatal(err)
	}
	if *path != "/bot"+token+"/sendChatAction" {
		t.Errorf("path = %s, want /bot<token>/sendChatAction", *path)
	}
	if sent["chat_id"] != float64(42) || sent["action"] != "typing" {
		t.Errorf("sent %v, want chat_id 42 and action typing", sent)
	}
}

// buttons is a keyboard of one button.
var buttons = &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{{Text: "Да", CallbackData: "bk:confirm:7"}}}}

// buttonsSent is how the buttons read once decoded from the request.
const buttonsSent = "map[inline_keyboard:[[map[callback_data:bk:confirm:7 text:Да]]]]"

func TestSendMessagePostsButtons(t *testing.T) {
	c, _, sent := serve(t, `{"ok":true,"result":{"chat":{"id":42,"type":"private"}}}`)
	if _, err := c.SendMessage(ctx, SendMessage{ChatID: 42, Text: "карточка", ReplyMarkup: buttons}); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(sent["reply_markup"]); got != buttonsSent {
		t.Errorf("reply_markup = %s, want %s", got, buttonsSent)
	}
}

func TestEditMessageTextPostsCard(t *testing.T) {
	c, path, sent := serve(t, `{"ok":true,"result":true}`)
	if err := c.EditMessageText(ctx, EditMessageText{ChatID: 42, MessageID: 5, Text: "карточка", ReplyMarkup: buttons}); err != nil {
		t.Fatal(err)
	}
	if *path != "/bot"+token+"/editMessageText" {
		t.Errorf("path = %s, want /bot<token>/editMessageText", *path)
	}
	if sent["chat_id"] != float64(42) || sent["message_id"] != float64(5) || sent["text"] != "карточка" ||
		fmt.Sprint(sent["reply_markup"]) != buttonsSent {
		t.Errorf("sent %v, want the chat, the message, the text and the buttons", sent)
	}
}

func TestEditMessageTextWithoutButtonsSendsNoMarkup(t *testing.T) {
	c, _, sent := serve(t, `{"ok":true,"result":true}`)
	if err := c.EditMessageText(ctx, EditMessageText{ChatID: 42, MessageID: 5, Text: "карточка"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := sent["reply_markup"]; ok {
		t.Errorf("sent %v, want no reply_markup", sent)
	}
}

func TestAnswerCallbackPostsText(t *testing.T) {
	c, path, sent := serve(t, `{"ok":true,"result":true}`)
	if err := c.AnswerCallback(ctx, "cb1", "Уже обработано"); err != nil {
		t.Fatal(err)
	}
	if *path != "/bot"+token+"/answerCallbackQuery" {
		t.Errorf("path = %s, want /bot<token>/answerCallbackQuery", *path)
	}
	if sent["callback_query_id"] != "cb1" || sent["text"] != "Уже обработано" {
		t.Errorf("sent %v, want the callback ID and the text", sent)
	}
}

func TestGetUpdatesAsksForUpdatesFromOffset(t *testing.T) {
	c, _, sent := serve(t, `{"ok":true,"result":[
		{"update_id":10,"message":{"from":{"id":42,"first_name":"Иван"},"chat":{"id":42,"type":"private"},"text":"hi"}},
		{"update_id":11,"callback_query":{"id":"cb1","from":{"id":1,"first_name":"Админ"},
			"message":{"message_id":5,"chat":{"id":1,"type":"private"}},"data":"bk:confirm:7"}}
	]}`)

	ups, err := c.GetUpdates(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if sent["offset"] != float64(10) || sent["timeout"] != float64(LongPoll) ||
		fmt.Sprint(sent["allowed_updates"]) != "[message callback_query]" {
		t.Errorf("sent %v, want offset 10, the long-poll timeout, messages and button presses", sent)
	}
	if len(ups) != 2 || ups[0].UpdateID != 10 || ups[0].Message.Text != "hi" {
		t.Fatalf("ups = %+v, want the message and the button press", ups)
	}
	if q := ups[1].CallbackQuery; q == nil || q.ID != "cb1" || q.From.ID != 1 || q.Data != "bk:confirm:7" ||
		q.Message == nil || q.Message.MessageID != 5 {
		t.Errorf("callback query = %+v, want it decoded", q)
	}
}

func TestSendPolicy(t *testing.T) {
	retry := SendPolicy().Retry
	for _, tc := range []struct {
		name  string
		resp  *httpx.Response
		err   error
		retry bool
	}{
		{"network failure", nil, errors.New("connection reset"), false},
		{"rate limit", &httpx.Response{StatusCode: http.StatusTooManyRequests}, nil, true},
		{"server error", &httpx.Response{StatusCode: http.StatusBadGateway}, nil, true},
		{"bad request", &httpx.Response{StatusCode: http.StatusBadRequest}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := retry(nil, tc.resp, tc.err); got != tc.retry {
				t.Fatalf("retry = %v, want %v", got, tc.retry)
			}
		})
	}
}

func TestFullName(t *testing.T) {
	if got := (User{FirstName: "Иван", LastName: "Т."}).FullName(); got != "Иван Т." {
		t.Errorf("got %q, want %q", got, "Иван Т.")
	}
	if got := (User{FirstName: "Иван"}).FullName(); got != "Иван" {
		t.Errorf("got %q, want %q", got, "Иван")
	}
}

func TestRetriesExhaustedReturnsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 1"}`)
	}))
	defer srv.Close()

	p := SendPolicy()
	p.Max, p.Base, p.Cap = 1, 1, 1 // keep the pause at a nanosecond
	_, err := New(srv.URL, token, httpx.Retrying(httpx.New(), p)).SendMessage(ctx, SendMessage{ChatID: 1, Text: "x"})
	var api *APIError
	if !errors.As(err, &api) || api.Code != 429 {
		t.Fatalf("err = %v, want the 429 from Telegram", err)
	}
}

func TestErrorsDoNotLeakToken(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens there now

	_, err := New(url, token, httpx.New()).SendMessage(ctx, SendMessage{ChatID: 1, Text: "x"})
	if err == nil {
		t.Fatal("want a connection error")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("token leaked: %v", err)
	}
}
