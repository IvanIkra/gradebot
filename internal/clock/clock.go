// Package clock keeps the time rules: the database keeps UTC in ISO 8601 with a "+00:00" offset, so plain string
// comparison orders it.
package clock

import (
	"fmt"
	"time"
)

// Layout of every moment the database keeps: seconds, and the offset written out, never "Z".
const Layout = "2006-01-02T15:04:05-07:00"

// Now is the moment the service runs at; tests pass their own.
type Now func() time.Time

// NowUTC is the real clock.
func NowUTC() time.Time { return time.Now().UTC() }

// ISO is a moment as the database keeps it: "2026-11-09T12:00:00+00:00".
func ISO(moment time.Time) string {
	return moment.UTC().Truncate(time.Second).Format(Layout)
}

var layouts = []string{
	"2006-01-02T15:04:05.999999999-07:00",
	"2006-01-02T15:04:05-07:00",
	"2006-01-02T15:04-07:00",
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04",
	"2006-01-02",
}

// Parse reads what Python's datetime.fromisoformat reads; a moment without an offset is taken as UTC.
func Parse(value string) (time.Time, error) {
	for _, layout := range layouts {
		if t, err := time.Parse(layout, value); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("not an ISO moment: %q", value)
}

// MustParse is Parse for values the bot wrote itself.
func MustParse(value string) time.Time {
	t, err := Parse(value)
	if err != nil {
		panic(err)
	}
	return t
}

// MonthStart is local midnight of the first day of moment's month, as UTC: the LLM limit is counted by local months.
func MonthStart(moment time.Time, zone *time.Location) time.Time {
	local := moment.In(zone)
	return time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, zone).UTC()
}

// Month is "2026-11": the local month of a moment.
func Month(moment time.Time, zone *time.Location) string {
	return moment.In(zone).Format("2006-01")
}

// DayStart is local midnight that starts moment's day, as UTC.
func DayStart(moment time.Time, zone *time.Location) time.Time {
	local := moment.In(zone)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, zone).UTC()
}

// Fake is a clock that moves only when told: tests start it at 2026-11-09 12:00 UTC.
type Fake struct{ Moment time.Time }

// NewFake is a fake clock at 2026-11-09 12:00 UTC.
func NewFake() *Fake { return &Fake{Moment: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)} }

// Now is the fake moment.
func (f *Fake) Now() time.Time { return f.Moment }

// Advance moves the clock on.
func (f *Fake) Advance(d time.Duration) { f.Moment = f.Moment.Add(d) }
