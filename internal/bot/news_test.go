package bot

import (
	"reflect"
	"strings"
	"testing"

	"github.com/IvanIkra/gradebot/internal/rich"
	"github.com/IvanIkra/gradebot/internal/telegram"
	"github.com/IvanIkra/gradebot/internal/telegram/fake"
)

func (f *fixture) comeInAsked(tgID int64, firstName string) {
	f.t.Helper()
	f.say(owner, "/invite")
	for f.bot.Asks[owner] != nil {
		f.answer(owner, "")
	}
	code := strings.SplitN(f.text(owner), "<code>", 2)[1][:7]
	f.say(tgID, "/start "+code, first(firstName), noUsername())
}

func (f *fixture) ownerCalls() []fake.Call {
	var out []fake.Call
	for _, c := range f.api.Calls {
		if (c.Method == "sendRichMessage" || c.Method == "editMessageText") && c.Params["chat_id"] == owner {
			out = append(out, c)
		}
	}
	return out
}

func TestNewsGathersInOneSilentMessage(t *testing.T) {
	f := setup(t)
	f.comeInAsked(petr, "Пётр")
	start := len(f.ownerCalls())
	f.say(petr, "/name Петров Пётр", noUsername())
	f.say(anna, "/start", noUsername()) // a stranger is no news
	calls := f.ownerCalls()[start-1:]
	if len(calls) != 2 || calls[0].Method != "sendRichMessage" || calls[0].Params["disable_notification"] != true ||
		calls[1].Method != "editMessageText" {
		t.Fatal(calls)
	}
	var state digestState
	f.DB.GetJSON(newsKey, &state)
	if calls[1].Params["message_id"] != state.MessageID {
		t.Fatal(calls[1].Params, state)
	}
	contains(t, f.text(owner), "Новости · 2", "вход по коду", "ФИО <b>Петров Пётр</b>")
	got := f.buttons(owner)
	if len(got) != 2 || got["Удалить Пётр"] == "" || got["Сбросить ФИО: Пётр"] == "" {
		t.Fatal(got)
	}
}

func TestTheOwnerLookingOrARequestStartsANewDigest(t *testing.T) {
	f := setup(t)
	f.comeInAsked(petr, "Пётр")
	f.say(owner, "/status") // the owner has seen the chat
	f.say(petr, "/name Петров Пётр", noUsername())
	if f.last(owner).Method != "sendRichMessage" {
		t.Fatal(f.last(owner))
	}
	lacks(t, f.text(owner), "Новости ·")
	f.bot.TellOwner(rich.New().P("a request")) // a message that needs the owner, with a sound
	f.say(petr, "/stop", noUsername())
	f.press(petr, "stop!")
	if f.last(owner).Method != "sendRichMessage" {
		t.Fatal(f.last(owner))
	}
	contains(t, f.text(owner), "через /stop")
	lacks(t, f.text(owner), "ФИО")
}

func TestAButtonInADigestAnswersInANewMessage(t *testing.T) {
	f := setup(t)
	f.comeInAsked(petr, "Пётр")
	digest := f.buttons(owner)
	var ids []int64
	f.DB.GetJSON(digestsKey, &ids)
	if len(ids) != 1 {
		t.Fatal(ids)
	}
	f.handle(callbackAt(owner, digest["Удалить Пётр"], ids[0]))
	calls := f.ownerCalls()
	last := calls[len(calls)-1]
	if last.Method != "sendRichMessage" { // the digest stays
		t.Fatal(last)
	}
	contains(t, last.Params["rich_message"].(map[string]string)["html"], "Удалить Пётр?")
}

func TestADigestThatCannotBeEditedIsStartedAnew(t *testing.T) {
	f := setup(t)
	f.comeInAsked(petr, "Пётр")
	f.api.Fail["editMessageText"] = &telegram.Error{Method: "editMessageText", Code: 400,
		Description: "Bad Request: message to edit not found"}
	f.say(petr, "/name Петров Пётр", noUsername())
	if last := f.api.Calls[len(f.api.Calls)-1]; last.Method != "sendRichMessage" {
		t.Fatal(last)
	}
	lacks(t, f.text(owner), "Новости ·")
}

var _ = reflect.DeepEqual
