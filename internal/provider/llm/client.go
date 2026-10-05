// Package llm calls an OpenAI-compatible /chat/completions API, such as OpenRouter's.
package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/IvanTurko/physics-school-bot/pkg/httpx"
)

// Timeout bounds one attempt, not the whole run of retries.
const Timeout = 25 * time.Second

// Policy retries on 429, 5xx and network failures.
func Policy() httpx.Policy {
	return httpx.Policy{Max: 2, Retry: httpx.RetryAny}
}

// Message is one entry of the conversation sent to the model.
type Message struct {
	Role string `json:"role"` // system, user, assistant or tool
	// Content is omitted when empty: some providers reject an empty text next to tool calls.
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall is the model asking to run a function.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // always "function"
	Function FunctionCall `json:"function"`
}

// FunctionCall names the function and carries its arguments as a JSON string.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool describes a function the model may call.
type Tool struct {
	Type     string   `json:"type"` // always "function"
	Function Function `json:"function"`
}

// Function is a tool's name, purpose and JSON Schema of its arguments.
type Function struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

// ChatRequest is the body of /chat/completions; an empty Model takes the
// client's default.
type ChatRequest struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	Tools     []Tool    `json:"tools,omitempty"`
	MaxTokens int       `json:"max_tokens,omitempty"`
	Reasoning Reasoning `json:"reasoning,omitzero"`
}

// Reasoning sets how much the model thinks before answering; its thinking
// counts against MaxTokens.
type Reasoning struct {
	Effort string `json:"effort"` // none turns thinking off
}

// ChatResponse is the part of the answer the bot reads.
type ChatResponse struct {
	Choices []Choice `json:"choices"`
}

// Choice is one variant of the answer.
type Choice struct {
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Reply is the model's message of the first choice; Chat never returns an answer without choices.
func (r ChatResponse) Reply() Message { return r.Choices[0].Message }

// APIError is an error the API reported, with or without an HTTP error status.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("llm: %d %s", e.Status, e.Message) }

// Client calls /chat/completions.
type Client struct {
	doer  httpx.Doer
	base  string
	key   string
	model string
}

// New creates a client of the API at baseURL, e.g. https://openrouter.ai/api/v1.
func New(baseURL, key, model string, doer httpx.Doer) *Client {
	return &Client{doer: doer, base: strings.TrimSuffix(baseURL, "/"), key: key, model: model}
}

// Chat sends the conversation and returns the model's answer.
func (c *Client) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	if req.Model == "" {
		req.Model = c.model
	}
	r, err := httpx.NewRequest(http.MethodPost, c.base).
		At("/chat/completions").
		Header("Authorization", "Bearer "+c.key).
		Header("X-Title", "physics_trial_bot").
		JSON(req).
		Build()
	if err != nil {
		return ChatResponse{}, fmt.Errorf("llm: %w", err)
	}

	resp, err := c.doer.Do(ctx, r)
	var exhausted *httpx.RetriesExhaustedError
	if errors.As(err, &exhausted) && exhausted.Response != nil {
		resp, err = exhausted.Response, nil
	}
	if err != nil {
		return ChatResponse{}, fmt.Errorf("llm: %w", err)
	}

	// Read the error field whatever the status: OpenRouter may put one in a 200.
	var body struct {
		ChatResponse

		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := resp.JSON(&body); err != nil {
		return ChatResponse{}, fmt.Errorf("llm: cannot decode HTTP %d answer: %w", resp.StatusCode, err)
	}
	if body.Error != nil {
		return ChatResponse{}, &APIError{Status: resp.StatusCode, Message: body.Error.Message}
	}
	if !resp.OK() {
		return ChatResponse{}, &APIError{Status: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
	}
	if len(body.Choices) == 0 {
		return ChatResponse{}, &APIError{Status: resp.StatusCode, Message: "answer has no choices"}
	}
	return body.ChatResponse, nil
}
