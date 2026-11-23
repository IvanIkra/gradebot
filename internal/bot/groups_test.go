package bot

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/IvanIkra/gradebot/internal/access"
	"github.com/IvanIkra/gradebot/internal/groups"
	"github.com/IvanIkra/gradebot/internal/sheets"
	"github.com/IvanIkra/gradebot/internal/store"
)

const geo = "https://docs.google.com/spreadsheets/d/1GeometryExampleSheetId_01234567"

func (f *fixture) plainUser(tgID int64, firstName string) {
	_, code := f.Access.Invite("")
	f.Access.Redeem(code, access.NewUser{TgID: tgID, TgName: firstName, Lang: "ru"})
}

func ids(list []sheets.Sheet) []int64 {
	var out []int64
	for _, s := range list {
		out = append(out, s.ID)
	}
	slices.Sort(out)
	return out
}

func sortedOf(values ...int64) []int64 {
	slices.Sort(values)
	return values
}

func TestGroupNames(t *testing.T) {
	f := setup(t)
	g := f.bot.Groups
	a1, _ := g.Create("A1")
	if found, _ := g.Find("a1"); found.ID != a1.ID {
		t.Fatal(found)
	}
	for _, bad := range []string{"", "two words", strings.Repeat("x", 21), "/a", "https://docs", "a1"} {
		if _, err := g.Create(bad); !groups.IsError(err) {
			t.Error(bad)
		}
	}
	b2, _ := g.Create("B2")
	if _, err := g.Rename(b2.ID, "A1"); err == nil {
		t.Fatal("renamed to a taken name")
	}
	if renamed, _ := g.Rename(b2.ID, "B3"); renamed.Name != "B3" {
		t.Fatal(renamed)
	}
}

func course(c string) *string { return &c }

func TestEachGroupHasItsSheets(t *testing.T) {
	f := setup(t)
	g := f.bot.Groups
	for _, id := range []int64{petr, anna, ira} {
		f.plainUser(id, "Тест")
	}
	a1g, _ := g.Create("A1")
	b2g, _ := g.Create("B2")
	a1, b2 := a1g.ID, b2g.ID
	g.Join(petr, a1)
	g.Join(anna, b2)
	calcSheet, added, _ := g.AddSheet(a1, calc, owner, course("Матан"))
	if added != 1 || calcSheet.Course != "Матан" {
		t.Fatal(calcSheet, added)
	}
	algSheet, _, _ := g.AddSheet(b2, alg, owner, nil)
	g.AddSheet(b2, calc, owner, nil) // a sheet in two groups
	if !reflect.DeepEqual(ids(f.Sheets.Followed(petr)), []int64{calcSheet.ID}) ||
		!reflect.DeepEqual(ids(f.Sheets.Followed(anna)), sortedOf(calcSheet.ID, algSheet.ID)) ||
		len(f.Sheets.Followed(ira)) != 0 {
		t.Fatal("followed")
	}
	var counts [][3]any
	for _, gr := range g.All() {
		counts = append(counts, [3]any{gr.Name, gr.Users, gr.Sheets})
	}
	if !reflect.DeepEqual(counts, [][3]any{{"A1", 1, 1}, {"B2", 1, 2}}) {
		t.Fatal(counts)
	}
	// Moving: the old group's sheets go, except those the new group has too; an own sheet stays.
	own, _, _ := f.Sheets.Follow(petr, geo, "")
	left, added2, _ := g.Join(petr, b2)
	if left != 0 || !reflect.DeepEqual(added2, []int64{algSheet.ID}) { // Calc is in both groups: kept
		t.Fatal(left, added2)
	}
	if !reflect.DeepEqual(ids(f.Sheets.Followed(petr)), sortedOf(calcSheet.ID, algSheet.ID, own.ID)) {
		t.Fatal(ids(f.Sheets.Followed(petr)))
	}
	left, added2, _ = g.Join(petr, a1)
	if left != 1 || len(added2) != 0 || slices.Contains(ids(f.Sheets.Followed(petr)), algSheet.ID) {
		t.Fatal(left, added2)
	}
	if left, added2, _ := g.Join(petr, a1); left != 0 || added2 != nil { // the same group: nothing changes
		t.Fatal(left, added2)
	}
	left, _, _ = g.Join(petr, 0)
	if left != 1 || !reflect.DeepEqual(ids(f.Sheets.Followed(petr)), []int64{own.ID}) {
		t.Fatal(left)
	}
	if _, _, err := g.Join(petr, 999); !groups.IsError(err) {
		t.Fatal(err)
	}
	// A sheet taken from a group stays with its followers; deleting a group keeps its users' sheets.
	if !g.RemoveSheet(b2, algSheet.ID) {
		t.Fatal("remove")
	}
	if _, ok := f.Sheets.Get(algSheet.ID); !ok || !reflect.DeepEqual(f.Sheets.Followers(algSheet.ID), []int64{anna}) {
		t.Fatal("kept")
	}
	if !g.Delete(b2) {
		t.Fatal("delete")
	}
	if u, _ := f.user(anna); u.Grp != 0 || len(f.Sheets.Followed(anna)) != 2 {
		t.Fatal(u)
	}
	f.Sheets.Unfollow(anna, algSheet.ID)
	if _, ok := f.Sheets.Get(algSheet.ID); ok { // in no group, nobody follows it
		t.Fatal("kept")
	}
}

func TestACodeWithAGroup(t *testing.T) {
	f := setup(t)
	a1, _ := f.bot.Groups.Create("A1")
	f.bot.Groups.AddSheet(a1.ID, calc, owner, course("Матан"))
	f.say(owner, "/invite a1 Петя")
	reply := f.text(owner)
	contains(t, reply, "Код «Петя» в группу <b>A1</b>")
	f.say(owner, "/codes")
	contains(t, f.text(owner), "<td>A1</td>")
	f.say(petr, "/start "+inviteCode(reply))
	sent := f.api.Sent(petr)
	welcome := sent[len(sent)-2] // then the question of the full name
	contains(t, welcome.Text, "Твоя группа: <b>A1</b>", "общие таблицы (1)")
	for _, data := range fakeButtons(welcome) {
		if strings.HasPrefix(data, "gj:") {
			t.Fatal(data)
		}
	}
	contains(t, f.text(owner), ": вход по коду «Петя», группа A1")
	if u, _ := f.user(petr); u.Grp != a1.ID || len(f.Sheets.Followed(petr)) != 1 {
		t.Fatal(u)
	}
}

func TestACodeWithoutAGroupAsksForOne(t *testing.T) {
	f := setup(t)
	a1, _ := f.bot.Groups.Create("A1")
	b2, _ := f.bot.Groups.Create("B2")
	f.bot.Groups.AddSheet(b2.ID, alg, owner, nil)
	f.say(owner, "/invite Петя")
	lacks(t, f.text(owner), "в группу")
	f.say(petr, "/start "+inviteCode(f.text(owner)))
	contains(t, f.text(petr), "В какой ты группе?") // one message: the name waits for the group
	if f.bot.Asks[petr] != nil {
		t.Fatal("asked")
	}
	want := map[string]string{"A1": "gj:" + itoa(a1.ID), "B2": "gj:" + itoa(b2.ID), "Без группы": "gj:0"}
	if got := f.buttons(petr); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	f.press(petr, "gj:"+itoa(b2.ID))
	sent := f.api.Sent(petr)
	edited, asked := sent[len(sent)-2], sent[len(sent)-1] // the welcome redrawn, then the full name asked
	contains(t, asked.Text, "Как ты записан в таблицах?")
	if edited.Method != "editMessageText" || f.bot.Asks[petr].Step != "student" {
		t.Fatal(edited)
	}
	contains(t, edited.Text, "Твоя группа: <b>B2</b>")
	if u, _ := f.user(petr); u.Grp != b2.ID {
		t.Fatal(u)
	}
	contains(t, f.text(owner), "Пётр (@petr_example): теперь в группе <b>B2</b>")
	s, _ := f.Sheets.ByURL(alg)
	if followed := f.Sheets.Followed(petr); len(followed) != 1 || followed[0].URL != s.URL {
		t.Fatal(followed)
	}
}

func TestNoGroupsNoQuestion(t *testing.T) {
	f := setup(t)
	f.say(owner, "/invite Петя")
	f.say(petr, "/start "+inviteCode(f.text(owner)))
	sent := f.api.Sent(petr)
	welcome := sent[len(sent)-2]
	contains(t, welcome.Text, "Ты в деле")
	lacks(t, welcome.Text, "группе")
	if len(fakeButtons(welcome)) != 0 {
		t.Fatal(fakeButtons(welcome))
	}
}

func TestGroupCommand(t *testing.T) {
	f := setup(t)
	a1, _ := f.bot.Groups.Create("A1")
	b2, _ := f.bot.Groups.Create("B2")
	f.bot.Groups.AddSheet(a1.ID, calc, owner, nil)
	f.plainUser(petr, "Пётр")
	f.say(petr, "/group")
	contains(t, f.text(petr), "Твоя группа: не выбрана")
	if got := f.buttons(petr); !reflect.DeepEqual(got, map[string]string{"A1": "gp:" + itoa(a1.ID), "B2": "gp:" +
		itoa(b2.ID)}) {
		t.Fatal(got)
	}
	f.press(petr, "gp:"+itoa(a1.ID))
	contains(t, f.text(petr), "Твоя группа: <b>A1</b>", "подписал на: 1")
	if got := f.buttons(petr); !reflect.DeepEqual(got, map[string]string{"B2": "gp:" + itoa(b2.ID),
		"Без группы": "gp:0"}) {
		t.Fatal(got)
	}
	f.press(petr, "gp:0")
	if u, _ := f.user(petr); u.Grp != 0 || len(f.Sheets.Followed(petr)) != 0 {
		t.Fatal(u)
	}
	contains(t, f.text(owner), "left the group <b>A1</b>") // no word from the owner yet: English
	f.press(petr, "gp:999")
	if f.lastNote() != "" {
		t.Fatal(f.lastNote())
	}
}

func TestTheOwnerManagesGroups(t *testing.T) {
	f := setup(t)
	f.say(owner, "/groups")
	contains(t, f.text(owner), "Групп пока нет")
	f.say(owner, "/groups A1")
	contains(t, f.text(owner), "Группа <b>A1</b> создана")
	f.say(owner, "/groups a1")
	contains(t, f.text(owner), "Такая группа уже есть")
	f.say(owner, "/groups A1 A2")
	contains(t, f.text(owner), "Переименована: <b>A2</b>")
	f.say(owner, "/groups Z9 Z8")
	contains(t, f.text(owner), "Такой группы нет")

	f.plainUser(petr, "Пётр")
	a2, _ := f.bot.Groups.Find("A2")
	f.bot.Groups.Join(petr, a2.ID)
	f.say(owner, "/groupsheet "+calc+" Матан") // one group: it may be left out
	contains(t, f.text(owner), "<b>Матан</b> — общая таблица группы <b>A2</b>", "добавлено сейчас: 1.")
	f.say(owner, "/groups B1")
	f.say(owner, "/groupsheet "+alg)
	contains(t, f.text(owner), "Какая группа?")
	if !strings.HasPrefix(f.buttons(owner)["B1"], "an:") {
		t.Fatal(f.buttons(owner))
	}
	f.say(owner, "/groupsheet B1 not-a-link")
	contains(t, f.text(owner), "Это не ссылка на таблицу Google")
	f.say(owner, "/users")
	contains(t, f.text(owner), "<td>A2</td>")

	f.say(owner, "/groupsheet")
	contains(t, f.text(owner), "Группа A2 · 1", "Группа B1 · 0")
	f.press(owner, f.buttons(owner)["Убрать из общих: Матан"])
	if len(f.bot.Groups.SheetsOf(a2.ID)) != 0 || len(f.Sheets.Followed(petr)) != 1 {
		t.Fatal("not removed")
	}
	f.say(owner, "/groups")
	view := f.buttons(owner)
	f.press(owner, view["Таблицы: A2"])
	contains(t, f.text(owner), "Общих таблиц пока нет")
	f.press(owner, view["Удалить A2"])
	contains(t, f.text(owner), "Удалить группу <b>A2</b>?")
	f.press(owner, f.buttons(owner)["Да, удалить"])
	if _, ok := f.bot.Groups.Find("A2"); ok {
		t.Fatal("kept")
	}
	if u, _ := f.user(petr); u.Grp != 0 {
		t.Fatal(u)
	}
}

func TestSharedSheetsFromBeforeGroupsBecomeOneGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db := store.MustOpen(path)
	for _, id := range []int{1, 2} {
		db.Exec("insert into users(tg_id, tg_name, lang, joined_at) values(?, 'Тест', 'ru', 't')", id)
	}
	for _, sid := range []string{strings.Repeat("a", 20), strings.Repeat("b", 20)} {
		db.Exec("insert into sheets(sheet_id, url, added_by, added_at) values(?, 'u', 1, 't')", sid)
	}
	db.Close()
	old, _ := sql.Open("sqlite", path)
	if _, err := old.Exec("alter table sheets add column group_sheet integer not null default 0; " +
		"update sheets set group_sheet = 1 where sheet_id like 'a%';"); err != nil {
		t.Fatal(err)
	}
	old.Close()
	db = store.MustOpen(path)
	all := store.Select[groups.Group](db, "select * from groups")
	if len(all) != 1 || all[0].Name != store.FirstGroup {
		t.Fatal(all)
	}
	if got := store.Column[int64](db, "select sheet from group_sheets"); !reflect.DeepEqual(got, []int64{1}) {
		t.Fatal(got)
	}
	if got := store.Column[int64](db, "select grp from users"); !reflect.DeepEqual(got, []int64{all[0].ID, all[0].ID}) {
		t.Fatal(got)
	}
	if store.Has(db, "select 1 from pragma_table_info('sheets') where name = 'group_sheet'") {
		t.Fatal("column kept")
	}
	db.Close()
	store.MustOpen(path).Close()
}

func TestAGroupFromTheCodeIsChangedByTheOwnerOnly(t *testing.T) {
	f := setup(t)
	a1, _ := f.bot.Groups.Create("A1")
	b2, _ := f.bot.Groups.Create("B2")
	f.bot.Groups.AddSheet(a1.ID, calc, owner, nil)
	f.bot.Groups.AddSheet(b2.ID, alg, owner, nil)
	f.say(owner, "/invite A1")
	f.say(petr, "/start "+inviteCode(f.text(owner)))
	f.say(petr, "/group")
	contains(t, f.text(petr), "перевести тебя в другую может только владелец")
	if len(f.buttons(petr)) != 0 {
		t.Fatal(f.buttons(petr))
	}
	f.press(petr, "gp:"+itoa(b2.ID))
	if u, _ := f.user(petr); f.lastNote() != "Группу меняет только владелец" || u.Grp != a1.ID {
		t.Fatal(f.lastNote())
	}
	f.say(owner, "/users")
	f.press(owner, f.buttons(owner)["Пётр"])
	contains(t, f.text(owner), "A1 (по коду)")
	f.press(owner, f.buttons(owner)["Группа"])
	contains(t, f.text(owner), "Перевести Пётр (@petr_example) из <b>A1</b> в:")
	want := map[string]string{"B2": "um:2001." + itoa(b2.ID), "Без группы": "um:2001.0", "Отмена": "uc:2001"}
	if got := f.buttons(owner); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	f.press(owner, "um:2001."+itoa(b2.ID))
	if u, _ := f.user(petr); u.Grp != b2.ID {
		t.Fatal(u)
	}
	contains(t, f.text(owner), "B2 (по коду)")
	contains(t, f.text(petr), "Владелец перевёл тебя в группу <b>B2</b>")
	s, _ := f.Sheets.ByURL(alg)
	if followed := f.Sheets.Followed(petr); len(followed) != 1 || followed[0].URL != s.URL {
		t.Fatal(followed)
	}
	f.press(owner, "um:2001.0") // in no group, the user picks one themselves
	contains(t, f.text(petr), "Владелец вывел тебя из группы")
	f.press(petr, "gp:"+itoa(a1.ID))
	if u, _ := f.user(petr); u.Grp != a1.ID {
		t.Fatal(u)
	}
}
