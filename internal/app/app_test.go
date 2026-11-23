package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IvanIkra/gradebot/internal/books"
	"github.com/IvanIkra/gradebot/internal/bot"
	"github.com/IvanIkra/gradebot/internal/layout"
	"github.com/IvanIkra/gradebot/internal/llm"
	"github.com/IvanIkra/gradebot/internal/poller"
	"github.com/IvanIkra/gradebot/internal/store"
	"github.com/IvanIkra/gradebot/internal/telegram"
	"github.com/IvanIkra/gradebot/internal/telegram/fake"
	"github.com/IvanIkra/gradebot/internal/testkit"
)

const owner = testkit.Owner

// google stands in for Google: data is the workbook, or status to answer with.
type google struct {
	mu      sync.Mutex
	data    []byte
	status  int
	titles  int
	exports int
}

type world struct {
	*testkit.Env
	t      *testing.T
	api    *fake.API
	bot    *bot.Bot
	poller *poller.Poller
	llm    *books.FakeLLM
	google *google
}

func setup(t *testing.T) *world {
	env := testkit.New(t)
	api := fake.New(true)
	b := bot.New(api.API, env.DB, env.Access, env.Sheets, owner, testkit.Zone, "gradebot_test_bot", env.Clock.Now)
	fakeLLM := &books.FakeLLM{}
	p := poller.New(env.DB, env.Sheets, b.Grades, layout.New(env.DB, fakeLLM))
	b.Poller, p.OnRead = p, b.AfterRead
	g := &google{data: books.Book("book")}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/htmlview") {
			g.titles++
			_, _ = w.Write([]byte(`<html><title>Матанализ &quot;1&quot; - Google Диск</title></html>`))
			return
		}
		g.exports++
		if g.status != 0 {
			w.WriteHeader(g.status)
			return
		}
		_, _ = w.Write(g.data)
	}))
	t.Cleanup(srv.Close)
	p.ExportURL, p.TitleURL = srv.URL+"/d/%s/export", srv.URL+"/d/%s/htmlview"
	return &world{Env: env, t: t, api: api, bot: b, poller: p, llm: fakeLLM, google: g}
}

// two: Ivan and Anna (in English) follow the test sheet.
func (w *world) two() int64 {
	w.AddUser(1, books.Ivan, "", "")
	w.AddUser(2, books.Anna, "en", "")
	sheet, _, _ := w.Sheets.Follow(1, books.URL, "")
	w.Sheets.Follow(2, books.URL, "")
	return sheet.ID
}

func message(sender int64, text string) telegram.Update {
	return telegram.Update{UpdateID: 1, Message: &telegram.Message{MessageID: 1, From: &telegram.User{ID: sender,
		FirstName: "Пётр", LanguageCode: "ru", Username: "petr_example"}, Chat: telegram.Chat{ID: sender,
		Type: "private"}, Text: text}}
}

func callback(sender int64, data string, messageID int64) telegram.Update {
	return telegram.Update{UpdateID: 1, CallbackQuery: &telegram.CallbackQuery{ID: "cb1",
		From: telegram.User{ID: sender, FirstName: "X"}, Data: data,
		Message: &telegram.Message{MessageID: messageID, Chat: telegram.Chat{ID: sender}}}}
}

func (w *world) text(chat int64) string { return w.api.Last(chat).Text }

func contains(t *testing.T, text string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(text, p) {
			t.Fatalf("no %q in:\n%s", p, text)
		}
	}
}

func TestOneDownloadAndOneLayoutForEveryFollower(t *testing.T) {
	w := setup(t)
	w.two()
	reads := w.poller.Poll(true)
	if w.google.exports != 1 || w.google.titles != 1 || len(w.llm.Prompts) != 2 || len(reads) != 1 {
		t.Fatal(w.google, len(w.llm.Prompts))
	}
	if reads[0].Sheet.Title != `Матанализ "1"` || reads[0].Error != "" {
		t.Fatal(reads[0])
	}
	contains(t, w.text(1), "подключена: вкладок 2, оценок 2", "Матанали") // Quiz 2 is a placeholder zero
	contains(t, w.text(2), "connected: tabs 2, grades 2")
	if len(w.bot.Grades.Waiting(0)) != 0 {
		t.Fatal("waiting")
	}
	w.google.data = books.Book("hw63_quiz9")
	w.Clock.Advance(time.Hour)
	sent := len(w.api.Sent(0))
	w.poller.Poll(true)
	if w.google.titles != 1 || len(w.llm.Prompts) != 2 { // the layout is cached
		t.Fatal(w.google.titles, len(w.llm.Prompts))
	}
	news := w.api.Sent(0)[sent:] // one message, to Ivan only
	if len(news) != 1 || news[0].ChatID != 1 || news[0].Method != "sendRichMessage" {
		t.Fatal(news)
	}
	url := strings.Split(books.URL, "/edit")[0]
	contains(t, news[0].Text, `<h3><a href="`+url+`">Матанализ "1"</a> #Матанали</h3>`,
		`<tr><td>ДЗ 1А</td><td align="right"><b>9</b> +1</td><td align="right">6.3 из 7</td>`+
			`<td align="right">8.4</td></tr>`,
		`<tr><td>Квиз 1</td><td align="right"><b>9</b></td><td align="right"></td><td align="right">8</td>`)
	if got := fake.Buttons(news[0]); !reflect.DeepEqual(got, map[string]string{"Все оценки": "grades"}) {
		t.Fatal(got)
	}
	last := w.api.Calls[len(w.api.Calls)-1]
	if _, quiet := last.Params["disable_notification"]; quiet { // 15:00 in Moscow: not quiet
		t.Fatal(last)
	}
	w.poller.Poll(true)
	if len(w.api.Sent(0)) != sent+1 || len(w.bot.Grades.Waiting(0)) != 0 { // nothing twice
		t.Fatal(len(w.api.Sent(0)))
	}
}

func TestARequestReadsOnlyThatSheetAndTellsTheUser(t *testing.T) {
	w := setup(t)
	sheet := w.two()
	w.AddUser(3, "Сидорова Мария", "", "")
	w.Sheets.Follow(3, "https://docs.google.com/spreadsheets/d/2ZyXwVuTsRqPoNmLkJiHgFeDc", "")
	w.bot.Handle(message(3, "/sheet "+books.URL))
	contains(t, w.text(3), "Сейчас прочитаю")
	reads := w.poller.Poll(false)
	if len(reads) != 1 || reads[0].Sheet.ID != sheet || !reflect.DeepEqual(reads[0].Requested, map[int64]bool{3: true}) ||
		w.google.exports != 1 {
		t.Fatal(reads)
	}
	contains(t, w.text(3), "подключена: вкладок 1, оценок 1") // her row is in the quizzes tab only
}

func TestANewNameForgetsTheGradesAndReadsAgain(t *testing.T) {
	w := setup(t)
	sheet := w.two()
	w.poller.Poll(true)
	w.google.data = books.Book("quiz9")
	w.bot.Handle(message(1, "/name Петрова Анна"))
	contains(t, w.text(1), "Сейчас прочитаю твои таблицы")
	if len(w.bot.Grades.Items(1, sheet)) != 0 {
		t.Fatal("kept")
	}
	reads := w.poller.Poll(false)
	if !reflect.DeepEqual(reads[0].Requested, map[int64]bool{1: true}) || len(w.bot.Grades.Waiting(0)) != 0 {
		t.Fatal(reads[0]) // a baseline for the new name, no news
	}
	contains(t, w.text(1), "подключена")
	var values []float64
	for _, it := range w.bot.Grades.Items(1, sheet) {
		values = append(values, it.Value)
	}
	if !reflect.DeepEqual(values, []float64{10, 9}) { // her grades now
		t.Fatal(values)
	}
}

func TestErrorsOfAUserAndOfASheet(t *testing.T) {
	w := setup(t)
	sheet := w.two()
	w.google.data = books.Book("namesake")
	w.AddUser(3, "Никто Никтоевич", "", "")
	w.Sheets.Follow(3, books.URL, "")
	w.poller.Request(sheet, 1)
	w.poller.Request(sheet, 3)
	w.poller.Poll(false)
	contains(t, w.text(1), "подключена: вкладок 1, оценок 1; ФИО подходит к нескольким строкам на вкладках: ДЗ-1А")
	contains(t, w.text(3), "твоей строки нет ни на одной вкладке")
	row, _ := store.Get[struct{ Error, BaselineAt string }](w.DB, "select error, baseline_at from subscriptions "+
		"where tg_id = 1")
	if row.Error != "ambiguous: ДЗ-1А" || row.BaselineAt == "" {
		t.Fatal(row)
	}
	w.google.data = []byte("<html>sign in</html>")
	w.poller.Request(sheet, 2)
	w.poller.Poll(false)
	contains(t, w.text(2), "closed: open it to anyone with the link")
}

func TestASheetFailingFor12HoursIsToldOnceAndItsReturnToo(t *testing.T) {
	w := setup(t)
	sheet := w.two()
	w.poller.Poll(true)
	w.google.status = 404
	w.poller.Poll(true)
	sent := len(w.api.Sent(0))
	w.Clock.Advance(13 * time.Hour)
	w.poller.Poll(true)
	w.poller.Poll(true)
	var chats []int64
	for _, m := range w.api.Sent(0)[sent:] {
		chats = append(chats, m.ChatID)
	}
	sort.Slice(chats, func(i, j int) bool { return chats[i] < chats[j] })
	if !reflect.DeepEqual(chats, []int64{1, 2, owner}) {
		t.Fatal(chats)
	}
	contains(t, w.text(1), "не читается с 09.11 15:00")
	contains(t, w.text(2), "cannot be read since")
	contains(t, w.text(owner), "download: HTTP 404")
	w.google.status = 0
	w.poller.Poll(true)
	contains(t, w.text(1), "снова читается")
	if len(w.api.Sent(0)) != sent+6 || len(w.bot.Grades.Waiting(0)) != 0 {
		t.Fatal(len(w.api.Sent(0)) - sent)
	}
	if s, _ := w.Sheets.Get(sheet); s.Error != "" {
		t.Fatal(s.Error)
	}
}

func TestAFailingSheetIsNotToldToWhomTheBotMustNotWrite(t *testing.T) {
	w := setup(t)
	w.two()
	w.poller.Poll(true)
	w.google.status = 404
	w.poller.Poll(true)
	w.DB.Exec("update users set expires_at = ?, access_told = 2 where tg_id = 2", "2026-11-09T12:00:00+00:00")
	sent := len(w.api.Sent(0))
	w.Clock.Advance(13 * time.Hour)
	w.poller.Poll(true)
	var chats []int64
	for _, m := range w.api.Sent(0)[sent:] {
		chats = append(chats, m.ChatID)
	}
	sort.Slice(chats, func(i, j int) bool { return chats[i] < chats[j] })
	if !reflect.DeepEqual(chats, []int64{1, owner}) {
		t.Fatal(chats)
	}
	contains(t, w.text(1), "не найдена: возможно, её удалили")
}

func TestRelayoutDropsTheCache(t *testing.T) {
	w := setup(t)
	w.two()
	w.poller.Poll(true)
	w.bot.Handle(message(owner, "/relayout "+books.URL))
	contains(t, w.text(owner), "сброшена")
	w.poller.Poll(false)
	if len(w.llm.Prompts) != 4 {
		t.Fatal(len(w.llm.Prompts))
	}
	w.bot.Handle(message(owner, "/relayout https://docs.google.com/spreadsheets/d/3QwErTyUiOpAsDfGhJkLzXcVb"))
	contains(t, w.text(owner), "Бот не читает такую таблицу")
}

func TestAGroupSheetIsReadForEveryoneAtOnce(t *testing.T) {
	w := setup(t)
	w.AddUser(1, books.Ivan, "", "")
	group, _ := w.bot.Groups.Create("A1")
	w.bot.Groups.Join(1, group.ID)
	w.bot.Handle(message(owner, "/groupsheet "+books.URL+" Calc"))
	reads := w.poller.Poll(false)
	if len(reads) != 1 || len(reads[0].Requested) != 0 || len(reads[0].Users) != 1 || !reads[0].Users[0].First {
		t.Fatal(reads)
	}
	contains(t, w.text(1), "Calc", "подключена")
}

func TestTheOwnerHearsAboutTheLLMLimit(t *testing.T) {
	w := setup(t)
	w.bot.BudgetNotice(llm.Stop, 1.02, 1.0)
	contains(t, w.text(owner), "monthly LLM limit is spent", "$1.02 / $1.00")
}

func TestTheOwnerSetsTheInterval(t *testing.T) {
	w := setup(t)
	w.bot.Handle(message(owner, "/interval"))
	contains(t, w.text(owner), "раз в <b>30 мин</b>")
	want := map[string]string{"10 мин": "iv:10", "15 мин": "iv:15", "60 мин": "iv:60"}
	if got := fake.Buttons(w.api.Last(owner)); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	w.bot.Handle(callback(owner, "iv:15", 77))
	if poller.IntervalOf(w.DB) != 15 || w.api.Last(owner).Method != "editMessageText" {
		t.Fatal(poller.IntervalOf(w.DB))
	}
	contains(t, w.text(owner), "iv:30")
	w.bot.Handle(message(owner, "/interval 20"))
	if poller.IntervalOf(w.DB) != 20 {
		t.Fatal(poller.IntervalOf(w.DB))
	}
	for _, wrong := range []string{"/interval 2", "/interval 500", "/interval often"} {
		w.bot.Handle(message(owner, wrong))
		contains(t, w.text(owner), "Минуты от 5 до 240")
	}
	w.bot.Handle(callback(owner, "iv:1", 555))
	w.bot.Handle(message(1, "/interval 5")) // a stranger: no
	if poller.IntervalOf(w.DB) != 20 {
		t.Fatal(poller.IntervalOf(w.DB))
	}
}

func TestPollHandlesUpdatesAndKeepsTheOffset(t *testing.T) {
	w := setup(t)
	s := &Service{Store: w.DB}
	first, second := message(owner, "/help"), message(owner, "/codes")
	first.UpdateID, second.UpdateID = 10, 11
	w.api.Updates = [][]telegram.Update{{first, second}}
	fails := 1
	call := w.api.API.Call
	w.api.API.Call = func(method string, params telegram.Params) (json.RawMessage, error) {
		if method == "sendRichMessage" && fails > 0 { // the first update fails, the second is still handled
			fails--
			w.api.Calls = append(w.api.Calls, fake.Call{Method: method, Params: params})
			panic("boom")
		}
		return call(method, params)
	}
	s.PollOnce(context.Background(), w.api.API, w.bot)
	var offset int64
	w.DB.GetJSON(Offset, &offset)
	if offset != 12 || w.api.Count("sendRichMessage") != 2 {
		t.Fatal(offset, w.api.Count("sendRichMessage"))
	}
}
