// Package fake is a Bot API that records the calls instead of going to Telegram.
package fake

import (
	"encoding/json"
	"html"
	"regexp"
	"strings"

	"github.com/IvanIkra/gradebot/internal/rich"
	"github.com/IvanIkra/gradebot/internal/telegram"
)

// Call is a recorded request.
type Call struct {
	Method string
	Params telegram.Params
}

// API is the real client with its calls recorded.
type API struct {
	*telegram.API
	Calls      []Call
	Fail       map[string]error // method -> the error it gives
	FailChat   map[int64]error  // chat -> the error every send to it gives
	ServerRich bool
	nextID     int64
	// Updates, when set, is what getUpdates gives, one batch a call.
	Updates [][]telegram.Update
}

// New makes a fake Bot API; rich false is a server without rich messages.
func New(rich bool) *API {
	f := &API{API: telegram.New("TOKEN", "http://bot.invalid"), Fail: map[string]error{},
		FailChat: map[int64]error{}, ServerRich: rich, nextID: 100}
	f.API.Call = f.call
	return f
}

func (f *API) call(method string, params telegram.Params) (json.RawMessage, error) {
	clean := telegram.Params{}
	for k, v := range params {
		if v == nil {
			continue
		}
		if m, ok := v.(*rich.Markup); ok && m == nil {
			continue
		}
		clean[k] = v
	}
	f.Calls = append(f.Calls, Call{method, clean})
	if method == "sendRichMessage" && !f.ServerRich {
		return nil, &telegram.Error{Method: method, Code: 404, Description: "Not Found"}
	}
	if err, ok := f.Fail[method]; ok {
		return nil, err
	}
	if strings.HasPrefix(method, "send") {
		if err, ok := f.FailChat[chatOf(clean)]; ok {
			f.Calls[len(f.Calls)-1].Method = "failed:" + method
			return nil, err
		}
		f.nextID++
		return json.Marshal(map[string]int64{"message_id": f.nextID})
	}
	if method == "getUpdates" {
		if len(f.Updates) == 0 {
			return json.RawMessage("[]"), nil
		}
		batch := f.Updates[0]
		f.Updates = f.Updates[1:]
		return json.Marshal(batch)
	}
	return json.RawMessage("true"), nil
}

func chatOf(params telegram.Params) int64 {
	switch v := params["chat_id"].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	}
	return 0
}

// Sent is a sent or edited message.
type Sent struct {
	ChatID   int64
	Text     string
	Method   string
	Keyboard *rich.Markup
	ID       int64 // the message edited; 0 for a new one
}

// Sent gives sent and edited messages, oldest first; chat 0 is every chat.
func (f *API) Sent(chat int64) []Sent {
	var out []Sent
	for _, c := range f.Calls {
		if c.Method != "sendRichMessage" && c.Method != "sendMessage" && c.Method != "editMessageText" {
			continue
		}
		text, _ := c.Params["text"].(string)
		if r, ok := c.Params["rich_message"].(map[string]string); ok {
			text = r["html"]
		}
		kb, _ := c.Params["reply_markup"].(*rich.Markup)
		id, _ := c.Params["message_id"].(int64)
		if chat == 0 || chatOf(c.Params) == chat {
			out = append(out, Sent{ChatID: chatOf(c.Params), Text: text, Method: c.Method, Keyboard: kb, ID: id})
		}
	}
	return out
}

// Last is the last message sent to or edited in a chat.
func (f *API) Last(chat int64) Sent {
	sent := f.Sent(chat)
	if len(sent) == 0 {
		return Sent{}
	}
	return sent[len(sent)-1]
}

// Methods are the methods called, in order.
func (f *API) Methods() []string {
	out := make([]string, len(f.Calls))
	for n, c := range f.Calls {
		out[n] = c.Method
	}
	return out
}

// Count counts the calls of a method.
func (f *API) Count(method string) int {
	n := 0
	for _, c := range f.Calls {
		if c.Method == method {
			n++
		}
	}
	return n
}

// Reset forgets the calls.
func (f *API) Reset() { f.Calls = nil }

var buttonRe = regexp.MustCompile(`<tg-button type="\w+" (?:data|url)="([^"]*)"[^>]*>(.*?)</tg-button>`)

// Buttons maps text to callback data (or url) of a message's buttons: its keyboard and those inside a rich message.
func Buttons(m Sent) map[string]string {
	found := map[string]string{}
	if m.Keyboard != nil {
		for _, row := range m.Keyboard.InlineKeyboard {
			for _, b := range row {
				if data, ok := b["callback_data"]; ok {
					found[b["text"]] = data
				} else {
					found[b["text"]] = b["url"]
				}
			}
		}
	}
	for _, match := range buttonRe.FindAllStringSubmatch(m.Text, -1) {
		found[html.UnescapeString(match[2])] = html.UnescapeString(match[1])
	}
	return found
}

// ButtonTexts is the texts of a message's buttons in order.
func ButtonTexts(m Sent) []string {
	var out []string
	if m.Keyboard != nil {
		for _, row := range m.Keyboard.InlineKeyboard {
			for _, b := range row {
				out = append(out, b["text"])
			}
		}
	}
	for _, match := range buttonRe.FindAllStringSubmatch(m.Text, -1) {
		out = append(out, html.UnescapeString(match[2]))
	}
	return out
}
