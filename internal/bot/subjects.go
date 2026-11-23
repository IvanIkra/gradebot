package bot

import (
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/IvanIkra/gradebot/internal/access"
	"github.com/IvanIkra/gradebot/internal/clock"
	"github.com/IvanIkra/gradebot/internal/formula"
	"github.com/IvanIkra/gradebot/internal/rich"
	"github.com/IvanIkra/gradebot/internal/sheets"
	"github.com/IvanIkra/gradebot/internal/store"
)

// Subjects: several sheets of one course read as one. A group's subject is run by its admin and the owner, a user's
// own one by that user, for the sheets they follow. In a scope (a group, a user) a sheet is in one subject at most.
//
// For a user, a followed sheet belongs to their own subject that has it, else to their group's, else to none: then
// it is a unit of its own, as before subjects. /grades and the messages of new grades show a section per unit.
//
// A course's sheets often repeat a work: the group's sheet and the lecturer's gradebook both have «ДЗ 1». Within a
// unit, a grade with the label (spaces and case aside), the grade and the bonus of one on another tab or sheet is a
// copy: shown and told once (dropCopies), and a formula counts works of one label once (mergeCopies). Copies whose
// grades differ are both shown: the difference is worth seeing.
//
// Callbacks `sj:<action>.<arg>`: `h` the root of /subjects, `l.<scope>` the subjects of a scope (`g<group>`,
// `u<user>`), `n.<scope>` a new one, `o.<subject>` its card, `p.<subject>.<sheet>` puts a sheet in it and
// `t.<subject>.<sheet>` takes one out, `r.<subject>` renames it, `d.<subject>` asks to delete it and `x.<subject>`
// deletes it.

// SubjectError is a bad name, or one the scope already has.
type SubjectError struct{ Msg string }

func (e *SubjectError) Error() string { return e.Msg }

// SubjectName checks a subject's name: its hashtag too, so as short as a sheet's course.
func SubjectName(text string) (string, error) {
	value, err := sheets.Course(text)
	if err != nil {
		return "", &SubjectError{err.Error()}
	}
	if value == "" {
		return "", &SubjectError{"a subject needs a name"}
	}
	return value, nil
}

// Subject is a row of subjects.
type Subject struct {
	ID        int64
	Grp       int64
	TgID      int64
	Name      string
	Formula   string
	Tree      string
	CreatedAt string
}

// Unit is what /grades and the messages of new grades show as one section: a subject, or a sheet in none.
type Unit struct {
	Name    string // the subject's name, or the sheet's title
	Tag     string // the course its hashtag is made of
	Sheets  []sheets.Sheet
	Subject *Subject
	Note    string // under the section: the outlook of the user's goal (goals.go)
}

func unitOf(s sheets.Sheet) *Unit {
	name := s.Title
	if name == "" {
		name = sheets.CourseOf(s)
	}
	return &Unit{Name: name, Tag: sheets.CourseOf(s), Sheets: []sheets.Sheet{s}}
}

// Subjects keeps the subjects.
type Subjects struct {
	db  *store.Store
	Now clock.Now
}

func NewSubjects(db *store.Store, now clock.Now) *Subjects { return &Subjects{db: db, Now: now} }

// Create makes a subject of a group (grp) or of a user (tgID).
func (s *Subjects) Create(name string, grp, tgID int64) (Subject, error) {
	name, err := SubjectName(name)
	if err != nil {
		return Subject{}, err
	}
	if s.taken(name, grp, tgID, 0) {
		return Subject{}, &SubjectError{"there is a subject «" + name + "» already"}
	}
	var sid int64
	s.db.Tx(func() {
		sid = s.db.Insert("insert into subjects(grp, tg_id, name, created_at) values(?, ?, ?, ?)",
			store.NullID(grp), store.NullID(tgID), name, clock.ISO(s.Now()))
	})
	subject, _ := s.Get(sid)
	return subject, nil
}

// taken is compared here, not by SQLite: its nocase folds only ASCII letters.
func (s *Subjects) taken(name string, grp, tgID, but int64) bool {
	for _, other := range s.Scope(grp, tgID) {
		if strings.EqualFold(strings.ToLower(other.Name), strings.ToLower(name)) && other.ID != but {
			return true
		}
	}
	return false
}

func (s *Subjects) Get(sid int64) (Subject, bool) {
	return store.Get[Subject](s.db, "select * from subjects where id = ?", sid)
}

func (s *Subjects) Rename(sid int64, name string) (Subject, error) {
	subject, ok := s.Get(sid)
	name, err := SubjectName(name)
	if err != nil {
		return Subject{}, err
	}
	if !ok {
		return Subject{}, &SubjectError{"no such subject"}
	}
	if s.taken(name, subject.Grp, subject.TgID, sid) {
		return Subject{}, &SubjectError{"there is a subject «" + name + "» already"}
	}
	s.db.Tx(func() { s.db.Exec("update subjects set name = ? where id = ?", name, sid) })
	subject, _ = s.Get(sid)
	return subject, nil
}

func (s *Subjects) Delete(sid int64) bool {
	var n int64
	s.db.Tx(func() { n = s.db.Exec("delete from subjects where id = ?", sid) })
	return n == 1
}

// Scope is a group's subjects or a user's, by name.
func (s *Subjects) Scope(grp, tgID int64) []Subject {
	rows := store.Select[Subject](s.db, "select * from subjects where grp is ? and tg_id is ?", store.NullID(grp),
		store.NullID(tgID))
	sort.SliceStable(rows, func(i, j int) bool { return strings.ToLower(rows[i].Name) < strings.ToLower(rows[j].Name) })
	return rows
}

func (s *Subjects) SheetsOf(sid int64) []int64 {
	return store.Column[int64](s.db, "select sheet from subject_sheets where subject = ? order by rowid", sid)
}

// Put puts the sheet into the subject, out of any other subject of the same scope.
func (s *Subjects) Put(sid, sheet int64) {
	subject, _ := s.Get(sid)
	s.db.Tx(func() {
		s.db.Exec("delete from subject_sheets where sheet = ? and subject in (select id from subjects "+
			"where grp is ? and tg_id is ?)", sheet, store.NullID(subject.Grp), store.NullID(subject.TgID))
		s.db.Exec("insert into subject_sheets(subject, sheet) values(?, ?)", sid, sheet)
	})
}

func (s *Subjects) Take(sid, sheet int64) bool {
	var n int64
	s.db.Tx(func() { n = s.db.Exec("delete from subject_sheets where subject = ? and sheet = ?", sid, sheet) })
	return n == 1
}

// --- formulas ---------------------------------------------------------------------------------------------------

// SetFormula sets the subject's formula, or with tgID that user's own one; a nil tree drops it.
func (s *Subjects) SetFormula(sid int64, text string, tree *formula.Tree, tgID int64) {
	s.db.Tx(func() {
		switch {
		case tgID == 0:
			var raw any
			if tree != nil {
				raw = tree.JSON()
			}
			var t any = text
			if tree == nil && text == "" {
				t = nil
			}
			s.db.Exec("update subjects set formula = ?, tree = ? where id = ?", t, raw, sid)
		case tree == nil:
			s.db.Exec("delete from subject_formulas where tg_id = ? and subject = ?", tgID, sid)
		default:
			s.db.Exec("insert into subject_formulas(tg_id, subject, formula, tree) values(?, ?, ?, ?) "+
				"on conflict(tg_id, subject) do update set formula = excluded.formula, tree = excluded.tree", tgID, sid,
				text, tree.JSON())
		}
	})
}

// FoundFormula is a formula and whose it is.
type FoundFormula struct {
	Text string
	Tree *formula.Tree
	Own  bool
}

func parseTree(raw string) *formula.Tree {
	tree, err := formula.Parse([]byte(raw))
	if err != nil {
		panic(err)
	}
	return tree
}

// OwnFormula is a user's own formula of a subject.
func (s *Subjects) OwnFormula(tgID, sid int64) *FoundFormula {
	row, ok := store.Get[struct{ Formula, Tree string }](s.db, "select formula, tree from subject_formulas where "+
		"tg_id = ? and subject = ?", tgID, sid)
	if !ok {
		return nil
	}
	return &FoundFormula{Text: row.Formula, Tree: parseTree(row.Tree), Own: true}
}

// SubjectFormula is the subject's formula.
func (s *Subjects) SubjectFormula(sid int64) *FoundFormula {
	row, ok := store.Get[struct{ Formula, Tree string }](s.db, "select formula, tree from subjects where id = ? and "+
		"tree is not null", sid)
	if !ok {
		return nil
	}
	return &FoundFormula{Text: row.Formula, Tree: parseTree(row.Tree)}
}

// FormulaFor is the formula a user's grades in a subject are counted by: their own, else the subject's.
func (s *Subjects) FormulaFor(tgID, sid int64) *FoundFormula {
	if own := s.OwnFormula(tgID, sid); own != nil {
		return own
	}
	return s.SubjectFormula(sid)
}

// PlanWork is a work of a subject's plan.
type PlanWork struct {
	Sheet     int64
	Key       string // the sheet's id and the work's key (subjectKey)
	Tab       string
	Label     string
	LabelRu   string
	Max       float64
	Position  int
	SheetName string
}

// Plan is every work of the subject's sheets, graded or not yet, in the subject's order, with the sheet's name. The
// key is the sheet's id and the work's key (subjectKey): two sheets may have a tab and a column alike.
func (s *Subjects) Plan(sid int64) []PlanWork {
	return store.Select[PlanWork](s.db, "select w.sheet, w.sheet || ':' || w.key key, w.tab, w.label, w.label_ru, "+
		"w.max, w.position, coalesce(nullif(s.course, ''), s.title, '') sheet_name "+
		"from subject_sheets j join sheet_works w on w.sheet = j.sheet join sheets s on s.id = j.sheet "+
		"where j.subject = ? order by j.rowid, w.position", sid)
}

// --- goals ------------------------------------------------------------------------------------------------------

// Goal is a user's goal in a subject; 0 for none.
func (s *Subjects) Goal(tgID, sid int64) int {
	goal, _ := store.Scalar[int](s.db, "select goal from subject_goals where tg_id = ? and subject = ?", tgID, sid)
	return goal
}

// SetGoal sets the goal; 0 drops it.
func (s *Subjects) SetGoal(tgID, sid int64, goal int) {
	s.db.Tx(func() {
		if goal == 0 {
			s.db.Exec("delete from subject_goals where tg_id = ? and subject = ?", tgID, sid)
		} else {
			s.db.Exec("insert into subject_goals(tg_id, subject, goal) values(?, ?, ?) "+
				"on conflict(tg_id, subject) do update set goal = excluded.goal", tgID, sid, goal)
		}
	})
}

// Units are the user's followed sheets (in their order) as sections: their own subjects first, then their
// group's.
func (s *Subjects) Units(user access.User, followed []sheets.Sheet) []*Unit {
	subjects := s.Scope(0, user.TgID)
	if user.Grp != 0 {
		subjects = append(s.Scope(user.Grp, 0), subjects...)
	}
	mine := map[int64]Subject{}
	for _, subject := range subjects {
		for _, sheet := range s.SheetsOf(subject.ID) {
			mine[sheet] = subject // the user's own come last and win
		}
	}
	var units []*Unit
	bySubject := map[int64]*Unit{}
	for _, sheet := range followed {
		subject, ok := mine[sheet.ID]
		switch {
		case !ok:
			units = append(units, unitOf(sheet))
		case bySubject[subject.ID] != nil:
			bySubject[subject.ID].Sheets = append(bySubject[subject.ID].Sheets, sheet)
		default:
			subj := subject
			bySubject[subject.ID] = &Unit{Name: subject.Name, Tag: subject.Name, Sheets: []sheets.Sheet{sheet},
				Subject: &subj}
			units = append(units, bySubject[subject.ID])
		}
	}
	return units
}

// --- copies -----------------------------------------------------------------------------------------------------

var spaces = regexp.MustCompile(`\s+`)

// workLabel is a work's label for telling copies: «ДЗ 1» and «дз1» are one.
func workLabel(label, labelRu string) string {
	if labelRu != "" {
		label = labelRu
	}
	return spaces.ReplaceAllString(strings.ToLower(label), "")
}

// place is the sheet and the tab of a grade, an event (its key starts with the tab) or a planned work.
func place(sheet int64, tab, key string) [2]any {
	if tab == "" {
		tab, _, _ = strings.Cut(key, "|")
	}
	return [2]any{sheet, tab}
}

type sign struct {
	label        string
	value, bonus float64
}

func signOf(r *Row) sign { return sign{workLabel(r.Label, r.LabelRu), r.Value, bonus(r.Bonus)} }

// dropCopies gives (the rows without copies, the copies): a copy has the label, the grade and the bonus of a row
// before it or of a known one, on another tab or sheet. A row marked new passes it on to the one it copies.
func dropCopies(rows []*Row, known []*Row) ([]*Row, []*Row) {
	first := map[sign]*Row{}
	for _, r := range known {
		if _, ok := first[signOf(r)]; !ok {
			first[signOf(r)] = r
		}
	}
	var kept, copies []*Row
	for _, r := range rows {
		sg := signOf(r)
		if original, ok := first[sg]; ok && place(original.Sheet, original.Tab, original.Key) != place(r.Sheet, r.Tab, r.Key) {
			if r.New {
				original.New = true
			}
			copies = append(copies, r)
			continue
		}
		if _, ok := first[sg]; !ok {
			first[sg] = r
		}
		kept = append(kept, r)
	}
	return kept, copies
}

// mergeCopies is a subject's plan with each label once, on its first tab: (the plan, copy's key -> the key it
// copies).
func mergeCopies(plan []PlanWork) ([]PlanWork, map[string]string) {
	first := map[string]PlanWork{}
	var kept []PlanWork
	aliases := map[string]string{}
	for _, w := range plan {
		label := workLabel(w.Label, w.LabelRu)
		original, ok := first[label]
		if label != "" && ok && place(original.Sheet, original.Tab, original.Key) != place(w.Sheet, w.Tab, w.Key) {
			aliases[w.Key] = original.Key
			continue
		}
		if !ok {
			first[label] = w
		}
		kept = append(kept, w)
	}
	return kept, aliases
}

// subjectKey is a grade's key within a subject (Subjects.Plan): its sheet's id and its key in the sheet.
func subjectKey(sheet int64, key string) string { return itoa(sheet) + ":" + key }

func scopeOf(subject Subject) string {
	if subject.Grp != 0 {
		return "g" + itoa(subject.Grp)
	}
	return "u" + itoa(subject.TgID)
}

// --- /subjects --------------------------------------------------------------------------------------------------

// canEdit: who (nil for the owner) may change the subjects of the scope.
func (b *Bot) canEdit(who *access.User, scope string) bool {
	if len(scope) < 2 || !isDigits(scope[1:]) {
		return false
	}
	kind, number := scope[:1], atoi(scope[1:])
	if who == nil {
		_, ok := b.Groups.Get(number)
		return kind == "g" && ok
	}
	if kind == "u" {
		return number == who.TgID
	}
	group, ok := b.adminGroup(*who)
	return kind == "g" && ok && group.ID == number
}

// subjectNames are the user's subjects and the sheets they follow in none, for a card: «Матан, Дискра» rather than
// four sheet titles.
func (b *Bot) subjectNames(user access.User) string {
	var names []string
	for _, unit := range b.Subjects.Units(user, b.Sheets.Followed(user.TgID)) {
		names = append(names, unit.Name)
	}
	if len(names) == 0 {
		return "—"
	}
	return strings.Join(names, ", ")
}

func (b *Bot) canSee(who *access.User, scope string) bool {
	return b.canEdit(who, scope) || (who != nil && who.Grp != 0 && scope == "g"+itoa(who.Grp))
}

// candidates are the sheets a subject of the scope may take: a group's shared ones, or the ones a user follows.
func (b *Bot) candidates(scope string) []sheets.Sheet {
	number := atoi(scope[1:])
	if scope[0] == 'g' {
		return b.Groups.SheetsOf(number)
	}
	return b.Sheets.Followed(number)
}

// cmdSubjects is /subjects: the user's own subjects and their group's; the owner picks a group, `/subjects
// <group>` opens one.
func (b *Bot) cmdSubjects(chat int64, who *access.User, arg, lang string) {
	if who == nil && trimPy(arg) != "" {
		if group, ok := b.Groups.Find(trimPy(arg)); ok {
			b.send(chat, b.subjectsView(who, "g"+itoa(group.ID), lang))
			return
		}
	}
	b.send(chat, b.subjectsRoot(who, lang))
}

func (b *Bot) subjectsRoot(who *access.User, lang string) *rich.Doc {
	doc := rich.New().H(tr(lang, "Subjects", "Предметы")).P(tr(lang,
		"A subject joins the sheets of one course: /grades and the messages of new grades show them as one section.",
		"Предмет объединяет таблицы одного курса: в /grades и в сообщениях о новых оценках они идут одной секцией."))
	if who == nil {
		all := b.Groups.All()
		if len(all) == 0 {
			return doc.P(tr(lang, "No groups yet: /groups.", "Групп пока нет: /groups."))
		}
		for start := 0; start < len(all); start += 4 {
			var row []rich.Button
			for _, g := range all[start:min(len(all), start+4)] {
				row = append(row, rich.B(g.Name, "sj:l.g"+itoa(g.ID)))
			}
			doc.Buttons(row...)
		}
		return doc
	}
	if who.Grp != 0 {
		if group, ok := b.Groups.Get(who.Grp); ok {
			b.scopePart(doc, who, "g"+itoa(group.ID), tr(lang, "Group ", "Группа ")+group.Name, lang)
		}
	}
	return b.scopePart(doc, who, "u"+itoa(who.TgID), tr(lang, "Mine", "Мои"), lang)
}

func (b *Bot) subjectsView(who *access.User, scope, lang string) *rich.Doc {
	if !b.canSee(who, scope) {
		return b.subjectsRoot(who, lang)
	}
	title := tr(lang, "Mine", "Мои")
	if scope[0] == 'g' {
		group, _ := b.Groups.Get(atoi(scope[1:]))
		title = tr(lang, "Group ", "Группа ") + group.Name
	}
	return b.scopePart(rich.New(), who, scope, title, lang).Buttons(rich.B(tr(lang, "« Subjects", "« Предметы"),
		"sj:h"))
}

// scopePart is a scope's subjects with their sheets; buttons to open them and to add one when who may.
func (b *Bot) scopePart(doc *rich.Doc, who *access.User, scope, title, lang string) *rich.Doc {
	number := atoi(scope[1:])
	var subjects []Subject
	if scope[0] == 'g' {
		subjects = b.Subjects.Scope(number, 0)
	} else {
		subjects = b.Subjects.Scope(0, number)
	}
	doc.H(title)
	names := map[int64]string{}
	for _, s := range b.candidates(scope) {
		names[s.ID] = sheets.Name(s)
	}
	if len(subjects) > 0 {
		var rows [][]any
		for _, s := range subjects {
			var parts []string
			for _, sheet := range b.Subjects.SheetsOf(s.ID) {
				name := names[sheet]
				if name == "" {
					name = b.sheetLabel(sheet)
				}
				parts = append(parts, name)
			}
			joined := strings.Join(parts, ", ")
			if joined == "" {
				joined = "—"
			}
			rows = append(rows, []any{rich.Bold(s.Name), joined})
		}
		doc.TableHTML(nil, rows, nil)
	} else {
		doc.P(tr(lang, "No subjects yet.", "Предметов пока нет."))
	}
	if b.canEdit(who, scope) {
		var opens []rich.Button
		for _, s := range subjects {
			opens = append(opens, rich.B(s.Name, "sj:o."+itoa(s.ID)))
		}
		for start := 0; start < len(opens); start += 4 {
			doc.Buttons(opens[start:min(len(opens), start+4)]...)
		}
		text := tr(lang, "New subject of mine", "Новый свой предмет")
		if scope[0] == 'g' {
			text = tr(lang, "New group subject", "Новый предмет группы")
		}
		doc.Buttons(rich.BS(text, "sj:n."+scope, "primary"))
	}
	return doc
}

func (b *Bot) sheetLabel(sheet int64) string {
	if found, ok := b.Sheets.Get(sheet); ok {
		return sheets.Name(found)
	}
	return "?"
}

func (b *Bot) subjectCard(who *access.User, sid int64, lang string) *rich.Doc {
	subject, ok := b.Subjects.Get(sid)
	if !ok || !b.canEdit(who, scopeOf(subject)) {
		return b.subjectsRoot(who, lang)
	}
	scope := scopeOf(subject)
	inside := b.Subjects.SheetsOf(sid)
	candidates := b.candidates(scope)
	doc := rich.New().H(tr(lang, "Subject ", "Предмет ") + subject.Name)
	if len(inside) > 0 {
		var rows [][]any
		for _, sheet := range inside {
			if s, ok := b.Sheets.Get(sheet); ok {
				title := s.Title
				if title == "" {
					title = sheets.Name(s)
				}
				rows = append(rows, []any{rich.Link(title, s.URL)})
			}
		}
		doc.TableHTML(nil, rows, nil)
	} else {
		doc.P(tr(lang, "No sheets in it yet: add them with the buttons below.",
			"Таблиц в нём пока нет: добавь их кнопками ниже."))
	}
	in := map[int64]bool{}
	for _, sheet := range inside {
		in[sheet] = true
		doc.Buttons(rich.B("− "+b.sheetLabel(sheet), sf("sj:t.%d.%d", sid, sheet)))
	}
	for _, s := range candidates {
		if !in[s.ID] {
			doc.Buttons(rich.B("+ "+sheets.Name(s), sf("sj:p.%d.%d", sid, s.ID)))
		}
	}
	if len(candidates) == 0 {
		if scope[0] == 'g' {
			doc.P(tr(lang, "There are no sheets to add: the group has no shared ones.",
				"Добавить нечего: у группы нет общих таблиц."))
		} else {
			doc.P(tr(lang, "There are no sheets to add: follow some with /sheet.",
				"Добавить нечего: подпишись на таблицы через /sheet."))
		}
	}
	doc.Buttons(rich.B(tr(lang, "Formula", "Формула"), "fm:s."+itoa(sid)))
	return doc.Buttons(rich.B(tr(lang, "Rename", "Переименовать"), "sj:r."+itoa(sid)),
		rich.BS(tr(lang, "Delete", "Удалить"), "sj:d."+itoa(sid), "danger"),
		rich.B(tr(lang, "« Back", "« Назад"), "sj:l."+scope))
}

func (b *Bot) nameStep(lang string) Step {
	return Step{Name: "name",
		Prompt: func(Data) *rich.Doc {
			return rich.New().P(tr(lang, "The subject's name, up to 10 characters: it is its hashtag too.",
				"Имя предмета, до 10 символов: оно же его хэштег."))
		},
		Check: func(value string, _ Data) (string, error) { return SubjectName(value) },
		Error: func(lang string, _ error) string {
			return tr(lang, "The name must be 1 to 10 characters.", "Имя — от 1 до 10 символов.")
		}}
}

func (b *Bot) subjectsCallback(chat, messageID int64, who *access.User, arg, lang string) string {
	action, _, rest := partition(arg, ".")
	switch action {
	case "h":
		b.Show(chat, messageID, b.subjectsRoot(who, lang))
		return ""
	case "l":
		b.Show(chat, messageID, b.subjectsView(who, rest, lang))
		return ""
	case "n":
		if !b.canEdit(who, rest) {
			return ""
		}
		number := atoi(rest[1:])
		grp, tgID := int64(0), number
		if rest[0] == 'g' {
			grp, tgID = number, 0
		}
		b.Flow(chat, lang, []Step{b.nameStep(lang)}, func(data Data) {
			b.subjectMade(chat, who, data["name"], grp, tgID, lang)
		}, nil)
		return ""
	}
	sid, _, sheet := partition(rest, ".")
	var subject Subject
	ok := false
	if isDigits(sid) {
		subject, ok = b.Subjects.Get(atoi(sid))
	}
	if !ok || !b.canEdit(who, scopeOf(subject)) {
		return tr(lang, "No such subject", "Такого предмета нет")
	}
	switch {
	case action == "o":
		b.Show(chat, messageID, b.subjectCard(who, subject.ID, lang))
	case (action == "p" || action == "t") && isDigits(sheet):
		if action == "p" {
			allowed := false
			for _, s := range b.candidates(scopeOf(subject)) {
				allowed = allowed || s.ID == atoi(sheet)
			}
			if !allowed {
				return ""
			}
			b.Subjects.Put(subject.ID, atoi(sheet))
		} else {
			b.Subjects.Take(subject.ID, atoi(sheet))
		}
		b.Show(chat, messageID, b.subjectCard(who, subject.ID, lang))
	case action == "r":
		b.Flow(chat, lang, []Step{b.nameStep(lang)}, func(data Data) {
			b.subjectRenamed(chat, who, subject.ID, data["name"], lang)
		}, nil)
	case action == "d":
		b.Show(chat, messageID, rich.New().P(tr(lang, "Delete the subject ", "Удалить предмет "),
			rich.Bold(subject.Name), tr(lang, "? Its sheets stay, each as a section of its own.",
				"? Таблицы останутся, каждая своей секцией.")).Buttons(
			rich.BS(tr(lang, "Yes, delete", "Да, удалить"), "sj:x."+itoa(subject.ID), "danger"),
			rich.B(tr(lang, "Cancel", "Отмена"), "sj:o."+itoa(subject.ID))))
	case action == "x":
		b.Subjects.Delete(subject.ID)
		b.Show(chat, messageID, b.subjectsView(who, scopeOf(subject), lang))
		return tr(lang, "Deleted", "Удалён")
	}
	return ""
}

func (b *Bot) subjectMade(chat int64, who *access.User, name string, grp, tgID int64, lang string) {
	subject, err := b.Subjects.Create(name, grp, tgID)
	if err != nil {
		var se *SubjectError
		if !errors.As(err, &se) {
			panic(err)
		}
		b.send(chat, tr(lang, "There is a subject «"+name+"» already.", "Предмет «"+name+"» уже есть."))
		return
	}
	b.send(chat, b.subjectCard(who, subject.ID, lang))
}

func (b *Bot) subjectRenamed(chat int64, who *access.User, sid int64, name, lang string) {
	if _, err := b.Subjects.Rename(sid, name); err != nil {
		b.send(chat, tr(lang, "There is a subject «"+name+"» already.", "Предмет «"+name+"» уже есть."))
		return
	}
	b.send(chat, b.subjectCard(who, sid, lang))
}

var _ = time.Second
