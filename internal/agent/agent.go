// Package agent runs the dialog: it gives the model the history and the tools,
// executes the tool calls it asks for, and returns the final answer.
package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/IvanTurko/physics-school-bot/internal/clock"
	"github.com/IvanTurko/physics-school-bot/internal/domain"
	"github.com/IvanTurko/physics-school-bot/internal/provider/llm"
)

// LLM is the chat model.
type LLM interface {
	Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error)
}

// History stores the dialog of each user.
type History interface {
	Append(ctx context.Context, userID int64, msgs ...domain.Message) error
	Last(ctx context.Context, userID int64, n int) ([]domain.Message, error) // never splits a tool call from its results
	Profile(ctx context.Context, userID int64) (domain.Profile, error)
}

// Tool is a function the model may call on behalf of the user.
type Tool interface {
	Definition() llm.Function
	// Call runs the tool for the user and returns a JSON result for the model;
	// failures the model should handle go into the result, not the error.
	Call(ctx context.Context, userID int64, args string) (string, error)
}

// Reply is what the bot answers to one user message.
type Reply struct {
	Text string
}

// ErrTooManySteps means the model kept calling tools without answering.
var ErrTooManySteps = errors.New("agent: no answer within the step limit")

// Limits of one turn.
const (
	HistorySize = 40  // messages of the dialog given to the model
	MaxSteps    = 6   // model calls per user message
	MaxTokens   = 700 // per model answer
)

// Agent answers users.
type Agent struct {
	llm     LLM
	history History
	clock   clock.Clock
	tz      *time.Location
	tools   map[string]Tool
	defs    []llm.Tool
}

// New creates an Agent that shows times in tz and offers the given tools.
func New(model LLM, history History, c clock.Clock, tz *time.Location, tools ...Tool) *Agent {
	a := &Agent{llm: model, history: history, clock: c, tz: tz, tools: make(map[string]Tool)}
	for _, t := range tools {
		def := t.Definition()
		a.tools[def.Name] = t
		a.defs = append(a.defs, llm.Tool{Type: "function", Function: def})
	}
	return a
}

// Handle answers one user message, saving each step as it happens, so a failure
// at any point leaves a valid history.
func (a *Agent) Handle(ctx context.Context, userID int64, text string) (Reply, error) {
	if err := a.history.Append(ctx, userID, domain.Message{Role: domain.RoleUser, Content: text}); err != nil {
		return Reply{}, fmt.Errorf("cannot save message: %w", err)
	}
	past, err := a.history.Last(ctx, userID, HistorySize)
	if err != nil {
		return Reply{}, fmt.Errorf("cannot load history: %w", err)
	}
	profile, err := a.history.Profile(ctx, userID)
	if err != nil {
		return Reply{}, fmt.Errorf("cannot load profile: %w", err)
	}
	system, err := renderPrompt(a.clock.Now().In(a.tz), describe(profile, a.tz))
	if err != nil {
		return Reply{}, fmt.Errorf("cannot render prompt: %w", err)
	}

	msgs := make([]llm.Message, 0, len(past)+1)
	msgs = append(msgs, llm.Message{Role: "system", Content: system})
	for _, m := range past {
		msgs = append(msgs, toLLM(m))
	}

	for range MaxSteps {
		// Turn thinking off: it spends MaxTokens and may leak into the answer.
		resp, err := a.llm.Chat(ctx, llm.ChatRequest{
			Messages: msgs, Tools: a.defs, MaxTokens: MaxTokens,
			Reasoning: llm.Reasoning{Effort: "none"},
		})
		if err != nil {
			return Reply{}, err
		}
		answer := fromLLM(resp.Reply())

		if len(answer.ToolCalls) == 0 {
			answer.Content = domain.FormatPhonesIn(strings.TrimSpace(answer.Content))
			if answer.Content == "" {
				return Reply{}, fmt.Errorf("agent: empty answer, finish reason %q", resp.Choices[0].FinishReason)
			}
			if err := a.history.Append(ctx, userID, answer); err != nil {
				return Reply{}, fmt.Errorf("cannot save answer: %w", err)
			}
			return Reply{Text: answer.Content}, nil
		}

		exchange := []domain.Message{answer}
		for _, call := range answer.ToolCalls {
			result, err := a.call(ctx, userID, call)
			if err != nil {
				return Reply{}, fmt.Errorf("cannot run tool %s: %w", call.Name, err)
			}
			exchange = append(exchange, domain.Message{Role: domain.RoleTool, ToolCallID: call.ID, Content: result})
		}
		if err := a.history.Append(ctx, userID, exchange...); err != nil {
			return Reply{}, fmt.Errorf("cannot save tool exchange: %w", err)
		}
		for _, m := range exchange {
			msgs = append(msgs, toLLM(m))
		}
	}
	return Reply{}, ErrTooManySteps
}

func (a *Agent) call(ctx context.Context, userID int64, call domain.ToolCall) (string, error) {
	t, ok := a.tools[call.Name]
	if !ok {
		return `{"ok":false,"error":"unknown_tool"}`, nil
	}
	return t.Call(ctx, userID, call.Arguments)
}

func toLLM(m domain.Message) llm.Message {
	out := llm.Message{Role: string(m.Role), Content: m.Content, ToolCallID: m.ToolCallID}
	for _, c := range m.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, llm.ToolCall{
			ID: c.ID, Type: "function", Function: llm.FunctionCall{Name: c.Name, Arguments: c.Arguments},
		})
	}
	return out
}

func fromLLM(m llm.Message) domain.Message {
	out := domain.Message{Role: domain.RoleAssistant, Content: m.Content}
	for _, c := range m.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, domain.ToolCall{ID: c.ID, Name: c.Function.Name, Arguments: c.Function.Arguments})
	}
	return out
}

// statuses name an active booking's status for the model.
var statuses = map[domain.Status]string{
	domain.StatusNew:       "ждёт подтверждения администратора",
	domain.StatusConfirmed: "подтверждена администратором",
}

// describe tells the model what the bot knows about the user.
func describe(p domain.Profile, tz *time.Location) string {
	if p.Last == nil {
		return "Пока ничего не известно."
	}
	active := "Активной записи нет."
	if p.Active != nil {
		start := p.Active.Start.In(tz)
		active = fmt.Sprintf("Активная запись: %s, %s, %s.", day(start), start.Format("15:04"), statuses[p.Active.Status])
	}
	known := fmt.Sprintf("%s\nИз последней заявки: %d класс, цель — %s, телефон %s.",
		active, p.Last.Grade, p.Last.Goal, domain.FormatPhone(p.Last.Phone))
	if p.TrialHeld {
		known += "\nПробный урок уже прошёл."
	}
	return known
}
