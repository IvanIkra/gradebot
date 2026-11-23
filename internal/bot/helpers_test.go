package bot

import (
	"regexp"
	"strings"
	"testing"

	"github.com/IvanIkra/gradebot/internal/access"
	"github.com/IvanIkra/gradebot/internal/books"
	"github.com/IvanIkra/gradebot/internal/telegram"
	"github.com/IvanIkra/gradebot/internal/telegram/fake"
	"github.com/IvanIkra/gradebot/internal/testkit"
)

const owner = testkit.Owner

type fixture struct {
	*testkit.Env
	t   *testing.T
	api *fake.API
	bot *Bot
	llm *books.FakeLLM
}

func setup(t *testing.T) *fixture {
	env := testkit.New(t)
	api := fake.New(true)
	b := New(api.API, env.DB, env.Access, env.Sheets, owner, testkit.Zone, "gradebot_test_bot", env.Clock.Now)
	fakeLLM := &books.FakeLLM{}
	b.LLM = fakeLLM
	return &fixture{Env: env, t: t, api: api, bot: b, llm: fakeLLM}
}

type msgOpt func(*telegram.Message)

func lang(code string) msgOpt  { return func(m *telegram.Message) { m.From.LanguageCode = code } }
func first(name string) msgOpt { return func(m *telegram.Message) { m.From.FirstName = name } }
func noUsername() msgOpt       { return func(m *telegram.Message) { m.From.Username = "" } }
func chatType(kind string) msgOpt {
	return func(m *telegram.Message) { m.Chat.Type = kind }
}

func message(sender int64, text string, opts ...msgOpt) telegram.Update {
	m := &telegram.Message{MessageID: 1, From: &telegram.User{ID: sender, FirstName: "Пётр", LanguageCode: "ru",
		Username: "petr_example"}, Chat: telegram.Chat{ID: sender, Type: "private"}, Text: text}
	for _, o := range opts {
		o(m)
	}
	return telegram.Update{UpdateID: 1, Message: m}
}

func callbackAt(sender int64, data string, messageID int64) telegram.Update {
	return telegram.Update{UpdateID: 1, CallbackQuery: &telegram.CallbackQuery{ID: "cb1",
		From: telegram.User{ID: sender, FirstName: "X"}, Data: data,
		Message: &telegram.Message{MessageID: messageID, Chat: telegram.Chat{ID: sender}}}}
}

func callback(sender int64, data string) telegram.Update { return callbackAt(sender, data, 555) }

func (f *fixture) handle(updates ...telegram.Update) {
	f.t.Helper()
	for _, u := range updates {
		f.bot.Handle(u)
	}
}

func (f *fixture) say(sender int64, text string, opts ...msgOpt) {
	f.t.Helper()
	f.bot.Handle(message(sender, text, opts...))
}

func (f *fixture) press(sender int64, data string) {
	f.t.Helper()
	f.bot.Handle(callback(sender, data))
}

// answer presses a button under the question the bot waits an answer to; "" is Skip.
func (f *fixture) answer(chat int64, value string) {
	f.t.Helper()
	f.bot.Handle(callbackAt(chat, "an:"+value, f.bot.Asks[chat].MessageID))
}

func (f *fixture) last(chat int64) fake.Sent { return f.api.Last(chat) }

func (f *fixture) text(chat int64) string { return f.api.Last(chat).Text }

func (f *fixture) buttons(chat int64) map[string]string { return fake.Buttons(f.api.Last(chat)) }

// answered tells that a button press was answered with the note.
func (f *fixture) answered(note string) bool {
	for _, c := range f.api.Calls {
		if c.Method == "answerCallbackQuery" && c.Params["text"] == note {
			return true
		}
	}
	return false
}

func (f *fixture) lastNote() string {
	for n := len(f.api.Calls) - 1; n >= 0; n-- {
		if f.api.Calls[n].Method == "answerCallbackQuery" {
			note, _ := f.api.Calls[n].Params["text"].(string)
			return note
		}
	}
	return ""
}

var codeRe = regexp.MustCompile(`<code>([A-Z0-9]{3}-[A-Z0-9]{3})</code>`)

func inviteCode(text string) string {
	found := codeRe.FindStringSubmatch(text)
	if found == nil {
		return ""
	}
	return found[1]
}

func (f *fixture) invite(note string) string {
	f.t.Helper()
	f.say(owner, "/invite "+note)
	return inviteCode(f.text(owner))
}

func (f *fixture) user(tgID int64) (access.User, bool) { return f.Access.User(tgID) }

func contains(t *testing.T, text string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(text, p) {
			t.Fatalf("no %q in:\n%s", p, text)
		}
	}
}

func lacks(t *testing.T, text string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if strings.Contains(text, p) {
			t.Fatalf("%q in:\n%s", p, text)
		}
	}
}

const (
	calc = "https://docs.google.com/spreadsheets/d/1CalcExampleSheetId_0123456789abc/edit#gid=0"
	alg  = "https://docs.google.com/spreadsheets/u/1/d/1AlgebraExampleSheetId-0123456789/edit"
)

func fakeButtons(m fake.Sent) map[string]string { return fake.Buttons(m) }
