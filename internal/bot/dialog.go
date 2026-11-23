package bot

import (
	"time"

	"github.com/IvanIkra/gradebot/internal/i18n"
	"github.com/IvanIkra/gradebot/internal/rich"
	"github.com/IvanIkra/gradebot/internal/sheets"
)

// Commands asked for step by step. A command from the menu comes without arguments, so the bot asks for each one
// it still needs in a message of its own: buttons for the usual answers, Skip for an argument that may stay empty,
// Cancel always; the next plain message, or a button, is the answer. A command typed with all its arguments runs at
// once, and any command drops the question that was waiting.
//
// A flow is a list of steps and what to do with the answers. Each answer is checked when it comes: a wrong one is
// asked again with the reason. Questions live in memory: a restart forgets them, as does an hour without an answer.
//
// Callbacks: `an:<value>` answers the question of the message it is under (empty: skipped), `ac` cancels it.

const (
	AskFor    = time.Hour
	answerRow = 4 // answer buttons in a row
)

// Data are the answers of a flow by step name; a skipped one is "".
type Data map[string]string

// Choice is a button of the usual answers: its text and the answer it gives.
type Choice struct{ Text, Value string }

// Step is a question of a flow.
type Step struct {
	Name     string                                        // the key of the answer in the flow's data
	Prompt   func(Data) *rich.Doc                          // the question, given the answers so far
	Check    func(value string, data Data) (string, error) // the answer as kept; an error asks again
	Error    func(lang string, err error) string           // why an answer was wrong, in the language
	Choices  func(Data) []Choice                           // buttons
	Skip     func(Data) string                             // the text of the button that leaves it empty; "" for none
	When     func(Data) bool                               // asked only when this holds
	NoCancel bool                                          // a view that takes a typed answer too
}

// Ask is the question the bot waits an answer to in a chat.
type Ask struct {
	Chat      int64
	Lang      string
	Steps     []Step
	Done      func(Data)
	Data      Data
	Step      string
	MessageID int64
	At        time.Time
}

// Flow asks for the first answer missing from data, or runs done with all of them. An empty answer is a skipped
// one.
func (b *Bot) Flow(chat int64, lang string, steps []Step, done func(Data), data Data) {
	copied := Data{}
	for k, v := range data {
		copied[k] = v
	}
	b.goOn(&Ask{Chat: chat, Lang: lang, Steps: steps, Done: done, Data: copied}, "")
}

func (b *Bot) goOn(ask *Ask, errText string) {
	data := ask.Data
	for _, step := range ask.Steps {
		if step.When != nil && !step.When(data) {
			continue
		}
		if value, ok := data[step.Name]; ok {
			if value == "" {
				continue
			}
			if step.Check == nil {
				continue
			}
			checked, err := step.Check(value, data)
			if err == nil {
				data[step.Name] = checked
				continue
			}
			if step.Error != nil {
				errText = step.Error(ask.Lang, err)
			} else {
				errText = err.Error()
			}
			delete(data, step.Name)
		}
		b.ask(ask, step, errText)
		return
	}
	delete(b.Asks, ask.Chat)
	ask.Done(data)
}

func (b *Bot) ask(ask *Ask, step Step, errText string) {
	doc := rich.New()
	if errText != "" {
		doc.P(errText)
	}
	doc.Extend(step.Prompt(ask.Data))
	var choices []rich.Button
	if step.Choices != nil {
		for _, c := range step.Choices(ask.Data) {
			choices = append(choices, rich.B(c.Text, "an:"+c.Value))
		}
	}
	for start := 0; start < len(choices); start += answerRow {
		doc.Buttons(choices[start:min(len(choices), start+answerRow)]...)
	}
	var last []rich.Button
	if step.Skip != nil {
		if skip := step.Skip(ask.Data); skip != "" {
			last = append(last, rich.B(skip, "an:"))
		}
	}
	if !step.NoCancel {
		last = append(last, rich.B(i18n.Tr(ask.Lang, "Cancel", "Отмена"), "ac"))
	}
	doc.Buttons(last...)
	ask.Step, ask.At = step.Name, b.Now()
	id, err := b.API.Send(ask.Chat, doc, nil, false)
	if err != nil {
		panic(err)
	}
	ask.MessageID = id
	b.Asks[ask.Chat] = ask
}

// Waiting is the question waiting in a chat; nil when none, or it is older than AskFor.
func (b *Bot) Waiting(chat int64) *Ask {
	ask := b.Asks[chat]
	if ask != nil && b.Now().Sub(ask.At) > AskFor {
		delete(b.Asks, chat)
		return nil
	}
	return ask
}

// Answer takes a plain message as the answer to a waiting question: false when nothing waits.
func (b *Bot) Answer(chat int64, value string) bool {
	ask := b.Waiting(chat)
	if ask == nil {
		return false
	}
	ask.Data[ask.Step] = trimPy(value)
	b.goOn(ask, "")
	return true
}

// dialogCallback is `an` and `ac` under a question; a button of an older question is told so.
func (b *Bot) dialogCallback(chat, messageID int64, kind, value, lang string) string {
	ask := b.Waiting(chat)
	if ask == nil || ask.MessageID != messageID {
		return i18n.Tr(lang, "This question is closed", "Этот вопрос уже закрыт")
	}
	if kind == "ac" {
		delete(b.Asks, chat)
		b.edit(chat, messageID, i18n.Tr(lang, "Cancelled.", "Отменено."))
		return ""
	}
	ask.Data[ask.Step] = value
	b.goOn(ask, "")
	return ""
}

// asksHint is the line under the list of commands.
func asksHint(lang string) string {
	return i18n.Tr(lang, "A command sent without its arguments asks for them one by one.",
		"Команда без аргументов спросит их по одному.")
}

// linkStep asks for a Google Sheets link, kept as the link up to the sheet's id.
func linkStep(lang, name string) Step {
	return Step{Name: name,
		Prompt: func(Data) *rich.Doc {
			return rich.New().P(i18n.Tr(lang, "Send the sheet's link: ", "Пришли ссылку на таблицу: "),
				rich.Code("https://docs.google.com/spreadsheets/d/…"))
		},
		Check: func(value string, _ Data) (string, error) {
			_, link, err := sheets.ParseURL(value)
			return link, err
		},
		Error: func(lang string, _ error) string {
			return i18n.Tr(lang, "That's not a Google Sheets link.", "Это не ссылка на таблицу Google.")
		}}
}
