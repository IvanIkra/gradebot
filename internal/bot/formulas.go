package bot

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/IvanIkra/gradebot/internal/access"
	"github.com/IvanIkra/gradebot/internal/formula"
	"github.com/IvanIkra/gradebot/internal/llm"
	"github.com/IvanIkra/gradebot/internal/pyfmt"
	"github.com/IvanIkra/gradebot/internal/rich"
	"github.com/IvanIkra/gradebot/internal/sheets"
)

// /formula: a subject's grade formula from the teacher's text, by Haiku, checked by a person before it is kept.
//
// Haiku turns the text and the subject's works into a flat list of nodes (structured output has no recursion),
// which becomes the formula package's tree here. It also lays the works out by part and gives each part label
// prefixes, so a work added later joins its part without asking again. The person sees the formula in words, the
// works of each part and what looks wrong, and keeps it with «Correct» or sends the text again.
//
// A subject's formula is set by whoever runs the subject (subjects.go); anyone who sees a subject may keep their
// own, which wins over it. A sheet in no subject gets a subject of its own when a formula is set for it.
//
// Callbacks `fm:<action>.<arg>`: `s.<subject>` shows a subject's formula, `n.<sheet>` makes a subject of a lone
// sheet and shows it, `e.<subject>.<s|u>` asks for the text (s: the subject's, u: one's own), `ok` keeps the formula
// just read, `x.<subject>` drops one's own.

const (
	TextChars        = 3000
	DraftFor         = time.Hour
	formulaMaxTokens = 4000
)

const FormulaSystem = `You turn the grade formula of a university course into a tree of nodes, and lay the course's
graded works out by the parts of the formula.

You get the text the teacher or a student wrote (it may be a whole paragraph of a syllabus: find the formula in it)
and the list of graded works the bot knows, each with an id like w3, its label, its tab and its maximum. Grades
are on a 0-10 scale.

Nodes (each has an id; children are referred to by id):
- part: works of one kind (homeworks, tests, the exam). agg: mean (the default), best (the mean of the k best:
  "4 best of 5", "the worst test is dropped" = best with k = count - 1), weighted (each work its own weight, given
  in works[].weight), points (sum of points over the sum of maximums). works: the ids of the works of this part.
  match: short label prefixes that will catch works of this part added later (for homeworks "ДЗ", "HW"); parts
  of one kind told apart by a number get the number in their prefixes ("КР1" and "КР2", not "КР" for both). count:
  how many works the part will have, only when the text says it. A part with no works yet (an exam not in the
  sheet) has works = [] and still its match.
- sum: weighted terms; percentages become fractions (30% -> 0.3). A bonus on top of the weights (activity points
  "plus up to 1 point") is a term with bonus = true.
- round: args = [the node rounded]; mode half_up (ordinary rounding, the default when the text says "rounded"),
  floor or ceil; digits after the point. Only when the text says it is rounded.
- min / max: args = the nodes; "at most 10" is min with a const 10.
- const: value.
- if: cond = {node, op, value}, then, else. A blocking exam ("an exam below 4 means the final grade is the exam")
  is if(exam < 4, exam, rest). An exemption ("with an accumulated 8 or more the exam may be skipped and the final is
  the accumulated grade") is if(accumulated >= 8, accumulated, rest).

name: the name of the node as the text calls it, short, in the text's language ("Накоп", "ДЗ", "Экзамен"); a part
always has a name; another node has one only when the text names it (an accumulated grade), else "". final: the id
of the node that is the final grade.

Use only what the text says; do not invent weights, rounding or conditions. Every work goes into at most one part.
A part the teacher grades as a whole (seminars, activity) takes the works the text says its grade counts or is made
of: "the seminar grade counts the current homeworks" puts the homeworks in the seminars part, with their match. Only
works the formula does not use at all stay out. When the text has no grade formula, found = false and reason says why.
Fields a node type does not use: empty strings, empty lists, null, 0 or false.`

func strictObject(order []string, properties map[string]any) map[string]any {
	required := make([]any, len(order))
	for n, name := range order {
		required[n] = name
	}
	return map[string]any{"type": "object", "properties": properties, "required": required,
		"additionalProperties": false}
}

var (
	idType       = map[string]any{"type": "string"}
	numberOrNull = map[string]any{"anyOf": []any{map[string]any{"type": "number"}, map[string]any{"type": "null"}}}
	intOrNull    = map[string]any{"anyOf": []any{map[string]any{"type": "integer"}, map[string]any{"type": "null"}}}
)

// FormulaSchema is the flat answer.
var FormulaSchema = strictObject([]string{"found", "reason", "final", "nodes"}, map[string]any{
	"found":  map[string]any{"type": "boolean"},
	"reason": map[string]any{"type": "string"},
	"final":  idType,
	"nodes": map[string]any{"type": "array", "items": strictObject([]string{"id", "type", "name", "agg", "k",
		"count", "works", "match", "terms", "mode", "digits", "args", "value", "cond", "then", "else"},
		map[string]any{
			"id":    idType,
			"type":  map[string]any{"type": "string", "enum": []any{"part", "sum", "const", "round", "min", "max", "if"}},
			"name":  map[string]any{"type": "string"},
			"agg":   map[string]any{"type": "string", "enum": []any{"mean", "best", "weighted", "points"}},
			"k":     intOrNull,
			"count": intOrNull,
			"works": map[string]any{"type": "array", "items": strictObject([]string{"work", "weight"},
				map[string]any{"work": idType, "weight": numberOrNull})},
			"match": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"terms": map[string]any{"type": "array", "items": strictObject([]string{"weight", "node", "bonus"},
				map[string]any{"weight": map[string]any{"type": "number"}, "node": idType,
					"bonus": map[string]any{"type": "boolean"}})},
			"mode":   map[string]any{"type": "string", "enum": []any{"half_up", "floor", "ceil"}},
			"digits": map[string]any{"type": "integer"},
			"args":   map[string]any{"type": "array", "items": idType},
			"value":  numberOrNull,
			"cond": map[string]any{"anyOf": []any{strictObject([]string{"node", "op", "value"}, map[string]any{
				"node": idType, "op": map[string]any{"type": "string", "enum": []any{"<", "<=", ">", ">="}},
				"value": map[string]any{"type": "number"}}), map[string]any{"type": "null"}}},
			"then": idType,
			"else": idType,
		})},
})

func formulaPrompt(text string, plan []PlanWork) string {
	var works []string
	for n, w := range plan {
		label := w.LabelRu
		if label == "" {
			label = w.Label
		}
		works = append(works, sf("w%d: «%s» (%s), tab «%s», sheet «%s», max %s", n+1, label, w.Label, w.Tab,
			w.SheetName, pyfmt.G(w.Max, 6)))
	}
	list := strings.Join(works, "\n")
	if list == "" {
		list = "(none known yet)"
	}
	return "The formula:\n" + text + "\n\nThe works:\n" + list
}

func getStr(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

// whole is a JSON number as an int when it is whole, as Python's json had it.
func whole(v any) any {
	if f, ok := v.(float64); ok && f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return int(f)
	}
	return v
}

func orDefault(v any, def any) any {
	if truthy(v) {
		return v
	}
	return def
}

// toTree makes the flat answer the formula package's tree; a formula.Error when it is no formula or does not hold
// together.
func toTree(answer map[string]any, plan []PlanWork) (*formula.Tree, error) {
	if !truthy(answer["found"]) {
		reason := getStr(answer, "reason")
		if reason == "" {
			reason = "no formula in the text"
		}
		return nil, &formula.Error{Msg: reason}
	}
	keys := map[string]string{}
	for n, w := range plan {
		keys[sf("w%d", n+1)] = w.Key
	}
	byID := map[string]map[string]any{}
	nodes, _ := answer["nodes"].([]any)
	for _, raw := range nodes {
		if node, ok := raw.(map[string]any); ok {
			byID[getStr(node, "id")] = node
		}
	}
	named := map[string]formula.Node{}
	var order []string
	names := map[string]string{} // a named node's name -> its id: two nodes of one name get two names
	var build func(nid string, path []string, top bool) (formula.Node, error)
	var inner func(node map[string]any, path []string) (formula.Node, error)
	build = func(nid string, path []string, top bool) (formula.Node, error) {
		for _, p := range path {
			if p == nid {
				return nil, &formula.Error{Msg: "node " + nid + " refers to itself"}
			}
		}
		node, ok := byID[nid]
		if !ok {
			return nil, &formula.Error{Msg: "no node " + nid}
		}
		name := trimPy(getStr(node, "name"))
		if getStr(node, "type") != "part" && name != "" && !top {
			for {
				if have, ok := names[name]; !ok {
					names[name] = nid
					break
				} else if have == nid {
					break
				}
				name += "'"
			}
			if _, ok := named[name]; !ok {
				named[name] = formula.Node{} // taken before it is built: a circle meets the path check above
				order = append(order, name)
				built, err := inner(node, append(append([]string(nil), path...), nid))
				if err != nil {
					return nil, err
				}
				named[name] = built
			}
			return formula.Node{"type": "ref", "name": name}, nil
		}
		return inner(node, append(append([]string(nil), path...), nid))
	}
	inner = func(node map[string]any, path []string) (formula.Node, error) {
		kind := getStr(node, "type")
		args, _ := node["args"].([]any)
		switch kind {
		case "part":
			name := getStr(node, "name")
			if name == "" {
				name = getStr(node, "id")
			}
			works := []any{}
			raw, _ := node["works"].([]any)
			for _, w := range raw {
				work, _ := w.(map[string]any)
				if key, ok := keys[getStr(work, "work")]; ok {
					works = append(works, formula.Node{"key": key, "weight": whole(orDefault(work["weight"], 1))})
				}
			}
			match, _ := node["match"].([]any)
			if match == nil {
				match = []any{}
			}
			return formula.Node{"type": "part", "name": trimPy(name), "agg": orDefault(node["agg"], "mean"),
				"k": whole(node["k"]), "count": whole(node["count"]), "match": match, "works": works}, nil
		case "sum":
			terms := []any{}
			raw, _ := node["terms"].([]any)
			for _, t := range raw {
				term, _ := t.(map[string]any)
				child, err := build(getStr(term, "node"), path, false)
				if err != nil {
					return nil, err
				}
				terms = append(terms, formula.Node{"weight": whole(term["weight"]), "node": child,
					"bonus": truthy(term["bonus"])})
			}
			return formula.Node{"type": "sum", "terms": terms}, nil
		case "const":
			return formula.Node{"type": "const", "value": whole(orDefault(node["value"], 0))}, nil
		case "round":
			if len(args) == 0 {
				return nil, &formula.Error{Msg: "a round of nothing"}
			}
			child, err := build(fmt.Sprint(args[0]), path, false)
			if err != nil {
				return nil, err
			}
			return formula.Node{"type": "round", "mode": orDefault(node["mode"], "half_up"),
				"digits": whole(orDefault(node["digits"], 0)), "node": child}, nil
		case "min", "max":
			children := []any{}
			for _, a := range args {
				child, err := build(fmt.Sprint(a), path, false)
				if err != nil {
					return nil, err
				}
				children = append(children, child)
			}
			return formula.Node{"type": kind, "nodes": children}, nil
		}
		cond, _ := node["cond"].(map[string]any)
		if len(cond) == 0 {
			return nil, &formula.Error{Msg: "an if without a condition"}
		}
		condNode, err := build(getStr(cond, "node"), path, false)
		if err != nil {
			return nil, err
		}
		then, err := build(getStr(node, "then"), path, false)
		if err != nil {
			return nil, err
		}
		els, err := build(getStr(node, "else"), path, false)
		if err != nil {
			return nil, err
		}
		return formula.Node{"type": "if", "cond": formula.Node{"node": condNode, "op": cond["op"],
			"value": whole(cond["value"])}, "then": then, "else": els}, nil
	}
	final, err := build(getStr(answer, "final"), nil, true)
	if err != nil {
		return nil, err
	}
	var pairs []any
	for _, name := range order {
		pairs = append(pairs, name, named[name])
	}
	tree := formula.NewTree(final, pairs...)
	if err := formula.Check(tree); err != nil {
		return nil, err
	}
	// as kept and read back: numbers as JSON has them
	return formula.Parse([]byte(tree.JSON()))
}

func formulaText(value string) (string, error) {
	if n := utf8.RuneCountInString(value); n == 0 || n > TextChars {
		return "", fmt.Errorf("1 to %d characters", TextChars)
	}
	return value, nil
}

// Draft is a formula read and shown, waiting for «Correct».
type Draft struct {
	Subject int64
	Target  string // "s": the subject's, "u": the user's own
	Text    string
	Tree    *formula.Tree
	At      time.Time
}

// sees: a user's own subject, or one of their group; the owner sees every group's.
func (b *Bot) sees(who *access.User, subject Subject) bool {
	if who == nil {
		return subject.Grp != 0
	}
	return subject.TgID == who.TgID || (subject.Grp != 0 && subject.Grp == who.Grp)
}

// cmdFormula is /formula: the user's subjects and lone sheets to pick from, or the one there is.
func (b *Bot) cmdFormula(chat int64, user access.User, arg string) {
	lang := user.Lang
	units := b.Subjects.Units(user, b.Sheets.Followed(user.TgID))
	if len(units) == 0 {
		b.send(chat, rich.New().P(noSheets(lang)).Buttons(addButton(lang)))
		return
	}
	if len(units) == 1 {
		unit := units[0]
		if unit.Subject != nil {
			if doc := b.formulaView(&user, unit.Subject.ID, lang); doc != nil {
				b.send(chat, doc)
				return
			}
		}
		b.send(chat, loneSheetView(lang, unit.Sheets[0]))
		return
	}
	doc := rich.New().H(tr(lang, "Formula", "Формула")).P(tr(lang,
		"Which subject's? A sheet in no subject becomes a subject of its own.",
		"Для какого предмета? Таблица вне предметов станет своим предметом."))
	var choice []rich.Button
	for _, u := range units {
		data := "fm:n." + itoa(u.Sheets[0].ID)
		if u.Subject != nil {
			data = "fm:s." + itoa(u.Subject.ID)
		}
		choice = append(choice, rich.B(u.Name, data))
	}
	for start := 0; start < len(choice); start += 3 {
		doc.Buttons(choice[start:min(len(choice), start+3)]...)
	}
	b.send(chat, doc)
}

func loneSheetView(lang string, sheet sheets.Sheet) *rich.Doc {
	return rich.New().P(tr(lang, "A formula belongs to a subject: the sheet becomes a subject of its own.",
		"Формула задаётся предмету: таблица станет своим предметом.")).Buttons(
		rich.BS(tr(lang, "Go on", "Дальше"), "fm:n."+itoa(sheet.ID), "primary"))
}

// ownSubject is a subject of the user's own made of a lone sheet: named by its course, numbered when the name is
// taken.
func (b *Bot) ownSubject(user access.User, sheet sheets.Sheet) Subject {
	course := []rune(sheets.CourseOf(sheet))
	base, err := SubjectName(string(course[:min(len(course), 10)]))
	if err != nil {
		panic(err)
	}
	for n := 1; n < 100; n++ {
		name := base
		if n > 1 {
			digits := itoa(int64(n))
			r := []rune(base)
			name, err = SubjectName(string(r[:min(len(r), 10-len(digits))]) + digits)
			if err != nil {
				panic(err)
			}
		}
		subject, err := b.Subjects.Create(name, 0, user.TgID)
		if err != nil {
			var se *SubjectError
			if errors.As(err, &se) {
				continue
			}
			panic(err)
		}
		b.Subjects.Put(subject.ID, sheet.ID)
		return subject
	}
	panic(&SubjectError{"no free name"})
}

// formulaView is a subject's formula in words, and what one may do with it; nil for a subject who cannot see.
func (b *Bot) formulaView(who *access.User, sid int64, lang string) *rich.Doc {
	subject, ok := b.Subjects.Get(sid)
	if !ok || !b.sees(who, subject) {
		return nil
	}
	doc := rich.New().H(tr(lang, "Formula · ", "Формула · ") + subject.Name)
	var own *FoundFormula
	if who != nil {
		own = b.Subjects.OwnFormula(who.TgID, sid)
	}
	shared := b.Subjects.SubjectFormula(sid)
	current := own
	if current == nil {
		current = shared
	}
	if current == nil {
		doc.P(tr(lang, "No formula yet. Send it as the teacher wrote it: I'll lay it out and show you before keeping "+
			"it.",
			"Формулы пока нет. Пришли её, как написал преподаватель: я разберу её и покажу, прежде чем сохранить."))
	} else {
		if current.Own {
			doc.P(rich.Italic(tr(lang, "Your own formula: it counts for you instead of the subject's.",
				"Твоя формула: для тебя она вместо формулы предмета.")))
		} else if subject.Grp != 0 {
			doc.P(rich.Italic(tr(lang, "The formula of the group's subject.", "Формула предмета группы.")))
		}
		doc.Quote(strings.Join(formula.Describe(current.Tree, lang), "\n"), false)
	}
	var buttons []rich.Button
	if b.canEdit(who, scopeOf(subject)) {
		verb := tr(lang, "Change", "Изменить")
		if shared == nil {
			verb = tr(lang, "Set", "Задать")
		}
		whose := ""
		if subject.Grp != 0 {
			whose = tr(lang, " for the group", " для группы")
		}
		buttons = append(buttons, rich.B(verb+whose, sf("fm:e.%d.s", sid)))
	}
	if who != nil && subject.TgID == 0 {
		buttons = append(buttons, rich.B(tr(lang, "Own formula", "Своя формула"), sf("fm:e.%d.u", sid)))
	}
	if own != nil {
		buttons = append(buttons, rich.BS(tr(lang, "Drop own", "Сбросить свою"), sf("fm:x.%d", sid), "danger"))
	}
	return doc.Buttons(buttons...)
}

func (b *Bot) textStep(lang string) Step {
	return Step{Name: "text",
		Prompt: func(Data) *rich.Doc {
			return rich.New().P(tr(lang,
				"Send the formula as the teacher wrote it, or the paragraph of the syllabus with it. For example:",
				"Пришли формулу, как её написал преподаватель, или абзац программы курса с ней. Например:")).P(
				rich.Code(tr(lang,
					"Final = round(0.4·Accumulated + 0.6·Exam); Accumulated = 0.5·HW (4 best of 5) + 0.5·Tests; "+
						"an exam below 4 is the final grade",
					"Итог = округл(0.4·Накоп + 0.6·Экз); Накоп = 0.5·ДЗ (4 лучших из 5) + 0.5·КР; экзамен ниже 4 — "+
						"итог равен экзамену")))
		},
		Check: func(value string, _ Data) (string, error) { return formulaText(value) },
		Error: func(lang string, _ error) string {
			return tr(lang, sf("The text must be 1 to %d characters.", TextChars),
				sf("Текст — от 1 до %d символов.", TextChars))
		}}
}

func (b *Bot) formulaCallback(chat, messageID int64, who *access.User, arg, lang string) string {
	action, _, rest := partition(arg, ".")
	if action == "ok" {
		return b.keepFormula(chat, messageID, who, lang)
	}
	if action == "n" && who != nil && isDigits(rest) {
		for _, s := range b.Sheets.Followed(who.TgID) {
			if s.ID == atoi(rest) {
				subject := b.ownSubject(*who, s)
				b.Show(chat, messageID, b.formulaView(who, subject.ID, lang))
				return ""
			}
		}
		return ""
	}
	sid, _, target := partition(rest, ".")
	var subject Subject
	ok := false
	if isDigits(sid) {
		subject, ok = b.Subjects.Get(atoi(sid))
	}
	if !ok || !b.sees(who, subject) {
		return tr(lang, "No such subject", "Такого предмета нет")
	}
	switch {
	case action == "s":
		b.Show(chat, messageID, b.formulaView(who, subject.ID, lang))
	case action == "e" && (target == "s" || target == "u"):
		if (target == "s" && !b.canEdit(who, scopeOf(subject))) || (target == "u" && who == nil) {
			return ""
		}
		b.Flow(chat, lang, []Step{b.textStep(lang)}, func(data Data) {
			b.readFormula(chat, who, subject, target, data["text"], lang)
		}, nil)
	case action == "x" && who != nil:
		b.Subjects.SetFormula(subject.ID, "", nil, who.TgID)
		b.Show(chat, messageID, b.formulaView(who, subject.ID, lang))
		return tr(lang, "Dropped", "Сброшена")
	}
	return ""
}

func (b *Bot) readFormula(chat int64, who *access.User, subject Subject, target, text, lang string) {
	if b.LLM == nil || !b.LLM.Available() {
		b.send(chat, tr(lang, "Reading formulas is off: the bot has no Haiku key.",
			"Разбор формул выключен: у бота нет ключа Haiku."))
		return
	}
	plan, _ := mergeCopies(b.Subjects.Plan(subject.ID)) // a work the sheets repeat, once
	answer, err := b.LLM.JSON("formula", FormulaSystem, formulaPrompt(text, plan), FormulaSchema, formulaMaxTokens)
	var tree *formula.Tree
	if err == nil {
		tree, err = toTree(answer, plan)
	}
	var fe *formula.Error
	switch {
	case err == nil:
	case llm.IsBudget(err):
		b.send(chat, tr(lang, "The monthly limit for Haiku is spent: try next month.",
			"Месячный лимит на Haiku исчерпан: попробуй в следующем месяце."))
		return
	case errors.As(err, &fe):
		b.send(chat, rich.New().P(tr(lang, "I could not make a formula of it: ", "Не получилось разобрать формулу: "),
			fe.Msg, ".").Buttons(rich.B(tr(lang, "Send again", "Прислать заново"), sf("fm:e.%d.%s", subject.ID, target))))
		return
	default:
		slog.Warn("formula of a subject", "subject", subject.ID, "error", err)
		b.send(chat, tr(lang, "Haiku did not answer: try again in a while.", "Haiku не ответил: попробуй чуть позже."))
		return
	}
	b.Drafts[chat] = &Draft{Subject: subject.ID, Target: target, Text: text, Tree: tree, At: b.Now()}
	b.send(chat, b.draftView(subject, target, tree, plan, lang))
}

func (b *Bot) draftView(subject Subject, target string, tree *formula.Tree, plan []PlanWork, lang string) *rich.Doc {
	doc := rich.New().H(tr(lang, "Check the formula · ", "Проверь формулу · ") + subject.Name)
	doc.Quote(strings.Join(formula.Describe(tree, lang), "\n"), false)
	planned := plannedOf(plan)
	laid := formula.New(tree, planned, map[string]formula.Grade{})
	labels := map[string]string{}
	for _, w := range plan {
		labels[w.Key] = w.Label
		if lang == "ru" && w.LabelRu != "" {
			labels[w.Key] = w.LabelRu
		}
	}
	var rows [][]any
	for _, p := range formula.Parts(tree) {
		name, _ := p["name"].(string)
		var works []string
		for _, w := range laid.Works(name) {
			if label, ok := labels[w.Key]; ok {
				works = append(works, label)
			}
		}
		text := strings.Join(works, ", ")
		if text == "" {
			text = tr(lang, "none yet", "пока нет")
		}
		rows = append(rows, []any{rich.Bold(name), text})
	}
	doc.TableHTML([]any{tr(lang, "Part", "Часть"), tr(lang, "Works", "Работы")}, rows, nil)
	for _, w := range formula.Warnings(tree, planned) {
		switch w.Kind {
		case "weights":
			where := tr(lang, "the formula", "формуле")
			if w.Name != "" {
				where = "«" + w.Name + "»"
			}
			total := pyfmt.G(*w.Number, 6)
			doc.P("⚠️ " + tr(lang, sf("The weights of %s add up to %s, not 1.", where, total),
				sf("Веса в %s дают %s, а не 1.", where, total)))
		case "empty":
			doc.P("⚠️ " + tr(lang, sf("«%s»: no works in the sheets yet, counted as one to come.", w.Name),
				sf("«%s»: в таблицах пока нет работ, считаю одной будущей.", w.Name)))
		default:
			doc.P("⚠️ " + tr(lang, sf("«%s» is in no part.", w.Name), sf("«%s» не входит ни в одну часть.", w.Name)))
		}
	}
	whose := tr(lang, "for the subject", "для предмета")
	if target == "s" && subject.Grp != 0 {
		whose = tr(lang, "for the whole group", "для всей группы")
	} else if target == "u" {
		whose = tr(lang, "for you only", "только для тебя")
	}
	doc.P(rich.Italic(tr(lang, "It will be kept ", "Сохраню ") + whose + "."))
	return doc.Buttons(rich.BS(tr(lang, "Correct", "Верно"), "fm:ok", "success"),
		rich.B(tr(lang, "Send again", "Прислать заново"), sf("fm:e.%d.%s", subject.ID, target)))
}

func (b *Bot) keepFormula(chat, messageID int64, who *access.User, lang string) string {
	draft := b.Drafts[chat]
	delete(b.Drafts, chat)
	if draft == nil || b.Now().Sub(draft.At) > DraftFor {
		return tr(lang, "This formula is no longer waiting: send it again", "Эта формула уже не ждёт: пришли заново")
	}
	subject, ok := b.Subjects.Get(draft.Subject)
	if !ok || !b.sees(who, subject) {
		return ""
	}
	if draft.Target == "s" {
		if !b.canEdit(who, scopeOf(subject)) {
			return ""
		}
		b.Subjects.SetFormula(subject.ID, draft.Text, draft.Tree, 0)
	} else {
		b.Subjects.SetFormula(subject.ID, draft.Text, draft.Tree, who.TgID)
	}
	b.Show(chat, messageID, b.formulaView(who, subject.ID, lang))
	return tr(lang, "Kept", "Сохранено")
}
