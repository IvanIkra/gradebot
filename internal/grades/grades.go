// Package grades keeps the grades of every user in every sheet they follow, and the new ones among them, one user at
// a time.
//
// A grade's key is its place: tab, column and the column's header, not its label, so a new label is no new grade.
// The first read of a sheet for a user is the baseline: it fills grade_items and records nothing. After it, a work
// new to the sheet, or a changed grade or bonus, is a new grade (grade_events), unless it is a placeholder zero (a
// zero before any real grade means "not graded yet", unless at least half of the other students have points in the
// work: then it is the student's own). A grade that leaves the sheet (a tab hidden, the row gone from one tab, a cell
// cleared) is kept as gone, so its return with the same grade is no new grade, and with another one it is new with
// the grade before. A column moved by one inserted before it keeps its grade: matched by tab, header and grade among
// the works that left in the same read. So does a column whose header changed (a date or a maximum fixed, a layout
// asked again that names other header rows): matched by tab, column and grade.
//
// Grades live as long as the subscription: unfollowing a sheet, leaving the bot or being deleted erases them
// (foreign keys), and a new full name erases all of the user's (Forget), so the next read is a baseline again.
package grades

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/IvanIkra/gradebot/internal/clock"
	"github.com/IvanIkra/gradebot/internal/layout"
	"github.com/IvanIkra/gradebot/internal/pyfmt"
	"github.com/IvanIkra/gradebot/internal/store"
)

const (
	GradedShare = 0.5                // a zero is real once this share of the other students has points in the work
	Window      = 7 * 24 * time.Hour // new grades wait this long to be sent, and are marked new this long
)

// ColumnOf is the column letter of a key «<tab>|<column>|<header>»; the tab is known, as it may hold «|» itself.
func ColumnOf(tab, key string) string {
	rest := key[min(len(key), len(tab)+1):]
	col, _, _ := strings.Cut(rest, "|")
	return col
}

// Item is a row of grade_items; Items gives Parts and New too.
type Item struct {
	TgID      int64
	Sheet     int64
	Key       string
	Tab       string
	Header    string
	Label     string
	LabelRu   string
	Value     float64
	Raw       float64
	Max       float64
	Bonus     *float64
	Position  int
	PartsJSON string `db:"parts"`
	Gone      bool
	Parts     [][2]string `db:"-"`
	New       bool        `db:"-"`
}

// Event is a row of grade_events: a new or changed grade.
type Event struct {
	ID       int64
	TgID     int64
	Sheet    int64
	Key      string
	Label    string
	LabelRu  string
	Value    float64
	Raw      float64
	Max      float64
	Bonus    *float64
	Previous *float64
	SeenAt   string
	PostID   *int64
}

// Grades keeps the grades.
type Grades struct {
	db  *store.Store
	Now clock.Now
}

// New makes Grades.
func New(db *store.Store, now clock.Now) *Grades {
	if now == nil {
		now = clock.NowUTC
	}
	return &Grades{db: db, Now: now}
}

func bonusOf(b *float64) float64 {
	if b == nil {
		return 0
	}
	return *b
}

type place struct {
	tab, where string
	value      float64
	bonus      float64
	hasBonus   bool
}

func placeOf(tab, where string, value float64, bonus *float64) place {
	return place{tab: tab, where: where, value: value, bonus: bonusOf(bonus), hasBonus: bonus != nil}
}

// Record stores a user's grades in a sheet and the new ones among them; true for the baseline (the first read).
// errorText: what is wrong with the rest (tabs left out), shown to the user; "" for none. The subscription must
// exist.
func (g *Grades) Record(tgID, sheet int64, items []layout.Item, tabs int, errorText string) bool {
	baseline, _ := store.Scalar[string](g.db, "select baseline_at from subscriptions where tg_id = ? and sheet = ?",
		tgID, sheet)
	first := baseline == ""
	rows := store.Select[Item](g.db, "select * from grade_items where tg_id = ? and sheet = ?", tgID, sheet)
	prev := map[string]*Item{}
	for n := range rows {
		prev[rows[n].Key] = &rows[n]
	}
	keys := map[string]bool{}
	for _, it := range items {
		keys[it.Key] = true
	}
	// A column inserted before others shifts their letters and so their keys: a work that leaves one key in this
	// read and is found under another with the same tab, header and grade is the same work, not a new grade.
	// A header that changed changes the key too: the same tab, column and grade is the same work.
	shifted, renamed := map[place][]*Item{}, map[place][]*Item{}
	for n := range rows {
		row := &rows[n]
		if !keys[row.Key] && !row.Gone {
			p := placeOf(row.Tab, "h:"+row.Header, row.Value, row.Bonus)
			shifted[p] = append(shifted[p], row)
			p = placeOf(row.Tab, "c:"+ColumnOf(row.Tab, row.Key), row.Value, row.Bonus)
			renamed[p] = append(renamed[p], row)
		}
	}
	now := clock.ISO(g.Now())
	seen, moved := map[string]bool{}, map[string]bool{}
	left := func(m map[place][]*Item, p place) *Item { // a work that left in this read and is not matched yet
		for len(m[p]) > 0 {
			candidates := m[p]
			row := candidates[len(candidates)-1]
			m[p] = candidates[:len(candidates)-1]
			if !moved[row.Key] {
				return row
			}
		}
		return nil
	}
	g.db.Tx(func() {
		for _, it := range items {
			old := prev[it.Key]
			if old == nil {
				old = left(shifted, placeOf(it.Tab, "h:"+it.Header, it.Value, it.Bonus))
				if old == nil {
					old = left(renamed, placeOf(it.Tab, "c:"+ColumnOf(it.Tab, it.Key), it.Value, it.Bonus))
				}
				if old != nil {
					moved[old.Key] = true
					g.db.Exec("update grade_events set key = ? where tg_id = ? and sheet = ? and key = ?", it.Key, tgID,
						sheet, old.Key)
				}
			}
			if it.Value == 0 && bonusOf(it.Bonus) == 0 && old == nil && (it.Graded == nil || *it.Graded < GradedShare) {
				continue // a placeholder for "not graded yet": a zero after a real grade, or in a work graded for the
				// group, is the student's own
			}
			seen[it.Key] = true
			var parts any
			if len(it.Parts) > 0 {
				parts = pyfmt.JSONPairs(it.Parts)
			}
			g.db.Exec("insert into grade_items(tg_id, sheet, key, tab, header, label, label_ru, value, raw, max, "+
				"bonus, position, parts) values(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) on conflict(tg_id, sheet, key) "+
				"do update set label = excluded.label, label_ru = excluded.label_ru, value = excluded.value, "+
				"raw = excluded.raw, max = excluded.max, bonus = excluded.bonus, position = excluded.position, "+
				"parts = excluded.parts, gone = 0", tgID, sheet, it.Key, it.Tab, it.Header, it.Label,
				store.Null(it.LabelRu), it.Value, it.Raw, it.Max, it.Bonus, it.Position, parts)
			changed := old == nil || old.Value != it.Value || bonusOf(old.Bonus) != bonusOf(it.Bonus)
			if changed && !first {
				var previous any
				if old != nil {
					previous = old.Value
				}
				g.db.Exec("insert into grade_events(tg_id, sheet, key, label, label_ru, value, raw, max, bonus, "+
					"previous, seen_at) values(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", tgID, sheet, it.Key, it.Label,
					store.Null(it.LabelRu), it.Value, it.Raw, it.Max, it.Bonus, previous, now)
			}
		}
		// A moved work lives under its new key now; the others that left may come back.
		for key := range moved {
			g.db.Exec("delete from grade_items where tg_id = ? and sheet = ? and key = ?", tgID, sheet, key)
		}
		for key := range prev {
			if !seen[key] && !moved[key] {
				g.db.Exec("update grade_items set gone = 1 where tg_id = ? and sheet = ? and key = ?", tgID, sheet, key)
			}
		}
		g.db.Exec("update subscriptions set baseline_at = coalesce(baseline_at, ?), read_at = ?, error = ?, "+
			"tabs = ? where tg_id = ? and sheet = ?", now, now, store.Null(errorText), tabs, tgID, sheet)
	})
	return first
}

// Failed: the user's rows could not be told (no_row, ambiguous); the grades stay as they were.
func (g *Grades) Failed(tgID, sheet int64, errorText string) {
	g.db.Tx(func() {
		g.db.Exec("update subscriptions set read_at = ?, error = ? where tg_id = ? and sheet = ?", clock.ISO(g.Now()),
			errorText, tgID, sheet)
	})
}

// Forget drops a user's grades, of one sheet or all (0): the next read is a baseline again (a new full name, a
// reset).
func (g *Grades) Forget(tgID, sheet int64) {
	where, args := "tg_id = ?", []any{tgID}
	if sheet != 0 {
		where, args = "tg_id = ? and sheet = ?", []any{tgID, sheet}
	}
	g.db.Tx(func() {
		for _, table := range []string{"grade_items", "grade_events"} {
			g.db.Exec("delete from "+table+" where "+where, args...)
		}
		g.db.Exec("update subscriptions set baseline_at = null, read_at = null, error = null, tabs = 0 where "+where,
			args...)
	})
}

// --- what the user sees -----------------------------------------------------------------------------------------

func (g *Grades) Count(tgID, sheet int64) int {
	n, _ := store.Scalar[int](g.db, "select count(*) from grade_items where tg_id = ? and sheet = ? and not gone",
		tgID, sheet)
	return n
}

// Items are a user's grades in a sheet in sheet order, the ones new within the window marked.
func (g *Grades) Items(tgID, sheet int64) []Item {
	fresh := map[string]bool{}
	for _, key := range store.Column[string](g.db, "select distinct key from grade_events where tg_id = ? and "+
		"sheet = ? and seen_at >= ?", tgID, sheet, g.since()) {
		fresh[key] = true
	}
	rows := store.Select[Item](g.db, "select * from grade_items where tg_id = ? and sheet = ? and not gone "+
		"order by position", tgID, sheet)
	for n := range rows {
		rows[n].Parts = ParseParts(rows[n].PartsJSON)
		rows[n].New = fresh[rows[n].Key]
	}
	return rows
}

// ParseParts reads the problems of a work as grade_items keeps them.
func ParseParts(raw string) [][2]string {
	if raw == "" {
		return nil
	}
	var parts [][2]string
	if err := json.Unmarshal([]byte(raw), &parts); err != nil {
		return nil
	}
	return parts
}

// Waiting are the new grades not sent yet, within the window, oldest first; of one user or of everyone (0).
func (g *Grades) Waiting(tgID int64) []Event {
	if tgID != 0 {
		return store.Select[Event](g.db, "select * from grade_events where post_id is null and seen_at >= ? and "+
			"tg_id = ? order by id", g.since(), tgID)
	}
	return store.Select[Event](g.db, "select * from grade_events where post_id is null and seen_at >= ? order by id",
		g.since())
}

// Sent marks events as sent in a message.
func (g *Grades) Sent(eventIDs []int64, msgID int64) {
	g.db.Tx(func() {
		for _, id := range eventIDs {
			g.db.Exec("update grade_events set post_id = ? where id = ?", msgID, id)
		}
	})
}

func (g *Grades) since() string { return clock.ISO(g.Now().Add(-Window)) }
