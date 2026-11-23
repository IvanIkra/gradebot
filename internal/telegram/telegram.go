// Package telegram is a thin Bot API client: long polling and the few methods the bot needs. No bot library: rich
// messages (Bot API 10.3+: buttons inside them came then) are new, and the bot needs only a handful of calls.
package telegram

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/IvanIkra/gradebot/internal/rich"
)

const (
	PollTimeout = 50 // seconds getUpdates waits for an update
	Retries     = 3  // 429 and 5xx answers are retried this many times
)

// Error is an answer of the Bot API that is not ok.
type Error struct {
	Method      string
	Code        int
	Description string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %d %s", e.Method, e.Code, e.Description) }

// Blocked tells that the user blocked the bot or deleted their account.
func (e *Error) Blocked() bool { return e.Code == 403 }

// AsError gives the Bot API error in err, or nil.
func AsError(err error) *Error {
	if e, ok := err.(*Error); ok {
		return e
	}
	return nil
}

// Params of a call; nil values are left out.
type Params map[string]any

// Caller makes one call of the Bot API and gives its result.
type Caller func(method string, params Params) (json.RawMessage, error)

// API is the Bot API client.
type API struct {
	base   string
	client *http.Client
	Rich   bool   // turned off for good when the Bot API answers that it has no rich messages
	Call   Caller // the HTTP call; tests put their own
	Sleep  func(time.Duration)
}

// New makes a client of the Bot API at apiURL.
func New(token, apiURL string) *API {
	a := &API{base: apiURL + "/bot" + token, client: &http.Client{Timeout: (PollTimeout + 20) * time.Second},
		Rich: true, Sleep: time.Sleep}
	a.Call = a.httpCall
	return a
}

type answer struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  struct {
		RetryAfter *float64 `json:"retry_after"`
	} `json:"parameters"`
}

func clean(params Params) Params {
	out := Params{}
	for k, v := range params {
		if v == nil {
			continue
		}
		switch x := v.(type) {
		case *rich.Markup:
			if x == nil {
				continue
			}
		case *int64:
			if x == nil {
				continue
			}
		case *string:
			if x == nil {
				continue
			}
		}
		out[k] = v
	}
	return out
}

func (a *API) httpCall(method string, params Params) (json.RawMessage, error) {
	body := marshal(clean(params))
	for attempt := 0; ; attempt++ {
		resp, err := a.client.Post(a.base+"/"+method, "application/json", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		var data answer
		if json.Unmarshal(raw, &data) != nil {
			text := []rune(string(raw))
			data = answer{ErrorCode: resp.StatusCode, Description: string(text[:min(len(text), 200)])}
		}
		if data.OK {
			return data.Result, nil
		}
		code := data.ErrorCode
		if code == 0 {
			code = resp.StatusCode
		}
		if attempt < Retries && (code == 429 || code >= 500) {
			wait := math.Pow(2, float64(attempt))
			if data.Parameters.RetryAfter != nil {
				wait = *data.Parameters.RetryAfter
			}
			slog.Warn("retrying", "method", method, "code", code, "in", wait)
			a.Sleep(time.Duration(wait * float64(time.Second)))
			continue
		}
		return nil, &Error{Method: method, Code: code, Description: data.Description}
	}
}

func marshal(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// --- the types --------------------------------------------------------------------------------------------------

// User is a Telegram user.
type User struct {
	ID           int64  `json:"id"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name,omitempty"`
	Username     string `json:"username,omitempty"`
	LanguageCode string `json:"language_code,omitempty"`
}

// Chat is where a message is.
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type,omitempty"`
}

// Message is a message the bot gets.
type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from,omitempty"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text,omitempty"`
}

// CallbackQuery is a press of a button.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}

// Update is what getUpdates gives.
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

// --- the methods ------------------------------------------------------------------------------------------------

// GetMe gives the bot's username.
func (a *API) GetMe() (string, error) {
	raw, err := a.Call("getMe", Params{})
	if err != nil {
		return "", err
	}
	var me struct {
		Username string `json:"username"`
	}
	_ = json.Unmarshal(raw, &me)
	return me.Username, nil
}

// GetUpdates waits for updates from offset on.
func (a *API) GetUpdates(offset *int64, timeout int) ([]Update, error) {
	params := Params{"timeout": timeout, "allowed_updates": []string{"message", "callback_query"}}
	if offset != nil {
		params["offset"] = *offset
	}
	raw, err := a.Call("getUpdates", params)
	if err != nil {
		return nil, err
	}
	var updates []Update
	if err := json.Unmarshal(raw, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

func messageID(raw json.RawMessage) int64 {
	var msg struct {
		MessageID int64 `json:"message_id"`
	}
	_ = json.Unmarshal(raw, &msg)
	return msg.MessageID
}

// Send sends a *rich.Doc as a rich message (classic HTML without rich messages), a string as a classic HTML
// message; it gives the message id.
func (a *API) Send(chatID int64, doc any, keyboard *rich.Markup, silent bool) (int64, error) {
	var quiet any
	if silent {
		quiet = true
	}
	if d, ok := doc.(*rich.Doc); ok && a.Rich {
		raw, err := a.Call("sendRichMessage", Params{"chat_id": chatID, "rich_message": map[string]string{
			"html": d.Rich()}, "reply_markup": markup(keyboard), "disable_notification": quiet})
		if err == nil {
			return messageID(raw), nil
		}
		e := AsError(err)
		if e == nil || !noRich(e) {
			return 0, err
		}
		slog.Warn("the Bot API has no rich messages: classic HTML from now on", "error", e)
		a.Rich = false
	}
	text, kb := Classic(doc, keyboard)
	raw, err := a.Call("sendMessage", Params{"chat_id": chatID, "text": text, "parse_mode": "HTML",
		"link_preview_options": map[string]bool{"is_disabled": true}, "reply_markup": markup(kb),
		"disable_notification": quiet})
	if err != nil {
		return 0, err
	}
	return messageID(raw), nil
}

// SendText sends classic HTML.
func (a *API) SendText(chatID int64, text string) (int64, error) {
	return a.Send(chatID, text, nil, false)
}

func markup(m *rich.Markup) any {
	if m == nil {
		return nil
	}
	return m
}

// Edit edits what Send sent. An unchanged message is not an error.
func (a *API) Edit(chatID, messageID int64, doc any, keyboard *rich.Markup) error {
	params := Params{"chat_id": chatID, "message_id": messageID}
	if d, ok := doc.(*rich.Doc); ok && a.Rich {
		params["rich_message"] = map[string]string{"html": d.Rich()}
		params["reply_markup"] = markup(keyboard)
	} else {
		text, kb := Classic(doc, keyboard)
		params["text"], params["parse_mode"] = text, "HTML"
		params["link_preview_options"] = map[string]bool{"is_disabled": true}
		params["reply_markup"] = markup(kb)
	}
	_, err := a.Call("editMessageText", params)
	if e := AsError(err); e != nil && strings.Contains(e.Description, "message is not modified") {
		return nil
	}
	return err
}

// Answer answers a button press, with a note or none.
func (a *API) Answer(callbackID string, text string) error {
	params := Params{"callback_query_id": callbackID}
	if text != "" {
		params["text"] = text
	}
	_, err := a.Call("answerCallbackQuery", params)
	return err
}

// Command is a command of the bot's menu.
type Command struct{ Command, Description string }

// SetCommands sets the menu: for a chat, or for everyone when chatID is 0; lang "" is the default.
func (a *API) SetCommands(commands []Command, chatID int64, lang string) error {
	scope := map[string]any{"type": "default"}
	if chatID != 0 {
		scope = map[string]any{"type": "chat", "chat_id": chatID}
	}
	list := make([]map[string]string, len(commands))
	for n, c := range commands {
		list[n] = map[string]string{"command": c.Command, "description": c.Description}
	}
	params := Params{"commands": list, "scope": scope}
	if lang != "" {
		params["language_code"] = lang
	}
	_, err := a.Call("setMyCommands", params)
	return err
}

// Classic is a classic message: the Doc's text, and its buttons under the keyboard given.
func Classic(doc any, keyboard *rich.Markup) (string, *rich.Markup) {
	d, ok := doc.(*rich.Doc)
	if !ok {
		return doc.(string), keyboard
	}
	var rows [][]map[string]string
	for _, row := range d.ButtonRows() {
		line := make([]map[string]string, len(row))
		for n, button := range row {
			line[n] = rich.InlineButton(button)
		}
		rows = append(rows, line)
	}
	if len(rows) > 0 {
		merged := &rich.Markup{}
		if keyboard != nil {
			merged.InlineKeyboard = append(merged.InlineKeyboard, keyboard.InlineKeyboard...)
		}
		merged.InlineKeyboard = append(merged.InlineKeyboard, rows...)
		keyboard = merged
	}
	return d.Plain(), keyboard
}

// noRich tells the answer of a Bot API server that does not know sendRichMessage.
func noRich(err *Error) bool {
	return err.Code == 404 || strings.Contains(strings.ToLower(err.Description), "method not found")
}
