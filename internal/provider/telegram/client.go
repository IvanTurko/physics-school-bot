// Package telegram calls the Telegram Bot API.
package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/IvanTurko/physics-school-bot/pkg/httpx"
)

// Per-attempt timeouts: a poll outlives the long-poll wait, a send gives up fast.
const (
	PollTimeout = 40 * time.Second
	SendTimeout = 10 * time.Second
)

// LongPoll is how long getUpdates waits for an update, in seconds.
const LongPoll = 30

// PollPolicy retries getUpdates on network failures, 429 and 5xx: reading twice is safe.
func PollPolicy() httpx.Policy {
	return httpx.Policy{Max: httpx.DefaultRetries, Retry: httpx.RetryAny}
}

// SendPolicy retries a send on 429 and 5xx, never on a network failure: the
// message may have arrived.
func SendPolicy() httpx.Policy {
	return httpx.Policy{Max: 3, Retry: func(_ *httpx.Request, resp *httpx.Response, err error) bool {
		return err == nil && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500)
	}}
}

// Client calls the Bot API.
type Client struct {
	doer  httpx.Doer
	base  string
	token string
}

// New creates a client of the API at baseURL, normally https://api.telegram.org.
func New(baseURL, token string, doer httpx.Doer) *Client {
	return &Client{doer: doer, base: strings.TrimSuffix(baseURL, "/"), token: token}
}

// APIError is an answer with "ok": false.
type APIError struct {
	Method      string
	Code        int
	Description string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram %s: %d %s", e.Method, e.Code, e.Description)
}

type envelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
}

// call posts params to the method and decodes the result into out.
func (c *Client) call(ctx context.Context, method string, params, out any) error {
	req, err := httpx.NewRequest(http.MethodPost, c.base).
		Segments("bot"+c.token, method).
		JSON(params).
		Build()
	if err != nil {
		return c.hide(fmt.Errorf("telegram %s: %w", method, err))
	}

	resp, err := c.doer.Do(ctx, req)
	var exhausted *httpx.RetriesExhaustedError
	if errors.As(err, &exhausted) && exhausted.Response != nil {
		resp, err = exhausted.Response, nil // decode the last answer: it carries Telegram's description
	}
	if err != nil {
		return c.hide(fmt.Errorf("telegram %s: %w", method, err))
	}

	var env envelope
	if err := json.Unmarshal(resp.Body, &env); err != nil {
		return fmt.Errorf("telegram %s: cannot decode HTTP %d answer: %w", method, resp.StatusCode, err)
	}
	if !env.OK {
		return &APIError{Method: method, Code: env.ErrorCode, Description: env.Description}
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		return fmt.Errorf("telegram %s: cannot decode result: %w", method, err)
	}
	return nil
}

// hide removes the token from the error's text, where a *url.Error puts the
// full URL.
func (c *Client) hide(err error) error {
	return &tokenFreeError{err: err, token: c.token}
}

type tokenFreeError struct {
	err   error
	token string
}

func (e *tokenFreeError) Error() string { return strings.ReplaceAll(e.err.Error(), e.token, "<token>") }
func (e *tokenFreeError) Unwrap() error { return e.err }

// GetMe returns the bot's own account.
func (c *Client) GetMe(ctx context.Context) (User, error) {
	var u User
	err := c.call(ctx, "getMe", struct{}{}, &u)
	return u, err
}

// GetUpdates long-polls for updates starting at offset.
func (c *Client) GetUpdates(ctx context.Context, offset int64) ([]Update, error) {
	var ups []Update
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         LongPoll,
		"allowed_updates": []string{"message", "callback_query"},
	}, &ups)
	return ups, err
}

// SendMessage sends a text message.
func (c *Client) SendMessage(ctx context.Context, m SendMessage) (Message, error) {
	var sent Message
	err := c.call(ctx, "sendMessage", m, &sent)
	return sent, err
}

// SendTyping shows the bot typing in the chat until its next message, for 5
// seconds at most.
func (c *Client) SendTyping(ctx context.Context, chatID int64) error {
	var ok bool
	return c.call(ctx, "sendChatAction", map[string]any{"chat_id": chatID, "action": "typing"}, &ok)
}

// EditMessageText replaces the text and buttons of a sent message.
func (c *Client) EditMessageText(ctx context.Context, m EditMessageText) error {
	var edited json.RawMessage // the message or true; neither is read
	return c.call(ctx, "editMessageText", m, &edited)
}

// AnswerCallback stops the spinner on a pressed button and shows text, if any.
func (c *Client) AnswerCallback(ctx context.Context, id, text string) error {
	var ok bool
	return c.call(ctx, "answerCallbackQuery", map[string]string{"callback_query_id": id, "text": text}, &ok)
}
