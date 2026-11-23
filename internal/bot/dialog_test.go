package bot

import (
	"reflect"
	"testing"
	"time"
)

func TestInviteStepByStep(t *testing.T) {
	f := setup(t)
	a1, _ := f.bot.Groups.Create("A1")
	f.say(owner, "/invite")
	contains(t, f.text(owner), "Какая группа?")
	want := map[string]string{"A1": "an:" + itoa(a1.ID), "Новая группа": "an:+", "Без группы: выберет сам": "an:",
		"Отмена": "ac"}
	if got := f.buttons(owner); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	f.answer(owner, itoa(a1.ID))
	contains(t, f.text(owner), "Кто входит по коду?")
	f.answer(owner, "") // members
	contains(t, f.text(owner), "Сколько человек")
	f.say(owner, "много") // wrong: asked again with the reason
	contains(t, f.text(owner), "Число от 1 до 1000", "Сколько человек")
	f.say(owner, "3")
	contains(t, f.text(owner), "Сколько действует код")
	f.answer(owner, "") // the default lifetime
	contains(t, f.text(owner), "На сколько открыть доступ")
	f.say(owner, "31.12")
	contains(t, f.text(owner), "Пометка для себя")
	f.say(owner, "семинар")
	reply := f.text(owner)
	contains(t, reply, "Код «семинар» в группу <b>A1</b>: вошли 0 из 3, действует до 16.11 15:00 МСК, "+
		"доступ до 31.12 23:59 МСК")
	invites := f.Access.OpenInvites()
	if len(invites) != 1 || invites[0].Grp != a1.ID || invites[0].Uses != 3 ||
		invites[0].AccessUntil != "2026-12-31T20:59:00+00:00" || inviteCode(reply) == "" || f.bot.Asks[owner] != nil {
		t.Fatal(invites)
	}
}

func TestInviteToANewGroup(t *testing.T) {
	f := setup(t)
	f.bot.Groups.Create("A1")
	f.say(owner, "/invite")
	f.answer(owner, "+")
	contains(t, f.text(owner), "Имя группы")
	f.say(owner, "A1") // taken: asked again
	contains(t, f.text(owner), "Такая группа уже есть")
	f.say(owner, "B2")
	f.answer(owner, "") // members
	contains(t, f.text(owner), "Сколько человек")
	for _, value := range []string{"x5", "", "", ""} { // people, lifetime, access, note
		f.answer(owner, value)
	}
	reply := f.text(owner)
	contains(t, reply, "Группа <b>B2</b> создана", "в группу <b>B2</b>")
	b2, _ := f.bot.Groups.Find("B2")
	if invites := f.Access.OpenInvites(); inviteCode(reply) == "" || len(invites) != 1 || invites[0].Grp != b2.ID {
		t.Fatal(invites)
	}
}

func TestNewGroupOfferedWithNoGroups(t *testing.T) {
	f := setup(t)
	f.say(owner, "/invite")
	want := map[string]string{"Новая группа": "an:+", "Без группы: выберет сам": "an:", "Отмена": "ac"}
	if got := f.buttons(owner); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
}

func TestACommandOrAnHourDropsTheQuestion(t *testing.T) {
	f := setup(t)
	f.say(owner, "/invite")
	f.say(owner, "/codes") // another command: the question is gone
	f.say(owner, "5")
	contains(t, f.text(owner), "Команды владельца")
	f.handle(callbackAt(owner, "an:x5", owner))
	if f.lastNote() != "Этот вопрос уже закрыт" {
		t.Fatal(f.lastNote())
	}
	f.say(owner, "/invite")
	f.Clock.Advance(time.Hour + time.Second)
	f.say(owner, "5")
	contains(t, f.text(owner), "Команды владельца")
	if len(f.Access.OpenInvites()) != 0 {
		t.Fatal("made")
	}
}

func TestTypedArgumentsRunAtOnce(t *testing.T) {
	f := setup(t)
	f.say(owner, "/invite Петя")
	if inviteCode(f.text(owner)) == "" || f.bot.Asks[owner] != nil {
		t.Fatal(f.text(owner))
	}
}

func TestNameAskedFor(t *testing.T) {
	f := setup(t)
	f.say(petr, "/start "+f.invite("Петя"))
	f.say(petr, "/name")
	contains(t, f.text(petr), "Как ты записан в таблицах?", "фамилию и имя")
	f.say(petr, "петров")
	contains(t, f.text(petr), "не похоже на ФИО")
	f.say(petr, "Петров Пётр")
	if u, _ := f.user(petr); u.Student != "Петров Пётр" {
		t.Fatal(u)
	}
}

func TestGroupsAndTheirSheetsByButtons(t *testing.T) {
	f := setup(t)
	f.say(owner, "/groups")
	f.press(owner, f.buttons(owner)["Новая группа"])
	f.say(owner, "A1")
	contains(t, f.text(owner), "Группа <b>A1</b> создана")
	f.press(owner, f.buttons(owner)["Добавить таблицу"])
	contains(t, f.text(owner), "Пришли ссылку")
	f.say(owner, calc)
	if _, ok := f.buttons(owner)["Название из Google"]; !ok {
		t.Fatal(f.buttons(owner))
	}
	f.say(owner, "Матан")
	contains(t, f.text(owner), "<b>Матан</b> — общая таблица группы <b>A1</b>")

	f.say(owner, "/groups")
	f.press(owner, f.buttons(owner)["Переименовать A1"])
	f.say(owner, "two words")
	contains(t, f.text(owner), "одно слово")
	f.say(owner, "B2")
	contains(t, f.text(owner), "Переименована: <b>B2</b>")

	f.bot.Groups.Create("C3")
	f.say(owner, "/groupsheet")
	f.press(owner, f.buttons(owner)["Добавить таблицу"])
	contains(t, f.text(owner), "Какая группа?")
	f.say(owner, "c3")
	f.say(owner, calc)
	if _, ok := f.buttons(owner)["Оставить «Матан»"]; !ok { // the sheet has a short name already
		t.Fatal(f.buttons(owner))
	}
	f.answer(owner, "")
	sheet, _ := f.Sheets.ByURL(calc)
	c3, _ := f.bot.Groups.Find("C3")
	if list := f.bot.Groups.SheetsOf(c3.ID); sheet.Course != "Матан" || len(list) != 1 || list[0].URL != sheet.URL {
		t.Fatal(sheet, list)
	}
}

func TestRelayoutByAButton(t *testing.T) {
	f := setup(t)
	a1, _ := f.bot.Groups.Create("A1")
	course := "Алг"
	f.bot.Groups.AddSheet(a1.ID, alg, owner, &course)
	f.say(owner, "/relayout")
	sheet, _ := f.Sheets.ByURL(alg)
	if f.buttons(owner)["Алг"] != "an:#"+itoa(sheet.ID) {
		t.Fatal(f.buttons(owner))
	}
	f.answer(owner, "#"+itoa(sheet.ID))
	contains(t, f.text(owner), "Разметка <b>Алг</b> сброшена")
}
