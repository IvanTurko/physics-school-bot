package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IvanTurko/physics-school-bot/pkg/httpx"
)

var ctx = context.Background()

// serve answers every call with status and answer and records the raw body
// of the last request.
func serve(t *testing.T, status int, answer string) (*Client, *[]byte) {
	sent := new([]byte)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("got %s with auth %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		*sent, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		io.WriteString(w, answer)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL+"/api/v1/", "key", "test/model", httpx.New()), sent
}

func TestChat(t *testing.T) {
	c, raw := serve(t, 200, `{"choices":[{"message":{"role":"assistant","content":"Здравствуйте!"},"finish_reason":"stop"}]}`)

	resp, err := c.Chat(ctx, ChatRequest{
		Messages: []Message{{Role: "system", Content: "ты менеджер"}, {Role: "user", Content: "привет"}},
		Tools: []Tool{{Type: "function", Function: Function{
			Name: "get_free_slots", Description: "свободное время", Parameters: json.RawMessage(`{"type":"object","properties":{}}`),
		}}},
		MaxTokens: 500,
		Reasoning: Reasoning{Effort: "none"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply().Content != "Здравствуйте!" {
		t.Errorf("reply = %+v", resp.Reply())
	}
	if resp.Choices[0].FinishReason != "stop" {
		t.Errorf("finish reason = %q, want stop", resp.Choices[0].FinishReason)
	}
	if !strings.Contains(string(*raw), `"reasoning":{"effort":"none"}`) {
		t.Errorf("sent %s, want reasoning effort none", *raw)
	}
	var sent ChatRequest
	if err := json.Unmarshal(*raw, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Model != "test/model" || len(sent.Messages) != 2 || sent.MaxTokens != 500 {
		t.Errorf("sent %+v", sent)
	}
	if len(sent.Tools) != 1 || sent.Tools[0].Function.Name != "get_free_slots" {
		t.Errorf("sent tools %+v", sent.Tools)
	}
}

func TestChatDecodesToolCalls(t *testing.T) {
	c, _ := serve(t, 200, `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[
		{"id":"call_1","type":"function","function":{"name":"book_trial","arguments":"{\"grade\":9}"}}
	]}}]}`)

	resp, err := c.Chat(ctx, ChatRequest{Messages: []Message{{Role: "user", Content: "запишите"}}})
	if err != nil {
		t.Fatal(err)
	}
	calls := resp.Reply().ToolCalls
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Function.Name != "book_trial" || calls[0].Function.Arguments != `{"grade":9}` {
		t.Fatalf("calls = %+v, want book_trial call_1 with {\"grade\":9}", calls)
	}
}

func TestChatOmitsEmptyContent(t *testing.T) {
	c, raw := serve(t, 200, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)

	_, err := c.Chat(ctx, ChatRequest{Messages: []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Type: "function", Function: FunctionCall{Name: "get_free_slots", Arguments: "{}"}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(*raw), `"content"`) {
		t.Errorf("sent %s, want no content field", *raw)
	}
}

func TestChatReadsLastAnswerAfterRetries(t *testing.T) {
	c, _ := serve(t, http.StatusServiceUnavailable, `{"error":{"message":"overloaded"}}`)
	c.doer = httpx.Retrying(httpx.New(), httpx.Policy{Max: 1, Base: 1, Cap: 1, Retry: httpx.RetryAny}) // a pause of a nanosecond

	_, err := c.Chat(ctx, ChatRequest{})
	var api *APIError
	if !errors.As(err, &api) || !strings.Contains(err.Error(), "overloaded") {
		t.Fatalf("err = %v, want the API's own message", err)
	}
}

func TestChatReportsAPIErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"error inside 200", 200, `{"error":{"message":"Provider returned error","code":502}}`, "Provider returned error"},
		{"error status", 503, `{}`, "Service Unavailable"},
		{"no choices", 200, `{"choices":[]}`, "no choices"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := serve(t, tt.status, tt.body)
			_, err := c.Chat(ctx, ChatRequest{})
			var api *APIError
			if !errors.As(err, &api) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want APIError with %q", err, tt.want)
			}
		})
	}
}
