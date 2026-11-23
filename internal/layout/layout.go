// Package layout is the layout of a tab by Haiku, and the grades it gives a student's row.
//
// Haiku sees a tab's name, its top rows above a student's row and that row without the name, and says which top
// rows are the header, which columns are graded works, their maximum, a bonus column and a short label in English
// and in Russian: a user sees the one of their language.
//
// A layout does not depend on the student, so it is one for everyone who follows the sheet: it is asked with the
// row of any follower that has numbers in the tab, and cached in kv by sheet, tab and label budget together with
// the text of the header rows it named and the names of the other tabs. While those stay, the layout stands,
// whatever the other rows do; a changed header, or a tab added or renamed, asks again. The other tabs are shown so
// that a summary tab that copies works with tabs of their own (a group's gradebook with a column «Коллок» next to a
// tab «Коллоквиум 1») leaves those columns out: each work is counted once, on its own tab. With no header named,
// any change in the top rows asks again. An answer the model cannot give (refused, cut) is cached as a skipped tab
// until the top rows change: the same input would fail again at every poll. A failure on the way (the network, the
// limit) is the caller's: the sheet is read again at the next poll.
package layout

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/IvanIkra/gradebot/internal/llm"
	"github.com/IvanIkra/gradebot/internal/pyfmt"
	"github.com/IvanIkra/gradebot/internal/store"
	"github.com/IvanIkra/gradebot/internal/workbook"
)

const (
	LabelBudget   = 23 // characters of "<course> <work>": "Calc HW 3"
	MinBudget     = 8
	Version       = 3         // bump when the prompt changes, to drop the cached layouts (2: label_ru, 3: other tabs)
	Prefix        = "layout:" // kv: + the sheet's Google id, ":", the tab's hash, ":", the budget
	MaxTokens     = 4000
	TokensPerWork = 80 // {"column", "label", "label_ru", "max", "bonus_column"}: a tab of dates must not cut it
)

const System = `You map one tab of a teacher's grade sheet to the graded works shown to a student.

You see the top rows of the tab, each with its number, and the student's own row, as ` + "`column=value`" + ` pairs. Column
letters are spreadsheet columns. The top rows hold the header (titles, dates, problem numbers, maximum points) and may
also hold other students' rows and statistics: use them only to understand the layout.

A graded work is the overall result of one homework, quiz, test, seminar, project, exam or extra sheet.
Not graded works: points for single problems (headers like 1, 2, 3a, №1, 4*), course or semester totals,
sums, averages, final or cumulative grades (ИТОГ, Итог, Средний балл, Сумма, НК, ИК), attendance, activity
points, group, comments, penalties.
When a work has per-problem columns plus a total column for that work (for example "Балл"), the work is
that total column. When the work also has its own grade column next to the points (for example "Сумма" then
"Оценка"), the work is the grade column with max 10: that is the grade the student gets, and it may be scaled
or capped differently from the points. When columns are just numbers, the tab name says what the work is (tab "ДЗ-1А" is
homework 1A, "КР1" is test 1, "Э1" is exam 1).

Return skip=true when the tab is a summary or statistics tab (Итоги, Статистика), an archive (old),
or has no graded works.

You also see the names of the sheet's other tabs. A tab that gathers the works of a group or of the course (a
gradebook) may copy works that have tabs of their own. A column whose work has its own tab is not a graded work
here: it is counted on its own tab. For example, with a tab "Коллоквиум 1", a column "Коллок" or "Коллоквиум 1" is
left out; with tabs "Доп. листок 1" and "Доп. листок 2", the columns "Доп 1" and "Доп 2" are left out, while "Доп 3"
with no tab of its own stays. Columns of such a tab whose works have no tab of their own are graded works as usual.

header_rows: the numbers of the top rows that describe the columns - titles, dates, problem numbers, maximum points -
in order. Never a row of a student, never statistics that change with the grades (averages, sums or counts over
students).

For each graded work:
- column: its column letter.
- max: the maximum points. It may be written in a header row anywhere, e.g. "Max за обыкновенные" followed
  by numbers - the first number is the maximum of the main score, the next one of the bonus. If the column
  is already a 0-10 grade, max is 10. If you cannot tell, null.
- bonus_column: the column with extra points for the same work (for example "Балл*"), or null.
- label: English, telegraphic, at most {budget} characters: "HW 1A", "Quiz 2", "Sem 3", "Test 1", "Exam",
  "Project", "Extra 2", "LMS". Dates as "HW Sep 17". No course name - it is added separately.
  Russian letter suffixes go by alphabet position, not by shape: а→A, б→B, в→C, г→D, д→E
  (tab "ДЗ-1В" is "HW 1C", not "HW 1B"). Labels must differ between works.
- label_ru: the same label in Russian, telegraphic, at most {budget} characters: "ДЗ 1А", "Квиз 2", "Сем 3",
  "КР 1", "Экзамен", "Проект", "Доп 2", "LMS". Dates as "ДЗ 17 сен". Russian letter suffixes stay as in the
  sheet (tab "ДЗ-1В" is "ДЗ 1В"). Labels must differ between works.

List works in sheet order.`

func obj(props map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

// Schema of the answer.
var Schema = obj(map[string]any{
	"skip":        map[string]any{"type": "boolean"},
	"reason":      map[string]any{"type": "string"},
	"header_rows": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
	"items": map[string]any{"type": "array", "items": obj(map[string]any{
		"column":       map[string]any{"type": "string"},
		"label":        map[string]any{"type": "string"},
		"label_ru":     map[string]any{"type": "string"},
		"max":          map[string]any{"anyOf": []any{map[string]any{"type": "number"}, map[string]any{"type": "null"}}},
		"bonus_column": map[string]any{"anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}}},
	}, "column", "label", "label_ru", "max", "bonus_column")},
}, "skip", "reason", "header_rows", "items")

// Work is a graded work of a layout.
type Work struct {
	Column      string   `json:"column"`
	Label       string   `json:"label"`
	LabelRu     string   `json:"label_ru"`
	Max         *float64 `json:"max"`
	BonusColumn *string  `json:"bonus_column"`
}

// Spec is a tab's layout.
type Spec struct {
	Skip       bool   `json:"skip"`
	Reason     string `json:"reason"`
	HeaderRows []int  `json:"header_rows"`
	Items      []Work `json:"items"`
	Failed     bool   `json:"failed,omitempty"`
}

// SpecOf reads a layout as the model or the cache gives it.
func SpecOf(m map[string]any) Spec {
	var s Spec
	s.Skip, _ = m["skip"].(bool)
	s.Reason, _ = m["reason"].(string)
	s.Failed, _ = m["failed"].(bool)
	if rows, ok := m["header_rows"].([]any); ok {
		for _, r := range rows {
			if f, ok := r.(float64); ok && f == math.Trunc(f) {
				s.HeaderRows = append(s.HeaderRows, int(f))
			}
		}
	}
	if items, ok := m["items"].([]any); ok {
		for _, raw := range items {
			it, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			var w Work
			w.Column = str(it["column"])
			w.Label = str(it["label"])
			w.LabelRu = str(it["label_ru"])
			if f, ok := it["max"].(float64); ok {
				w.Max = &f
			} else if v := it["max"]; v != nil {
				if f, ok := Number(v); ok {
					w.Max = &f
				}
			}
			if b, ok := it["bonus_column"].(string); ok && b != "" {
				w.BonusColumn = &b
			}
			s.Items = append(s.Items, w)
		}
	}
	return s
}

// str is str(value or "") of Python.
func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return ""
	case float64:
		if x == 0 {
			return ""
		}
		return pyfmt.Num(x)
	}
	return fmt.Sprint(v)
}

// Budget is the characters left for a work's label after the course and a space.
func Budget(course string) int {
	return max(MinBudget, LabelBudget-utf8.RuneCountInString(course)-1)
}

// Key is the kv key of a tab's layout: the hash of the tab's title and the version, as Python's json.dumps wrote
// them.
func Key(sheetID, tab string, labelBudget int) string {
	sum := sha256.Sum256([]byte("[" + pyfmt.JSONString(tab) + ", " + strconv.Itoa(Version) + "]"))
	return fmt.Sprintf("%s%s:%s:%d", Prefix, sheetID, hex.EncodeToString(sum[:])[:24], labelBudget)
}

func sortedCols[V any](cells map[string]V) []string {
	cols := make([]string, 0, len(cells))
	for c := range cells {
		cols = append(cols, c)
	}
	sort.Slice(cols, func(i, j int) bool { return workbook.Index(cols[i]) < workbook.Index(cols[j]) })
	return cols
}

func line(cells map[string]string) string {
	var parts []string
	for _, c := range sortedCols(cells) {
		parts = append(parts, c+"="+cells[c])
	}
	if len(parts) == 0 {
		return "(empty)"
	}
	return strings.Join(parts, " | ")
}

// Prompt is what the model sees of a tab: its top rows above the student and the student's row without the name.
func Prompt(row *workbook.Row, others []string) string {
	var above []string
	for _, r := range row.Above() {
		above = append(above, fmt.Sprintf("row %d: %s", r.Number, line(r.Cells)))
	}
	aboveText := strings.Join(above, "\n")
	if aboveText == "" {
		aboveText = "(no rows above the student)"
	}
	mine := map[string]string{}
	for c, v := range row.Cells {
		mine[c] = workbook.Text(v)
	}
	var quoted []string
	for _, title := range others {
		quoted = append(quoted, "«"+title+"»")
	}
	tabs := strings.Join(quoted, ", ")
	if tabs == "" {
		tabs = "(none)"
	}
	return fmt.Sprintf("Tab: \"%s\"\nOther tabs of the sheet: %s\nStudent name is in column %s.\n%s\n"+
		"student row (row %d): %s", row.Tab.Title, tabs, row.NameCol, aboveText, row.Number, line(mine))
}

var underscores = regexp.MustCompile(`^[+-]?(\d+(_\d+)*)?(\.(\d+(_\d+)*)?)?([eE][+-]?\d+(_\d+)*)?$`)

// Number is a cell as a number, as Python's float() read it: a comma for the point too; false for no number.
func Number(value any) (float64, bool) {
	switch v := value.(type) {
	case nil, bool:
		return 0, false
	case int64:
		return float64(v), true
	case int:
		return float64(v), true
	case float64:
		return v, true
	case workbook.BigInt:
		f, err := strconv.ParseFloat(string(v), 64)
		return f, err == nil
	}
	text := strings.ReplaceAll(strings.TrimSpace(workbook.Text(value)), ",", ".")
	if s, ok := value.(string); ok {
		text = strings.ReplaceAll(strings.TrimFunc(s, isSpace), ",", ".")
	}
	if strings.ContainsAny(text, "xXpP") && !isWordNumber(text) { // no hex: float() has none
		return 0, false
	}
	if strings.Contains(text, "_") {
		if !underscores.MatchString(text) {
			return 0, false
		}
		text = strings.ReplaceAll(text, "_", "")
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil {
		var e *strconv.NumError
		if errorsAs(err, &e) && e.Err == strconv.ErrRange {
			return f, true
		}
		return 0, false
	}
	return f, true
}

func isWordNumber(text string) bool {
	switch strings.ToLower(strings.TrimLeft(text, "+-")) {
	case "inf", "infinity", "nan":
		return true
	}
	return false
}

func isSpace(r rune) bool {
	return r == ' ' || (r >= '\t' && r <= '\r') || r == 0x85 || r == 0xa0 || r > 0xff && strings.ContainsRune("                　", r) || (r >= 0x1c && r <= 0x1f)
}

func errorsAs(err error, target **strconv.NumError) bool {
	e, ok := err.(*strconv.NumError)
	if ok {
		*target = e
	}
	return ok
}

// Sample is the row to ask about: the first one with a number in it, so the model sees what a grade looks like.
func Sample(rows []*workbook.Row) *workbook.Row {
	for _, row := range rows {
		for _, v := range row.Cells {
			if _, ok := Number(v); ok {
				return row
			}
		}
	}
	return rows[0]
}

// Item is a graded work in a student's row.
type Item struct {
	Key      string // <tab>|<column>|<its header>
	Tab      string
	Header   string
	Label    string // without the course
	Value    float64
	Raw      float64
	Max      float64
	Bonus    *float64
	Position int
	LabelRu  string      // the label in Russian; "" when the layout has none
	Graded   *float64    // the share of the other students with points in the column; nil: no others
	Parts    [][2]string // the work's problems in the own row: (its number, its points)
}

// Planned is a work a laid-out tab has a column for, graded for someone or not yet: what a formula is made of.
type Planned struct {
	Key, Tab, Label, LabelRu string
	Max                      float64
}

// WorkKey is (key, header) of a work's column: the key is its place, not its label.
func WorkKey(tab *workbook.Tab, col string, headerRows []int) (string, string) {
	header := ""
	if col != "" {
		header = tab.ColumnHeader(col, headerRows)
	}
	return tab.Title + "|" + col + "|" + header, header
}

func squeeze(s string) string { return strings.Join(strings.Fields(s), " ") }

func maxOf(w Work) float64 {
	if w.Max == nil || *w.Max == 0 {
		return 10
	}
	return *w.Max
}

// Plan is every work of a laid-out tab, in sheet order, whoever is graded in it.
func Plan(tab *workbook.Tab, spec Spec) []Planned {
	if spec.Skip {
		return nil
	}
	var works []Planned
	keys := map[string]bool{}
	for _, it := range spec.Items {
		key, _ := WorkKey(tab, it.Column, spec.HeaderRows)
		maximum := maxOf(it)
		if keys[key] || maximum <= 0 {
			continue
		}
		keys[key] = true
		works = append(works, Planned{Key: key, Tab: tab.Title, Label: squeeze(it.Label), LabelRu: squeeze(it.LabelRu),
			Max: maximum})
	}
	return works
}

// Label is a work's label in the user's language: the Russian one when there is one.
func Label(label, labelRu, lang string) string {
	if lang == "ru" && labelRu != "" {
		return labelRu
	}
	return label
}

var (
	problemRe = regexp.MustCompile(`^(?:№?\s*\d{1,2}\s*[a-zа-яё]?\)?\s*\*{0,2})$`)
	answerRe  = regexp.MustCompile(`^(?:\d{1,2}\s*вопрос|вопрос\s*\d{1,2}|задача(\s*\d{1,2})?|опрос|.{0,20}штраф)$`)
	nameRe    = regexp.MustCompile(`^(?:[\p{L}\p{Nl}\p{No}][\p{L}\p{N}_.\-]*(?: [\p{L}\p{Nl}\p{No}][\p{L}\p{N}_.\-]*){1,3})$`)
	letterRe  = regexp.MustCompile(`[a-zа-яё]`)
	cleanRe   = regexp.MustCompile(`[№\s)]`)
)

// Problem is the short name of a problem's column from its header rows («ДЗ 1 / №3 a)» -> «3a», «7 / 1а» -> «1а»),
// "" for a column that is no problem (a sum, a grade, a copy of another work). A part with № or a letter wins over
// a bare number: a row of maximums is numbers too.
func Problem(header string) string {
	var found []string
	for _, p := range strings.Split(header, " / ") {
		part := strings.ToLower(strings.TrimSpace(p))
		if problemRe.MatchString(part) {
			found = append(found, part)
		}
	}
	if len(found) > 0 {
		best := found[len(found)-1]
		for _, part := range found {
			if strings.Contains(part, "№") || letterRe.MatchString(part) {
				best = part
				break
			}
		}
		return cleanRe.ReplaceAllString(best, "")
	}
	var words []string
	for _, p := range strings.Split(header, " / ") {
		if answerRe.MatchString(strings.ToLower(strings.TrimSpace(p))) {
			words = append(words, strings.TrimSpace(p))
		}
	}
	if len(words) > 0 {
		return words[len(words)-1]
	}
	return ""
}

// groupHeaders: a column of the students' groups; a stream's sheet is graded group by group.
var groupHeaders = map[string]bool{"группа": true, "group": true}

// Groups gives each row's group (by number, from 1) when a column above the row is headed «Группа»: the last group
// named at or above it (a group's number stands once at its first row, or on every row). Nil without such a column.
func Groups(row *workbook.Row) []string {
	name, cells := workbook.Index(row.NameCol), row.Tab.Cells
	j := -1
	for _, r := range cells[:min(len(cells), row.Number-1)] {
		for k, c := range r {
			if s, ok := c.(string); ok && k != name && groupHeaders[strings.ToLower(squeeze(s))] {
				j = k
				break
			}
		}
		if j >= 0 {
			break
		}
	}
	if j < 0 {
		return nil
	}
	out, current := []string{""}, ""
	for _, r := range cells {
		if j < len(r) {
			if t := workbook.Text(r[j]); t != "" {
				current = t
			}
		}
		out = append(out, current)
	}
	return out
}

func positive(v any) bool {
	f, ok := Number(v)
	return ok && f > 0
}

// GradedShares gives, for each column, the share of the other students of the tab with points above zero in it: a
// zero is the student's own when the work is graded for the group, a placeholder when nobody has points yet. Others
// are the rows with a person's name in the name column and a number somewhere (a blank copy of the list is none); in
// a stream's sheet with a group column, those of the student's own group, as another group may be graded later.
// Only the shares are kept.
func GradedShares(row *workbook.Row, headerRows []int) map[string]float64 {
	name := workbook.Index(row.NameCol)
	group := Groups(row)
	own := ""
	if group != nil && row.Number < len(group) {
		own = group[row.Number]
	}
	var others, same [][]workbook.Value
	for i, cells := range row.Tab.Cells {
		n := i + 1
		if n == row.Number || slices.Contains(headerRows, n) || name >= len(cells) {
			continue
		}
		who, ok := cells[name].(string)
		if !ok || !nameRe.MatchString(squeeze(who)) {
			continue
		}
		numeric := false
		for j, c := range cells {
			if _, ok := Number(c); ok && j != name {
				numeric = true
				break
			}
		}
		if numeric {
			others = append(others, cells)
			if own != "" && group[n] == own {
				same = append(same, cells)
			}
		}
	}
	if len(same) > 0 {
		others = same
	}
	if len(others) == 0 {
		return map[string]float64{}
	}
	shares := map[string]float64{}
	for col := range row.Cells {
		j := workbook.Index(col)
		count := 0
		for _, r := range others {
			if j < len(r) && positive(r[j]) {
				count++
			}
		}
		shares[col] = float64(count) / float64(len(others))
	}
	return shares
}

// PartsOf gives each work's problems: the own row's cells between the work before it (or the name) and its column
// whose header names a problem, the works' and bonus columns aside.
func PartsOf(row *workbook.Row, spec Spec) map[string][][2]string {
	var items []Work
	own := map[string]bool{}
	for _, it := range spec.Items {
		if it.Column != "" {
			items = append(items, it)
			own[it.Column] = true
			if it.BonusColumn != nil {
				own[*it.BonusColumn] = true
			}
		}
	}
	sort.SliceStable(items, func(a, b int) bool {
		return workbook.Index(items[a].Column) < workbook.Index(items[b].Column)
	})
	out, before := map[string][][2]string{}, workbook.Index(row.NameCol)
	cols := sortedCols(row.Cells)
	for _, it := range items {
		at := workbook.Index(it.Column)
		var found [][2]string
		for _, col := range cols {
			if i := workbook.Index(col); i <= before || i >= at || own[col] {
				continue
			}
			value := row.Cells[col]
			name := Problem(row.Tab.ColumnHeader(col, spec.HeaderRows))
			shown := workbook.Text(value)
			if name != "" && shown != "" {
				if raw, ok := Number(value); ok {
					found = append(found, [2]string{name, pyfmt.G(pyfmt.Round(raw, 2), 6)})
				} else {
					r := []rune(shown)
					found = append(found, [2]string{name, string(r[:min(len(r), 8)])})
				}
			}
		}
		out[it.Column] = found
		before = max(before, at)
	}
	return out
}

// Extract gives the works of a laid-out tab that have a number in the row; positions go on from start.
func Extract(row *workbook.Row, spec Spec, start int) []Item {
	if spec.Skip {
		return nil
	}
	var items []Item
	keys := map[string]bool{}
	shares, parts := GradedShares(row, spec.HeaderRows), PartsOf(row, spec)
	for _, it := range spec.Items {
		raw, ok := Number(row.Cells[it.Column])
		key, header := WorkKey(row.Tab, it.Column, spec.HeaderRows)
		if !ok || keys[key] {
			continue
		}
		maximum := maxOf(it)
		if maximum <= 0 {
			continue
		}
		value := raw
		if maximum != 10 {
			value = raw / maximum * 10
		}
		var bonus *float64
		if it.BonusColumn != nil {
			if b, ok := Number(row.Cells[*it.BonusColumn]); ok && b != 0 {
				bonus = &b
			}
		}
		var graded *float64
		if share, ok := shares[it.Column]; ok {
			graded = &share
		}
		keys[key] = true
		items = append(items, Item{Key: key, Tab: row.Tab.Title, Header: header, Label: squeeze(it.Label),
			Value: pyfmt.Round(value, 2), Raw: raw, Max: maximum, Bonus: bonus, Position: start + len(items),
			LabelRu: squeeze(it.LabelRu), Graded: graded, Parts: parts[it.Column]})
	}
	return items
}

// Layouts asks and caches layouts.
type Layouts struct {
	db  *store.Store
	LLM llm.Asker
}

// New makes Layouts.
func New(db *store.Store, asker llm.Asker) *Layouts { return &Layouts{db: db, LLM: asker} }

type cached struct {
	Watch  []int               `json:"watch"`
	Header []map[string]string `json:"header"`
	Others []string            `json:"others"`
	Spec   map[string]any      `json:"spec"`
}

func sameHeader(a, b []map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for n := range a {
		if len(a[n]) != len(b[n]) || (len(a[n]) > 0 && !reflect.DeepEqual(a[n], b[n])) {
			return false
		}
	}
	return true
}

// Get gives the cached layout while the rows it watches have the same text and the other tabs (their titles) are
// the same, else Haiku's, asked about one of rows (the followers' rows in the tab). An llm error or BudgetExceeded
// when it could not be asked.
func (l *Layouts) Get(sheetID string, tab *workbook.Tab, rows []*workbook.Row, labelBudget int,
	others []string) (Spec, error) {
	key := Key(sheetID, tab.Title, labelBudget)
	sortedOthers := slices.Clone(others)
	sort.Strings(sortedOthers)
	if sortedOthers == nil {
		sortedOthers = []string{}
	}
	var c cached
	if l.db.GetJSON(key, &c) && c.Spec != nil && sameHeader(tab.Header(c.Watch), c.Header) &&
		slices.Equal(orEmpty(c.Others), sortedOthers) {
		return SpecOf(c.Spec), nil
	}
	row := Sample(rows)
	var shown []int
	columns := map[string]bool{}
	for c := range row.Cells {
		columns[c] = true
	}
	for _, r := range row.Above() {
		shown = append(shown, r.Number)
		for c := range r.Cells {
			columns[c] = true
		}
	}
	system := strings.ReplaceAll(System, "{budget}", strconv.Itoa(labelBudget))
	var spec map[string]any
	answer, err := l.LLM.JSON("grades", system, Prompt(row, others), Schema,
		max(MaxTokens, TokensPerWork*len(columns)))
	switch {
	case err == nil:
		spec = answer
		rows := map[int]bool{}
		for _, n := range SpecOf(map[string]any{"header_rows": spec["header_rows"]}).HeaderRows {
			if slices.Contains(shown, n) {
				rows[n] = true
			}
		}
		header := []any{}
		for _, n := range sortedKeys(rows) {
			header = append(header, n)
		}
		spec["header_rows"] = header
	case llm.IsBadAnswer(err): // the same input would fail again: skipped until the top rows change
		slog.Warn("layout skipped", "tab", tab.Title, "error", err)
		spec = map[string]any{"skip": true, "reason": "no answer: " + err.Error(), "header_rows": []any{},
			"items": []any{}, "failed": true}
	default:
		return Spec{}, err
	}
	// The header the model named is what the layout stands on; with none named, the whole top.
	watch := []int{}
	for _, v := range spec["header_rows"].([]any) {
		watch = append(watch, v.(int))
	}
	if len(watch) == 0 {
		for _, r := range tab.Top {
			watch = append(watch, r.Number)
		}
	}
	header := tab.Header(watch)
	if header == nil {
		header = []map[string]string{}
	}
	l.db.PutJSON(key, map[string]any{"watch": watch, "header": header, "others": sortedOthers, "spec": spec})
	result := SpecOf(roundTrip(spec))
	slog.Info("layout", "tab", tab.Title, "skip", result.Skip, "works", len(result.Items))
	return result, nil
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func sortedKeys(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// roundTrip makes ints floats again, as a spec read from the cache has them.
func roundTrip(spec map[string]any) map[string]any {
	var out map[string]any
	if err := jsonUnmarshal(store.Marshal(spec), &out); err != nil {
		panic(err)
	}
	return out
}

// Forget drops every cached layout of a sheet: /relayout, and the button under a complaint.
func (l *Layouts) Forget(sheetID string) int64 {
	return l.db.DeletePrefix(Prefix + sheetID + ":")
}
