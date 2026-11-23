package bot

import (
	"reflect"
	"testing"
	"time"

	"github.com/IvanIkra/gradebot/internal/groups"
	"github.com/IvanIkra/gradebot/internal/layout"
)

const colloq = "https://docs.google.com/spreadsheets/d/1ColloqExampleSheetId_0123456789a/edit"

func subjectItem(key, label string, value float64) layout.Item {
	return layout.Item{Key: key, Tab: "T", Header: key, Label: label, Value: value, Raw: value, Max: 10}
}

type groupA1 struct {
	*fixture
	group  groups.Group
	shared map[string]int64
}

// withA1 is group A1 with Ada its admin and Bob in it, both with full names, and three shared sheets.
func withA1(t *testing.T) groupA1 {
	f := setup(t)
	group, _ := f.bot.Groups.Create("A1")
	f.comeIn(ada, "A1 админ", "Ада")
	f.comeIn(bob, "A1", "Боб")
	f.Access.SetStudent(ada, "Адова Ада")
	f.Access.SetStudent(bob, "Бобов Боб")
	shared := map[string]int64{}
	for _, pair := range [][2]string{{calc, "Матанализ ДЗ"}, {colloq, "Матанализ колок"}, {alg, "Алгебра"}} {
		sheet, _, _ := f.bot.Groups.AddSheet(group.ID, pair[0], owner, nil)
		f.Sheets.SetTitle(sheet.ID, pair[1])
		shared[pair[1]] = sheet.ID
	}
	return groupA1{f, group, shared}
}

func TestNamesAndScopes(t *testing.T) {
	g := withA1(t)
	s := g.bot.Subjects
	matan, _ := s.Create("Матан", g.group.ID, 0)
	if _, err := s.Create("матан", g.group.ID, 0); err == nil { // the same name in the same scope
		t.Fatal("twice")
	}
	if _, err := s.Create("Математический анализ", g.group.ID, 0); err == nil { // longer than a hashtag may be
		t.Fatal("long")
	}
	mine, err := s.Create("Матан", 0, bob) // another scope: fine
	if err != nil {
		t.Fatal(err)
	}
	other, _ := s.Create("Анализ", g.group.ID, 0)
	s.Put(matan.ID, g.shared["Матанализ ДЗ"])
	s.Put(other.ID, g.shared["Матанализ ДЗ"]) // moves it within the group's subjects
	s.Put(mine.ID, g.shared["Матанализ ДЗ"])  // a user's own scope is apart
	if len(s.SheetsOf(matan.ID)) != 0 || !reflect.DeepEqual(s.SheetsOf(other.ID), []int64{g.shared["Матанализ ДЗ"]}) ||
		!reflect.DeepEqual(s.SheetsOf(mine.ID), []int64{g.shared["Матанализ ДЗ"]}) {
		t.Fatal(s.SheetsOf(other.ID))
	}
	g.bot.Groups.Delete(g.group.ID)
	if _, ok := s.Get(other.ID); ok {
		t.Fatal("kept")
	}
	if _, ok := s.Get(mine.ID); !ok {
		t.Fatal("dropped")
	}
}

func unitNames(units []*Unit) []string {
	var out []string
	for _, u := range units {
		out = append(out, u.Name)
	}
	return out
}

func TestUnitsOwnSubjectWins(t *testing.T) {
	g := withA1(t)
	matan, _ := g.bot.Subjects.Create("Матан", g.group.ID, 0)
	g.bot.Subjects.Put(matan.ID, g.shared["Матанализ ДЗ"])
	g.bot.Subjects.Put(matan.ID, g.shared["Матанализ колок"])
	b, _ := g.user(bob)
	units := g.bot.Subjects.Units(b, g.Sheets.Followed(bob))
	var got [][]string
	for _, u := range units {
		row := []string{u.Name}
		for _, s := range u.Sheets {
			row = append(row, s.Title)
		}
		got = append(got, row)
	}
	if !reflect.DeepEqual(got, [][]string{{"Матан", "Матанализ ДЗ", "Матанализ колок"}, {"Алгебра", "Алгебра"}}) {
		t.Fatal(got)
	}
	mine, _ := g.bot.Subjects.Create("Колок", 0, bob)
	g.bot.Subjects.Put(mine.ID, g.shared["Матанализ колок"])
	if names := unitNames(g.bot.Subjects.Units(b, g.Sheets.Followed(bob))); !reflect.DeepEqual(names,
		[]string{"Матан", "Колок", "Алгебра"}) {
		t.Fatal(names)
	}
	a, _ := g.user(ada)
	if names := unitNames(g.bot.Subjects.Units(a, g.Sheets.Followed(ada))); !reflect.DeepEqual(names,
		[]string{"Матан", "Алгебра"}) {
		t.Fatal(names)
	}
}

func TestTheAdminJoinsTwoSheetsAndAMemberSeesOneSection(t *testing.T) {
	g := withA1(t)
	g.as(ada, "/subjects")
	g.press(ada, g.buttons(ada)["Новый предмет группы"])
	g.as(ada, "Матан")
	contains(t, g.text(ada), "Предмет Матан")
	g.press(ada, g.buttons(ada)["+ Матанализ ДЗ"])
	g.press(ada, g.buttons(ada)["+ Матанализ колок"])
	card := g.buttons(ada)
	if card["− Матанализ ДЗ"] == "" || card["− Матанализ колок"] == "" {
		t.Fatal(card)
	}
	calcID, colloqID := g.shared["Матанализ ДЗ"], g.shared["Матанализ колок"]
	g.bot.Grades.Record(bob, calcID, []layout.Item{subjectItem("hw1", "ДЗ 1", 7)}, 1, "")
	g.bot.Grades.Record(bob, colloqID, nil, 0, "")
	g.Clock.Advance(30 * time.Minute)
	g.bot.Grades.Record(bob, colloqID, []layout.Item{subjectItem("c1", "Колок", 9)}, 1, "")
	g.bot.SendGrades()
	news := g.text(bob)
	if n := countOf(news, "<h3>"); n != 1 {
		t.Fatal(news)
	}
	contains(t, news, "#Матан", "Колок", "Матанализ ДЗ</a>")
	g.as(bob, "/grades")
	contains(t, g.text(bob), "Матан · оценок: 2 · новых: 1", "Алгебра · оценок: 0")
	got := g.buttons(bob)
	if got["Сообщить об ошибке: Матанализ ДЗ"] == "" || got["Сообщить об ошибке: Матанализ колок"] == "" {
		t.Fatal(got)
	}
}

func countOf(text, part string) int {
	n := 0
	for i := 0; i+len(part) <= len(text); i++ {
		if text[i:i+len(part)] == part {
			n++
		}
	}
	return n
}

func TestAMemberCannotRunTheGroupsSubjectsButHasOwn(t *testing.T) {
	g := withA1(t)
	g.bot.Subjects.Create("Матан", g.group.ID, 0)
	g.as(bob, "/subjects")
	contains(t, g.text(bob), "Группа A1", "Матан")
	view := g.buttons(bob)
	for _, data := range view {
		if data == "sj:n.g"+itoa(g.group.ID) {
			t.Fatal(view)
		}
	}
	g.press(bob, "sj:n.g"+itoa(g.group.ID))
	if g.bot.Asks[bob] != nil { // no question asked
		t.Fatal("asked")
	}
	g.press(bob, view["Новый свой предмет"])
	g.as(bob, "Мой")
	mine := g.bot.Subjects.Scope(0, bob)[0]
	g.press(bob, sf("sj:p.%d.%d", mine.ID, g.shared["Алгебра"]))
	if !reflect.DeepEqual(g.bot.Subjects.SheetsOf(mine.ID), []int64{g.shared["Алгебра"]}) {
		t.Fatal(g.bot.Subjects.SheetsOf(mine.ID))
	}
	other, _, _ := g.Sheets.Follow(ada, "https://docs.google.com/spreadsheets/d/1OtherExampleSheetId_0123456789ab/e", "")
	g.press(bob, sf("sj:p.%d.%d", mine.ID, other.ID)) // not a sheet Bob follows
	if !reflect.DeepEqual(g.bot.Subjects.SheetsOf(mine.ID), []int64{g.shared["Алгебра"]}) {
		t.Fatal(g.bot.Subjects.SheetsOf(mine.ID))
	}
}

func TestTheOwnerRunsAGroupsSubjects(t *testing.T) {
	g := withA1(t)
	g.press(owner, "sj:h")
	if g.buttons(owner)["A1"] != "sj:l.g"+itoa(g.group.ID) {
		t.Fatal(g.buttons(owner))
	}
	g.press(owner, "sj:n.g"+itoa(g.group.ID))
	g.say(owner, "Алг")
	subject := g.bot.Subjects.Scope(g.group.ID, 0)[0]
	g.press(owner, sf("sj:p.%d.%d", subject.ID, g.shared["Алгебра"]))
	g.press(owner, sf("sj:r.%d", subject.ID))
	g.say(owner, "Алгебра")
	if s, _ := g.bot.Subjects.Get(subject.ID); s.Name != "Алгебра" {
		t.Fatal(s)
	}
	g.press(owner, sf("sj:d.%d", subject.ID))
	g.press(owner, g.buttons(owner)["Да, удалить"])
	if _, ok := g.bot.Subjects.Get(subject.ID); ok {
		t.Fatal("kept")
	}
}

func TestAUsersCardListsSubjectsNotSheets(t *testing.T) {
	g := withA1(t)
	matan, _ := g.bot.Subjects.Create("Матан", g.group.ID, 0)
	g.bot.Subjects.Put(matan.ID, g.shared["Матанализ ДЗ"])
	g.bot.Subjects.Put(matan.ID, g.shared["Матанализ колок"])
	g.press(owner, "uc:3002")
	g.press(ada, "mc:3002")
	for _, chat := range []int64{owner, ada} {
		contains(t, g.text(chat), "Матан, Алгебра")
		lacks(t, g.text(chat), "Матанализ ДЗ")
	}
}
