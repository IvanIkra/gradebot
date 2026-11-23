package bot

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/IvanIkra/gradebot/internal/books"
	"github.com/IvanIkra/gradebot/internal/formula"
	"github.com/IvanIkra/gradebot/internal/sheets"
)

var plan = []sheets.Work{{Key: "T|B|1", Tab: "ДЗ", Label: "HW 1", LabelRu: "ДЗ 1", Max: 10},
	{Key: "T|C|2", Tab: "ДЗ", Label: "HW 2", LabelRu: "ДЗ 2", Max: 10},
	{Key: "K|B|1", Tab: "КР", Label: "Test 1", LabelRu: "КР 1", Max: 10}}

func planRows() []PlanWork {
	var out []PlanWork
	for _, w := range plan {
		out = append(out, PlanWork{Key: w.Key, Tab: w.Tab, Label: w.Label, LabelRu: w.LabelRu, Max: w.Max})
	}
	return out
}

// node is a node as the schema has it: every field, the unused ones empty.
func node(id, kind, name string, fields string) string {
	base := `"id": "` + id + `", "type": "` + kind + `", "name": "` + name + `"`
	defaults := map[string]string{"agg": `"mean"`, "k": "null", "count": "null", "works": "[]", "match": "[]",
		"terms": "[]", "mode": `"half_up"`, "digits": "0", "args": "[]", "value": "null", "cond": "null",
		"then": `""`, "else": `""`}
	var given map[string]json.RawMessage
	_ = json.Unmarshal([]byte("{"+fields+"}"), &given)
	for _, key := range []string{"agg", "k", "count", "works", "match", "terms", "mode", "digits", "args", "value",
		"cond", "then", "else"} {
		value := defaults[key]
		if v, ok := given[key]; ok {
			value = string(v)
		}
		base += `, "` + key + `": ` + value
	}
	return "{" + base + "}"
}

func answerJSON(found bool, reason, final string, nodes ...string) map[string]any {
	raw := `{"found": ` + map[bool]string{true: "true", false: "false"}[found] + `, "reason": "` + reason +
		`", "final": "` + final + `", "nodes": [`
	for n, nd := range nodes {
		if n > 0 {
			raw += ", "
		}
		raw += nd
	}
	raw += "]}"
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		panic(raw)
	}
	return out
}

var answerNodes = []string{
	node("hw", "part", "ДЗ", `"agg": "best", "k": 1, "works": [{"work": "w1", "weight": null}, {"work": "w2", "weight": null}], "match": ["ДЗ"]`),
	node("kr", "part", "КР", `"works": [{"work": "w3", "weight": null}], "match": ["КР"]`),
	node("ex", "part", "Экз", `"match": ["Экз"]`),
	node("acc", "sum", "Накоп", `"terms": [{"weight": 0.5, "node": "hw", "bonus": false}, {"weight": 0.5, "node": "kr", "bonus": false}]`),
	node("mix", "sum", "", `"terms": [{"weight": 0.6, "node": "acc", "bonus": false}, {"weight": 0.4, "node": "ex", "bonus": false}]`),
	node("r", "round", "", `"args": ["mix"]`),
	node("f", "if", "Итог", `"cond": {"node": "ex", "op": "<", "value": 4}, "then": "ex", "else": "r"`),
}

func answer() map[string]any { return answerJSON(true, "", "f", answerNodes...) }

func formulaLLM(a map[string]any) *books.FakeLLM {
	return &books.FakeLLM{Answer: func(task, prompt string) map[string]any {
		if task != "formula" {
			panic(task)
		}
		return a
	}}
}

func TestTheFlatAnswerBecomesATree(t *testing.T) {
	tree, err := toTree(answer(), planRows())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tree.Names, []string{"Накоп"}) || tree.Final["type"] != "if" {
		t.Fatal(tree.Names, tree.Final)
	}
	terms := tree.Nodes["Накоп"]["terms"].([]any)
	part := terms[0].(formula.Node)["node"].(formula.Node)
	var keys []string
	for _, w := range part["works"].([]any) {
		keys = append(keys, w.(formula.Node)["key"].(string))
	}
	if part["agg"] != "best" || !reflect.DeepEqual(keys, []string{"T|B|1", "T|C|2"}) {
		t.Fatal(part)
	}
}

func TestAnswersThatAreNoFormula(t *testing.T) {
	for _, a := range []map[string]any{
		answerJSON(false, "this is a timetable", "f", answerNodes...),
		answerJSON(true, "", "nope", answerNodes...),
		answerJSON(true, "", "a",
			node("a", "sum", "A", `"terms": [{"weight": 1, "node": "b", "bonus": false}]`),
			node("b", "sum", "B", `"terms": [{"weight": 1, "node": "a", "bonus": false}]`)),
	} {
		if _, err := toTree(a, planRows()); err == nil {
			t.Error(a)
		} else if _, ok := err.(*formula.Error); !ok {
			t.Error(err)
		}
	}
}

// withMatan is the group's subject «Матан» of two sheets, laid out.
func withMatan(t *testing.T) (groupA1, int64) {
	g := withA1(t)
	subject, _ := g.bot.Subjects.Create("Матан", g.group.ID, 0)
	g.bot.Subjects.Put(subject.ID, g.shared["Матанализ ДЗ"])
	g.bot.Subjects.Put(subject.ID, g.shared["Матанализ колок"])
	g.Sheets.SetPlan(g.shared["Матанализ ДЗ"], plan[:2])
	g.Sheets.SetPlan(g.shared["Матанализ колок"], plan[2:])
	g.bot.LLM = formulaLLM(answer())
	return g, subject.ID
}

func TestTheAdminSetsTheGroupsFormula(t *testing.T) {
	g, matan := withMatan(t)
	g.as(ada, "/formula")
	g.press(ada, g.buttons(ada)["Матан"])
	contains(t, g.text(ada), "Формулы пока нет")
	g.press(ada, g.buttons(ada)["Задать для группы"])
	g.as(ada, "Итог = округл(0.6·Накоп + 0.4·Экз), экзамен ниже 4 — итог равен экзамену")
	contains(t, g.bot.LLM.(*books.FakeLLM).Prompts[0], "w1: «ДЗ 1» (HW 1), tab «ДЗ», sheet «Матанализ ДЗ», max 10")
	contains(t, g.text(ada), "Проверь формулу · Матан", "Накоп = 0.5·ДЗ (лучшие 1) + 0.5·КР",
		"Итог = если Экз &lt; 4, то Экз, иначе округл(0.6·Накоп + 0.4·Экз)", "<td><b>ДЗ</b></td><td>ДЗ 1, ДЗ 2</td>",
		"в таблицах пока нет работ", "для всей группы")
	g.press(ada, g.buttons(ada)["Верно"])
	if found := g.bot.Subjects.SubjectFormula(matan); found == nil || found.Tree.Final["type"] != "if" {
		t.Fatal(found)
	}
	g.as(bob, "/formula")
	g.press(bob, g.buttons(bob)["Матан"])
	contains(t, g.text(bob), "Формула предмета группы")
	if _, ok := g.buttons(bob)["Задать для группы"]; ok {
		t.Fatal(g.buttons(bob))
	}
	g.press(bob, sf("fm:e.%d.s", matan)) // not Bob's to set
	if g.bot.Asks[bob] != nil {
		t.Fatal("asked")
	}
}

func TestAMemberKeepsAnOwnFormula(t *testing.T) {
	g, matan := withMatan(t)
	g.press(bob, sf("fm:s.%d", matan))
	g.press(bob, g.buttons(bob)["Своя формула"])
	g.as(bob, "Итог = 0.5·ДЗ + 0.5·КР")
	contains(t, g.text(bob), "только для тебя")
	g.press(bob, "fm:ok")
	if g.bot.Subjects.OwnFormula(bob, matan) == nil || g.bot.Subjects.SubjectFormula(matan) != nil ||
		!g.bot.Subjects.FormulaFor(bob, matan).Own || g.bot.Subjects.FormulaFor(ada, matan) != nil {
		t.Fatal("own")
	}
	contains(t, g.text(bob), "Твоя формула")
	g.press(bob, g.buttons(bob)["Сбросить свою"])
	if g.bot.Subjects.OwnFormula(bob, matan) != nil {
		t.Fatal("kept")
	}
}

func TestALoneSheetBecomesASubjectOfOnesOwn(t *testing.T) {
	g, _ := withMatan(t)
	g.as(bob, "/formula")
	g.press(bob, g.buttons(bob)["Алгебра"])
	own := g.bot.Subjects.Scope(0, bob)
	if len(own) != 1 || !reflect.DeepEqual(g.bot.Subjects.SheetsOf(own[0].ID), []int64{g.shared["Алгебра"]}) {
		t.Fatal(own)
	}
	contains(t, g.text(bob), "Формула · ")
}

func TestWhatCannotBeRead(t *testing.T) {
	g, matan := withMatan(t)
	g.bot.LLM = formulaLLM(answerJSON(false, "no formula here", "f", answerNodes...))
	g.press(ada, sf("fm:e.%d.s", matan))
	g.as(ada, "привет")
	contains(t, g.text(ada), "Не получилось разобрать формулу: no formula here")
	g.press(ada, "fm:ok")
	if g.bot.Subjects.SubjectFormula(matan) != nil {
		t.Fatal("kept")
	}
	g.bot.LLM = nil
	g.press(ada, sf("fm:e.%d.s", matan))
	g.as(ada, "Итог = ДЗ")
	contains(t, g.text(ada), "Разбор формул выключен")
}

func TestTheOwnerSetsItFromTheSubjectsCard(t *testing.T) {
	g, matan := withMatan(t)
	g.press(owner, sf("sj:o.%d", matan))
	g.press(owner, g.buttons(owner)["Формула"])
	g.press(owner, g.buttons(owner)["Задать для группы"])
	g.say(owner, "Итог = 0.5·ДЗ + 0.5·КР")
	g.press(owner, "fm:ok")
	if g.bot.Subjects.SubjectFormula(matan) == nil {
		t.Fatal("not kept")
	}
}
