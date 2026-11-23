package bot

import (
	"encoding/json"
	"sort"

	"github.com/IvanIkra/gradebot/internal/access"
	"github.com/IvanIkra/gradebot/internal/formula"
	"github.com/IvanIkra/gradebot/internal/rich"
)

// /goal and the outlook of a subject's grade: a line under the subject in /grades, and under its new grades when the
// user has a goal in it.
//
// The outlook comes from the subject's formula (the user's own, else the subject's) and the user's grades in the
// subject's sheets (formula.OutlookOf): the range the final grade may still take, a forecast, and for a goal what is
// needed on what is left, or that it is reached or out of reach. Without a formula there is nothing to count: a goal
// then only asks for one.
//
// Callbacks `gl:<action>.<arg>`: `s.<subject>` shows a subject's goal, `n.<sheet>` makes a subject of a lone sheet
// first, `v.<subject>.<goal>` sets the goal (0 drops it).

const minGoal, maxGoal = 4, 10

// deepCopy copies a tree node through JSON values.
func deepCopy(v any) any {
	switch x := v.(type) {
	case formula.Node:
		out := formula.Node{}
		for k, val := range x {
			out[k] = deepCopy(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for n, val := range x {
			out[n] = deepCopy(val)
		}
		return out
	}
	return v
}

// aliased is the tree with the works of its parts that copy another one replaced by it, each once.
func aliased(tree *formula.Tree, aliases map[string]string) *formula.Tree {
	copied := &formula.Tree{Names: append([]string(nil), tree.Names...), Nodes: map[string]formula.Node{}}
	for name, node := range tree.Nodes {
		copied.Nodes[name] = deepCopy(node).(formula.Node)
	}
	copied.Final = deepCopy(tree.Final).(formula.Node)
	copied = formula.NewTree(copied.Final, namedPairs(copied)...)
	for _, part := range formula.Parts(copied) {
		seen := map[string]bool{}
		works := []any{}
		raw, _ := part["works"].([]any)
		for _, w := range raw {
			work, _ := w.(formula.Node)
			key, _ := work["key"].(string)
			if alias, ok := aliases[key]; ok {
				key = alias
			}
			if !seen[key] {
				seen[key] = true
				changed := formula.Node{}
				for k, v := range work {
					changed[k] = v
				}
				changed["key"] = key
				works = append(works, changed)
			}
		}
		part["works"] = works
	}
	return copied
}

func namedPairs(t *formula.Tree) []any {
	var out []any
	for _, name := range t.Names {
		out = append(out, name, t.Nodes[name])
	}
	return out
}

// outlookText is «From 5 to 9 · forecast 8 · 8 needs 7.4 on average on what is left».
func outlookText(view formula.Outlook, lang string) string {
	goal := 0
	if view.Goal != nil {
		goal = int(*view.Goal)
	}
	if view.Settled() {
		text := tr(lang, "Final grade: ", "Итог: ")
		if len(view.Open) > 0 {
			text = tr(lang, "What is left no longer changes the final grade: ", "Итог уже не зависит от оставшегося: ")
		}
		text += score(view.Low)
		if view.Goal != nil {
			if view.Reached() {
				text += " · " + tr(lang, sf("goal %d reached", goal), sf("цель %d набрана", goal))
			} else {
				text += " · " + tr(lang, sf("goal %d not reached", goal), sf("цель %d не набрана", goal))
			}
		}
		return text
	}
	parts := []string{tr(lang, sf("The final grade will be %s to %s", score(view.Low), score(view.High)),
		sf("Итог будет от %s до %s", score(view.Low), score(view.High)))}
	if view.Forecast != nil {
		parts = append(parts, tr(lang, "forecast "+score(*view.Forecast), "прогноз "+score(*view.Forecast)))
	}
	if view.Goal != nil {
		switch {
		case view.Reached():
			parts = append(parts, tr(lang, sf("goal %d is sure even with zeros", goal), sf("цель %d уже гарантирована", goal)))
		case view.OutOfReach():
			parts = append(parts, tr(lang, sf("goal %d is out of reach", goal), sf("цель %d недостижима", goal)))
		case view.Need != nil && len(view.Open) == 1:
			parts = append(parts, tr(lang, sf("%d needs %s in «%s»", goal, score(*view.Need), view.Open[0]),
				sf("до %d нужно %s за «%s»", goal, score(*view.Need), view.Open[0])))
		case view.Need != nil:
			parts = append(parts, tr(lang, sf("%d needs %s on average on what is left", goal, score(*view.Need)),
				sf("до %d нужно в среднем %s на оставшемся", goal, score(*view.Need))))
		}
	}
	return joinStrings(parts, " · ")
}

func joinStrings(parts []string, sep string) string {
	out := ""
	for n, p := range parts {
		if n > 0 {
			out += sep
		}
		out += p
	}
	return out
}

func plannedOf(plan []PlanWork) []formula.Planned {
	out := make([]formula.Planned, len(plan))
	for n, w := range plan {
		out[n] = formula.Planned{Key: w.Key, Label: w.Label, LabelRu: w.LabelRu, Max: w.Max}
	}
	return out
}

// subjectFormulaOf is the formula of a subject with the user's grades in it, or nil without a formula.
func (b *Bot) subjectFormulaOf(user access.User, sid int64) *formula.Formula {
	found := b.Subjects.FormulaFor(user.TgID, sid)
	if found == nil {
		return nil
	}
	plan, aliases := mergeCopies(b.Subjects.Plan(sid))
	var items []*Row
	for _, sheet := range b.Subjects.SheetsOf(sid) {
		for _, it := range b.Grades.Items(user.TgID, sheet) {
			items = append(items, rowOfItem(it))
		}
	}
	sort.SliceStable(items, func(i, j int) bool { // a work's own grade before a copy's
		_, a := aliases[subjectKey(items[i].Sheet, items[i].Key)]
		_, c := aliases[subjectKey(items[j].Sheet, items[j].Key)]
		return !a && c
	})
	got := map[string]formula.Grade{}
	for _, it := range items {
		key := subjectKey(it.Sheet, it.Key)
		if alias, ok := aliases[key]; ok {
			key = alias
		}
		if _, ok := got[key]; !ok {
			got[key] = formula.Grade{Value: it.Value, Bonus: it.Bonus, Max: it.Max}
		}
	}
	return formula.New(aliased(found.Tree, aliases), plannedOf(plan), got)
}

// outlookNote is the line under a subject: the outlook; with goalOnly, only when the user has a goal in it.
func (b *Bot) outlookNote(user access.User, sid int64, lang string, goalOnly bool) string {
	goal := b.Subjects.Goal(user.TgID, sid)
	if goalOnly && goal == 0 {
		return ""
	}
	fm := b.subjectFormulaOf(user, sid)
	if fm == nil {
		if goal != 0 {
			return tr(lang, sf("Goal %d: to count it, the subject needs a formula: /formula", goal),
				sf("Цель %d: чтобы её считать, предмету нужна формула: /formula", goal))
		}
		return ""
	}
	var g *float64
	if goal != 0 {
		value := float64(goal)
		g = &value
	}
	return outlookText(formula.OutlookOf(fm, g), lang)
}

func (b *Bot) withNotes(user access.User, units []*Unit, goalOnly bool) []*Unit {
	for _, unit := range units {
		if unit.Subject != nil {
			unit.Note = b.outlookNote(user, unit.Subject.ID, user.Lang, goalOnly)
		}
	}
	return units
}

// cmdGoal is /goal: the user's subjects and lone sheets to pick from, or the one there is.
func (b *Bot) cmdGoal(chat int64, user access.User, arg string) {
	lang := user.Lang
	units := b.Subjects.Units(user, b.Sheets.Followed(user.TgID))
	if len(units) == 0 {
		b.send(chat, rich.New().P(noSheets(lang)).Buttons(addButton(lang)))
		return
	}
	if len(units) == 1 && units[0].Subject != nil {
		b.send(chat, b.goalView(user, units[0].Subject.ID))
		return
	}
	doc := rich.New().H(tr(lang, "Goal", "Цель")).P(tr(lang,
		"In which subject? A sheet in no subject becomes a subject of its own.",
		"По какому предмету? Таблица вне предметов станет своим предметом."))
	var choice []rich.Button
	for _, u := range units {
		data := "gl:n." + itoa(u.Sheets[0].ID)
		if u.Subject != nil {
			data = "gl:s." + itoa(u.Subject.ID)
		}
		choice = append(choice, rich.B(u.Name, data))
	}
	for start := 0; start < len(choice); start += 3 {
		doc.Buttons(choice[start:min(len(choice), start+3)]...)
	}
	b.send(chat, doc)
}

func (b *Bot) goalView(user access.User, sid int64) *rich.Doc {
	lang := user.Lang
	subject, _ := b.Subjects.Get(sid)
	goal := b.Subjects.Goal(user.TgID, sid)
	doc := rich.New().H(tr(lang, "Goal · ", "Цель · ") + subject.Name)
	if goal != 0 {
		doc.P(tr(lang, "Your goal: ", "Твоя цель: "), rich.Bold(itoa(int64(goal))))
	} else {
		doc.P(tr(lang, "No goal yet: pick the grade you aim at.", "Цели пока нет: выбери оценку, к которой идёшь."))
	}
	if note := b.outlookNote(user, sid, lang, false); note != "" {
		doc.P(rich.Italic(note))
	}
	var choice []rich.Button
	for n := minGoal; n <= maxGoal; n++ {
		style := ""
		if n == goal {
			style = "primary"
		}
		choice = append(choice, rich.BS(itoa(int64(n)), sf("gl:v.%d.%d", sid, n), style))
	}
	doc.Buttons(choice[:4]...)
	rest := choice[4:]
	if goal != 0 {
		rest = append(rest, rich.B(tr(lang, "No goal", "Без цели"), sf("gl:v.%d.0", sid)))
	}
	doc.Buttons(rest...)
	return doc.Buttons(rich.B(tr(lang, "Formula", "Формула"), "fm:s."+itoa(sid)))
}

func (b *Bot) goalCallback(chat, messageID int64, user access.User, arg, lang string) string {
	action, _, rest := partition(arg, ".")
	if action == "n" && isDigits(rest) {
		for _, s := range b.Sheets.Followed(user.TgID) {
			if s.ID == atoi(rest) {
				b.edit(chat, messageID, b.goalView(user, b.ownSubject(user, s).ID))
				return ""
			}
		}
		return ""
	}
	sid, _, value := partition(rest, ".")
	var subject Subject
	ok := false
	if isDigits(sid) {
		subject, ok = b.Subjects.Get(atoi(sid))
	}
	if !ok || !b.sees(&user, subject) {
		return tr(lang, "No such subject", "Такого предмета нет")
	}
	switch {
	case action == "s":
		b.edit(chat, messageID, b.goalView(user, subject.ID))
	case action == "v" && isDigits(value) && (value == "0" || (atoi(value) >= minGoal && atoi(value) <= maxGoal)):
		b.Subjects.SetGoal(user.TgID, subject.ID, int(atoi(value)))
		b.edit(chat, messageID, b.goalView(user, subject.ID))
		return tr(lang, "Saved", "Сохранено")
	}
	return ""
}

var _ = json.Marshal
