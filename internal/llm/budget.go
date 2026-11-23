package llm

import (
	"math"
	"slices"
	"sync"
	"time"

	"github.com/IvanIkra/gradebot/internal/clock"
	"github.com/IvanIkra/gradebot/internal/pyfmt"
	"github.com/IvanIkra/gradebot/internal/store"
)

// One limit in dollars a month for every LLM call. Months are local months (TZ).
//
// Before a call the worst case is reserved: the input estimated from its length plus max_tokens of output. A call
// whose reserve does not fit what is left is not made: new tabs wait for the next month, layouts already cached keep
// working. The owner is told once a month when the spending crosses WarnShare of the limit, and once when a call is
// refused.

const (
	LongPrompt    = 100_000 // tokens; longer prompts are billed at the long rates
	CacheWrite    = 1.25    // times the input price
	CacheRead     = 0.1
	CharsPerToken = 2 // pessimistic for Russian text: the reserve must not come out below the real cost
	WarnShare     = 0.8
	notified      = "llm.notified" // kv: {"month": "2026-11", "levels": [...]}, what the owner was told this month
)

// Price is $ per million tokens.
type Price struct{ Input, Output, LongInput, LongOutput float64 }

// Prices of the models the bot calls.
var Prices = map[string]Price{"claude-haiku-5-5": {Input: 0.10, Output: 0.50, LongInput: 0.50, LongOutput: 2.50}}

// Level is what the owner is told of the spending.
type Level string

const (
	Warn Level = "warn" // WarnShare of the limit is spent
	Stop Level = "stop" // a call was refused: no new layouts until the next month
)

// BudgetExceeded: the call was not made; the caller tries again later.
type BudgetExceeded struct{}

func (BudgetExceeded) Error() string {
	return "the monthly LLM limit is spent, new layouts wait for the next month"
}

// Cost of a call in dollars.
func Cost(p Price, input, output, cacheWrite, cacheRead int64) float64 {
	in, out := p.Input, p.Output
	if input+cacheWrite+cacheRead > LongPrompt {
		in, out = p.LongInput, p.LongOutput
	}
	total := (float64(input)+float64(cacheWrite)*CacheWrite+float64(cacheRead)*CacheRead)*in + float64(output)*out
	return total / 1_000_000
}

// Budget is the monthly limit.
type Budget struct {
	journal  *Journal
	db       *store.Store
	Limit    float64
	zone     *time.Location
	Now      clock.Now
	Notify   func(level Level, spent, limit float64) // once a month each
	mu       sync.Mutex
	reserved map[int]float64
	ticket   int
}

// NewBudget makes the limit.
func NewBudget(j *Journal, db *store.Store, monthlyUSD float64, zone *time.Location, now clock.Now) *Budget {
	if now == nil {
		now = clock.NowUTC
	}
	return &Budget{journal: j, db: db, Limit: monthlyUSD, zone: zone, Now: now, reserved: map[int]float64{}}
}

// Estimate is the worst case of a call.
func Estimate(p Price, inputChars, maxTokens int) float64 {
	return Cost(p, int64(math.Ceil(float64(inputChars)/CharsPerToken)), int64(maxTokens), 0, 0)
}

// Spent is this month's real cost plus what calls in flight have reserved.
func (b *Budget) Spent() float64 {
	b.mu.Lock()
	reserved := 0.0
	for _, v := range b.reserved {
		reserved += v
	}
	b.mu.Unlock()
	return b.journal.CostSince(clock.MonthStart(b.Now(), b.zone)) + reserved
}

// Reserve gives a ticket for Release, or BudgetExceeded.
func (b *Budget) Reserve(cost float64) (int, error) {
	if b.Spent()+cost > b.Limit {
		b.announce(Stop)
		return 0, BudgetExceeded{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ticket++
	b.reserved[b.ticket] = cost
	return b.ticket, nil
}

// Release: after the call is in the journal, the real cost replaces the reserve.
func (b *Budget) Release(ticket int) {
	b.mu.Lock()
	delete(b.reserved, ticket)
	b.mu.Unlock()
	if b.Spent() >= WarnShare*b.Limit {
		b.announce(Warn)
	}
}

// Status is what /status shows.
type Status struct {
	SpentUSD float64
	LimitUSD float64
	Stopped  bool
}

func (b *Budget) Status() Status {
	return Status{SpentUSD: pyfmt.Round(b.Spent(), 4), LimitUSD: b.Limit, Stopped: slices.Contains(b.told(), "stop")}
}

type notice struct {
	Month  string   `json:"month"`
	Levels []string `json:"levels"`
}

func (b *Budget) told() []string {
	var saved notice
	if b.db.GetJSON(notified, &saved) && saved.Month == clock.Month(b.Now(), b.zone) {
		return saved.Levels
	}
	return nil
}

func (b *Budget) announce(level Level) {
	told := b.told()
	if slices.Contains(told, string(level)) {
		return
	}
	b.db.PutJSON(notified, notice{Month: clock.Month(b.Now(), b.zone), Levels: append(told, string(level))})
	if b.Notify != nil {
		b.Notify(level, b.Spent(), b.Limit)
	}
}
