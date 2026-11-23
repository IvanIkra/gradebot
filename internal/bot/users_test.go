package bot

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/IvanIkra/gradebot/internal/access"
	"github.com/IvanIkra/gradebot/internal/i18n"
)

// withPetr is a user who came in with a code.
func withPetr(t *testing.T) *fixture {
	f := setup(t)
	f.say(owner, "/invite Петя")
	f.say(petr, "/start "+inviteCode(f.text(owner)))
	return f
}

func TestNameTellsTheOwnerAndCanBeReset(t *testing.T) {
	f := withPetr(t)
	f.say(petr, "/name петров")
	contains(t, f.text(petr), "не похоже на ФИО")
	f.say(petr, "/name  Петров   Пётр ")
	if u, _ := f.user(petr); u.Student != "Петров Пётр" {
		t.Fatal(u)
	}
	contains(t, f.text(petr), "Записал: <b>Петров Пётр</b>", "просто вставь")
	contains(t, f.text(owner), "Пётр (@petr_example): ФИО <b>Петров Пётр</b>")
	note := f.buttons(owner)
	f.say(petr, "/name Петров Пётр")
	contains(t, f.text(petr), "уже записано")
	f.say(petr, "/name Петров Пётр Петрович")
	contains(t, f.text(owner), "(было Петров Пётр)")
	f.press(owner, note["Сбросить ФИО: Пётр"])
	if u, _ := f.user(petr); u.Student != "" {
		t.Fatal(u)
	}
	contains(t, f.text(owner), "ФИО <b>Петров Пётр Петрович</b> у Пётр (@petr_example) сброшено")
	contains(t, f.text(petr), "Владелец сбросил твоё ФИО")
	f.say(petr, "/name")
	contains(t, f.text(petr), "Как ты записан в таблицах?")
}

func TestStartAgainSaysYouAreIn(t *testing.T) {
	f := withPetr(t)
	for _, text := range []string{"/start", "/start ABC-DEF"} {
		f.say(petr, text)
		contains(t, f.text(petr), "Ты уже в списке")
	}
}

func TestSheetsFollowAndUnfollow(t *testing.T) {
	f := withPetr(t)
	f.Access.SetRank(petr, access.Trusted)
	f.say(petr, "/sheets")
	contains(t, f.text(petr), "Таблиц пока нет")
	f.say(petr, "/sheet https://example.com/x")
	contains(t, f.text(petr), "не ссылка на таблицу Google")
	f.say(petr, calc) // the answer to the link asked again
	contains(t, f.text(petr), "Подписал на таблицу.", "нужно ФИО")
	f.say(petr, "/sheet "+calc)
	contains(t, f.text(petr), "уже в твоих подписках")
	f.say(petr, "/sheets")
	contains(t, f.text(petr), "Твои таблицы · 1", "ещё нет") // not read yet
	f.press(petr, f.buttons(petr)["Отписаться: …789abc"])
	if len(f.Sheets.Followed(petr)) != 0 {
		t.Fatal("followed")
	}
	contains(t, f.text(petr), "Таблиц пока нет")
}

func TestQuietHours(t *testing.T) {
	f := withPetr(t)
	f.say(petr, "/quiet")
	contains(t, f.text(petr), "Тихие часы: <b>23:00–8:00 МСК</b>")
	if got := f.buttons(petr); !reflect.DeepEqual(got, map[string]string{"Выключить": "qt:off"}) {
		t.Fatal(got)
	}
	f.say(petr, "21-6") // other hours, typed after /quiet
	contains(t, f.text(petr), "<b>21:00–6:00 МСК</b>")
	f.say(petr, "/quiet")
	f.press(petr, "qt:off")
	if u, _ := f.user(petr); u.QuietFrom != nil || u.QuietTo != nil {
		t.Fatal(u)
	}
	if got := f.buttons(petr); !reflect.DeepEqual(got, map[string]string{"23:00–8:00": "qt:23-8"}) {
		t.Fatal(got)
	}
	f.say(petr, "/quiet 22-7")
	contains(t, f.text(petr), "<b>22:00–7:00 МСК</b>")
	f.say(petr, "/quiet ночью")
	contains(t, f.text(petr), "Часы вида 23-8 или off")
	if u, _ := f.user(petr); *u.QuietFrom != 22 {
		t.Fatal(u)
	}
}

func TestLanguage(t *testing.T) {
	f := withPetr(t)
	f.say(petr, "/lang")
	if got := f.buttons(petr); !reflect.DeepEqual(got, map[string]string{"English": "lg:en", "Русский ✓": "lg:ru"}) {
		t.Fatal(got)
	}
	f.press(petr, "lg:en")
	if u, _ := f.user(petr); u.Lang != "en" {
		t.Fatal(u)
	}
	contains(t, f.text(petr), "Language: English")
	f.say(petr, "/help")
	contains(t, f.text(petr), "follow a Google Sheet")
	f.say(petr, "/lang RU")
	if u, _ := f.user(petr); u.Lang != "ru" {
		t.Fatal(u)
	}
}

func TestStopDropsOwnSheets(t *testing.T) {
	f := withPetr(t)
	f.Access.SetRank(petr, access.Trusted)
	f.say(petr, "/sheet "+calc)
	id := f.Sheets.Followed(petr)[0].ID
	f.press(petr, "stop!")
	if _, ok := f.Sheets.Get(id); ok {
		t.Fatal("kept")
	}
}

func TestOnlyTheOwnerNamesASheet(t *testing.T) {
	f := withPetr(t)
	f.say(petr, "/help")
	contains(t, f.text(petr), "/sheet &lt;ссылка&gt;</td>", "просто прислать ссылку")
	f.say(petr, "/sheet")
	contains(t, f.text(petr), "Пришли ссылку на таблицу")
	f.handle(callbackAt(petr, "ac", f.bot.Asks[petr].MessageID))
	contains(t, f.text(petr), "Отменено")
	if f.bot.Asks[petr] != nil {
		t.Fatal("asks")
	}
	f.say(petr, "/sheet "+alg+" Алгебра") // words after the link are no name
	lacks(t, f.text(owner), "Алгебра")
	f.press(owner, f.buttons(owner)["Одобрить"])
	contains(t, f.text(owner), "Одобрено")
	if s, _ := f.Sheets.ByURL(alg); s.Course != "" {
		t.Fatal(s)
	}
	f.press(owner, f.buttons(owner)["Задать имя"])
	f.say(owner, "Алгебра")
	if s, _ := f.Sheets.ByURL(alg); s.Course != "Алгебра" {
		t.Fatal(s)
	}
	contains(t, f.text(owner), "Имя: <b>Алгебра</b>")
	f.say(owner, "/rename "+alg+" Алг")
	contains(t, f.text(owner), "Имя: <b>Алг</b>")
	f.say(owner, "/rename")
	s, _ := f.Sheets.ByURL(alg)
	if f.buttons(owner)["Алг"] != "an:#"+itoa(s.ID) {
		t.Fatal(f.buttons(owner))
	}
	f.answer(owner, "#"+itoa(s.ID))
	f.answer(owner, "") // «Title from Google»
	contains(t, f.text(owner), "Короткого имени нет", "из первых слов")
}

func TestALinkPastedAloneFollowsTheSheet(t *testing.T) {
	f := withPetr(t)
	f.Access.SetRank(petr, access.Trusted)
	if f.bot.Asks[petr].Step != "student" { // the full name is asked after coming in
		t.Fatal(f.bot.Asks[petr])
	}
	f.say(petr, calc) // a link instead: it is followed, not taken for a name
	s, _ := f.Sheets.ByURL(calc)
	followed := f.Sheets.Followed(petr)
	if len(followed) != 1 || followed[0].URL != s.URL {
		t.Fatal(followed)
	}
	if u, _ := f.user(petr); u.Student != "" {
		t.Fatal(u)
	}
}

func TestAddASheetFromTheList(t *testing.T) {
	f := withPetr(t)
	f.say(petr, "/sheets")
	contains(t, f.text(petr), "просто вставь её сюда")
	f.press(petr, f.buttons(petr)["Добавить таблицу"])
	contains(t, f.text(petr), "Пришли ссылку на таблицу")
	if f.bot.Asks[petr].Step != "link" {
		t.Fatal(f.bot.Asks[petr])
	}
}

func TestDaysInWords(t *testing.T) {
	var got []string
	for _, n := range []int{1, 3, 7, 11, 21, 22, 30} {
		got = append(got, i18n.Days("ru", n))
	}
	if !reflect.DeepEqual(got, []string{"1 день", "3 дня", "7 дней", "11 дней", "21 день", "22 дня", "30 дней"}) ||
		i18n.Days("en", 1) != "1 day" || i18n.Days("en", 30) != "30 days" {
		t.Fatal(got)
	}
}

func TestAUserWithoutAFullNameIsRemindedOnce(t *testing.T) {
	f := withPetr(t)
	f.bot.remindNames()
	count := len(f.api.Sent(petr))
	f.Clock.Advance(24*time.Hour + time.Minute)
	f.bot.remindNames()
	f.bot.remindNames()
	if len(f.api.Sent(petr)) != count+1 {
		t.Fatal(len(f.api.Sent(petr)))
	}
	contains(t, f.text(petr), "нужно ФИО")
}

func TestFeedbackReachesTheOwnerWhoAnswers(t *testing.T) {
	f := withPetr(t)
	f.say(petr, "/feedback")
	contains(t, f.text(petr), "Что передать владельцу")
	f.say(petr, "Не вижу оценки за <КР2>")
	contains(t, f.text(petr), "Ответ придёт сюда")
	contains(t, f.text(owner), "Пётр (@petr_example) пишет:", "Не вижу оценки за &lt;КР2&gt;")
	note := f.buttons(owner)
	for _, c := range f.api.Calls {
		if c.Method == "sendRichMessage" && c.Params["chat_id"] == owner {
			if _, quiet := c.Params["disable_notification"]; quiet {
				if html := c.Params["rich_message"].(map[string]string)["html"]; strings.Contains(html, "пишет:") {
					t.Fatal("a message that needs the owner comes with a sound")
				}
			}
		}
	}
	f.press(owner, note["Ответить"])
	contains(t, f.text(owner), "Ответ для Пётр (@petr_example)")
	f.say(owner, "Скоро починю")
	contains(t, f.text(petr), "Ответ владельца:", "Скоро починю")
	contains(t, f.text(owner), "Отправлено: Пётр")
}

func TestFeedbackHasADailyLimit(t *testing.T) {
	f := withPetr(t)
	for n := range 5 {
		f.say(petr, "/feedback идея "+itoa(int64(n)))
	}
	f.say(petr, "/feedback ещё одна")
	contains(t, f.text(petr), "На сегодня хватит")
	lacks(t, f.text(owner), "ещё одна")
	f.Clock.Advance(24*time.Hour + time.Minute)
	f.say(petr, "/feedback ещё одна")
	contains(t, f.text(owner), "ещё одна")
}

func TestTimeZoneNamedWithTimes(t *testing.T) {
	f := setup(t)
	at := "2026-11-16T12:00:00+00:00"
	if f.bot.When(at, "") != "16.11 15:00" || f.bot.When(at, "ru") != "16.11 15:00 МСК" ||
		f.bot.When(at, "en") != "16.11 15:00 MSK" {
		t.Fatal(f.bot.When(at, "ru"))
	}
	f.bot.Zone, _ = time.LoadLocation("Asia/Yekaterinburg")
	if got := f.bot.When(at, "ru"); got != "16.11 17:00 UTC+5" {
		t.Fatal(got)
	}
}

