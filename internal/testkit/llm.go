package testkit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/IvanTurko/physics-school-bot/internal/provider/llm"
)

// FakeLLM is a /chat/completions server that gives the queued answers in order.
type FakeLLM struct {
	*state

	t        testing.TB
	srv      *httptest.Server
	answers  []llm.Message
	requests []llm.ChatRequest
	read     int // how many requests WaitRequest returned
}

func newFakeLLM(t testing.TB) *FakeLLM {
	f := &FakeLLM{state: newState(), t: t}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// Text is a model answer for the user.
func Text(s string) llm.Message { return llm.Message{Role: "assistant", Content: s} }

// ToolCall is a model answer that calls the tool with JSON args.
func ToolCall(name, args string) llm.Message {
	return llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{
		{ID: "call_" + name, Type: "function", Function: llm.FunctionCall{Name: name, Arguments: args}},
	}}
}

// Reply queues the answer to the next request; a request waits for it.
func (f *FakeLLM) Reply(m llm.Message) {
	f.update(func() { f.answers = append(f.answers, m) })
}

// WaitRequest returns the next request the test has not read, as soon as it
// arrives, before it is answered.
func (f *FakeLLM) WaitRequest() llm.ChatRequest {
	f.t.Helper()
	var req llm.ChatRequest
	if !f.await(context.Background(), func() bool {
		if f.read == len(f.requests) {
			return false
		}
		req = f.requests[f.read]
		f.read++
		return true
	}) {
		f.t.Fatalf("no model request within %v", wait)
	}
	return req
}

func (f *FakeLLM) serve(w http.ResponseWriter, r *http.Request) {
	var req llm.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		f.t.Errorf("cannot read model request: %v", err)
		return
	}
	f.update(func() { f.requests = append(f.requests, req) })

	var next llm.Message
	if !f.await(r.Context(), func() bool {
		if len(f.answers) == 0 {
			return false
		}
		next, f.answers = f.answers[0], f.answers[1:]
		return true
	}) {
		// Answer 400 so the client does not retry: an unscripted turn fails after one wait.
		w.WriteHeader(http.StatusBadRequest)
		answer(f.t, w, map[string]any{"error": map[string]string{"message": "no answer queued"}})
		return
	}
	answer(f.t, w, llm.ChatResponse{Choices: []llm.Choice{{Message: next}}})
}
