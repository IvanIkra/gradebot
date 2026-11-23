package grades_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/IvanIkra/gradebot/internal/grades"
	"github.com/IvanIkra/gradebot/internal/layout"
	"github.com/IvanIkra/gradebot/internal/store"
	"github.com/IvanIkra/gradebot/internal/testkit"
)

type opt func(*layout.Item)

func at(position int) opt      { return func(i *layout.Item) { i.Position = position } }
func header(h string) opt      { return func(i *layout.Item) { i.Header = h } }
func graded(share float64) opt { return func(i *layout.Item) { i.Graded = &share } }
func parts(p ...[2]string) opt { return func(i *layout.Item) { i.Parts = p } }

func item(key, label string, value float64, opts ...opt) layout.Item {
	it := layout.Item{Key: key, Tab: "T", Header: key, Label: label, Value: value, Raw: value, Max: 10}
	for _, o := range opts {
		o(&it)
	}
	return it
}

func setup(t *testing.T) (*testkit.Env, *grades.Grades, int64) {
	env := testkit.New(t)
	env.AddUser(1, "Иванов Иван", "", "")
	env.AddUser(2, "Петрова Анна", "", "")
	sheet, _, _ := env.Sheets.Follow(1, "https://docs.google.com/spreadsheets/d/1AbCdEfGhIjKlMnOpQrStUvWx", "")
	env.Sheets.Follow(2, sheet.URL, "")
	return env, grades.New(env.DB, env.Clock.Now), sheet.ID
}

const ivan, anna = int64(1), int64(2)

func labels(items []grades.Item) []string {
	var out []string
	for _, i := range items {
		out = append(out, i.Label)
	}
	return out
}

func TestFirstReadIsABaselineThenChangesAreNew(t *testing.T) {
	env, g, sheet := setup(t)
	if !g.Record(ivan, sheet, []layout.Item{item("k1", "HW 1A", 8.43), item("k2", "Quiz 1", 0)}, 2, "") {
		t.Fatal("no baseline")
	}
	if len(g.Waiting(0)) != 0 || !reflect.DeepEqual(labels(g.Items(ivan, sheet)), []string{"HW 1A"}) {
		t.Fatal("the zero is a placeholder")
	}
	env.Clock.Advance(24 * time.Hour)
	if g.Record(ivan, sheet, []layout.Item{item("k1", "HW 1A", 9), item("k2", "Quiz 1", 8, at(1)),
		item("k3", "Quiz 2", 0, at(2))}, 2, "") {
		t.Fatal("baseline again")
	}
	events := map[string]grades.Event{}
	for _, e := range g.Waiting(ivan) {
		events[e.Label] = e
	}
	if len(events) != 2 || *events["HW 1A"].Previous != 8.43 || events["Quiz 1"].Previous != nil {
		t.Fatal(events)
	}
	for _, i := range g.Items(ivan, sheet) {
		if !i.New {
			t.Fatal(i)
		}
	}
	g.Sent([]int64{events["HW 1A"].ID}, 77)
	if w := g.Waiting(ivan); len(w) != 1 || w[0].Label != "Quiz 1" {
		t.Fatal(w)
	}
	env.Clock.Advance(8 * 24 * time.Hour)
	if len(g.Waiting(0)) != 0 {
		t.Fatal("out of the window")
	}
	for _, i := range g.Items(ivan, sheet) {
		if i.New {
			t.Fatal(i)
		}
	}
}

func TestUsersOfOneSheetAreApart(t *testing.T) {
	env, g, sheet := setup(t)
	if !g.Record(ivan, sheet, []layout.Item{item("k1", "HW", 8)}, 1, "") ||
		!g.Record(anna, sheet, []layout.Item{item("k1", "HW", 6)}, 1, "") { // her own baseline
		t.Fatal("baselines")
	}
	env.Clock.Advance(time.Hour)
	g.Record(anna, sheet, []layout.Item{item("k1", "HW", 7)}, 1, "")
	w := g.Waiting(0)
	if len(w) != 1 || w[0].TgID != anna || w[0].Value != 7 || g.Items(ivan, sheet)[0].Value != 8 ||
		g.Count(anna, sheet) != 1 {
		t.Fatal(w)
	}
}

func TestAGradeThatLeavesAndComesBack(t *testing.T) {
	env, g, sheet := setup(t)
	g.Record(ivan, sheet, []layout.Item{item("k1", "HW", 8), item("k2", "Quiz", 7, at(1))}, 2, "")
	env.Clock.Advance(time.Hour)
	g.Record(ivan, sheet, []layout.Item{item("k1", "HW", 8)}, 1, "") // the quiz tab hidden for a while
	if !reflect.DeepEqual(labels(g.Items(ivan, sheet)), []string{"HW"}) {
		t.Fatal(g.Items(ivan, sheet))
	}
	g.Record(ivan, sheet, []layout.Item{item("k1", "HW", 8), item("k2", "Quiz", 7, at(1))}, 2, "")
	if len(g.Waiting(0)) != 0 { // back with the same grade: no news
		t.Fatal(g.Waiting(0))
	}
	g.Record(ivan, sheet, []layout.Item{item("k1", "HW", 8)}, 1, "")
	g.Record(ivan, sheet, []layout.Item{item("k1", "HW", 8), item("k2", "Quiz", 9, at(1))}, 2, "")
	if w := g.Waiting(0); len(w) != 1 || w[0].Label != "Quiz" || w[0].Value != 9 || *w[0].Previous != 7 {
		t.Fatal(w)
	}
}

func TestAMovedColumnIsNoNewGrade(t *testing.T) {
	env, g, sheet := setup(t)
	g.Record(ivan, sheet, []layout.Item{item("T|E|Квиз 1", "Quiz 1", 8, header("Квиз 1"))}, 1, "")
	env.Clock.Advance(time.Hour)
	g.Record(ivan, sheet, []layout.Item{item("T|F|Квиз 1", "Quiz 1", 8, header("Квиз 1"))}, 1, "")
	if items := g.Items(ivan, sheet); len(g.Waiting(0)) != 0 || len(items) != 1 || items[0].Key != "T|F|Квиз 1" {
		t.Fatal(items)
	}
	g.Record(ivan, sheet, []layout.Item{item("T|F|Квиз 1", "Quiz 1", 9, header("Квиз 1"))}, 1, "")
	env.Clock.Advance(time.Hour)
	g.Record(ivan, sheet, []layout.Item{item("T|E|Квиз 1", "Quiz 1", 9, header("Квиз 1"))}, 1, "") // moved back
	if w := g.Waiting(0); len(w) != 1 || w[0].Key != "T|E|Квиз 1" || w[0].Value != 9 {
		t.Fatal(w)
	}
}

func TestForgetMakesTheNextReadABaseline(t *testing.T) {
	_, g, sheet := setup(t)
	g.Record(ivan, sheet, []layout.Item{item("k1", "HW", 8)}, 1, "")
	g.Record(anna, sheet, []layout.Item{item("k1", "HW", 8)}, 1, "")
	g.Failed(ivan, sheet, "no_row")
	g.Forget(ivan, 0)
	if len(g.Items(ivan, sheet)) != 0 || len(g.Items(anna, sheet)) == 0 {
		t.Fatal("forget")
	}
	if !g.Record(ivan, sheet, []layout.Item{item("k1", "HW", 5)}, 1, "") || len(g.Waiting(0)) != 0 {
		t.Fatal("baseline")
	}
}

func TestGradesGoWithTheSubscriptionAndTheUser(t *testing.T) {
	env, g, sheet := setup(t)
	g.Record(ivan, sheet, []layout.Item{item("k1", "HW", 8)}, 1, "")
	g.Record(anna, sheet, []layout.Item{item("k1", "HW", 8)}, 1, "")
	env.Sheets.Unfollow(ivan, sheet)
	env.Access.Delete(anna)
	if store.Has(env.DB, "select 1 from grade_items") {
		t.Fatal("kept")
	}
}

func TestAZeroAfterARealGradeIsKept(t *testing.T) {
	env, g, sheet := setup(t)
	g.Record(ivan, sheet, []layout.Item{item("k1", "HW", 8)}, 1, "")
	env.Clock.Advance(time.Hour)
	g.Record(ivan, sheet, []layout.Item{item("k1", "HW", 0)}, 1, "") // lowered to a real zero: news
	if w := g.Waiting(ivan); len(w) != 1 || w[0].Value != 0 || *w[0].Previous != 8 {
		t.Fatal(w)
	}
	env.Clock.Advance(time.Hour)
	g.Record(ivan, sheet, []layout.Item{item("k1", "HW", 0)}, 1, "") // still there, no news
	if items := g.Items(ivan, sheet); len(items) != 1 || items[0].Value != 0 || len(g.Waiting(ivan)) != 1 {
		t.Fatal(items)
	}
}

func TestAMovedZeroKeepsItsGrade(t *testing.T) {
	env, g, sheet := setup(t)
	g.Record(ivan, sheet, []layout.Item{item("T|B|HW", "HW", 8, header("HW"))}, 1, "")
	g.Record(ivan, sheet, []layout.Item{item("T|B|HW", "HW", 0, header("HW"))}, 1, "")
	env.Clock.Advance(time.Hour)
	g.Record(ivan, sheet, []layout.Item{item("T|C|HW", "HW", 0, header("HW"))}, 1, "") // a column inserted before
	if items := g.Items(ivan, sheet); len(items) != 1 || items[0].Key != "T|C|HW" || items[0].Value != 0 {
		t.Fatal(items)
	}
}

func TestAHeaderThatChangedKeepsTheGrade(t *testing.T) {
	env, g, sheet := setup(t)
	g.Record(ivan, sheet, []layout.Item{item("T|C|ДЗ 1 · 12.09", "HW", 8, header("ДЗ 1 · 12.09")),
		item("T|D|ДЗ 2", "HW 2", 6, at(1), header("ДЗ 2"))}, 2, "")
	env.Clock.Advance(time.Hour)
	g.Record(ivan, sheet, []layout.Item{item("T|C|ДЗ 1 · 19.09", "HW", 8, header("ДЗ 1 · 19.09")),
		item("T|D|ДЗ 2 / 10", "HW 2", 7, at(1), header("ДЗ 2 / 10"))}, 2, "")
	if w := g.Waiting(ivan); len(w) != 1 || w[0].Key != "T|D|ДЗ 2 / 10" || w[0].Value != 7 || w[0].Previous != nil {
		t.Fatal(w)
	}
	items := g.Items(ivan, sheet)
	if items[0].Key != "T|C|ДЗ 1 · 19.09" || items[1].Key != "T|D|ДЗ 2 / 10" {
		t.Fatal(items)
	}
}

func TestAZeroIsRealWhenTheGroupIsGraded(t *testing.T) {
	env, g, sheet := setup(t)
	g.Record(ivan, sheet, []layout.Item{item("k1", "HW", 0, graded(0)), item("k2", "Quiz", 0, graded(0.4))}, 1, "")
	if len(g.Items(ivan, sheet)) != 0 { // nobody, or too few, graded yet: placeholders
		t.Fatal(g.Items(ivan, sheet))
	}
	env.Clock.Advance(time.Hour)
	g.Record(ivan, sheet, []layout.Item{item("k1", "HW", 0, graded(0.8)), item("k2", "Quiz", 0, graded(0.4))}, 1, "")
	if items := g.Items(ivan, sheet); len(items) != 1 || items[0].Label != "HW" || items[0].Value != 0 {
		t.Fatal(items)
	}
	if w := g.Waiting(ivan); len(w) != 1 || w[0].Label != "HW" || w[0].Previous != nil {
		t.Fatal(w)
	}
}

func TestProblemsAreKeptAndAChangeOfThemAloneIsNoNews(t *testing.T) {
	env, g, sheet := setup(t)
	g.Record(ivan, sheet, []layout.Item{item("k1", "HW", 8, parts([2]string{"1", "1"}, [2]string{"2", "0.5"}))}, 1, "")
	if p := g.Items(ivan, sheet)[0].Parts; !reflect.DeepEqual(p, [][2]string{{"1", "1"}, {"2", "0.5"}}) {
		t.Fatal(p)
	}
	if raw, _ := store.Scalar[string](env.DB, "select parts from grade_items"); raw != `[["1", "1"], ["2", "0.5"]]` {
		t.Fatal(raw)
	}
	env.Clock.Advance(time.Hour)
	g.Record(ivan, sheet, []layout.Item{item("k1", "HW", 8, parts([2]string{"1", "0.5"}, [2]string{"2", "1"}))}, 1, "")
	if p := g.Items(ivan, sheet)[0].Parts; !reflect.DeepEqual(p, [][2]string{{"1", "0.5"}, {"2", "1"}}) ||
		len(g.Waiting(ivan)) != 0 {
		t.Fatal(p)
	}
}
