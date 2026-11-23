package bot

import (
	"math"
	"regexp"
	"strings"

	"github.com/IvanIkra/gradebot/internal/grades"
	"github.com/IvanIkra/gradebot/internal/layout"
	"github.com/IvanIkra/gradebot/internal/pyfmt"
	"github.com/IvanIkra/gradebot/internal/rich"
	"github.com/IvanIkra/gradebot/internal/sheets"
)

// Grades as messages: new grades after a poll, and /grades.
//
// A unit is a section: a sheet in no subject headed by its title, linked to the sheet, and its course's hashtag; a
// subject headed by its hashtag, its sheets linked under it. «By problem» sends a unit's works with the points of
// their problems as the sheet has them in the student's row. A work is a table row: its label in the user's language
// (without the course, the heading has it), the grade on 0-10 with the bonus, the sheet's own points when its scale
// is not 0-10, and the grade before for a changed one. Columns empty in the whole section are left out.

const right = "right"

// Row is a grade as a card shows it: a grade of grade_items or an event of grade_events.
type Row struct {
	Sheet          int64
	Key, Tab       string
	Label, LabelRu string
	Value          float64
	Raw, Max       float64
	Bonus          *float64
	Previous       *float64
	New            bool
	IDs            []int64 // the events it stands for
	Parts          [][2]string
}

func rowOfItem(i grades.Item) *Row {
	return &Row{Sheet: i.Sheet, Key: i.Key, Tab: i.Tab, Label: i.Label, LabelRu: i.LabelRu, Value: i.Value,
		Raw: i.Raw, Max: i.Max, Bonus: i.Bonus, New: i.New, Parts: i.Parts}
}

func rowOfEvent(e grades.Event) *Row {
	return &Row{Sheet: e.Sheet, Key: e.Key, Label: e.Label, LabelRu: e.LabelRu, Value: e.Value, Raw: e.Raw,
		Max: e.Max, Bonus: e.Bonus, Previous: e.Previous, IDs: []int64{e.ID}}
}

func bonus(b *float64) float64 {
	if b == nil {
		return 0
	}
	return *b
}

var nonWord = regexp.MustCompile(`[^\p{L}\p{N}_]+`)

// hashtag is 'Calc' -> '#Calc'; a tag holds letters, digits and _ only.
func hashtag(course string) string {
	tag := strings.Trim(nonWord.ReplaceAllString(course, "_"), "_")
	if tag == "" || isDigits(tag) {
		return ""
	}
	return "#" + tag
}

// score is 8.43 -> '8.4', 9.0 -> '9', 9.96 -> '10'.
func score(value float64) string {
	rounded := pyfmt.Round(value, 1)
	if rounded == math.Trunc(rounded) {
		return itoa(int64(rounded))
	}
	return sf("%.1f", rounded)
}

// points is the sheet's own numbers: 5.9, 7, 2.5.
func points(value float64) string { return pyfmt.G(pyfmt.Round(value, 2), 6) }

func signed(value float64) string {
	text := pyfmt.G(value, 6)
	if value >= 0 && !strings.HasPrefix(text, "-") {
		return "+" + text
	}
	return text
}

// heading is the parts of a section's heading: a sheet's title linked to it and the course's hashtag; a subject's
// hashtag.
func heading(unit *Unit) []any {
	tag := hashtag(unit.Tag)
	if unit.Subject != nil {
		if tag == "" {
			return []any{unit.Name}
		}
		return []any{tag}
	}
	sheet := unit.Sheets[0]
	title := sheet.Title
	if title == "" {
		title = sheets.CourseOf(sheet)
	}
	parts := []any{rich.Link(title, sheet.URL)}
	if tag != "" {
		parts = append(parts, " ", tag)
	}
	return parts
}

// sheetLinks: under a subject's heading, its sheets, linked.
func sheetLinks(doc *rich.Doc, unit *Unit) *rich.Doc {
	if unit.Subject != nil {
		var links []any
		for _, s := range unit.Sheets {
			title := s.Title
			if title == "" {
				title = sheets.Name(s)
			}
			links = append(links, rich.Link(title, s.URL))
		}
		doc.P(rich.JoinSep(" · ", links...))
	}
	return doc
}

func gradeCell(r *Row, isNew bool) rich.HTML {
	var value rich.HTML
	if isNew {
		value = rich.Mark(score(r.Value))
	} else {
		value = rich.Bold(score(r.Value))
	}
	extra := ""
	if bonus(r.Bonus) != 0 {
		extra = " " + signed(*r.Bonus)
	}
	return rich.Join(value, extra)
}

func rowLabel(r *Row, lang string) string { return layout.Label(r.Label, r.LabelRu, lang) }

type column struct {
	title, align string
	get          func(*Row) any
}

func worksTable(doc *rich.Doc, rows []*Row, lang string, withPrevious bool) *rich.Doc {
	of := tr(lang, "of", "из")
	columns := []column{
		{tr(lang, "Work", "Задание"), "", func(r *Row) any {
			if r.New {
				return rich.Mark(rowLabel(r, lang))
			}
			return rowLabel(r, lang)
		}},
		{tr(lang, "Grade", "Оценка"), right, func(r *Row) any { return gradeCell(r, r.New) }},
		{tr(lang, "Points", "Баллы"), right, func(r *Row) any {
			if r.Max != 10 {
				return points(r.Raw) + " " + of + " " + points(r.Max)
			}
			return ""
		}},
	}
	if withPrevious {
		columns = append(columns, column{tr(lang, "Was", "Было"), right, func(r *Row) any {
			if r.Previous != nil {
				return score(*r.Previous)
			}
			return ""
		}})
	}
	cells := make([][]any, len(rows))
	for n, r := range rows {
		for _, c := range columns {
			cells[n] = append(cells[n], c.get(r))
		}
	}
	var keep []int
	for n := range columns {
		any := n < 2
		for _, row := range cells {
			any = any || rich.Esc(row[n]) != ""
		}
		if any {
			keep = append(keep, n)
		}
	}
	var header []any
	var align []string
	for _, n := range keep {
		header = append(header, columns[n].title)
		align = append(align, columns[n].align)
	}
	var body [][]any
	for _, row := range cells {
		var line []any
		for _, n := range keep {
			line = append(line, row[n])
		}
		body = append(body, line)
	}
	return doc.TableHTML(header, body, align)
}

// --- new grades -------------------------------------------------------------------------------------------------

// collapse turns the events of one sheet waiting to be sent into one row per work: the latest grade with the grade
// before the first of them, and the events it stands for. A work that went back to where it was is no news: its ids
// come second, to be marked sent all the same.
func collapse(events []grades.Event) ([]*Row, []int64) {
	var order []string
	byKey := map[string][]grades.Event{}
	for _, e := range events {
		if _, ok := byKey[e.Key]; !ok {
			order = append(order, e.Key)
		}
		byKey[e.Key] = append(byKey[e.Key], e)
	}
	var rows []*Row
	var dropped []int64
	for _, key := range order {
		group := byKey[key]
		last, first := group[len(group)-1], group[0]
		var ids []int64
		for _, e := range group {
			ids = append(ids, e.ID)
		}
		if len(group) > 1 && first.Previous != nil && *first.Previous == last.Value &&
			bonus(first.Bonus) == bonus(last.Bonus) {
			dropped = append(dropped, ids...)
			continue
		}
		row := rowOfEvent(last)
		row.Previous, row.IDs = first.Previous, ids
		rows = append(rows, row)
	}
	return rows, dropped
}

func newsSection(unit *Unit, rows []*Row, lang string) *rich.Doc {
	doc := worksTable(sheetLinks(rich.New().H(heading(unit)...), unit), rows, lang, true)
	if unit.Note != "" {
		doc.P(rich.Italic(unit.Note))
	}
	return doc
}

func allGradesButton(lang string) rich.Button { return rich.B(tr(lang, "All grades", "Все оценки"), "grades") }

// Section is a unit with its new rows.
type Section struct {
	Unit *Unit
	Rows []*Row
}

// Message is a Doc and the event ids it sends.
type Message struct {
	Doc *rich.Doc
	IDs []int64
}

// newsMessages makes as few messages as fit the limits of (unit, rows), a section of a unit whole when it fits
// alone, each with the event ids it sends. Each ends with the «All grades» button.
func newsMessages(sections []Section, lang string) []Message {
	done := func(doc *rich.Doc) *rich.Doc { return doc.Clone().Buttons(allGradesButton(lang)) }
	var pieces func(unit *Unit, rows []*Row) []Message
	pieces = func(unit *Unit, rows []*Row) []Message {
		doc := newsSection(unit, rows, lang)
		if len(rows) == 1 || done(doc).Fits() {
			var ids []int64
			for _, r := range rows {
				ids = append(ids, r.IDs...)
			}
			return []Message{{doc, ids}}
		}
		half := len(rows) / 2
		return append(pieces(unit, rows[:half]), pieces(unit, rows[half:])...)
	}
	var messages []Message
	for _, section := range sections {
		if len(section.Rows) == 0 {
			continue
		}
		for _, piece := range pieces(section.Unit, section.Rows) {
			if n := len(messages); n > 0 && done(messages[n-1].Doc.Clone().Extend(piece.Doc)).Fits() {
				messages[n-1].Doc.Extend(piece.Doc)
				messages[n-1].IDs = append(messages[n-1].IDs, piece.IDs...)
			} else {
				messages = append(messages, Message{piece.Doc.Clone(), append([]int64(nil), piece.IDs...)})
			}
		}
	}
	for n := range messages {
		messages[n].Doc = done(messages[n].Doc)
	}
	return messages
}

// --- /grades ----------------------------------------------------------------------------------------------------

// gradesSection is a unit in /grades: details headed «Calc · 12 grades · 2 new», open when some are new; a
// subject's sheets linked, its outlook, the table of its grades, a «Report a mistake» button for each sheet's layout
// and a subject's «Goal» and «Formula» inside.
func gradesSection(unit *Unit, items []*Row, lang string, problems []string, table bool) *rich.Doc {
	fresh := 0
	for _, it := range items {
		if it.New {
			fresh++
		}
	}
	summary := []string{unit.Name, tr(lang, sf("%d grades", len(items)), sf("оценок: %d", len(items)))}
	if fresh > 0 {
		summary = append(summary, tr(lang, sf("%d new", fresh), sf("новых: %d", fresh)))
	}
	inner := sheetLinks(rich.New(), unit)
	if unit.Note != "" {
		inner.P(rich.Italic(unit.Note))
	}
	for _, problem := range problems {
		inner.P(problem)
	}
	if len(items) > 0 && table {
		worksTable(inner, items, lang, false)
	} else if len(items) == 0 && len(problems) == 0 {
		inner.P(tr(lang, "No grades yet.", "Оценок пока нет."))
	}
	if len(items) > 0 {
		report := tr(lang, "Report a mistake", "Сообщить об ошибке")
		graded := map[int64]bool{}
		for _, it := range items {
			graded[it.Sheet] = true
		}
		for _, s := range unit.Sheets {
			if graded[s.ID] {
				text := report
				if len(graded) != 1 {
					text = report + ": " + sheets.Name(s)
				}
				inner.Buttons(rich.BS(text, "wr:"+itoa(s.ID), "link"))
			}
		}
	}
	for _, it := range items {
		if len(it.Parts) > 0 {
			inner.Buttons(rich.B(tr(lang, "By problem", "По задачам"), "pt:"+unitRef(unit)))
			break
		}
	}
	if unit.Subject != nil {
		sid := itoa(unit.Subject.ID)
		inner.Buttons(rich.B(tr(lang, "Goal", "Цель"), "gl:s."+sid), rich.B(tr(lang, "Formula", "Формула"), "fm:s."+sid))
	}
	return rich.New().Details(strings.Join(summary, " · "), inner, fresh > 0)
}

// unitRef is a unit in a callback: «s<subject>», or «h<sheet>» for a sheet in no subject.
func unitRef(unit *Unit) string {
	if unit.Subject != nil {
		return "s" + itoa(unit.Subject.ID)
	}
	return "h" + itoa(unit.Sheets[0].ID)
}

const problemsPerRow = 8 // problem columns in one table: wider ones scroll sideways on a phone

// problemsWork is a work «By problem»: «HW 1 · grade 7 · 3.5 of 5», then its problems' numbers over their points, in
// tables of at most problemsPerRow columns.
func problemsWork(item *Row, lang string) *rich.Doc {
	facts := []any{rich.Join(tr(lang, "grade ", "оценка "), gradeCell(item, false))}
	if item.Max != 10 {
		facts = append(facts, tr(lang, points(item.Raw)+" of "+points(item.Max), points(item.Raw)+" из "+points(item.Max)))
	}
	doc := rich.New().P(rich.Join(rich.Bold(rowLabel(item, lang)), " · ", rich.JoinSep(" · ", facts...)))
	parts := item.Parts
	for at := 0; at < len(parts); at += problemsPerRow {
		chunk := parts[at:min(len(parts), at+problemsPerRow)]
		var names, values []string
		var align []string
		for _, p := range chunk {
			names, values, align = append(names, p[0]), append(values, p[1]), append(align, right)
		}
		doc.Table(names, [][]string{values}, align)
	}
	return doc
}

// problemsDocs is «By problem»: each work with a table of its problems' points, as few messages as fit.
func problemsDocs(unit *Unit, items []*Row, lang string) []*rich.Doc {
	title := rich.Join(unit.Name, tr(lang, " · by problem", " · по задачам"))
	var works []*rich.Doc
	for _, it := range items {
		if len(it.Parts) > 0 {
			works = append(works, problemsWork(it, lang))
		}
	}
	if len(works) == 0 {
		return []*rich.Doc{rich.New().H(title).P(tr(lang, "The sheets show no problems of these works.",
			"В таблицах нет баллов по задачам этих работ."))}
	}
	docs := []*rich.Doc{rich.New().H(title)}
	for _, work := range works {
		last := docs[len(docs)-1]
		if last.Len() > 1 && !last.Clone().Extend(work).Fits() {
			docs = append(docs, rich.New().H(title))
		}
		docs[len(docs)-1].Extend(work)
	}
	return docs
}
