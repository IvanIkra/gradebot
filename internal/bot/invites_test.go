package bot

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/IvanIkra/gradebot/internal/access"
	"github.com/IvanIkra/gradebot/internal/groups"
	"github.com/IvanIkra/gradebot/internal/store"
	"github.com/IvanIkra/gradebot/internal/testkit"
)

const ira = int64(2003)

var now = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func args(t *testing.T, text string, known map[string]groups.Group) InviteArgs {
	t.Helper()
	found, err := InviteArgsOf(text, func(word string) (groups.Group, bool) {
		g, ok := known[strings.ToLower(word)]
		return g, ok
	}, now, testkit.Zone, false)
	if err != nil {
		t.Fatal(text, err)
	}
	return found
}

func TestInviteArgs(t *testing.T) {
	found := args(t, "A1 x5 3д +30d Петя  с семинара", map[string]groups.Group{"a1": {ID: 7}})
	if found.Group == nil || found.Group.ID != 7 || found.Uses != 5 || found.Lifetime != 72*time.Hour ||
		found.AccessDays != 30 || !found.AccessUntil.IsZero() || found.Rank != 0 || found.Note != "Петя с семинара" {
		t.Fatal(found)
	}
	if a := args(t, "x* 12h", nil); a.Uses != 0 || a.Lifetime != 12*time.Hour {
		t.Fatal(a)
	}
	if args(t, "х3", nil).Uses != 3 { // a Cyrillic х
		t.Fatal("х3")
	}
	if a := args(t, "+31.12", nil); !a.AccessUntil.Equal(time.Date(2026, 12, 31, 23, 59, 0, 0, testkit.Zone)) {
		t.Fatal(a.AccessUntil)
	}
	if args(t, "+1.09", nil).AccessUntil.Year() != 2027 { // gone this year: the next one
		t.Fatal("+1.09")
	}
	if a := args(t, "+1.9.27", nil); !a.AccessUntil.Equal(time.Date(2027, 9, 1, 23, 59, 0, 0, testkit.Zone)) {
		t.Fatal(a.AccessUntil)
	}
	if args(t, "Петя", nil).Uses != 1 || args(t, "", nil).Note != "" {
		t.Fatal("defaults")
	}
	for _, bad := range []string{"x0", "x1001", "0д", "400d", "+0д", "+31.02", "+1.09.2025"} {
		if _, err := InviteArgsOf(bad, noGroup, now, testkit.Zone, false); err == nil {
			t.Error(bad)
		}
	}
}

func TestACodeForTwo(t *testing.T) {
	f := setup(t)
	f.say(owner, "/invite x2 Семинар")
	reply := f.text(owner)
	contains(t, reply, "Код «Семинар»: вошли 0 из 2", "доступ бессрочный")
	code := inviteCode(reply)
	f.say(petr, "/start "+code)
	contains(t, f.text(owner), ": вход по коду «Семинар» (1/2)")
	f.say(owner, "/codes")
	contains(t, f.text(owner), `<td align="right">1/2</td>`)
	f.say(anna, "/start "+code, first("Анна"))
	if _, ok := f.user(anna); !ok || len(f.Access.OpenInvites()) != 0 {
		t.Fatal("anna")
	}
	f.say(ira, "/start "+code, first("Ира"))
	if _, ok := f.user(ira); ok {
		t.Fatal("ira")
	}
	contains(t, f.text(ira), "Этот код не подходит")
}

func TestACodeForAnyoneUntilRevoked(t *testing.T) {
	f := setup(t)
	id, code := f.Access.InviteWith(access.Options{Uses: 0, Lifetime: 12 * time.Hour})
	for tgID := int64(3000); tgID < 3010; tgID++ {
		if _, ok := f.Access.Redeem(code, access.NewUser{TgID: tgID, TgName: "Тест", Lang: "ru"}); !ok {
			t.Fatal(tgID)
		}
	}
	if f.Access.OpenInvites()[0].Used != 10 || !f.Access.Revoke(id) {
		t.Fatal("used")
	}
	if _, ok := f.Access.Redeem(code, access.NewUser{TgID: 3999, TgName: "Тест", Lang: "ru"}); ok {
		t.Fatal("revoked")
	}
	_, short := f.Access.InviteWith(access.Options{Uses: 0, Lifetime: 12 * time.Hour})
	f.Clock.Advance(12*time.Hour + time.Second)
	if _, ok := f.Access.Redeem(short, access.NewUser{TgID: 3999, TgName: "Тест", Lang: "ru"}); ok {
		t.Fatal("expired")
	}
}

func TestInviteHelp(t *testing.T) {
	f := setup(t)
	f.say(owner, "/invite help")
	contains(t, f.text(owner), "в любом порядке")
	f.say(owner, "/invite x0")
	contains(t, f.text(owner), "в любом порядке")
}

func TestAccessForAWhile(t *testing.T) {
	f := setup(t)
	f.say(owner, "/invite +30д Петя")
	contains(t, f.text(owner), "доступ на 30 дней")
	f.say(petr, "/start "+inviteCode(f.text(owner)))
	sent := f.api.Sent(petr)
	contains(t, sent[len(sent)-2].Text, "Доступ открыт до <b>09.12 15:00 МСК</b>")
	contains(t, f.text(owner), "доступ до 09.12 15:00")
	f.say(petr, "/name Петров Пётр")
	f.Sheets.Follow(petr, "https://docs.google.com/spreadsheets/d/1CalcExampleSheetId_0123456789abc", "")
	if len(f.Sheets.ToRead()) != 1 {
		t.Fatal("to read")
	}
	f.bot.checkAccess()
	ownerCount := len(f.api.Sent(owner))
	f.Clock.Advance(29*24*time.Hour + time.Hour)
	f.bot.checkAccess()
	f.bot.checkAccess() // told once
	contains(t, f.text(petr), "Доступ к боту закончится <b>09.12 15:00 МСК</b>")
	warned := 0
	for _, m := range f.api.Sent(petr) {
		if strings.Contains(m.Text, "закончится") {
			warned++
		}
	}
	if warned != 1 {
		t.Fatal(warned)
	}
	f.Clock.Advance(24 * time.Hour)
	f.bot.checkAccess()
	f.bot.checkAccess()
	contains(t, f.text(petr), "Доступ к боту закончился")
	if got := f.buttons(petr); !reflect.DeepEqual(got, map[string]string{"Попросить продлить": "pr"}) {
		t.Fatal(got)
	}
	if len(f.api.Sent(owner)) != ownerCount { // the owner hears nothing of it by itself
		t.Fatal("owner told")
	}
	sheet := f.Sheets.Followed(petr)[0].ID
	if len(f.Sheets.ToRead()) != 0 || len(f.Sheets.Readers(sheet)) != 0 {
		t.Fatal("still read")
	}
	f.say(owner, "/status")
	contains(t, f.text(owner), `<td>доступ закончился</td><td align="right">1</td>`)

	f.press(petr, "pr") // the user asks: now the owner hears, once
	if f.lastNote() != "Отправлено владельцу" {
		t.Fatal(f.lastNote())
	}
	request := f.last(owner)
	contains(t, request.Text, "просьба продлить доступ, он закончился 09.12 15:00")
	want := map[string]string{"+7 дней": "ax:2001.7", "+30 дней": "ax:2001.30", "+90 дней": "ax:2001.90",
		"Бессрочно": "ax:2001.0", "Удалить": "du:2001"}
	if got := f.buttons(owner); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	f.press(petr, "pr")
	if f.lastNote() != "Просьба уже отправлена" || f.last(owner).Text != request.Text {
		t.Fatal(f.lastNote())
	}
	f.say(petr, "/grades")
	contains(t, f.text(petr), "Доступ к боту закончился")
	f.press(petr, "grades")
	if f.lastNote() != "Доступ закончился" {
		t.Fatal(f.lastNote())
	}
	f.say(owner, "/users")
	contains(t, f.text(owner), "09.12 15:00 (закончился)")

	f.press(owner, "ax:2001.7") // from the end, or from now when it has ended
	if u, _ := f.user(petr); u.ExpiresAt != "2026-12-16T13:00:00+00:00" {
		t.Fatal(u.ExpiresAt)
	}
	contains(t, f.text(petr), "Владелец продлил доступ: <b>до 16.12 16:00 МСК</b>")
	if len(f.Sheets.ToRead()) != 1 {
		t.Fatal("not read again")
	}
	f.say(owner, "/users")
	f.press(owner, f.buttons(owner)["Пётр"])
	f.press(owner, f.buttons(owner)["Доступ"])
	contains(t, f.text(owner), "Доступ Пётр (@petr_example): <b>16.12 16:00 МСК</b>")
	if u, _ := f.user(petr); u.ProlongAsked { // prolonged: may ask again next time
		t.Fatal(u)
	}
	f.press(owner, f.buttons(owner)["Бессрочно"])
	if u, _ := f.user(petr); u.ExpiresAt != "" {
		t.Fatal(u)
	}
	contains(t, f.text(petr), "бессрочно")
	f.press(petr, "pr")
	if f.lastNote() != "Доступ и так бессрочный" {
		t.Fatal(f.lastNote())
	}
}

func TestCodesUsedBeforeCountAsUsed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db := store.MustOpen(path)
	db.Exec("insert into invites(code_hash, created_at, expires_at, used_by) values('h', 't', 'u', 1)")
	db.Exec("alter table invites drop column used")
	db.Close()
	db = store.MustOpen(path)
	defer db.Close()
	row, _ := store.Get[struct{ Uses, Used int }](db, "select uses, used from invites")
	if row.Uses != 1 || row.Used != 1 {
		t.Fatal(row)
	}
}

func TestLeavingWorksAfterTheAccessEnded(t *testing.T) {
	f := setup(t)
	f.say(owner, "/invite +1д")
	f.say(petr, "/start "+inviteCode(f.text(owner)))
	f.Clock.Advance(48 * time.Hour)
	f.say(petr, "/stop")
	if f.buttons(petr)["Да, удалить"] != "stop!" {
		t.Fatal(f.buttons(petr))
	}
	f.press(petr, "stop!")
	if _, ok := f.user(petr); ok {
		t.Fatal("kept")
	}
	contains(t, f.text(petr), "Твои данные удалены")
}
