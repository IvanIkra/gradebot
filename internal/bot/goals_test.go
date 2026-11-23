package bot

import (
	"testing"
	"time"

	"github.com/IvanIkra/gradebot/internal/formula"
	"github.com/IvanIkra/gradebot/internal/layout"
)

func TestOutlookInWords(t *testing.T) {
	g := func(v float64) *float64 { return &v }
	for _, c := range []struct {
		view formula.Outlook
		text string
	}{
		{formula.Outlook{Low: 7, High: 7, Forecast: g(7), Goal: g(8)}, "Итог: 7 · цель 8 не набрана"},
		{formula.Outlook{Low: 9, High: 9, Forecast: g(9), Open: []string{"Экз"}, Goal: g(8)},
			"Итог уже не зависит от оставшегося: 9 · цель 8 набрана"},
		{formula.Outlook{Low: 2, High: 10, Forecast: g(8), Open: []string{"ДЗ", "Экз"}, Goal: g(8), Need: g(7.4)},
			"Итог будет от 2 до 10 · прогноз 8 · до 8 нужно в среднем 7.4 на оставшемся"},
		{formula.Outlook{Low: 3, High: 9, Open: []string{"Экз"}, Goal: g(6), Need: g(5.75)},
			"Итог будет от 3 до 9 · до 6 нужно 5.8 за «Экз»"},
		{formula.Outlook{Low: 6, High: 9, Forecast: g(8), Open: []string{"Экз"}, Goal: g(6)},
			"Итог будет от 6 до 9 · прогноз 8 · цель 6 уже гарантирована"},
		{formula.Outlook{Low: 2, High: 7, Forecast: g(5), Open: []string{"Экз"}, Goal: g(8)},
			"Итог будет от 2 до 7 · прогноз 5 · цель 8 недостижима"},
		{formula.Outlook{Low: 2, High: 7, Forecast: g(5), Open: []string{"Экз"}}, "Итог будет от 2 до 7 · прогноз 5"},
	} {
		if got := outlookText(c.view, "ru"); got != c.text {
			t.Errorf("%s\n%s", got, c.text)
		}
	}
}

func goalItem(key, tab, label string, value float64) layout.Item {
	return layout.Item{Key: key, Tab: tab, Header: key, Label: label, Value: value, Raw: value, Max: 10, LabelRu: label}
}

// withGraded: the group's formula for Матан, Bob's homework 8 and test 6.
func withGraded(t *testing.T) (groupA1, int64, int64, int64) {
	g, matan := withMatan(t)
	tree, err := toTree(answer(), g.bot.Subjects.Plan(matan))
	if err != nil {
		t.Fatal(err)
	}
	g.bot.Subjects.SetFormula(matan, "…", tree, 0)
	calcID, colloqID := g.shared["Матанализ ДЗ"], g.shared["Матанализ колок"]
	g.bot.Grades.Record(bob, calcID, []layout.Item{goalItem("T|B|1", "ДЗ", "ДЗ 1", 8)}, 1, "")
	g.bot.Grades.Record(bob, colloqID, []layout.Item{goalItem("K|B|1", "КР", "КР 1", 6)}, 1, "")
	return g, matan, calcID, colloqID
}

func TestAGoalAndWhatItNeeds(t *testing.T) {
	g, sid, _, _ := withGraded(t)
	g.as(bob, "/goal")
	g.press(bob, g.buttons(bob)["Матан"])
	contains(t, g.text(bob), "Цели пока нет", "Итог будет от 0 до 9 · прогноз 7")
	view := g.buttons(bob)
	g.press(bob, view["8"])
	if g.bot.Subjects.Goal(bob, sid) != 8 {
		t.Fatal(g.bot.Subjects.Goal(bob, sid))
	}
	// ДЗ: the best of 8 and x; КР 6; the exam x: round(0.6·(0.5·x + 3) + 0.4·x) reaches 8 from 8.2
	contains(t, g.text(bob), "Твоя цель: <b>8</b>", "до 8 нужно в среднем 8.2 на оставшемся")
	view = g.buttons(bob)
	g.as(bob, "/grades")
	contains(t, g.text(bob), "Итог будет от 0 до 9 · прогноз 7 · до 8 нужно в среднем 8.2")
	if got := g.buttons(bob); got["Цель"] != sf("gl:s.%d", sid) || got["Формула"] != sf("fm:s.%d", sid) {
		t.Fatal(got)
	}
	g.press(bob, view["Без цели"])
	if g.bot.Subjects.Goal(bob, sid) != 0 {
		t.Fatal("kept")
	}
}

func TestNewGradesCarryTheLineOnlyWithAGoal(t *testing.T) {
	g, sid, calcID, _ := withGraded(t)
	g.Clock.Advance(30 * time.Minute)
	g.bot.Grades.Record(bob, calcID, []layout.Item{goalItem("T|B|1", "ДЗ", "ДЗ 1", 9)}, 1, "")
	g.bot.SendGrades()
	lacks(t, g.text(bob), "Итог будет")
	g.bot.Subjects.SetGoal(bob, sid, 7)
	g.Clock.Advance(30 * time.Minute)
	g.bot.Grades.Record(bob, calcID, []layout.Item{goalItem("T|B|1", "ДЗ", "ДЗ 1", 10)}, 1, "")
	g.bot.SendGrades()
	contains(t, g.text(bob), "до 7 нужно")
}

func TestAGoalWithoutAFormulaAsksForOne(t *testing.T) {
	g, matan := withMatan(t)
	g.bot.Subjects.SetGoal(bob, matan, 8)
	g.press(bob, sf("gl:s.%d", matan))
	contains(t, g.text(bob), "чтобы её считать, предмету нужна формула: /formula")
}
