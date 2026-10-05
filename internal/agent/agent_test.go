package agent_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/IvanTurko/physics-school-bot/internal/agent"
	"github.com/IvanTurko/physics-school-bot/internal/clock"
	"github.com/IvanTurko/physics-school-bot/internal/domain"
	"github.com/IvanTurko/physics-school-bot/internal/provider/llm"
	"github.com/IvanTurko/physics-school-bot/internal/storage/sqlite"
	"github.com/IvanTurko/physics-school-bot/internal/testkit"
)

var ctx = context.Background()

// scripted answers with the queued messages in order and records the requests.
type scripted struct {
	answers  []llm.Message
	finish   string // finish reason of every answer
	requests []llm.ChatRequest
}

func (s *scripted) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	s.requests = append(s.requests, req)
	if len(s.answers) == 0 {
		return llm.ChatResponse{}, errors.New("script is over")
	}
	resp := llm.ChatResponse{Choices: []llm.Choice{{Message: s.answers[0], FinishReason: s.finish}}}
	s.answers = s.answers[1:]
	return resp, nil
}

func text(s string) llm.Message { return llm.Message{Role: "assistant", Content: s} }

func toolCall(id, name string) llm.Message {
	return llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{
		{ID: id, Type: "function", Function: llm.FunctionCall{Name: name, Arguments: "{}"}},
	}}
}

func setup(t *testing.T, model *scripted, tools ...agent.Tool) (*agent.Agent, *sqlite.Store, int64) {
	now := clock.NewFake(time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC))
	store := sqlite.New(testkit.NewDB(t), now)
	u, err := store.EnsureUser(ctx, 42, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return agent.New(model, store, now, time.UTC, tools...), store, u.ID
}

func TestHandleRemembersDialog(t *testing.T) {
	model := &scripted{answers: []llm.Message{text("Здравствуйте! В каком классе ученик?"), text("Отлично, 9 класс.")}}
	a, _, user := setup(t, model)

	if r, err := a.Handle(ctx, user, "привет"); err != nil || r.Text != "Здравствуйте! В каком классе ученик?" {
		t.Fatalf("first reply = %+v, %v", r, err)
	}
	if _, err := a.Handle(ctx, user, "9"); err != nil {
		t.Fatal(err)
	}

	got := model.requests[1].Messages
	if len(got) != 4 || got[0].Role != "system" || got[1].Content != "привет" || got[2].Role != "assistant" || got[3].Content != "9" {
		t.Fatalf("second request = %+v", got)
	}
}

func TestHandleUnknownToolGoesBackToModel(t *testing.T) {
	model := &scripted{answers: []llm.Message{toolCall("c1", "no_such_tool"), text("Не получилось, давайте иначе.")}}
	a, _, user := setup(t, model)

	if _, err := a.Handle(ctx, user, "запишите"); err != nil {
		t.Fatal(err)
	}
	second := model.requests[1].Messages
	last := second[len(second)-1]
	if last.Role != "tool" || last.ToolCallID != "c1" || last.Content != `{"ok":false,"error":"unknown_tool"}` {
		t.Fatalf("tool result sent = %+v", last)
	}
}

func TestHandleRejectsEmptyAnswer(t *testing.T) {
	model := &scripted{answers: []llm.Message{text("  \n")}}
	a, _, user := setup(t, model)

	if _, err := a.Handle(ctx, user, "привет"); err == nil {
		t.Fatal("want an error for a blank answer")
	}
}

func TestHandleStepLimit(t *testing.T) {
	model := &scripted{}
	for range agent.MaxSteps {
		model.answers = append(model.answers, toolCall("c", "loop"))
	}
	a, store, user := setup(t, model)

	if _, err := a.Handle(ctx, user, "x"); !errors.Is(err, agent.ErrTooManySteps) {
		t.Fatalf("err = %v, want ErrTooManySteps", err)
	}
	hist, err := store.Last(ctx, user, 40)
	if err != nil {
		t.Fatal(err)
	}
	if last := hist[len(hist)-1]; last.Role != domain.RoleTool {
		t.Fatalf("history ends with %+v, want the last tool result saved", last)
	}
}

func TestHandleFormatsPhonesInAnswer(t *testing.T) {
	model := &scripted{answers: []llm.Message{text("Номер для связи 8 999 123 45 67 — верно?")}}
	a, store, user := setup(t, model)

	r, err := a.Handle(ctx, user, "хочу на пробный")
	if err != nil {
		t.Fatal(err)
	}
	hist, err := store.Last(ctx, user, 40)
	if err != nil {
		t.Fatal(err)
	}
	want := "Номер для связи +7 999 123-45-67 — верно?"
	if r.Text != want || hist[len(hist)-1].Content != want {
		t.Fatalf("reply %q, saved %q; want %q in both", r.Text, hist[len(hist)-1].Content, want)
	}
}

func TestHandleTurnsThinkingOff(t *testing.T) {
	model := &scripted{answers: []llm.Message{text("Здравствуйте!")}}
	a, _, user := setup(t, model)

	if _, err := a.Handle(ctx, user, "привет"); err != nil {
		t.Fatal(err)
	}
	if r := model.requests[0].Reasoning; r.Effort != "none" {
		t.Errorf("reasoning = %+v, want effort none", r)
	}
}

func TestEmptyAnswerTellsFinishReason(t *testing.T) {
	model := &scripted{answers: []llm.Message{text("")}, finish: "length"}
	a, _, user := setup(t, model)

	if _, err := a.Handle(ctx, user, "дорого"); !strings.Contains(fmt.Sprint(err), `"length"`) {
		t.Errorf("err = %v, want the finish reason", err)
	}
}
