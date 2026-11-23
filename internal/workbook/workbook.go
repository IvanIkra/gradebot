// Package workbook reads a downloaded grade sheet: its visible tabs, and in them the rows of the students who
// follow it.
//
// The workbook is parsed once per read, then searched for every follower's full name. A name is compared without
// case, "ё" and punctuation, and matches the start of a cell: "Иванов Иван" finds "ИВАНОВ Иван Сергеевич". A row
// with nothing but the name is no match when another row has more: teachers keep blank copies of the list below the
// real one. A name that still matches two rows of one tab leaves that tab out, not the first row: two namesakes in a
// group are told apart by a longer name.
//
// Of every tab the top rows are kept (TopRows, whoever reads it): the header is somewhere among them, and the layout
// is cached by the text of the header rows (package layout), so it must not depend on whose row is asked about.
//
// The xlsx is read here rather than with a library, so that a cell's value is what openpyxl gave the Python version
// of the bot: an integer or a real by how the number is written, a date by its number format. The texts of the
// header rows go into the layout's cache keys.
package workbook

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/IvanIkra/gradebot/internal/pyfmt"
)

// TopRows is the first rows of a tab the model sees: the header is somewhere among them.
const TopRows = 15

// NotWorkbookError: a sheet closed to the link gives a login page instead of an xlsx.
type NotWorkbookError struct{ Msg string }

func (e *NotWorkbookError) Error() string { return e.Msg }

// BrokenWorkbookError: an xlsx that opens but whose tabs cannot be parsed.
type BrokenWorkbookError struct{ Msg string }

func (e *BrokenWorkbookError) Error() string { return e.Msg }

// Value is a cell's raw value: nil, int64, float64, string, bool, time.Time (a date), TimeOfDay or Duration.
type Value = any

// BigInt is an integer too long for int64, as its digits.
type BigInt string

// TimeOfDay is a date-formatted number below 1: a time without a date.
type TimeOfDay struct{ D time.Duration }

// Duration is a number formatted as elapsed time ([h]:mm).
type Duration struct{ D time.Duration }

var notWordSpace = regexp.MustCompile(`[^\p{L}\p{N}_\s-]`)

// Norm is lower case, ё as е, punctuation but "-" as spaces: "ИВАНОВ  Иван," -> "иванов иван".
func Norm(text string) string {
	text = strings.ReplaceAll(strings.ToLower(text), "ё", "е")
	return strings.Join(strings.Fields(notWordSpace.ReplaceAllString(text, " ")), " ")
}

// Matches tells that the cell starts with the words of the name.
func Matches(cell Value, student []string) bool {
	s, ok := cell.(string)
	if !ok {
		return false
	}
	words := strings.Fields(Norm(s))
	if len(words) < len(student) {
		return false
	}
	for n, w := range student {
		if words[n] != w {
			return false
		}
	}
	return true
}

// Text is a cell as text, as str() wrote it in Python.
func Text(value Value) string {
	switch v := value.(type) {
	case nil:
		return ""
	case time.Time:
		if v.Hour() == 0 && v.Minute() == 0 && v.Second() == 0 && v.Nanosecond() == 0 {
			return v.Format("2006-01-02")
		}
		return v.Format("2006-01-02 15:04")
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprint(int64(v))
		}
		return pyfmt.Repr(v)
	case int64:
		return fmt.Sprint(v)
	case bool:
		if v {
			return "True"
		}
		return "False"
	case BigInt:
		return string(v)
	case TimeOfDay:
		return clockText(v.D)
	case Duration:
		return durationText(v.D)
	case string:
		return strings.TrimFunc(v, isPySpace)
	}
	return strings.TrimFunc(fmt.Sprint(value), isPySpace)
}

func isPySpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// clockText is str(datetime.time): "HH:MM:SS", with microseconds when there are any.
func clockText(d time.Duration) string {
	h, m := int(d/time.Hour), int(d%time.Hour/time.Minute)
	s, us := int(d%time.Minute/time.Second), int(d%time.Second/time.Microsecond)
	if us != 0 {
		return fmt.Sprintf("%02d:%02d:%02d.%06d", h, m, s, us)
	}
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}

// durationText is str(datetime.timedelta): "1 day, 2:03:04".
func durationText(d time.Duration) string {
	us := d.Microseconds()
	days := us / (86400 * 1e6)
	rest := us - days*86400*1e6
	if rest < 0 {
		days--
		rest += 86400 * 1e6
	}
	sec := rest / 1e6
	text := fmt.Sprintf("%d:%02d:%02d", sec/3600, sec%3600/60, sec%60)
	if frac := rest % 1e6; frac != 0 {
		text += fmt.Sprintf(".%06d", frac)
	}
	if days != 0 {
		unit := "days"
		if days == 1 || days == -1 {
			unit = "day"
		}
		text = fmt.Sprintf("%d %s, %s", days, unit, text)
	}
	return text
}

// Column is 0 -> A, 26 -> AA.
func Column(index int) string {
	letters := ""
	index++
	for index > 0 {
		rest := (index - 1) % 26
		index = (index - 1) / 26
		letters = string(rune('A'+rest)) + letters
	}
	return letters
}

// Index is A -> 0, AA -> 26.
func Index(col string) int {
	n := 0
	for _, letter := range col {
		n = n*26 + int(letter) - 64
	}
	return n - 1
}

// TopRow is one of the first rows of a tab that is not empty: its number and its cells as text by column letter.
type TopRow struct {
	Number int
	Cells  map[string]string
}

// Tab is a visible tab.
type Tab struct {
	Title string
	Cells [][]Value // every row, raw values
	Top   []TopRow  // the first TopRows rows that are not empty
}

// Header is the rows named the header, in tab order.
func (t *Tab) Header(numbers []int) []map[string]string {
	var out []map[string]string
	for _, row := range t.Top {
		for _, n := range numbers {
			if n == row.Number {
				out = append(out, row.Cells)
				break
			}
		}
	}
	return out
}

// ColumnHeader is the header of a column: its texts in the header rows, joined.
func (t *Tab) ColumnHeader(col string, numbers []int) string {
	var parts []string
	for _, row := range t.Header(numbers) {
		if v := row[col]; v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, " / ")
}

// Row is one student's row in a tab, without their name.
type Row struct {
	Tab     *Tab
	Number  int              // in the tab, from 1
	NameCol string           // the column of the name
	Cells   map[string]Value // column letter -> raw value
}

// Above is the top rows above this one: what the model sees of the tab next to it.
func (r *Row) Above() []TopRow {
	var out []TopRow
	for _, row := range r.Tab.Top {
		if row.Number < r.Number {
			out = append(out, row)
		}
	}
	return out
}

// newTab keeps the cells and the top rows.
func newTab(title string, cells [][]Value) *Tab {
	tab := &Tab{Title: title, Cells: cells}
	for i, raw := range cells[:min(len(cells), TopRows)] {
		row := map[string]string{}
		for j, c := range raw {
			if t := Text(c); t != "" {
				row[Column(j)] = t
			}
		}
		if len(row) > 0 {
			tab.Top = append(tab.Top, TopRow{Number: i + 1, Cells: row})
		}
	}
	return tab
}

// Find gives (the student's row in every tab that has one, the tabs where the name is in more than one row with
// data).
func Find(tabs []*Tab, student string) ([]*Row, []string) {
	target := strings.Fields(Norm(student))
	var rows []*Row
	var twice []string
	for _, tab := range tabs {
		var found []*Row
		for i, r := range tab.Cells {
			j := -1
			for k, c := range r {
				if Matches(c, target) {
					j = k
					break
				}
			}
			if j < 0 {
				continue
			}
			cells := map[string]Value{}
			for k, c := range r {
				if k != j && Text(c) != "" {
					cells[Column(k)] = c
				}
			}
			found = append(found, &Row{Tab: tab, Number: i + 1, NameCol: Column(j), Cells: cells})
		}
		var filled []*Row
		for _, row := range found {
			if len(row.Cells) > 0 {
				filled = append(filled, row)
			}
		}
		if len(filled) == 0 && len(found) > 0 { // a blank copy of the list is no match
			filled = found[:1]
		}
		if len(filled) > 1 {
			twice = append(twice, tab.Title)
		} else if len(filled) == 1 {
			rows = append(rows, filled[0])
		}
	}
	return rows, twice
}
