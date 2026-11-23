// Package llm is Claude Haiku for the layout of grade sheets and for formulas: the client, a journal of every call
// and a monthly limit.
package llm

import (
	"log/slog"
	"time"

	"github.com/IvanIkra/gradebot/internal/clock"
	"github.com/IvanIkra/gradebot/internal/pyfmt"
	"github.com/IvanIkra/gradebot/internal/store"
)

// Run is one LLM call, a failed one too, with its tokens, cost and duration.
type Run struct {
	ID           int64
	At           string
	Task         string
	Model        string
	OK           bool
	Error        string
	InputTokens  int64
	OutputTokens int64
	CacheWrite   int64
	CacheRead    int64
	CostUSD      float64
	DurationS    float64
}

// Journal keeps every call.
type Journal struct{ db *store.Store }

func NewJournal(db *store.Store) *Journal { return &Journal{db: db} }

// Add records a call.
func (j *Journal) Add(r Run) {
	j.db.Tx(func() {
		j.db.Exec("insert into llm_runs(at, task, model, ok, error, input_tokens, output_tokens, cache_write, "+
			"cache_read, cost_usd, duration_s) values(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", r.At, r.Task, r.Model,
			store.Bool(r.OK), store.Null(r.Error), r.InputTokens, r.OutputTokens, r.CacheWrite, r.CacheRead,
			r.CostUSD, r.DurationS)
	})
}

// Runs are the last calls, newest first.
func (j *Journal) Runs(limit int) []Run {
	return store.Select[Run](j.db, "select * from llm_runs order by id desc limit ?", limit)
}

// CostSince is what the calls since a moment cost.
func (j *Journal) CostSince(since time.Time) float64 {
	cost, _ := store.Scalar[float64](j.db, "select coalesce(sum(cost_usd), 0) from llm_runs where at >= ?",
		clock.ISO(since))
	return cost
}

// Totals are calls, failed calls and cost since a moment: for the owner's /status.
type Totals struct {
	Runs    int
	Failed  int
	CostUSD float64
}

func (j *Journal) Totals(since time.Time) Totals {
	t, _ := store.Get[struct {
		Runs   int
		Failed int
		Cost   float64
	}](j.db, "select count(*) runs, coalesce(sum(ok = 0), 0) failed, coalesce(sum(cost_usd), 0) cost "+
		"from llm_runs where at >= ?", clock.ISO(since))
	return Totals{Runs: t.Runs, Failed: t.Failed, CostUSD: pyfmt.Round(t.Cost, 6)}
}

func (j *Journal) safeAdd(r Run) {
	defer func() {
		if p := recover(); p != nil {
			slog.Error("cannot record an LLM run", "error", p)
		}
	}()
	j.Add(r)
}
