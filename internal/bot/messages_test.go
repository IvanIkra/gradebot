package bot

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/IvanIkra/gradebot/internal/books"
	"github.com/IvanIkra/gradebot/internal/grades"
	"github.com/IvanIkra/gradebot/internal/layout"
	"github.com/IvanIkra/gradebot/internal/llm"
	"github.com/IvanIkra/gradebot/internal/rich"
	"github.com/IvanIkra/gradebot/internal/sheets"
	"github.com/IvanIkra/gradebot/internal/telegram"
	"github.com/IvanIkra/gradebot/internal/telegram/fake"
)

const ivan = int64(1)

func gradeItem(key, label string, value float64, opts ...func(*layout.Item)) layout.Item {
	it := layout.Item{Key: key, Tab: "T", Header: key, Label: label, Value: value, Raw: value, Max: 10}
	for _, o := range opts {
		o(&it)
	}
	return it
}

func raw(r, mx float64) func(*layout.Item)    { return func(i *layout.Item) { i.Raw, i.Max = r, mx } }
func pos(n int) func(*layout.Item)            { return func(i *layout.Item) { i.Position = n } }
func parts(p ...[2]string) func(*layout.Item) { return func(i *layout.Item) { i.Parts = p } }

// withFollowed: Ivan follows a sheet titled «Матанализ», read once (the baseline).
func withFollowed(t *testing.T) (*fixture, int64) {
	f := setup(t)
	f.AddUser(ivan, "Иванов Иван", "", "")
	sheet, _, _ := f.Sheets.Follow(ivan, books.URL, "")
	f.Sheets.SetTitle(sheet.ID, "Матанализ")
	f.bot.Grades.Record(ivan, sheet.ID, []layout.Item{gradeItem("k1", "HW 1", 7, raw(3.5, 5))}, 1, "")
	return f, sheet.ID
}

func (f *fixture) news(sheet int64, items ...layout.Item) {
	f.Clock.Advance(30 * time.Minute)
	f.bot.Grades.Record(ivan, sheet, items, 1, "")
}

func lower(n int) func() {
	saved := rich.MaxBlocks
	rich.MaxBlocks = n
	return func() { rich.MaxBlocks = saved }
}

// --- rich -------------------------------------------------------------------------------------------------------

func TestDetailsButtonsAndAlignment(t *testing.T) {
	inner := rich.New().TableHTML([]any{"Задание", "Оценка"}, [][]any{{rich.Mark("ДЗ 1"), rich.Bold("8")}},
		[]string{"", "right"})
	doc := rich.New().Details("Матан · 1", inner.Buttons(rich.BS("Неверно", "wr:1", "link")), true).Buttons(
		rich.B("Обновить", "rf"), rich.B("Открыть", "https://example.com/?a=1&b=2"))
	want := `<details open><summary>Матан · 1</summary><table bordered striped compact><tr><th>Задание</th>` +
		`<th align="right">Оценка</th></tr><tr><td><b><u>ДЗ 1</u></b></td><td align="right"><b>8</b></td></tr>` +
		`</table><tg-button-row><tg-button type="callback_data" data="wr:1" style="link">Неверно</tg-button>` +
		`</tg-button-row></details><tg-button-row><tg-button type="callback_data" data="rf">Обновить</tg-button>` +
		`<tg-button type="url" url="https://example.com/?a=1&amp;b=2">Открыть</tg-button></tg-button-row>`
	if got := doc.Rich(); got != want {
		t.Fatalf("%s\n%s", got, want)
	}
	if doc.Count() != 6 { // details, table, two rows, a row of buttons inside, one outside
		t.Fatal(doc.Count())
	}
	if p := doc.Plain(); !strings.Contains(p, "<u>ДЗ 1</u>") || strings.Contains(p, "tg-button") {
		t.Fatal(p)
	}
	var sizes []int
	for _, row := range doc.ButtonRows() {
		sizes = append(sizes, len(row))
	}
	if !reflect.DeepEqual(sizes, []int{1, 2}) {
		t.Fatal(sizes)
	}
	for _, bad := range []func(){
		func() {
			var nine []rich.Button
			for range 9 {
				nine = append(nine, rich.B("x", "y"))
			}
			rich.New().Buttons(nine...)
		},
		func() { rich.New().Buttons(rich.BS("x", "y", "blue")) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("no panic")
				}
			}()
			bad()
		}()
	}
}

func TestLimits(t *testing.T) {
	var rows [][]string
	for range 10 {
		rows = append(rows, []string{"x"})
	}
	doc := rich.New().Table(nil, rows, nil)
	if !doc.Fits() {
		t.Fatal("does not fit")
	}
	defer lower(10)()
	if doc.Fits() {
		t.Fatal("fits")
	}
}

func TestWithoutRichMessagesTheButtonsGoToTheKeyboard(t *testing.T) {
	api := fake.New(false)
	api.Send(5, rich.New().P("hi").Buttons(rich.BS("Да", "yes", "danger"), rich.B("Сайт", "https://example.com")), nil,
		false)
	last := api.Calls[len(api.Calls)-1]
	want := &rich.Markup{InlineKeyboard: [][]map[string]string{{{"text": "Да", "callback_data": "yes"},
		{"text": "Сайт", "url": "https://example.com"}}}}
	if last.Method != "sendMessage" || !reflect.DeepEqual(last.Params["reply_markup"], want) {
		t.Fatal(last)
	}
	api.Edit(5, 9, rich.New().P("hi").Buttons(rich.B("Да", "yes")), &rich.Markup{InlineKeyboard: [][]map[string]string{
		{{"text": "A", "callback_data": "a"}}}})
	kb := api.Calls[len(api.Calls)-1].Params["reply_markup"].(*rich.Markup)
	if len(kb.InlineKeyboard) != 2 || len(kb.InlineKeyboard[0]) != 1 || len(kb.InlineKeyboard[1]) != 1 {
		t.Fatal(kb)
	}
}

// --- cards ------------------------------------------------------------------------------------------------------

func TestHashtagAndNumbers(t *testing.T) {
	if hashtag("Calc 2") != "#Calc_2" || hashtag("2024") != "" || hashtag("") != "" {
		t.Fatal(hashtag("Calc 2"))
	}
	if score(8.43) != "8.4" || score(9.0) != "9" || score(9.96) != "10" || points(5.9) != "5.9" || points(7.0) != "7" {
		t.Fatal(score(8.43))
	}
}

func event(id int64, key string, value float64, previous *float64) grades.Event {
	return grades.Event{ID: id, Key: key, Label: key, Value: value, Raw: value, Max: 10, Previous: previous}
}

func fp(v float64) *float64 { return &v }

func TestCollapseKeepsTheLatestWithTheFirstPrevious(t *testing.T) {
	rows, dropped := collapse([]grades.Event{event(1, "a", 6, fp(5)), event(2, "b", 7, nil), event(3, "a", 8, fp(6)),
		event(4, "c", 4, fp(5)), event(5, "c", 5, fp(4))})
	if len(rows) != 2 || rows[0].Key != "a" || rows[0].Value != 8 || *rows[0].Previous != 5 ||
		!reflect.DeepEqual(rows[0].IDs, []int64{1, 3}) || rows[1].Key != "b" || rows[1].Previous != nil ||
		!reflect.DeepEqual(rows[1].IDs, []int64{2}) {
		t.Fatal(rows)
	}
	if !reflect.DeepEqual(dropped, []int64{4, 5}) { // c went back to 5: no news
		t.Fatal(dropped)
	}
}

func TestNewsIsSplitWhenItDoesNotFit(t *testing.T) {
	unit := unitOf(sheets.Sheet{ID: 1, SheetID: strings.Repeat("x", 25), URL: books.URL, Title: "Матанализ",
		Course: "Calc"})
	var rows []*Row
	for n := range 12 {
		r := rowOfEvent(event(int64(n), "k"+itoa(int64(n)), 8, nil))
		rows = append(rows, r)
	}
	messages := newsMessages([]Section{{unit, rows}, {unit, rows[:1]}}, "ru")
	want := append([]int64{}, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 0)
	if len(messages) != 1 || !reflect.DeepEqual(messages[0].IDs, want) ||
		strings.Count(messages[0].Doc.Rich(), "<h3>") != 2 {
		t.Fatal(messages)
	}
	defer lower(12)()
	messages = newsMessages([]Section{{unit, rows}}, "ru")
	var all []int64
	for _, m := range messages {
		if !m.Doc.Fits() {
			t.Fatal("does not fit")
		}
		all = append(all, m.IDs...)
		if got := fake.Buttons(fake.Sent{Text: m.Doc.Rich()}); !reflect.DeepEqual(got, map[string]string{
			"Все оценки": "grades"}) {
			t.Fatal(got)
		}
	}
	if len(messages) < 2 || len(all) != 12 {
		t.Fatal(len(messages), all)
	}
}

// --- sending ----------------------------------------------------------------------------------------------------

func TestNewGradesComeInOneMessageAndQuietlyAtNight(t *testing.T) {
	f, sheet := withFollowed(t)
	f.news(sheet, gradeItem("k1", "HW 1", 9, raw(4.5, 5)), gradeItem("k2", "Quiz 1", 6, pos(1)))
	f.Clock.Moment = time.Date(2026, 11, 9, 21, 0, 0, 0, time.UTC) // midnight in Moscow
	f.bot.SendGrades()
	sent := f.api.Sent(ivan)
	if len(sent) != 1 {
		t.Fatal(sent)
	}
	contains(t, sent[0].Text, "HW 1", "Quiz 1", "4.5 из 5")
	if f.api.Calls[len(f.api.Calls)-1].Params["disable_notification"] != true || len(f.bot.Grades.Waiting(0)) != 0 {
		t.Fatal("not quiet")
	}
}

func TestAFailureKeepsTheGradesForTheNextPoll(t *testing.T) {
	f, sheet := withFollowed(t)
	f.news(sheet, gradeItem("k1", "HW 1", 9))
	f.api.FailChat[ivan] = &telegram.Error{Method: "sendRichMessage", Code: 500, Description: "Internal Server Error"}
	f.bot.SendGrades()
	if u, _ := f.user(ivan); len(f.bot.Grades.Waiting(0)) != 1 || u.Paused {
		t.Fatal(u)
	}
	delete(f.api.FailChat, ivan)
	f.bot.SendGrades()
	if len(f.bot.Grades.Waiting(0)) != 0 || len(f.api.Sent(ivan)) != 1 {
		t.Fatal("not sent")
	}
}

func TestAUserWhoBlockedTheBotIsPausedAndTheOwnerTold(t *testing.T) {
	f, sheet := withFollowed(t)
	f.news(sheet, gradeItem("k1", "HW 1", 9))
	f.api.FailChat[ivan] = &telegram.Error{Method: "sendRichMessage", Code: 403,
		Description: "Forbidden: bot was blocked by the user"}
	f.bot.SendGrades()
	if u, _ := f.user(ivan); !u.Paused || len(f.bot.Grades.Waiting(0)) != 1 {
		t.Fatal(u)
	}
	delete(f.api.FailChat, ivan)
	if text := f.text(owner); !strings.Contains(text, "бот заблокирован") && !strings.Contains(text, "blocked the bot") {
		t.Fatal(text)
	}
	found := false
	for _, data := range f.buttons(owner) {
		found = found || data == "du:1"
	}
	if !found || f.last(owner).Method != "sendRichMessage" {
		t.Fatal(f.buttons(owner))
	}
	f.bot.SendGrades()
	if len(f.Sheets.ToRead()) != 0 || len(f.api.Sent(ivan)) != 0 { // paused: neither read nor told
		t.Fatal("told")
	}
	f.say(ivan, "/grades") // back: unpaused, the grades go next time
	f.bot.SendGrades()
	if len(f.bot.Grades.Waiting(0)) != 0 {
		t.Fatal("waiting")
	}
}

// --- /grades ----------------------------------------------------------------------------------------------------

func TestGradesViewMarksTheNewOnesAndRefreshesInPlace(t *testing.T) {
	f, sheet := withFollowed(t)
	f.say(ivan, "/grades")
	contains(t, f.text(ivan), "<details><summary>Матанализ · оценок: 1</summary>", "<td>HW 1</td>", "3.5 из 5")
	want := map[string]string{"Сообщить об ошибке": "wr:" + itoa(sheet), "Обновить": "rf"}
	if got := f.buttons(ivan); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	f.news(sheet, gradeItem("k1", "HW 1", 7, raw(3.5, 5)), gradeItem("k2", "Quiz 1", 9, pos(1)))
	f.handle(callbackAt(ivan, "rf", 321))
	edited := f.last(ivan)
	if edited.Method != "editMessageText" || edited.ID != 321 {
		t.Fatal(edited)
	}
	contains(t, edited.Text, "<details open><summary>Матанализ · оценок: 2 · новых: 1</summary>",
		"<b><u>Quiz 1</u></b>", "<b><u>9</u></b>")
	f.press(ivan, "grades") // «All grades» under a notification: a new message
	if f.last(ivan).Method != "sendRichMessage" {
		t.Fatal(f.last(ivan))
	}
}

func TestGradesViewTellsWhatIsWrong(t *testing.T) {
	f := setup(t)
	f.AddUser(ivan, "", "", "")
	f.say(ivan, "/grades")
	contains(t, f.text(ivan), "Как ты записан в таблицах?")
	if f.bot.Asks[ivan] == nil {
		t.Fatal("not asked")
	}
	f.Access.SetStudent(ivan, "Иванов Иван")
	sheet, _, _ := f.Sheets.Follow(ivan, books.URL, "")
	f.say(ivan, "/grades")
	contains(t, f.text(ivan), "Ещё не прочитана")
	lacks(t, f.text(ivan), "Сообщить об ошибке")
	f.bot.Grades.Failed(ivan, sheet.ID, "no_row")
	f.say(ivan, "/grades")
	contains(t, f.text(ivan), "твоей строки нет ни на одной вкладке")
}

func TestALongGradesViewFoldsTheOldTables(t *testing.T) {
	f, sheet := withFollowed(t)
	f.news(sheet, gradeItem("k1", "HW 1", 7, raw(3.5, 5)), gradeItem("k2", "Quiz 1", 9, pos(1)))
	other, _, _ := f.Sheets.Follow(ivan, "https://docs.google.com/spreadsheets/d/2ZyXwVuTsRqPoNmLkJiHgFeDc", "")
	var labs []layout.Item
	for n := range 30 {
		labs = append(labs, gradeItem("o"+itoa(int64(n)), "Lab "+itoa(int64(n)), 8, pos(n)))
	}
	f.bot.Grades.Record(ivan, other.ID, labs, 1, "")
	defer lower(30)()
	f.say(ivan, "/grades")
	contains(t, f.text(ivan), "<b><u>Quiz 1</u></b>", "только там, где есть новые")
	lacks(t, f.text(ivan), "Lab 1")
}

type fakePoller struct {
	relaid    []int64
	requested [][2]int64
}

func (p *fakePoller) Request(sheet, tgID int64) {
	p.requested = append(p.requested, [2]int64{sheet, tgID})
}
func (p *fakePoller) Relayout(sheet sheets.Sheet) { p.relaid = append(p.relaid, sheet.ID) }
func (p *fakePoller) Rescheduled()                {}

func TestAComplaintReachesTheOwnerOnceADay(t *testing.T) {
	f, sheet := withFollowed(t)
	p := &fakePoller{}
	f.bot.Poller = p
	f.press(ivan, "wr:"+itoa(sheet))
	if !f.answered("Отправлено владельцу") {
		t.Fatal(f.lastNote())
	}
	complaint := f.last(owner)
	contains(t, complaint.Text, "says the layout of", "<td>T</td><td>HW 1 (k1)</td>")
	want := map[string]string{"Lay out again": "rl:" + itoa(sheet), "Open the sheet": strings.Split(books.URL, "/edit")[0]}
	if got := f.buttons(owner); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	f.press(ivan, "wr:"+itoa(sheet))
	if f.lastNote() != "Уже отправлено сегодня" || f.last(owner).Text != complaint.Text {
		t.Fatal(f.lastNote())
	}
	f.Clock.Advance(24*time.Hour + time.Minute)
	f.press(ivan, "wr:"+itoa(sheet))
	count := 0
	for _, m := range f.api.Sent(owner) {
		if strings.Contains(m.Text, "says the layout of") {
			count++
		}
	}
	if count != 2 {
		t.Fatal(count)
	}
	f.press(owner, "rl:"+itoa(sheet))
	if !reflect.DeepEqual(p.relaid, []int64{sheet}) {
		t.Fatal(p.relaid)
	}
	f.press(ivan, "wr:999") // not their sheet
	if f.lastNote() != "" {
		t.Fatal(f.lastNote())
	}
}

// --- /sheets ----------------------------------------------------------------------------------------------------

func TestSheetsViewAndCheck(t *testing.T) {
	f, sheet := withFollowed(t)
	p := &fakePoller{}
	f.bot.Poller = p
	f.say(ivan, "/sheets")
	contains(t, f.text(ivan), "<th>Прочитана, МСК</th>", "<td>09.11 15:00</td>", `<td align="right">1</td>`,
		`data="us:`+itoa(sheet)+`" style="danger"`)
	lacks(t, f.text(ivan), "Ошибка")
	want := map[string]string{"Проверить: Матанализ": "ck:" + itoa(sheet), "Отписаться: Матанализ": "us:" + itoa(sheet),
		"Добавить таблицу": "add"}
	if got := f.buttons(ivan); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	f.press(ivan, "ck:"+itoa(sheet))
	if !reflect.DeepEqual(p.requested, [][2]int64{{sheet, ivan}}) {
		t.Fatal(p.requested)
	}
	f.press(ivan, "ck:999")
	if len(p.requested) != 1 {
		t.Fatal(p.requested)
	}
}

// --- /status ----------------------------------------------------------------------------------------------------

func TestStatus(t *testing.T) {
	f, sheet := withFollowed(t)
	f.say(owner, "/status")
	contains(t, f.text(owner), "Таблицы · 1", "Не подключён")
	journal := llm.NewJournal(f.DB)
	f.bot.Budget, f.bot.Journal = llm.NewBudget(journal, f.DB, 1.0, f.bot.Zone, f.Clock.Now), journal
	journal.Add(llm.Run{At: "2026-11-09T10:00:00+00:00", Task: "grades", Model: "m", CostUSD: 0.012, DurationS: 1})
	f.Sheets.ReadFailed(sheet, "closed: not a workbook (BadZipFile)")
	f.say(owner, "/status", lang("en"))
	contains(t, f.text(owner), "<td>failing</td>", "<blockquote expandable>Матанализ: closed: not a workbook",
		`<td align="right">$0.012 / $1.00</td><td>calls 1, failed 1</td>`)
	if got := f.buttons(owner); !reflect.DeepEqual(got, map[string]string{"Refresh": "st"}) {
		t.Fatal(got)
	}
	f.handle(callbackAt(owner, "st", 42))
	if f.last(owner).Method != "editMessageText" {
		t.Fatal(f.last(owner))
	}
}

func TestByProblemSendsThePointsOfEachWorksProblems(t *testing.T) {
	f, sheet := withFollowed(t)
	f.news(sheet, gradeItem("k1", "HW 1", 7, raw(3.5, 5), parts([2]string{"1", "1"}, [2]string{"2a", "0.5"})),
		gradeItem("k2", "Quiz 1", 9, pos(1)))
	f.say(ivan, "/grades")
	if f.buttons(ivan)["По задачам"] != "pt:h"+itoa(sheet) {
		t.Fatal(f.buttons(ivan))
	}
	f.press(ivan, "pt:h"+itoa(sheet))
	text := f.text(ivan)
	contains(t, text, "Матанализ · по задачам", "<p><b>HW 1</b> · оценка <b>7</b> · 3.5 из 5</p>",
		`<table bordered striped compact><tr><th align="right">1</th><th align="right">2a</th></tr>`+
			`<tr><td align="right">1</td><td align="right">0.5</td></tr></table>`)
	lacks(t, text, "Quiz 1")
	count := len(f.api.Calls)
	f.press(ivan, "pt:h999")         // not the user's: nothing
	if len(f.api.Calls) != count+1 { // only the callback answer
		t.Fatal(f.api.Calls[count:])
	}
}

func TestByProblemSplitsManyProblemsIntoTablesOfEight(t *testing.T) {
	var p [][2]string
	for n := 1; n <= 10; n++ {
		p = append(p, [2]string{itoa(int64(n)), "1"})
	}
	work := problemsWork(&Row{Label: "HW 2", Value: 9, Raw: 9, Max: 10, Parts: p}, "en")
	if work.Len() != 3 {
		t.Fatal(work.Len())
	}
	contains(t, work.Rich(), "<th align=\"right\">8</th></tr>", "<th align=\"right\">9</th><th align=\"right\">10</th>")
	if !strings.HasPrefix(work.Plain(), "<b>HW 2</b> · grade <b>9</b>\n\n<i>1 · 2 · 3") {
		t.Fatal(work.Plain())
	}
}
