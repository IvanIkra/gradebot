package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/IvanIkra/gradebot/internal/clock"
	"github.com/IvanIkra/gradebot/internal/store"
	"github.com/anthropics/anthropic-sdk-go/v2"
)

var schema = map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "integer"}},
	"required": []string{"x"}}

func answer(text, stop string, output int) *anthropic.Message {
	var m anthropic.Message
	raw := fmt.Sprintf(`{"id":"m","type":"message","role":"assistant","model":"claude-haiku-5-5","stop_reason":%q,`+
		`"content":[{"type":"text","text":%q}],"usage":{"input_tokens":1000,"output_tokens":%d,`+
		`"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}`, stop, text, output)
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		panic(err)
	}
	return &m
}

type fixture struct {
	budget  *Budget
	journal *Journal
	clock   *clock.Fake
	calls   []anthropic.MessageNewParams
}

func setup(t *testing.T) *fixture {
	db := store.MustOpen(":memory:")
	t.Cleanup(func() { db.Close() })
	t.Setenv("ANTHROPIC_API_KEY", "test")
	zone, _ := time.LoadLocation("Europe/Moscow")
	f := &fixture{journal: NewJournal(db), clock: clock.NewFake()}
	f.budget = NewBudget(f.journal, db, 1.0, zone, f.clock.Now)
	return f
}

func (f *fixture) haiku(response *anthropic.Message) *Haiku {
	h := NewHaiku(f.budget, f.journal)
	h.Send = func(_ context.Context, p anthropic.MessageNewParams) (*anthropic.Message, error) {
		f.calls = append(f.calls, p)
		return response, nil
	}
	return h
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

func TestACallIsLoggedAndItsReserveReleased(t *testing.T) {
	f := setup(t)
	got, err := f.haiku(answer(`{"x": 1}`, "end_turn", 100)).JSON("grades", "sys", "data", schema, 4000)
	if err != nil || got["x"] != 1.0 {
		t.Fatal(got, err)
	}
	body, _ := json.Marshal(f.calls[0])
	if f.calls[0].Model != "claude-haiku-5-5" || !strings.Contains(string(body),
		`"output_config":{"effort":"low","format":{"schema":{"properties":{"x":{"type":"integer"}},"required":["x"],"type":"object"},"type":"json_schema"}}`) {
		t.Fatal(string(body))
	}
	run := f.journal.Runs(100)[0]
	if !run.OK || run.Task != "grades" || !near(run.CostUSD, (1000*0.10+100*0.50)/1e6) ||
		!near(f.budget.Spent(), run.CostUSD) {
		t.Fatal(run)
	}
}

func TestACutAnswerIsABadAnswerAndIsLogged(t *testing.T) {
	f := setup(t)
	_, err := f.haiku(answer(`{"x": `, "max_tokens", 4000)).JSON("grades", "s", "d", schema, 4000)
	if !IsBadAnswer(err) || !strings.Contains(err.Error(), "max_tokens") || f.journal.Runs(100)[0].OK {
		t.Fatal(err)
	}
}

func TestNoKey(t *testing.T) {
	f := setup(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	if _, err := f.haiku(answer("{}", "end_turn", 1)).JSON("grades", "s", "d", schema, 10); !IsError(err) ||
		!strings.Contains(err.Error(), "not set") {
		t.Fatal(err)
	}
}

func run(cost float64, at string) Run {
	return Run{At: at, Task: "grades", Model: "m", OK: true, CostUSD: cost, DurationS: 1}
}

func TestTheLimitIsMonthlyAndToldOnce(t *testing.T) {
	f := setup(t)
	var told []Level
	f.budget.Notify = func(level Level, spent, limit float64) { told = append(told, level) }
	f.journal.Add(run(0.5, "2026-10-31T22:00:00+00:00")) // 1 November in Moscow: this month
	f.journal.Add(run(5.0, "2026-10-31T20:00:00+00:00")) // October in Moscow
	if !near(f.budget.Spent(), 0.5) {
		t.Fatal(f.budget.Spent())
	}
	release := func(cost float64) error {
		ticket, err := f.budget.Reserve(cost)
		if err == nil {
			f.budget.Release(ticket)
		}
		return err
	}
	_ = release(0.1)
	if len(told) != 0 {
		t.Fatal(told)
	}
	f.journal.Add(run(0.35, "2026-11-09T10:00:00+00:00")) // 0.85 of 1 now
	_ = release(0.01)
	_ = release(0.01)
	if len(told) != 1 || told[0] != Warn {
		t.Fatal(told)
	}
	if !IsBudget(release(0.2)) || !IsBudget(release(0.2)) {
		t.Fatal("not refused")
	}
	if len(told) != 2 || told[1] != Stop || !f.budget.Status().Stopped {
		t.Fatal(told)
	}
	f.clock.Advance(25 * 24 * time.Hour) // December: a new month
	if f.budget.Spent() != 0 || f.budget.Status().Stopped || release(0.3) != nil {
		t.Fatal("december")
	}
}

func TestARefusedCallIsNotMade(t *testing.T) {
	f := setup(t)
	f.budget.Limit = 0.000001
	if _, err := f.haiku(answer(`{"x": 1}`, "end_turn", 1)).JSON("grades", "sys", "data", schema, 4000); !IsBudget(err) {
		t.Fatal(err)
	}
	if len(f.calls) != 0 || len(f.journal.Runs(100)) != 0 {
		t.Fatal("made")
	}
}
