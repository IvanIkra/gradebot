package bot

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/IvanIkra/gradebot/internal/formula"
	"github.com/IvanIkra/gradebot/internal/layout"
	"github.com/IvanIkra/gradebot/internal/sheets"
)

func copyRow(sheet int64, key, label string, value float64, isNew bool) *Row {
	tab, _, _ := strings.Cut(key, "|")
	return &Row{Sheet: sheet, Key: key, Tab: tab, Label: label, LabelRu: label, Value: value, New: isNew}
}

func keysOf(rows []*Row) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Key)
	}
	return out
}

func TestCopiesAreTheSameLabelAndGradeElsewhere(t *testing.T) {
	items := []*Row{copyRow(1, "ДЗ|B|1", "ДЗ 1", 9, false), copyRow(1, "ДЗ|C|2", "ДЗ 2", 8, false),
		copyRow(2, "262|D|x", "дз1", 9, true),                                          // the gradebook's copy: spaces and case aside
		copyRow(2, "262|E|y", "ДЗ 2", 7, false),                                        // another grade: both are shown
		copyRow(1, "ДЗ|F|z", "ДЗ 3", 5, false), copyRow(1, "ДЗ|G|w", "ДЗ 3", 5, false)} // one tab: not copies
	kept, copies := dropCopies(items, nil)
	if !reflect.DeepEqual(keysOf(copies), []string{"262|D|x"}) || !kept[0].New { // the copy's «new» passes on
		t.Fatal(keysOf(copies))
	}
	if !reflect.DeepEqual(keysOf(kept), []string{"ДЗ|B|1", "ДЗ|C|2", "262|E|y", "ДЗ|F|z", "ДЗ|G|w"}) {
		t.Fatal(keysOf(kept))
	}
	kept, copies = dropCopies([]*Row{copyRow(2, "262|D|x", "ДЗ 1", 9, false)},
		[]*Row{copyRow(1, "ДЗ|B|1", "ДЗ 1", 9, false)})
	if len(kept) != 0 || len(copies) != 1 {
		t.Fatal(kept, copies)
	}
}

func TestAPlanHasEachLabelOnce(t *testing.T) {
	plan := []PlanWork{{Key: "1:ДЗ|B|1", Sheet: 1, Tab: "ДЗ", Label: "HW 1", LabelRu: "ДЗ 1"},
		{Key: "2:262|D|x", Sheet: 2, Tab: "262", Label: "HW 1", LabelRu: "ДЗ 1"},
		{Key: "2:262|E|y", Sheet: 2, Tab: "262", Label: "Test 1", LabelRu: "КР 1"}}
	kept, aliases := mergeCopies(plan)
	var keys []string
	for _, w := range kept {
		keys = append(keys, w.Key)
	}
	if !reflect.DeepEqual(keys, []string{"1:ДЗ|B|1", "2:262|E|y"}) ||
		!reflect.DeepEqual(aliases, map[string]string{"2:262|D|x": "1:ДЗ|B|1"}) {
		t.Fatal(keys, aliases)
	}
}

// withCopies is «Матан» of the group's sheet (ДЗ 1, КР 1) and the gradebook (its tab «262» with ДЗ 1 again).
func withCopies(t *testing.T) (groupA1, int64, int64, int64) {
	g := withA1(t)
	own, book := g.shared["Матанализ ДЗ"], g.shared["Матанализ колок"]
	subject, _ := g.bot.Subjects.Create("Матан", g.group.ID, 0)
	g.bot.Subjects.Put(subject.ID, own)
	g.bot.Subjects.Put(subject.ID, book)
	g.Sheets.SetPlan(own, []sheets.Work{{Key: "ДЗ|B|1", Tab: "ДЗ", Label: "HW 1", LabelRu: "ДЗ 1", Max: 10},
		{Key: "ДЗ|C|2", Tab: "ДЗ", Label: "HW 2", LabelRu: "ДЗ 2", Max: 10},
		{Key: "КР|B|1", Tab: "КР", Label: "Test 1", LabelRu: "КР 1", Max: 10}})
	g.Sheets.SetPlan(book, []sheets.Work{{Key: "262|D|x", Tab: "262", Label: "HW 1", LabelRu: "ДЗ 1", Max: 10}})
	return g, subject.ID, own, book
}

func copyItem(key, label string, value float64) layout.Item {
	tab, _, _ := strings.Cut(key, "|")
	return layout.Item{Key: key, Tab: tab, Header: key, Label: label, Value: value, Raw: value, Max: 10, LabelRu: label}
}

func TestGradesAndNewsShowACopyOnce(t *testing.T) {
	g, _, own, book := withCopies(t)
	g.bot.Grades.Record(bob, own, []layout.Item{copyItem("ДЗ|B|1", "ДЗ 1", 9)}, 1, "")
	g.bot.Grades.Record(bob, book, nil, 0, "")
	g.Clock.Advance(30 * time.Minute)
	g.bot.Grades.Record(bob, book, []layout.Item{copyItem("262|D|x", "ДЗ 1", 9)}, 1, "") // the gradebook catches up
	g.bot.SendGrades()
	for _, m := range g.api.Sent(bob) {
		lacks(t, m.Text, "ДЗ 1") // nothing new: a copy of a known grade
	}
	if len(g.bot.Grades.Waiting(bob)) != 0 {
		t.Fatal("waiting")
	}
	g.as(bob, "/grades")
	if n := strings.Count(g.text(bob), "ДЗ 1"); n != 1 {
		t.Fatal(n)
	}
	contains(t, g.text(bob), "Матан · оценок: 1")
	g.Clock.Advance(30 * time.Minute)
	g.bot.Grades.Record(bob, book, []layout.Item{copyItem("262|D|x", "ДЗ 1", 7)}, 1, "") // says otherwise: both shown
	g.bot.SendGrades()
	contains(t, g.text(bob), "ДЗ 1")
	g.as(bob, "/grades")
	if n := strings.Count(g.text(bob), "ДЗ 1"); n != 2 {
		t.Fatal(n)
	}
}

func TestAFormulaCountsACopyOnce(t *testing.T) {
	g, sid, own, book := withCopies(t)
	works := []any{formula.Node{"key": sf("%d:ДЗ|B|1", own)}, formula.Node{"key": sf("%d:262|D|x", book)},
		formula.Node{"key": sf("%d:ДЗ|C|2", own)}}
	tree := formula.NewTree(formula.Node{"type": "part", "name": "ДЗ", "agg": "mean", "match": []any{}, "works": works})
	g.bot.Subjects.SetFormula(sid, "…", tree, 0)
	g.bot.Grades.Record(bob, own, nil, 0, "")
	g.bot.Grades.Record(bob, book, []layout.Item{copyItem("262|D|x", "ДЗ 1", 9)}, 1, "") // graded only in the gradebook
	b, _ := g.user(bob)
	fm := g.bot.subjectFormulaOf(b, sid)
	var keys []string
	for _, w := range fm.Works("ДЗ") {
		keys = append(keys, w.Key)
	}
	if !reflect.DeepEqual(keys, []string{sf("%d:ДЗ|B|1", own), sf("%d:ДЗ|C|2", own)}) {
		t.Fatal(keys)
	}
	if v := fm.At(5); math.Abs(v-7) > 1e-9 { // (9 + 5) / 2, not (9 + 9 + 5) / 3
		t.Fatal(v)
	}
	if parts := formula.Parts(fm.Tree); len(parts) != 1 || parts[0]["name"] != "ДЗ" {
		t.Fatal(parts)
	}
}
