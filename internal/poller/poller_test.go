package poller_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IvanIkra/gradebot/internal/books"
	"github.com/IvanIkra/gradebot/internal/grades"
	"github.com/IvanIkra/gradebot/internal/layout"
	"github.com/IvanIkra/gradebot/internal/llm"
	"github.com/IvanIkra/gradebot/internal/poller"
	"github.com/IvanIkra/gradebot/internal/testkit"
	"github.com/IvanIkra/gradebot/internal/workbook"
)

// Google is a local server standing in for Google: Data is the workbook, or Status to answer with.
type Google struct {
	mu      sync.Mutex
	Data    []byte
	Status  int
	Titles  int
	Exports int
}

func NewGoogle(t *testing.T, p *poller.Poller) *Google {
	g := &Google{Data: books.Book("book")}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/htmlview") {
			g.Titles++
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html><title>Матанализ &quot;1&quot; - Google Диск</title></html>`))
			return
		}
		g.Exports++
		if g.Status != 0 {
			w.WriteHeader(g.Status)
			return
		}
		_, _ = w.Write(g.Data)
	}))
	t.Cleanup(srv.Close)
	p.ExportURL, p.TitleURL = srv.URL+"/d/%s/export", srv.URL+"/d/%s/htmlview"
	return g
}

type fixture struct {
	env    *testkit.Env
	llm    *books.FakeLLM
	poller *poller.Poller
	google *Google
	grades *grades.Grades
	sheet  int64
}

func setup(t *testing.T) *fixture {
	env := testkit.New(t)
	fake := &books.FakeLLM{}
	g := grades.New(env.DB, env.Clock.Now)
	p := poller.New(env.DB, env.Sheets, g, layout.New(env.DB, fake))
	f := &fixture{env: env, llm: fake, poller: p, grades: g}
	f.google = NewGoogle(t, p)
	env.AddUser(1, books.Ivan, "", "")
	env.AddUser(2, books.Anna, "en", "")
	sheet, _, _ := env.Sheets.Follow(1, books.URL, "")
	env.Sheets.Follow(2, books.URL, "")
	f.sheet = sheet.ID
	return f
}

func TestOneDownloadAndOneLayoutForEveryFollower(t *testing.T) {
	f := setup(t)
	reads := f.poller.Poll(true)
	if f.google.Exports != 1 || f.google.Titles != 1 || len(f.llm.Prompts) != 2 || len(reads) != 1 {
		t.Fatal(f.google, len(f.llm.Prompts))
	}
	read := reads[0]
	if read.Sheet.Title != `Матанализ "1"` || read.Error != "" {
		t.Fatal(read)
	}
	want := []poller.UserRead{{TgID: 1, Tabs: 2, Items: 2, First: true}, {TgID: 2, Tabs: 2, Items: 2, First: true}}
	if !reflect.DeepEqual(read.Users, want) {
		t.Fatal(read.Users)
	}
	f.google.Data = books.Book("hw63_quiz9")
	f.env.Clock.Advance(time.Hour)
	f.poller.Poll(true)
	if f.google.Titles != 1 || len(f.llm.Prompts) != 2 { // the layout is cached
		t.Fatal(f.google.Titles, len(f.llm.Prompts))
	}
	w := f.grades.Waiting(0)
	if len(w) != 2 || w[0].TgID != 1 || w[1].TgID != 1 {
		t.Fatal(w)
	}
}

func TestErrorsOfAUserAndOfASheet(t *testing.T) {
	f := setup(t)
	f.google.Data = books.Book("namesake")
	f.env.AddUser(3, "Никто Никтоевич", "", "")
	f.env.Sheets.Follow(3, books.URL, "")
	f.poller.Request(f.sheet, 1)
	f.poller.Request(f.sheet, 3)
	reads := f.poller.Poll(false)
	errs := map[int64]string{}
	for _, u := range reads[0].Users {
		errs[u.TgID] = u.Error
	}
	if !reflect.DeepEqual(errs, map[int64]string{1: "ambiguous: ДЗ-1А", 2: "", 3: "no_row"}) ||
		!reflect.DeepEqual(reads[0].Requested, map[int64]bool{1: true, 3: true}) {
		t.Fatal(errs, reads[0].Requested)
	}
	if items := f.grades.Items(1, f.sheet); len(items) != 1 || items[0].Label != "Quiz 1" {
		t.Fatal(items)
	}
	f.google.Data = []byte("<html>sign in</html>")
	f.poller.Request(f.sheet, 2)
	reads = f.poller.Poll(false)
	if s, _ := f.env.Sheets.Get(f.sheet); !strings.HasPrefix(reads[0].Error, "closed:") ||
		!strings.HasPrefix(s.Error, "closed:") {
		t.Fatal(reads[0].Error)
	}
}

func TestASheetFailingFor12HoursIsStale(t *testing.T) {
	f := setup(t)
	f.poller.Poll(true)
	f.google.Status = 404
	f.poller.Poll(true)
	if s, _ := f.env.Sheets.Get(f.sheet); s.Error != "download: HTTP 404 Not Found" {
		t.Fatal(s.Error)
	}
	f.env.Clock.Advance(13 * time.Hour)
	if r := f.poller.Poll(true); r[0].Stale != "2026-11-09T12:00:00+00:00" {
		t.Fatal(r[0])
	}
	if r := f.poller.Poll(true); r[0].Stale != "" {
		t.Fatal("told twice")
	}
	f.google.Status = 0
	if r := f.poller.Poll(true); !r[0].Back || r[0].Error != "" {
		t.Fatal(r[0])
	}
}

func TestAFailedLayoutRecordsNothingUntilTheNextPoll(t *testing.T) {
	f := setup(t)
	f.llm.Err = llm.BudgetExceeded{}
	if r := f.poller.Poll(true); !strings.HasPrefix(r[0].Error, "budget:") || len(f.grades.Items(1, f.sheet)) != 0 {
		t.Fatal(r[0])
	}
	f.llm.Err = nil
	if r := f.poller.Poll(true); r[0].Error != "" || len(f.grades.Items(1, f.sheet)) != 2 {
		t.Fatal(r[0])
	}
}

func TestNobodyToReadFor(t *testing.T) {
	env := testkit.New(t)
	p := poller.New(env.DB, env.Sheets, grades.New(env.DB, env.Clock.Now), layout.New(env.DB, &books.FakeLLM{}))
	g := NewGoogle(t, p)
	env.AddUser(1, "", "", "") // no full name yet
	env.Sheets.Follow(1, books.URL, "")
	env.AddUser(2, books.Anna, "", "")
	env.Sheets.Follow(2, books.URL, "")
	env.Access.SetPaused(2, true) // blocked the bot
	if r := p.Poll(true); len(r) != 0 || g.Exports != 0 {
		t.Fatal(r)
	}
}

type renaming struct {
	poller.Layouts
	rename func()
}

func (r renaming) Get(sheetID string, tab *workbook.Tab, rows []*workbook.Row, budget int, others []string) (layout.Spec, error) {
	r.rename()
	return r.Layouts.Get(sheetID, tab, rows, budget, others)
}

func TestANameChangedDuringTheReadRecordsNothing(t *testing.T) {
	f := setup(t)
	f.poller.Layouts = renaming{f.poller.Layouts, func() { f.env.Access.SetStudent(1, "Сидорова Мария") }}
	f.poller.Poll(true)
	if len(f.grades.Items(1, f.sheet)) != 0 || len(f.grades.Items(2, f.sheet)) == 0 {
		t.Fatal("recorded")
	}
}

func TestASheetThatBreaksTheReadDoesNotStopTheOthers(t *testing.T) {
	f := setup(t)
	f.env.AddUser(3, "Сидоров Пётр", "", "")
	other, _, _ := f.env.Sheets.Follow(3, "https://docs.google.com/spreadsheets/d/2ZyXwVuTsRqPoNmLkJiHgFeDcBa", "")
	calls := 0
	f.poller.Load = func(data []byte) ([]*workbook.Tab, error) {
		calls++
		if calls == 1 {
			panic(errors.New("boom"))
		}
		return workbook.Load(data)
	}
	reads := f.poller.Poll(true)
	if len(reads) != 2 || !strings.HasPrefix(reads[0].Error, "broken: ") || reads[1].Error != "" || calls != 2 {
		t.Fatal(reads)
	}
	if s, _ := f.env.Sheets.Get(reads[0].Sheet.ID); !strings.HasPrefix(s.Error, "broken: ") {
		t.Fatal(s.Error)
	}
	if reads[0].Sheet.ID != f.sheet || reads[1].Sheet.ID != other.ID {
		t.Fatal(reads)
	}
}

func TestThePlanHasEveryWorkGradedOrNot(t *testing.T) {
	f := setup(t)
	f.poller.Poll(true)
	var got [][3]any
	for _, w := range f.env.Sheets.Plan(f.sheet) {
		got = append(got, [3]any{w.Tab, w.LabelRu, w.Max})
	}
	want := [][3]any{{"ДЗ-1А", "ДЗ 1А", 7.0}, {"Квизы", "Квиз 1", 10.0}, {"Квизы", "Квиз 2", 10.0}}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
}

func TestANewIntervalAppliesAtOnceAndRequestsKeepTheSchedule(t *testing.T) {
	f := setup(t)
	f.poller.Minute = 20 * time.Millisecond // 30 "minutes" is 0.6 s
	var mu sync.Mutex
	var polls []bool
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		f.poller.Run(ctx, func(full bool) {
			mu.Lock()
			polls = append(polls, full)
			mu.Unlock()
			f.poller.Poll(false) // takes the requests
		})
		close(done)
	}()
	snapshot := func() []bool {
		mu.Lock()
		defer mu.Unlock()
		return append([]bool(nil), polls...)
	}
	time.Sleep(100 * time.Millisecond)
	if got := snapshot(); !reflect.DeepEqual(got, []bool{true}) {
		t.Fatal(got)
	}
	f.poller.Request(1, 0)
	time.Sleep(50 * time.Millisecond)
	if got := snapshot(); !reflect.DeepEqual(got, []bool{true, false}) {
		t.Fatal(got)
	}
	f.poller.Lock.Lock()            // as the bot holds it
	poller.SetInterval(f.env.DB, 5) // 0.1 s
	f.poller.Lock.Unlock()
	f.poller.Rescheduled()
	time.Sleep(300 * time.Millisecond)
	cancel()
	<-done
	got, full := snapshot(), 0
	for _, p := range got {
		if p {
			full++
		}
	}
	if got[1] || full < 3 {
		t.Fatal(got)
	}
}

func TestInterval(t *testing.T) {
	env := testkit.New(t)
	if poller.IntervalOf(env.DB) != 30 || poller.SetInterval(env.DB, 2) == nil || poller.SetInterval(env.DB, 20) != nil ||
		poller.IntervalOf(env.DB) != 20 {
		t.Fatal("interval")
	}
}
