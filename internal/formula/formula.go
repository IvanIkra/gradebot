// Package formula keeps a subject's grade formula as a tree, evaluated here: Haiku only turns the teacher's text
// into the tree.
//
// The tree is JSON: `{"nodes": {name: node}, "final": node}`. Named nodes («Накоп») are shown by name and used with
// `ref`. A node is one of:
//
//   - `{"type": "part", "name", "agg", "k", "count", "works": [{"key", "weight"}], "match": [prefix]}`: works of a
//     kind, 0-10; a work of the plan in no part's `works` joins the part whose `match` its label starts with (a
//     homework added after the formula was laid out): the longest prefix wins, so «КР 2» goes to «КР2», not «КР»;
//     case, spaces, hyphens and dots aside. `agg`: `mean`; `best` (the mean of the `k` best); `weighted` (by each
//     work's weight); `points` (by each work's maximum, as the sum of points over the sum of maximums). `count`:
//     how many works the part will have when the formula says so; the plan may know fewer yet.
//   - `{"type": "sum", "terms": [{"weight", "node", "bonus"}]}`: a weighted sum; a bonus term is on top of the
//     weights.
//   - `{"type": "ref", "name"}`, `{"type": "const", "value"}`.
//   - `{"type": "round", "mode": "half_up" | "floor" | "ceil", "digits", "node"}`.
//   - `{"type": "min" | "max", "nodes": [...]}`.
//   - `{"type": "if", "cond": {"node", "op", "value"}, "then", "else"}`: `op` is <, <=, > or >=.
//
// A work is graded (its grade with the bonus, at most 10) or not yet; one not yet takes the value the caller fills
// in for its part. A part with no works yet (the exam is not in the sheet) is one work not graded yet.
//
// The outlook of a goal: the lowest and the highest grade with everything left at one grade from 0 to 10, a
// forecast (what is left at the user's own mean of the part, else of the subject), and the least grade on
// everything left from which on the goal is reached. All by trying every grade by 0.1: rounding makes the formula a
// step function, and a blocking condition («the exam below 4 is the final grade») can even make it go down where
// the exam crosses 4.
package formula

import (
	"fmt"
	"math"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/IvanIkra/gradebot/internal/i18n"
	"github.com/IvanIkra/gradebot/internal/pyfmt"
)

var (
	Aggs   = []string{"mean", "best", "weighted", "points"}
	Rounds = []string{"half_up", "floor", "ceil"}
)

const (
	Eps        = 1e-9
	MaxNodes   = 200
	Step       = 0.1  // the grades the search for what is needed tries
	WeightsOff = 0.01 // weights of a sum that are this far from 1 are warned of
)

var ops = map[string]func(a, b float64) bool{
	"<":  func(a, b float64) bool { return a < b-Eps },
	"<=": func(a, b float64) bool { return a <= b+Eps }, // 3.9999999 is 4
	">":  func(a, b float64) bool { return a > b+Eps },
	">=": func(a, b float64) bool { return a >= b-Eps },
}

func pyG(x float64) string { return pyfmt.G(x, 6) }

// Error is a tree that cannot be evaluated.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// --- checking ---------------------------------------------------------------------------------------------------

// Check gives an error for unknown nodes, missing fields, references to no node or in a circle.
func Check(t *Tree) error {
	if t == nil || t.badNodes || !t.hasFinal {
		return &Error{"a formula is {nodes, final}"}
	}
	seen := 0
	var walk func(v any, path []string) error
	walk = func(v any, path []string) error {
		seen++
		if seen > MaxNodes {
			return &Error{"the formula is too big"}
		}
		node, ok := v.(Node)
		if !ok || node["__not_object"] != nil {
			return &Error{"a node is an object"}
		}
		kind := node["type"]
		switch kind {
		case "part":
			agg := get(node, "agg", "mean")
			if !truthy(node["name"]) || !slices.Contains(Aggs, str(agg)) || agg == nil {
				return &Error{"a part needs a name and a known agg"}
			}
			if node["agg"] == "best" && !(isInt(node["k"]) && f(node["k"]) > 0) {
				return &Error{"best of a part needs k"}
			}
			for _, w := range list(node["works"]) {
				work, ok := w.(Node)
				if !ok || !truthy(work["key"]) {
					return &Error{"a work of a part needs its key"}
				}
			}
		case "sum":
			if !truthy(node["terms"]) {
				return &Error{"a sum needs terms"}
			}
			for _, raw := range list(node["terms"]) {
				term, ok := raw.(Node)
				if !ok {
					return &Error{"a term needs a weight"}
				}
				if _, ok := num(term["weight"]); !ok {
					return &Error{"a term needs a weight"}
				}
				if err := walk(term["node"], path); err != nil {
					return err
				}
			}
		case "ref":
			name := str(node["name"])
			named, ok := t.Nodes[name]
			if _, isString := node["name"].(string); !ok || !isString {
				return &Error{fmt.Sprintf("no node «%v»", pyStr(node["name"]))}
			}
			if slices.Contains(path, name) {
				return &Error{fmt.Sprintf("«%s» refers to itself", name)}
			}
			return walk(named, append(slices.Clone(path), name))
		case "const":
			if _, ok := num(node["value"]); !ok {
				return &Error{"a const needs a value"}
			}
		case "round":
			if !slices.Contains(Rounds, str(get(node, "mode", "half_up"))) || !isInt(get(node, "digits", 0)) {
				return &Error{"a round needs a known mode"}
			}
			return walk(node["node"], path)
		case "min", "max":
			if !truthy(node["nodes"]) {
				return &Error{fmt.Sprintf("%s needs nodes", kind)}
			}
			for _, inner := range list(node["nodes"]) {
				if err := walk(inner, path); err != nil {
					return err
				}
			}
		case "if":
			cond := nodeOf(node["cond"])
			if _, ok := ops[str(cond["op"])]; !ok {
				return &Error{"a condition is a node, an op and a number"}
			}
			if _, ok := num(cond["value"]); !ok {
				return &Error{"a condition is a node, an op and a number"}
			}
			for _, inner := range []any{cond["node"], node["then"], node["else"]} {
				if err := walk(inner, path); err != nil {
					return err
				}
			}
		default:
			return &Error{fmt.Sprintf("unknown node type %s", pyRepr(kind))}
		}
		return nil
	}
	if err := walk(t.finalJSON, nil); err != nil {
		return err
	}
	for _, name := range t.Names {
		if err := walk(t.Nodes[name], []string{name}); err != nil {
			return err
		}
	}
	return nil
}

func pyStr(v any) string {
	if v == nil {
		return "None"
	}
	return fmt.Sprint(v)
}

func pyRepr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return "'" + x + "'"
	}
	return fmt.Sprint(v)
}

// children are the nodes inside a node.
func children(node Node) []Node {
	var out []Node
	switch node["type"] {
	case "sum":
		for _, t := range list(node["terms"]) {
			out = append(out, nodeOf(nodeOf(t)["node"]))
		}
	case "min", "max":
		for _, inner := range list(node["nodes"]) {
			out = append(out, nodeOf(inner))
		}
	case "round":
		out = append(out, nodeOf(node["node"]))
	case "if":
		out = append(out, nodeOf(nodeOf(node["cond"])["node"]), nodeOf(node["then"]), nodeOf(node["else"]))
	}
	return out
}

// Nodes is every node of the tree once, the named ones and the final one with what is inside them.
func Nodes(t *Tree) []Node {
	var out []Node
	var walk func(node Node)
	walk = func(node Node) {
		out = append(out, node)
		for _, child := range children(node) {
			walk(child)
		}
	}
	for _, name := range t.Names {
		walk(t.Nodes[name])
	}
	walk(t.Final)
	return out
}

// Parts are the parts, each once by name, in the order the tree has them.
func Parts(t *Tree) []Node {
	var out []Node
	seen := map[string]bool{}
	for _, node := range Nodes(t) {
		if node["type"] == "part" && !seen[str(node["name"])] {
			seen[str(node["name"])] = true
			out = append(out, node)
		}
	}
	return out
}

// --- evaluating -------------------------------------------------------------------------------------------------

// Work is a work of a part.
type Work struct {
	Key    string
	Value  *float64 // 0-10 with the bonus, at most 10; nil: not graded yet
	Weight float64
	Max    float64
}

// Planned is a work of the subject's plan (a row of sheet_works).
type Planned struct {
	Key, Label, LabelRu string
	Max                 float64
}

// Grade is a user's grade of a work (a row of grade_items).
type Grade struct {
	Value float64
	Bonus *float64
	Max   float64
}

var bareRe = regexp.MustCompile(`[\s\-.]`)

// Bare is a label or a prefix as matched: «КР-1», «кр 1» and «КР.1» are one.
func Bare(label string) string {
	return bareRe.ReplaceAllString(strings.ToLower(label), "")
}

// Matching is the part whose longest `match` prefix one of the labels starts with; the first such part on a tie.
func Matching(found []Node, labels []string) Node {
	var best Node
	length := 0
	bare := make([]string, len(labels))
	for n, l := range labels {
		bare[n] = Bare(l)
	}
	for _, part := range found {
		for _, p := range list(part["match"]) {
			prefix := Bare(str(p))
			if n := len([]rune(prefix)); n > length {
				for _, l := range bare {
					if strings.HasPrefix(l, prefix) {
						best, length = part, n
						break
					}
				}
			}
		}
	}
	return best
}

// Formula is a checked tree with a user's grades: the subject's plan, the user's grades by key.
type Formula struct {
	Tree   *Tree
	Plan   []Planned
	Grades map[string]Grade
	names  []string
	works  map[string][]Work
}

// New lays the plan and the grades out into the parts.
func New(t *Tree, plan []Planned, grades map[string]Grade) *Formula {
	fm := &Formula{Tree: t, Plan: plan, Grades: grades, works: map[string][]Work{}}
	planned := map[string]Planned{}
	for _, w := range plan {
		planned[w.Key] = w
	}
	found := Parts(t)
	listed := map[string]bool{}
	for _, part := range found {
		for _, w := range list(part["works"]) {
			listed[str(nodeOf(w)["key"])] = true
		}
	}
	joined := map[string][]Node{}
	for _, w := range plan {
		if listed[w.Key] {
			continue
		}
		if part := Matching(found, []string{w.LabelRu, w.Label}); part != nil {
			name := str(part["name"])
			joined[name] = append(joined[name], Node{"key": w.Key})
		}
	}
	for _, part := range found {
		name := str(part["name"])
		var works []Work
		all := append(slices.Clone(list(part["works"])), anyNodes(joined[name])...)
		for _, raw := range all {
			w := nodeOf(raw)
			key := str(w["key"])
			plannedWork, isPlanned := planned[key]
			grade, isGraded := grades[key]
			if !isPlanned && !isGraded {
				continue // a work the sheet no longer has
			}
			var value *float64
			if isGraded {
				v := math.Min(10, grade.Value+bonus(grade.Bonus))
				value = &v
			}
			maximum := grade.Max
			if isPlanned {
				maximum = plannedWork.Max
			}
			weight := 1.0
			if truthy(w["weight"]) {
				weight = f(w["weight"])
			}
			works = append(works, Work{Key: key, Value: value, Weight: weight, Max: maximum})
		}
		count := 0
		if truthy(part["count"]) {
			count = intOf(part["count"])
		}
		for range max(1-len(works), count-len(works)) {
			works = append(works, Work{Weight: 1, Max: 10}) // works the sheet has no column for yet
		}
		if _, seen := fm.works[name]; !seen {
			fm.names = append(fm.names, name)
		}
		fm.works[name] = works
	}
	return fm
}

func anyNodes(nodes []Node) []any {
	out := make([]any, len(nodes))
	for n, node := range nodes {
		out[n] = node
	}
	return out
}

func bonus(b *float64) float64 {
	if b == nil {
		return 0
	}
	return *b
}

// Works are the works of a part.
func (fm *Formula) Works(part string) []Work { return fm.works[part] }

// OpenParts are the parts with works not graded yet.
func (fm *Formula) OpenParts() []string {
	var out []string
	for _, name := range fm.names {
		for _, w := range fm.works[name] {
			if w.Value == nil {
				out = append(out, name)
				break
			}
		}
	}
	return out
}

// Mean is the mean of the graded works of a part, or of all of them (""); false with none graded.
func (fm *Formula) Mean(part string) (float64, bool) {
	sum, n := 0.0, 0
	for _, name := range fm.names {
		if part != "" && part != name {
			continue
		}
		for _, w := range fm.works[name] {
			if w.Value != nil {
				sum += *w.Value
				n++
			}
		}
	}
	if n == 0 {
		return 0, false
	}
	return sum / float64(n), true
}

// Value is the final grade with the works not graded yet at fill (one per part).
func (fm *Formula) Value(fill func(part string) float64) float64 {
	named := map[string]float64{}
	var ev func(node Node) float64
	ev = func(node Node) float64 {
		switch node["type"] {
		case "part":
			return fm.part(node, fill(str(node["name"])))
		case "sum":
			total := 0.0
			for _, t := range list(node["terms"]) {
				term := nodeOf(t)
				total += f(term["weight"]) * ev(nodeOf(term["node"]))
			}
			return total
		case "ref":
			name := str(node["name"])
			if v, ok := named[name]; ok {
				return v
			}
			named[name] = ev(fm.Tree.Nodes[name])
			return named[name]
		case "const":
			return f(node["value"])
		case "round":
			return Rounded(ev(nodeOf(node["node"])), str(get(node, "mode", "half_up")), intOf(get(node, "digits", 0)))
		case "min", "max":
			var out float64
			for n, inner := range list(node["nodes"]) {
				v := ev(nodeOf(inner))
				if n == 0 || (node["type"] == "min" && v < out) || (node["type"] == "max" && v > out) {
					out = v
				}
			}
			return out
		}
		cond := nodeOf(node["cond"])
		if ops[str(cond["op"])](ev(nodeOf(cond["node"])), f(cond["value"])) {
			return ev(nodeOf(node["then"]))
		}
		return ev(nodeOf(node["else"]))
	}
	return ev(fm.Tree.Final)
}

// At is Value with everything left at one grade.
func (fm *Formula) At(x float64) float64 {
	return fm.Value(func(string) float64 { return x })
}

func (fm *Formula) part(node Node, fill float64) float64 {
	works := fm.Works(str(node["name"]))
	values := make([]float64, len(works))
	for n, w := range works {
		values[n] = fill
		if w.Value != nil {
			values[n] = *w.Value
		}
	}
	switch get(node, "agg", "mean") {
	case "best":
		best := slices.Clone(values)
		sort.SliceStable(best, func(i, j int) bool { return best[i] > best[j] })
		best = best[:min(len(best), intOf(node["k"]))]
		return sumOf(best) / float64(len(best))
	case "weighted":
		total, sum := 0.0, 0.0
		for n, w := range works {
			total += w.Weight
			sum += values[n] * w.Weight
		}
		if total == 0 {
			return 0
		}
		return sum / total
	case "points":
		total, sum := 0.0, 0.0
		for n, w := range works {
			total += w.Max
			sum += values[n] * w.Max
		}
		if total == 0 {
			return 0
		}
		return sum / total
	}
	return sumOf(values) / float64(len(values))
}

func sumOf(values []float64) float64 {
	total := 0.0
	for _, v := range values {
		total += v
	}
	return total
}

// Rounded is half up as schools round (7.5 -> 8, not banker's 8 -> 8 and 6.5 -> 6), or down, or up; 7.9999999 is 8.
func Rounded(value float64, mode string, digits int) float64 {
	scale := math.Pow10(digits)
	switch mode {
	case "floor":
		return math.Floor(value*scale+Eps) / scale
	case "ceil":
		return math.Ceil(value*scale-Eps) / scale
	}
	return math.Floor(value*scale+0.5+Eps) / scale
}

// --- the goal ---------------------------------------------------------------------------------------------------

// Outlook of a goal.
type Outlook struct {
	Low      float64  // the lowest grade with everything left at one grade
	High     float64  // the highest
	Forecast *float64 // what is left at the user's own mean; nil with nothing graded
	Open     []string // parts with works left
	Goal     *float64
	Need     *float64 // the least grade on everything left from which on the goal is reached
}

// Settled: nothing left can change the grade; all is graded, or a condition (an exemption) made the rest moot.
func (o Outlook) Settled() bool { return o.High-o.Low < Eps }

func (o Outlook) Reached() bool { return o.Goal != nil && o.Low >= *o.Goal-Eps }

func (o Outlook) OutOfReach() bool { return o.Goal != nil && o.High < *o.Goal-Eps }

// OutlookOf tries every grade by 0.1 for everything left; goal nil for none.
func OutlookOf(fm *Formula, goal *float64) Outlook {
	steps := int(pyfmt.RoundInt(10 / Step))
	fills := make([]float64, steps+1)
	values := make([]float64, steps+1)
	for n := range fills {
		fills[n] = pyfmt.Round(float64(n)*Step, 1)
		values[n] = fm.At(fills[n])
	}
	out := Outlook{Low: slices.Min(values), High: slices.Max(values), Open: fm.OpenParts(), Goal: goal}
	if overall, ok := fm.Mean(""); ok {
		forecast := fm.Value(func(part string) float64 {
			if m, ok := fm.Mean(part); ok {
				return m
			}
			return overall
		})
		out.Forecast = &forecast
	}
	if goal != nil && !out.Reached() && !out.OutOfReach() {
		for n := len(fills) - 1; n >= 0; n-- {
			if values[n] < *goal-Eps {
				break
			}
			x := fills[n]
			out.Need = &x
		}
	}
	return out
}

// --- in words ---------------------------------------------------------------------------------------------------

// Describe is the formula as lines a person checks: each named node, then the final grade.
func Describe(t *Tree, lang string) []string {
	tr := func(en, ru string) string { return i18n.Tr(lang, en, ru) }
	var text func(node Node, inner bool) string
	text = func(node Node, inner bool) string {
		switch node["type"] {
		case "part":
			note := ""
			switch get(node, "agg", "mean") {
			case "best":
				note = tr(" (best "+number(node["k"])+")", " (лучшие "+number(node["k"])+")")
			case "points":
				note = tr(" (by points)", " (по баллам)")
			case "weighted":
				note = tr(" (weighted)", " (с весами)")
			}
			return str(node["name"]) + note
		case "sum":
			var terms []string
			for _, raw := range list(node["terms"]) {
				t := nodeOf(raw)
				plus := ""
				if truthy(t["bonus"]) {
					plus = "+ "
				}
				terms = append(terms, plus+number(t["weight"])+"·"+text(nodeOf(t["node"]), true))
			}
			joined := strings.ReplaceAll(strings.Join(terms, " + "), "+ + ", "+ ")
			if inner && len(terms) > 1 {
				return "(" + joined + ")"
			}
			return joined
		case "ref":
			return str(node["name"])
		case "const":
			return number(node["value"])
		case "round":
			how := map[string]string{"half_up": tr("round", "округл"), "floor": tr("floor", "вниз"),
				"ceil": tr("ceil", "вверх")}[str(get(node, "mode", "half_up"))]
			digits := ""
			if d := get(node, "digits", 0); truthy(d) {
				digits = ", " + number(d)
			}
			return how + "(" + text(nodeOf(node["node"]), false) + digits + ")"
		case "min", "max":
			var inner []string
			for _, n := range list(node["nodes"]) {
				inner = append(inner, text(nodeOf(n), false))
			}
			return str(node["type"]) + "(" + strings.Join(inner, ", ") + ")"
		}
		cond := nodeOf(node["cond"])
		return tr("if ", "если ") + text(nodeOf(cond["node"]), false) + " " + str(cond["op"]) + " " +
			number(cond["value"]) + tr(": ", ", то ") + text(nodeOf(node["then"]), false) + tr(", else ", ", иначе ") +
			text(nodeOf(node["else"]), false)
	}
	var lines []string
	for _, name := range t.Names {
		lines = append(lines, name+" = "+text(t.Nodes[name], false))
	}
	return append(lines, tr("Final = ", "Итог = ")+text(t.Final, false))
}

// Warning is what a person should look at before saving: (kind, name, number).
type Warning struct {
	Kind   string // weights, empty, outside
	Name   string
	Number *float64
}

func same(a, b Node) bool { return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer() }

// Warnings: `weights` is a sum (its name, "" for an unnamed one) whose weights, bonuses aside, add up to the number,
// not 1; `empty` a part with no works yet; `outside` a work of the subject (its label) in no part.
func Warnings(t *Tree, plan []Planned) []Warning {
	var out []Warning
	for _, node := range Nodes(t) {
		if node["type"] != "sum" {
			continue
		}
		total := 0.0
		for _, raw := range list(node["terms"]) {
			if term := nodeOf(raw); !truthy(term["bonus"]) {
				total += f(term["weight"])
			}
		}
		if math.Abs(total-1) > WeightsOff {
			name := ""
			for _, n := range t.Names {
				if same(t.Nodes[n], node) {
					name = n
				}
			}
			rounded := pyfmt.Round(total, 4)
			out = append(out, Warning{"weights", name, &rounded})
		}
	}
	planned := map[string]bool{}
	for _, w := range plan {
		planned[w.Key] = true
	}
	used := map[string]bool{}
	laid := New(t, plan, map[string]Grade{})
	for _, part := range Parts(t) {
		any := false
		for _, w := range laid.Works(str(part["name"])) {
			if planned[w.Key] {
				used[w.Key], any = true, true
			}
		}
		if !any {
			out = append(out, Warning{Kind: "empty", Name: str(part["name"])})
		}
	}
	for _, w := range plan {
		if !used[w.Key] {
			label := w.LabelRu
			if label == "" {
				label = w.Label
			}
			out = append(out, Warning{Kind: "outside", Name: label})
		}
	}
	return out
}
