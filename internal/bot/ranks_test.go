package bot

import (
	"fmt"
	"strings"
	"testing"

	"github.com/IvanIkra/gradebot/internal/access"
	"github.com/IvanIkra/gradebot/internal/sheets"
)

// withNamedPetr is a member who came in with a code and set a full name.
func withNamedPetr(t *testing.T) *fixture {
	f := withPetr(t)
	f.say(petr, "/name Петров Пётр")
	return f
}

func TestANewSheetOfAMemberWaitsForApproval(t *testing.T) {
	f := withNamedPetr(t)
	if u, _ := f.user(petr); u.Rank != access.Member {
		t.Fatal(u)
	}
	f.say(petr, "/sheet "+calc)
	contains(t, f.text(petr), "сначала её одобрит владелец")
	if _, ok := f.Sheets.ByURL(calc); ok || len(f.Sheets.Followed(petr)) != 0 {
		t.Fatal("followed")
	}
	contains(t, f.text(owner), "просьба подписать на таблицу")
	request := f.buttons(owner)
	f.say(petr, "/sheet "+calc)
	contains(t, f.text(petr), "уже ждёт одобрения")
	f.say(petr, "/sheets")
	contains(t, f.text(petr), "Ждут одобрения владельца")
	f.press(owner, request["Одобрить"])
	contains(t, f.text(owner), "Одобрено:")
	contains(t, f.text(petr), "Владелец одобрил таблицу", "Сейчас прочитаю")
	s, _ := f.Sheets.ByURL(calc)
	if followed := f.Sheets.Followed(petr); len(followed) != 1 || followed[0].SheetID != s.SheetID ||
		len(f.Sheets.Requests(0)) != 0 {
		t.Fatal(followed)
	}
	f.press(owner, request["Одобрить"])
	if f.lastNote() != "Уже решено" {
		t.Fatal(f.lastNote())
	}
}

func TestASheetTheBotReadsNeedsNoApproval(t *testing.T) {
	f := withNamedPetr(t)
	a1, _ := f.bot.Groups.Create("A1")
	f.bot.Groups.AddSheet(a1.ID, alg, owner, nil) // a group's sheet: Petr is in no group
	f.say(petr, "/sheet "+alg)
	contains(t, f.text(petr), "Подписал на")
	if len(f.Sheets.Followed(petr)) != 1 {
		t.Fatal("not followed")
	}
}

func TestDeclineAndCancel(t *testing.T) {
	f := withNamedPetr(t)
	f.say(petr, "/sheet "+calc+" Матан")
	f.press(owner, f.buttons(owner)["Отклонить"])
	contains(t, f.text(owner), "Отклонено:")
	contains(t, f.text(petr), "Владелец отклонил таблицу")
	if _, ok := f.Sheets.ByURL(calc); ok || len(f.Sheets.Requests(0)) != 0 {
		t.Fatal("kept")
	}
	f.say(petr, "/sheet "+alg)
	f.say(petr, "/sheets")
	f.press(petr, f.buttons(petr)["Отменить заявку 1"])
	if len(f.Sheets.Requests(0)) != 0 {
		t.Fatal("kept")
	}
	contains(t, f.text(petr), "Таблиц пока нет")
}

func TestTheOwnerTrustsAUserInUsers(t *testing.T) {
	f := withNamedPetr(t)
	f.say(owner, "/users")
	f.press(owner, f.buttons(owner)["Пётр"])
	contains(t, f.text(owner), "обычный")
	f.press(owner, f.buttons(owner)["Доверять"])
	if u, _ := f.user(petr); u.Rank != access.Trusted {
		t.Fatal(u)
	}
	contains(t, f.text(owner), "доверенный")
	card := f.buttons(owner)
	f.say(petr, "/sheet "+calc)
	contains(t, f.text(petr), "Подписал на таблицу")
	if len(f.Sheets.Requests(0)) != 0 {
		t.Fatal("asked")
	}
	contains(t, f.text(owner), "новая таблица") // a sheet new to the bot: the owner names it
	s, _ := f.Sheets.ByURL(calc)
	if f.buttons(owner)["Задать имя"] != "rn:"+itoa(s.ID) {
		t.Fatal(f.buttons(owner))
	}
	f.press(owner, card["Сделать обычным"])
	if u, _ := f.user(petr); u.Rank != access.Member {
		t.Fatal(u)
	}
}

func TestRequestsCountTowardsTheLimit(t *testing.T) {
	f := withNamedPetr(t)
	for n := range sheets.PerUser - 1 {
		if _, _, err := f.Sheets.Ask(petr, fmt.Sprintf("https://docs.google.com/spreadsheets/d/%s%04d",
			strings.Repeat("x", 20), n), ""); err != nil {
			t.Fatal(err)
		}
	}
	f.Sheets.Follow(petr, calc, "")
	if _, _, err := f.Sheets.Ask(petr, alg, ""); !sheets.IsError(err) {
		t.Fatal(err)
	}
	_, _, ok, err := f.Sheets.Approve(f.Sheets.Requests(petr)[0].ID) // a request becomes a sheet within the limit
	if !ok || err != nil || len(f.Sheets.Followed(petr)) != 2 {
		t.Fatal(ok, err)
	}
}

func TestUsersComeInPagesAndCanBeSearched(t *testing.T) {
	f := setup(t)
	for n := range 23 {
		student := ""
		if n%2 == 1 {
			student = fmt.Sprintf("Студент Номер%d", n)
		}
		f.AddUser(int64(3000+n), student, "ru", fmt.Sprintf("Человек%d", n))
	}
	f.say(owner, "/users")
	contains(t, f.text(owner), "Пользователи · 23", "Человек22")
	lacks(t, f.text(owner), "Человек12")
	first := f.buttons(owner)
	if _, ok := first["▶"]; !ok {
		t.Fatal(first)
	}
	if _, ok := first["1 / 3"]; !ok {
		t.Fatal(first)
	}
	if _, ok := first["◀"]; ok {
		t.Fatal(first)
	}
	f.press(owner, first["▶"])
	contains(t, f.text(owner), "Человек12")
	if _, ok := f.buttons(owner)["◀"]; !ok {
		t.Fatal(f.buttons(owner))
	}
	f.press(owner, "up:99") // past the end: the last page
	contains(t, f.text(owner), "Человек0")
	if _, ok := f.buttons(owner)["3 / 3"]; !ok {
		t.Fatal(f.buttons(owner))
	}
	f.say(owner, "/users номер1") // the full name, any case
	contains(t, f.text(owner), "Пользователи · 6 · «номер1»") // 1, 11, 13, 15, 17, 19
	f.press(owner, f.buttons(owner)["Человек11"])
	contains(t, f.text(owner), "Студент Номер11")
	f.press(owner, "users") // back keeps the search
	contains(t, f.text(owner), "«номер1»")
	f.press(owner, "ua")
	contains(t, f.text(owner), "Пользователи · 23")
	f.say(owner, "/users нетакого")
	contains(t, f.text(owner), "Никого не нашлось")
}
