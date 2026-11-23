package formula

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

func plan(keys ...string) []Planned {
	var out []Planned
	for _, k := range keys {
		out = append(out, Planned{Key: k, Label: k, LabelRu: strings.ToUpper(k), Max: 10})
	}
	return out
}

func graded(pairs ...any) map[string]Grade {
	out := map[string]Grade{}
	for n := 0; n+1 < len(pairs); n += 2 {
		out[pairs[n].(string)] = Grade{Value: toF(pairs[n+1]), Max: 10}
	}
	return out
}

func toF(v any) float64 {
	x, _ := num(v)
	return x
}

func part(name string, keys ...string) Node {
	works := []any{}
	for _, k := range keys {
		works = append(works, Node{"key": k})
	}
	return Node{"type": "part", "name": name, "agg": "mean", "works": works}
}

func with(n Node, kv ...any) Node {
	out := Node{}
	for k, v := range n {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

type term struct {
	w float64
	n Node
}

func total(terms []term, bonus ...term) Node {
	list := []any{}
	for _, t := range terms {
		list = append(list, Node{"weight": t.w, "node": t.n})
	}
	for _, t := range bonus {
		list = append(list, Node{"weight": t.w, "node": t.n, "bonus": true})
	}
	return Node{"type": "sum", "terms": list}
}

func ref(name string) Node { return Node{"type": "ref", "name": name} }

func rnd(node Node) Node { return Node{"type": "round", "mode": "half_up", "digits": 0, "node": node} }

func when(node Node, op string, value float64, then, els Node) Node {
	return Node{"type": "if", "cond": Node{"node": node, "op": op, "value": value}, "then": then, "else": els}
}

func formula(t *testing.T, final Node, works []Planned, grades map[string]Grade, named ...any) *Formula {
	t.Helper()
	tree := NewTree(final, named...)
	if err := Check(tree); err != nil {
		t.Fatal(err)
	}
	return New(tree, works, grades)
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestWeightsAndMeans(t *testing.T) {
	fm := formula(t, total([]term{{0.3, part("ДЗ", "h1", "h2")}, {0.7, part("Экз", "ex")}}), plan("h1", "h2", "ex"),
		graded("h1", 6, "h2", 8, "ex", 9))
	if !near(fm.At(0), 0.3*7+0.7*9) || len(fm.OpenParts()) != 0 {
		t.Fatal(fm.At(0))
	}
}

func TestNestedNamedNodes(t *testing.T) {
	fm := formula(t, total([]term{{0.6, ref("Накоп")}, {0.4, part("Экз", "ex")}}), plan("h1", "t1", "ex"),
		graded("h1", 8, "t1", 6, "ex", 5), "Накоп", total([]term{{0.5, part("ДЗ", "h1")}, {0.5, part("КР", "t1")}}))
	if !near(fm.At(0), 0.6*7+0.4*5) {
		t.Fatal(fm.At(0))
	}
}

func TestBestKOfNWithWorksNotGradedYet(t *testing.T) {
	fm := formula(t, with(part("ДЗ", "h1", "h2", "h3"), "agg", "best", "k", 2), plan("h1", "h2", "h3"),
		graded("h1", 4, "h2", 9))
	if !near(fm.At(0), 6.5) || !near(fm.At(10), 9.5) || !reflect.DeepEqual(fm.OpenParts(), []string{"ДЗ"}) {
		t.Fatal(fm.At(0), fm.At(10))
	}
}

func TestOwnWeightsOfWorksAndPoints(t *testing.T) {
	weighted := Node{"type": "part", "name": "КР", "agg": "weighted",
		"works": []any{Node{"key": "t1", "weight": 0.4}, Node{"key": "t2", "weight": 0.6}}}
	if v := formula(t, weighted, plan("t1", "t2"), graded("t1", 5, "t2", 10)).At(0); !near(v, 8) {
		t.Fatal(v)
	}
	works := append(plan("a"), Planned{Key: "b", Label: "b", LabelRu: "B", Max: 30})
	byPoints := formula(t, with(part("Баллы", "a", "b"), "agg", "points"), works, graded("a", 10, "b", 5))
	if v := byPoints.At(0); !near(v, (10*10+5*30)/40.0) {
		t.Fatal(v)
	}
}

func TestRounding(t *testing.T) {
	for _, c := range []struct {
		value    float64
		mode     string
		digits   int
		expected float64
	}{{7.5, "half_up", 0, 8}, {6.5, "half_up", 0, 7}, {7.49, "half_up", 0, 7}, {7.9999999999, "floor", 0, 8},
		{7.01, "ceil", 0, 8}, {7.0000000001, "ceil", 0, 7}, {7.25, "half_up", 1, 7.3}, {7.99, "floor", 1, 7.9}} {
		if got := Rounded(c.value, c.mode, c.digits); !near(got, c.expected) {
			t.Errorf("%v %s %d: %v", c.value, c.mode, c.digits, got)
		}
	}
}

// blockingExam: Итог = экзамен, если он ниже 4, иначе round(0.6·Накоп + 0.4·Экз).
func blockingExam() Node {
	exam := part("Экз", "ex")
	return when(exam, "<", 4, exam, rnd(total([]term{{0.6, ref("Накоп")}, {0.4, exam}})))
}

func TestABlockingExam(t *testing.T) {
	if v := formula(t, blockingExam(), plan("h1", "ex"), graded("h1", 9, "ex", 3.5), "Накоп", part("ДЗ", "h1")).At(0); v != 3.5 {
		t.Fatal(v)
	}
	if v := formula(t, blockingExam(), plan("h1", "ex"), graded("h1", 9, "ex", 4), "Накоп", part("ДЗ", "h1")).At(0); v != 7 {
		t.Fatal(v)
	}
}

func goal(g float64) *float64 { return &g }

func TestAnExemption(t *testing.T) {
	exemption := when(ref("Накоп"), ">=", 8, rnd(ref("Накоп")), rnd(total([]term{{0.5, ref("Накоп")},
		{0.5, part("Экз")}})))
	fm := formula(t, exemption, plan("h1"), graded("h1", 8.5), "Накоп", part("ДЗ", "h1"))
	view := OutlookOf(fm, goal(9))
	if !view.Settled() || view.Low != 9 || !view.Reached() || !reflect.DeepEqual(view.Open, []string{"Экз"}) {
		t.Fatal(view)
	}
}

func TestABonusOnTopAndACap(t *testing.T) {
	capped := Node{"type": "min", "nodes": []any{Node{"type": "const", "value": 10},
		total([]term{{1, part("Работы", "w")}}, term{0.1, part("Активность", "act")})}}
	if v := formula(t, capped, plan("w", "act"), graded("w", 9, "act", 8)).At(0); !near(v, 9.8) {
		t.Fatal(v)
	}
	if v := formula(t, capped, plan("w", "act"), graded("w", 10, "act", 10)).At(0); v != 10 {
		t.Fatal(v)
	}
}

func TestTheBetterOfTwoWays(t *testing.T) {
	exam, acc := part("Экз", "ex"), part("Накоп", "h1")
	better := Node{"type": "max", "nodes": []any{exam, total([]term{{0.5, exam}, {0.5, acc}})}}
	if v := formula(t, better, plan("ex", "h1"), graded("ex", 6, "h1", 10)).At(0); v != 8 {
		t.Fatal(v)
	}
	if v := formula(t, better, plan("ex", "h1"), graded("ex", 9, "h1", 5)).At(0); v != 9 {
		t.Fatal(v)
	}
}

func TestABonusColumnAddsToItsWorkUpTo10(t *testing.T) {
	two, one := 2.0, 1.0
	grades := map[string]Grade{"h1": {Value: 9, Bonus: &two, Max: 10}, "h2": {Value: 6, Bonus: &one, Max: 10}}
	if v := formula(t, part("ДЗ", "h1", "h2"), plan("h1", "h2"), grades).At(0); !near(v, 8.5) {
		t.Fatal(v)
	}
}

func TestAPartWithNoWorksYetAndACountFromTheFormula(t *testing.T) {
	fm := formula(t, total([]term{{0.5, with(part("ДЗ", "h1"), "count", 4)}, {0.5, part("Экз")}}), plan("h1"),
		graded("h1", 8))
	if len(fm.Works("ДЗ")) != 4 || len(fm.Works("Экз")) != 1 || !near(fm.At(10), 0.5*(8+30)/4+5) {
		t.Fatal(fm.Works("ДЗ"))
	}
}

func TestWorksTheSheetNoLongerHasAreLeftOut(t *testing.T) {
	fm := formula(t, part("ДЗ", "h1", "gone"), plan("h1"), graded("h1", 7))
	if w := fm.Works("ДЗ"); len(w) != 1 || w[0].Key != "h1" || fm.At(0) != 7 {
		t.Fatal(w)
	}
}

func TestTheNeedRangeAndForecast(t *testing.T) {
	fm := formula(t, rnd(total([]term{{0.3, part("ДЗ", "h1", "h2", "h3")}, {0.7, part("Экз", "ex")}})),
		plan("h1", "h2", "h3", "ex"), graded("h1", 7, "h2", 9))
	view := OutlookOf(fm, goal(8))
	if view.Low != 2 || view.High != 10 || !near(*view.Need, 7.4) || *view.Forecast != math.RoundToEven(0.3*8+0.7*8) {
		t.Fatal(view, *view.Need, *view.Forecast)
	}
	if view.Reached() || view.OutOfReach() || !reflect.DeepEqual(view.Open, []string{"ДЗ", "Экз"}) {
		t.Fatal(view)
	}
}

func TestReachedAndOutOfReach(t *testing.T) {
	fm := formula(t, total([]term{{0.5, part("A", "a")}, {0.5, part("B", "b")}}), plan("a", "b"), graded("a", 2))
	if v := OutlookOf(fm, goal(7)); !v.OutOfReach() || v.Need != nil {
		t.Fatal(v)
	}
	if !OutlookOf(fm, goal(1)).Reached() || !near(*OutlookOf(fm, goal(4)).Need, 6) {
		t.Fatal("reach")
	}
}

func TestNeedOverABlockingExamThatMakesTheGradeGoDown(t *testing.T) {
	fm := formula(t, blockingExam(), plan("h1", "ex"), graded("h1", 2), "Накоп", part("ДЗ", "h1"))
	if !near(fm.At(3.9), 3.9) || fm.At(4) != 3 || !near(*OutlookOf(fm, goal(4)).Need, 5.8) {
		t.Fatal(fm.At(3.9), fm.At(4), *OutlookOf(fm, goal(4)).Need)
	}
}

func TestNothingGradedHasNoForecast(t *testing.T) {
	view := OutlookOf(formula(t, part("ДЗ", "h1"), plan("h1"), map[string]Grade{}), goal(6))
	if view.Forecast != nil || *view.Need != 6 {
		t.Fatal(view)
	}
}

func TestBadTrees(t *testing.T) {
	for _, raw := range []string{
		`{"final": {"type": "magic"}}`,
		`{"final": {"type": "ref", "name": "Накоп"}}`,
		`{"nodes": {"A": {"type": "ref", "name": "B"}, "B": {"type": "ref", "name": "A"}}, "final": {"type": "ref", "name": "A"}}`,
		`{"final": {"type": "part", "name": "ДЗ", "agg": "best", "works": []}}`,
		`{"final": {"type": "sum", "terms": [{"weight": "half", "node": {"type": "part", "name": "ДЗ"}}]}}`,
		`{"final": {"type": "if", "cond": {"node": {"type": "part", "name": "Экз"}, "op": "≈", "value": 4}, "then": {"type": "part", "name": "Экз"}, "else": {"type": "part", "name": "Экз"}}}`,
		`{"nodes": {}}`,
		`{"nodes": [], "final": {"type": "const", "value": 1}}`,
		`{"final": {"type": "part", "name": "ДЗ", "agg": "best", "k": 2.0}}`,
	} {
		tree, err := Parse([]byte(raw))
		if err == nil {
			err = Check(tree)
		}
		if _, ok := err.(*Error); !ok {
			t.Errorf("%s: %v", raw, err)
		}
	}
}

func TestParseKeepsOrderAndRoundTrips(t *testing.T) {
	raw := `{"nodes": {"Б": {"type": "const", "value": 1}, "А": {"type": "const", "value": 2.5}}, "final": {"type": "ref", "name": "А"}}`
	tree, err := Parse([]byte(raw))
	if err != nil || Check(tree) != nil || !reflect.DeepEqual(tree.Names, []string{"Б", "А"}) {
		t.Fatal(tree, err)
	}
	again, err := Parse([]byte(tree.JSON()))
	if err != nil || !reflect.DeepEqual(again.Names, tree.Names) || New(again, nil, nil).At(0) != 2.5 {
		t.Fatal(tree.JSON())
	}
}

func TestInWords(t *testing.T) {
	tree := NewTree(blockingExam(), "Накоп", total([]term{{0.5, with(part("ДЗ"), "agg", "best", "k", 4)},
		{0.5, part("КР")}}))
	if err := Check(tree); err != nil {
		t.Fatal(err)
	}
	want := []string{"Накоп = 0.5·ДЗ (лучшие 4) + 0.5·КР",
		"Итог = если Экз < 4, то Экз, иначе округл(0.6·Накоп + 0.4·Экз)"}
	if got := Describe(tree, "ru"); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	if got := Describe(tree, "en")[1]; got != "Final = if Экз < 4: Экз, else round(0.6·Накоп + 0.4·Экз)" {
		t.Fatal(got)
	}
}

func TestWarnings(t *testing.T) {
	tree := NewTree(total([]term{{0.6, ref("Накоп")}, {0.4, part("Экз")}}, term{0.1, part("Доп", "x")}),
		"Накоп", total([]term{{0.5, part("ДЗ", "h1")}, {0.4, part("КР", "t1")}}))
	got := Warnings(tree, plan("h1", "t1", "lab"))
	if len(got) != 4 || got[0].Kind != "weights" || got[0].Name != "Накоп" || *got[0].Number != 0.9 ||
		got[1] != (Warning{Kind: "empty", Name: "Экз"}) || got[2] != (Warning{Kind: "empty", Name: "Доп"}) ||
		got[3] != (Warning{Kind: "outside", Name: "LAB"}) {
		t.Fatal(got)
	}
}

func TestANewWorkJoinsThePartItsLabelMatches(t *testing.T) {
	tree := NewTree(total([]term{{0.5, with(part("ДЗ", "h1"), "match", []any{"ДЗ"})},
		{0.5, with(part("КР", "t1"), "match", []any{"КР"})}}))
	works := []Planned{{"h1", "HW 1", "ДЗ 1", 10}, {"t1", "Test 1", "КР 1", 10}, {"h2", "HW 2", "дз 2", 10},
		{"x", "Lab", "Лаба", 10}}
	fm := New(tree, works, graded("h1", 6, "h2", 8, "t1", 5))
	var keys []string
	for _, w := range fm.Works("ДЗ") {
		keys = append(keys, w.Key)
	}
	if !reflect.DeepEqual(keys, []string{"h1", "h2"}) || !near(fm.At(0), 0.5*7+0.5*5) {
		t.Fatal(keys)
	}
	if got := Warnings(tree, works); len(got) != 1 || got[0] != (Warning{Kind: "outside", Name: "Лаба"}) {
		t.Fatal(got)
	}
}

func TestTheLongestPrefixWins(t *testing.T) {
	tree := NewTree(total([]term{{0.3, with(part("КР"), "match", []any{"КР"})},
		{0.3, with(part("КР2"), "match", []any{"кр-2"})}, {0.4, with(part("Семинары"), "match", []any{"ДЗ"})}}))
	works := []Planned{{"t1", "Test 1", "КР 1", 10}, {"t2", "Test 2", "КР 2", 10}, {"h1", "HW Sep 17", "ДЗ 17 сен", 10},
		{"i1", "IHW 1", "ИДЗ-1", 10}}
	fm := New(tree, works, map[string]Grade{})
	var got [][]string
	for _, name := range []string{"КР", "КР2", "Семинары"} {
		var keys []string
		for _, w := range fm.Works(name) {
			keys = append(keys, w.Key)
		}
		got = append(got, keys)
	}
	if !reflect.DeepEqual(got, [][]string{{"t1"}, {"t2"}, {"h1"}}) {
		t.Fatal(got)
	}
	if w := Warnings(tree, works); len(w) != 1 || w[0] != (Warning{Kind: "outside", Name: "ИДЗ-1"}) {
		t.Fatal(w)
	}
}
