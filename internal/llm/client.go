package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"
	"unicode/utf8"

	"github.com/IvanIkra/gradebot/internal/clock"
	"github.com/IvanIkra/gradebot/internal/pyfmt"
	"github.com/IvanIkra/gradebot/internal/store"
	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

// Model is Claude Haiku with JSON-schema structured output.
const Model = "claude-haiku-5-5"

// Tasks the LLM is called for.
var Tasks = map[string]bool{"grades": true, "formula": true}

// Error: the call failed. Bad: the model answered, but not usably (a refusal, a cut answer, no text); the same
// input would fail again.
type Error struct {
	Msg string
	Bad bool
}

func (e *Error) Error() string { return e.Msg }

// IsBadAnswer tells an answer that the same input would give again.
func IsBadAnswer(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Bad
}

// IsError tells an LLM error (a bad answer too), not the budget.
func IsError(err error) bool {
	var e *Error
	return errors.As(err, &e)
}

// IsBudget tells a call the monthly limit did not let happen.
func IsBudget(err error) bool {
	var e BudgetExceeded
	return errors.As(err, &e)
}

// Asker is what the layout and the formulas need of the LLM.
type Asker interface {
	Available() bool
	JSON(task, system, prompt string, schema map[string]any, maxTokens int) (map[string]any, error)
}

// Sender makes one Messages API call.
type Sender func(ctx context.Context, params anthropic.MessageNewParams) (*anthropic.Message, error)

// Haiku calls the model behind the monthly limit and the journal.
type Haiku struct {
	budget  *Budget
	journal *Journal
	Model   string
	Send    Sender // the API call; tests put their own
}

// NewHaiku makes the client; the API client is made on first use.
func NewHaiku(budget *Budget, journal *Journal) *Haiku {
	h := &Haiku{budget: budget, journal: journal, Model: Model}
	var client *anthropic.Client
	h.Send = func(ctx context.Context, params anthropic.MessageNewParams) (*anthropic.Message, error) {
		if client == nil {
			c := anthropic.NewClient(option.WithRequestTimeout(120*time.Second), option.WithMaxRetries(3))
			client = &c
		}
		return client.Messages.New(ctx, params)
	}
	return h
}

// Available tells that a key is set.
func (h *Haiku) Available() bool {
	return os.Getenv("ANTHROPIC_API_KEY") != "" || os.Getenv("ANTHROPIC_AUTH_TOKEN") != ""
}

// JSON is one request whose answer must match schema.
//
// BudgetExceeded when the limit does not let the call happen (nothing is spent or logged then), an *Error when the
// call failed (logged with whatever tokens it spent), Bad when the failure is in the answer itself.
func (h *Haiku) JSON(task, system, prompt string, schema map[string]any, maxTokens int) (map[string]any, error) {
	if !Tasks[task] {
		panic(fmt.Sprintf("unknown LLM task %q", task))
	}
	if !h.Available() {
		return nil, &Error{Msg: "ANTHROPIC_API_KEY is not set"}
	}
	price, ok := Prices[h.Model]
	if !ok {
		return nil, &Error{Msg: "no price for " + h.Model}
	}
	inputChars := utf8.RuneCountInString(system) + utf8.RuneCountInString(prompt) +
		utf8.RuneCount(store.Marshal(schema))
	ticket, err := h.budget.Reserve(Estimate(price, inputChars, maxTokens))
	if err != nil {
		return nil, err
	}
	defer h.budget.Release(ticket)
	started := time.Now()
	response, text, err := h.ask(system, prompt, schema, maxTokens)
	var answer map[string]any
	if err == nil {
		if e := json.Unmarshal([]byte(text), &answer); e != nil || answer == nil {
			err = &Error{Msg: fmt.Sprintf("the answer is not JSON: %v", e), Bad: true}
		}
	}
	h.record(task, price, started, response, err)
	if err != nil {
		return nil, err
	}
	return answer, nil
}

func (h *Haiku) record(task string, price Price, started time.Time, response *anthropic.Message, err error) {
	run := Run{At: clock.ISO(clock.NowUTC()), Task: task, Model: h.Model, OK: err == nil,
		DurationS: pyfmt.Round(time.Since(started).Seconds(), 2)}
	if err != nil {
		run.Error = err.Error()
	}
	if response != nil { // tokens are spent on a refusal or a cut answer too
		u := response.Usage
		run.InputTokens, run.OutputTokens = u.InputTokens, u.OutputTokens
		run.CacheWrite, run.CacheRead = u.CacheCreationInputTokens, u.CacheReadInputTokens
	}
	run.CostUSD = Cost(price, run.InputTokens, run.OutputTokens, run.CacheWrite, run.CacheRead)
	h.journal.safeAdd(run)
}

func (h *Haiku) ask(system, prompt string, schema map[string]any, maxTokens int) (*anthropic.Message,
	string, error) {
	response, err := h.Send(context.Background(), anthropic.MessageNewParams{
		Model:     anthropic.Model(h.Model),
		MaxTokens: int64(maxTokens),
		System:    []anthropic.TextBlockParam{{Text: system}},
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(prompt))},
		OutputConfig: anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffort("low"),
			Format: anthropic.JSONOutputFormatParam{Schema: schema}},
	})
	if err != nil {
		var apierr *anthropic.Error
		if errors.As(err, &apierr) {
			switch apierr.StatusCode {
			case 401:
				return nil, "", &Error{Msg: "Anthropic API key was rejected"}
			case 429:
				return nil, "", &Error{Msg: "Anthropic rate limit"}
			}
			return nil, "", &Error{Msg: fmt.Sprintf("Anthropic API error %d: %s", apierr.StatusCode, apierr.Message)}
		}
		return nil, "", &Error{Msg: fmt.Sprintf("cannot reach the Anthropic API: %v", err)}
	}
	slog.Info("haiku", "in", response.Usage.InputTokens, "out", response.Usage.OutputTokens)
	problem := ""
	switch response.StopReason {
	case "refusal":
		problem = fmt.Sprintf("request declined (%s)", response.StopDetails.Category)
	case "max_tokens":
		problem = "answer was cut at max_tokens"
	}
	text, found := "", false
	for _, block := range response.Content {
		if block.Type == "text" {
			text, found = block.Text, true
			break
		}
	}
	if problem == "" && !found {
		problem = "no text in the answer"
	}
	if problem != "" {
		return response, "", &Error{Msg: problem, Bad: true}
	}
	return response, text, nil
}
