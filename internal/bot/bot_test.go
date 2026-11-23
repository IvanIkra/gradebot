package bot

import (
	"reflect"
	"strings"
	"testing"

	"github.com/IvanIkra/gradebot/internal/access"
	"github.com/IvanIkra/gradebot/internal/telegram"
)

const petr, anna = int64(2001), int64(2002)

func TestCommand(t *testing.T) {
	for in, want := range map[string][2]string{"/start@gradebot_test_bot  K7QF-29XM ": {"/start", "K7QF-29XM"},
		"/USERS": {"/users", ""}, "K7QF-29XM": {"", "K7QF-29XM"}} {
		if cmd, arg := Command(in); cmd != want[0] || arg != want[1] {
			t.Error(in, cmd, arg)
		}
	}
}

func TestStrangerNeedsACode(t *testing.T) {
	f := setup(t)
	f.say(petr, "привет")
	contains(t, f.text(petr), "Вход по коду приглашения")
	f.say(anna, "/start", lang("en"))
	contains(t, f.text(anna), "invite code")
	f.say(petr, "/start", chatType("group"))
	if len(f.api.Sent(petr)) != 1 {
		t.Fatal(f.api.Sent(petr))
	}
}

func TestInviteAndComeIn(t *testing.T) {
	f := setup(t)
	code := f.invite("Петя")
	text := f.text(owner)
	contains(t, text, "«Петя»", "16.11 15:00", "<code>https://t.me/gradebot_test_bot?start="+code+"</code>")
	if got := f.buttons(owner); !reflect.DeepEqual(got, map[string]string{"Отправить в чат": "https://t.me/share/url" +
		"?url=https%3A%2F%2Ft.me%2Fgradebot_test_bot%3Fstart%3D" + code}) {
		t.Fatal(got)
	}
	f.say(petr, "/start "+code)
	contains(t, f.text(petr), "Как ты записан в таблицах?")
	if f.bot.Asks[petr].Step != "student" {
		t.Fatal(f.bot.Asks[petr])
	}
	if u, _ := f.user(petr); u.Lang != "ru" {
		t.Fatal(u)
	}
	contains(t, f.text(owner), "Пётр (@petr_example): вход по коду «Петя»")
	if got := f.buttons(owner); !reflect.DeepEqual(got, map[string]string{"Удалить Пётр": "du:2001"}) {
		t.Fatal(got)
	}
	f.say(anna, "/start "+code)
	if _, ok := f.user(anna); ok {
		t.Fatal("anna in")
	}
	contains(t, f.text(anna), "Этот код не подходит")
}

func TestPastedCodeWorks(t *testing.T) {
	f := setup(t)
	code := f.invite("Петя")
	f.say(petr, strings.ReplaceAll(strings.ToLower(code), "-", ""), lang("en"))
	if u, _ := f.user(petr); u.Lang != "en" {
		t.Fatal(u)
	}
	sent := f.api.Sent(petr)
	contains(t, sent[len(sent)-2].Text, "You're in")
}

func TestTooManyWrongCodesSilenceTheBot(t *testing.T) {
	f := setup(t)
	for range access.WrongLimit {
		f.say(petr, "/start AAAA-AAAA")
	}
	count := len(f.api.Sent(petr))
	f.say(petr, "/start AAAA-AAAA")
	f.say(petr, "hello")
	if len(f.api.Sent(petr)) != count || count != access.WrongLimit-1 {
		t.Fatal(count)
	}
}

func TestNamesAreEscaped(t *testing.T) {
	f := setup(t)
	code := f.invite("<i>x</i>")
	f.say(petr, "/start "+code, first("<b>Пётр</b>"), noUsername())
	text := f.text(owner)
	contains(t, text, "&lt;b&gt;Пётр&lt;/b&gt;", "&lt;i&gt;x&lt;/i&gt;")
	lacks(t, text, "<b>Пётр")
}

func TestCodesAndRevoke(t *testing.T) {
	f := setup(t)
	f.say(owner, "/codes")
	contains(t, f.text(owner), "Действующих кодов нет")
	f.invite("Аня")
	f.say(owner, "/codes")
	contains(t, f.text(owner), "<table", "Аня")
	f.press(owner, f.buttons(owner)["Отозвать Аня"])
	contains(t, f.text(owner), "Отозвать код <b>Аня</b>?")
	if len(f.Access.OpenInvites()) != 1 {
		t.Fatal("revoked early")
	}
	f.press(owner, f.buttons(owner)["Да, отозвать"])
	if len(f.Access.OpenInvites()) != 0 || f.last(owner).Method != "editMessageText" || !f.answered("Отозван") {
		t.Fatal(f.last(owner))
	}
	contains(t, f.text(owner), "Действующих кодов нет")
}

func TestOwnerDeletesAUser(t *testing.T) {
	f := setup(t)
	f.say(petr, "/start "+f.invite("Петя"))
	f.say(owner, "/users")
	contains(t, f.text(owner), "Пётр (@petr_example)")
	f.press(owner, f.buttons(owner)["Пётр"])
	contains(t, f.text(owner), "Петя", "обычный")
	f.press(owner, f.buttons(owner)["Удалить"])
	contains(t, f.text(owner), "Удалить Пётр (@petr_example)?")
	f.press(owner, f.buttons(owner)["Отмена"])
	if _, ok := f.user(petr); !ok {
		t.Fatal("deleted")
	}
	contains(t, f.text(owner), "« Пользователи") // back to the card
	f.press(owner, "dy:2001")
	if _, ok := f.user(petr); ok {
		t.Fatal("kept")
	}
	contains(t, f.text(petr), "закрыл тебе доступ")
	contains(t, f.text(owner), "Пользователей пока нет")
	f.say(petr, "/help")
	contains(t, f.text(petr), "Вход по коду приглашения")
}

func TestDeletingAUserWhoBlockedTheBot(t *testing.T) {
	f := setup(t)
	f.say(petr, "/start "+f.invite("Петя"))
	blocked := &telegram.Error{Method: "sendRichMessage", Code: 403, Description: "Forbidden: bot was blocked by the user"}
	f.api.FailChat[petr] = blocked
	f.press(owner, "dy:2001")
	if _, ok := f.user(petr); ok {
		t.Fatal("kept")
	}
}

func TestUserStops(t *testing.T) {
	f := setup(t)
	f.say(petr, "/start "+f.invite("Петя"))
	f.say(petr, "/stop")
	ask := f.buttons(petr)
	f.press(petr, ask["Отмена"])
	if _, ok := f.user(petr); !ok {
		t.Fatal("deleted")
	}
	contains(t, f.text(petr), "Остаёмся")
	f.press(petr, ask["Да, удалить"])
	if _, ok := f.user(petr); ok {
		t.Fatal("kept")
	}
	contains(t, f.text(owner), "Выход из бота через /stop: Пётр (@petr_example)")
}

func TestStrangersCallbacksDoNothing(t *testing.T) {
	f := setup(t)
	f.say(petr, "/start "+f.invite("Петя"))
	f.press(anna, "dy:2001")
	f.press(petr, "dy:2001")
	if _, ok := f.user(petr); !ok {
		t.Fatal("deleted")
	}
	last := f.api.Calls[len(f.api.Calls)-1]
	if last.Method != "answerCallbackQuery" || !reflect.DeepEqual(last.Params, telegram.Params{"callback_query_id": "cb1"}) {
		t.Fatal(last)
	}
}

func TestOwnerLanguageFollowsTheirClient(t *testing.T) {
	f := setup(t)
	f.say(owner, "/help", lang("en"))
	contains(t, f.text(owner), "Owner's commands")
	if f.bot.OwnerLang() != "en" {
		t.Fatal(f.bot.OwnerLang())
	}
	f.say(owner, "/help", lang("ru"))
	contains(t, f.text(owner), "Команды владельца")
}
