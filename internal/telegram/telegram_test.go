package telegram_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/IvanIkra/gradebot/internal/rich"
	"github.com/IvanIkra/gradebot/internal/telegram"
	"github.com/IvanIkra/gradebot/internal/telegram/fake"
)

type server struct {
	answers  map[string][]map[string]any
	requests []struct {
		method string
		body   map[string]any
	}
	url string
}

func newServer(t *testing.T) *server {
	s := &server{answers: map[string][]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.requests = append(s.requests, struct {
			method string
			body   map[string]any
		}{method, body})
		queue := s.answers[method]
		if len(queue) == 0 {
			queue = []map[string]any{{"ok": true, "result": map[string]any{"message_id": 7}}}
		}
		answer := queue[0]
		if len(queue) > 1 {
			s.answers[method] = queue[1:]
		}
		status := 200
		if answer["ok"] != true {
			status = int(answer["error_code"].(float64))
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(answer)
	}))
	t.Cleanup(srv.Close)
	s.url = srv.URL
	return s
}

func TestCallRetries429ThenRaisesErrors(t *testing.T) {
	s := newServer(t)
	s.answers["getMe"] = []map[string]any{
		{"ok": false, "error_code": 429.0, "parameters": map[string]any{"retry_after": 0}},
		{"ok": true, "result": map[string]any{"username": "b"}}}
	s.answers["sendMessage"] = []map[string]any{{"ok": false, "error_code": 403.0,
		"description": "Forbidden: bot was blocked"}}
	api := telegram.New("TOKEN", s.url)
	api.Sleep = func(time.Duration) {}
	if name, err := api.GetMe(); err != nil || name != "b" {
		t.Fatal(name, err)
	}
	_, err := api.SendText(1, "hi")
	if e := telegram.AsError(err); e == nil || !e.Blocked() {
		t.Fatal(err)
	}
	var methods []string
	for _, r := range s.requests {
		methods = append(methods, r.method)
	}
	if !reflect.DeepEqual(methods, []string{"getMe", "getMe", "sendMessage"}) {
		t.Fatal(methods)
	}
	want := map[string]any{"chat_id": 1.0, "text": "hi", "parse_mode": "HTML",
		"link_preview_options": map[string]any{"is_disabled": true}}
	if got := s.requests[2].body; !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
}

func TestRichMessageAndSilent(t *testing.T) {
	s := newServer(t)
	api := telegram.New("TOKEN", s.url)
	if id, err := api.Send(5, rich.New().H("x"), nil, true); err != nil || id != 7 {
		t.Fatal(id, err)
	}
	want := map[string]any{"chat_id": 5.0, "rich_message": map[string]any{"html": "<h3>x</h3>"},
		"disable_notification": true}
	if len(s.requests) != 1 || s.requests[0].method != "sendRichMessage" || !reflect.DeepEqual(s.requests[0].body, want) {
		t.Fatal(s.requests)
	}
}

func TestFallsBackToClassicHTMLForGood(t *testing.T) {
	api := fake.New(false)
	_, _ = api.Send(5, rich.New().H("x").P("y"), nil, false)
	_, _ = api.Send(5, rich.New().H("z"), nil, false)
	_ = api.Edit(5, 101, rich.New().H("w"), nil)
	if m := api.Methods(); !reflect.DeepEqual(m, []string{"sendRichMessage", "sendMessage", "sendMessage",
		"editMessageText"}) {
		t.Fatal(m)
	}
	if api.Calls[1].Params["text"] != "<b>x</b>\n\ny" || api.Calls[3].Params["text"] != "<b>w</b>" || api.Rich {
		t.Fatal(api.Calls)
	}
}

func TestOtherRichErrorsAreNotHidden(t *testing.T) {
	api := fake.New(true)
	api.Fail["sendRichMessage"] = &telegram.Error{Method: "sendRichMessage", Code: 400,
		Description: "Bad Request: can't parse"}
	if _, err := api.Send(5, rich.New().H("x"), nil, false); err == nil || !api.Rich {
		t.Fatal(err)
	}
}

func TestUnchangedEditIsFine(t *testing.T) {
	api := fake.New(true)
	api.Fail["editMessageText"] = &telegram.Error{Method: "editMessageText", Code: 400,
		Description: "Bad Request: message is not modified"}
	if err := api.Edit(5, 1, rich.New().H("x"), nil); err != nil {
		t.Fatal(err)
	}
	if r := api.Calls[0].Params["rich_message"].(map[string]string); r["html"] != "<h3>x</h3>" {
		t.Fatal(r)
	}
}
