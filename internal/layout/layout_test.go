package layout

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/IvanIkra/gradebot/internal/books"
	"github.com/IvanIkra/gradebot/internal/llm"
	"github.com/IvanIkra/gradebot/internal/store"
	"github.com/IvanIkra/gradebot/internal/workbook"
)

func load(name string) []*workbook.Tab {
	tabs, err := workbook.Load(books.Book(name))
	if err != nil {
		panic(err)
	}
	return tabs
}

func quizzes(student, book string) (*workbook.Tab, []*workbook.Row) {
	parsed := load(book)
	var tab *workbook.Tab
	for _, t := range parsed {
		if t.Title == "Квизы" {
			tab = t
		}
	}
	found, _ := workbook.Find(parsed, student)
	var rows []*workbook.Row
	for _, r := range found {
		if r.Tab == tab {
			rows = append(rows, r)
		}
	}
	return tab, rows
}

func setup(t *testing.T) (*Layouts, *books.FakeLLM, *store.Store) {
	db := store.MustOpen(":memory:")
	t.Cleanup(func() { db.Close() })
	fake := &books.FakeLLM{}
	return New(db, fake), fake, db
}

func get(t *testing.T, l *Layouts, sheet, book string, budget int) Spec {
	t.Helper()
	tab, rows := quizzes(books.Ivan, book)
	spec, err := l.Get(sheet, tab, rows, budget, nil)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestOneLayoutForEveryFollower(t *testing.T) {
	l, fake, _ := setup(t)
	tab, ivan := quizzes(books.Ivan, "book")
	_, anna := quizzes(books.Anna, "book")
	spec, _ := l.Get(books.SheetID, tab, append(ivan, anna...), 18, nil)
	if !reflect.DeepEqual(spec.HeaderRows, []int{1, 2, 3}) { // 99 was not shown
		t.Fatal(spec.HeaderRows)
	}
	again, _ := l.Get(books.SheetID, tab, anna, 18, nil)
	if !reflect.DeepEqual(again, spec) || len(fake.Prompts) != 1 {
		t.Fatal(again, len(fake.Prompts))
	}
}

func TestTheLayoutStandsWhileTheHeaderRowsStay(t *testing.T) {
	l, fake, _ := setup(t)
	get(t, l, books.SheetID, "book", 18)
	get(t, l, books.SheetID, "quiz6", 18)      // the own grade
	get(t, l, books.SheetID, "classmate4", 18) // another student's, in the top rows
	if len(fake.Prompts) != 1 {
		t.Fatal(len(fake.Prompts))
	}
	get(t, l, books.SheetID, "book", 12)               // another budget: another label
	get(t, l, "2ZyXwVuTsRqPoNmLkJiHgFeDc", "book", 18) // another sheet
	if len(fake.Prompts) != 3 {
		t.Fatal(len(fake.Prompts))
	}
	get(t, l, books.SheetID, "inserted", 18) // the header changed
	if len(fake.Prompts) != 4 {
		t.Fatal(len(fake.Prompts))
	}
}

func TestOtherTabsAreInTheKey(t *testing.T) {
	l, fake, _ := setup(t)
	tab, rows := quizzes(books.Ivan, "book")
	l.Get(books.SheetID, tab, rows, 18, []string{"ДЗ-1А"})
	l.Get(books.SheetID, tab, rows, 18, []string{"ДЗ-1А"})
	l.Get(books.SheetID, tab, rows, 18, []string{"ДЗ-1А", "Коллоквиум"})
	if len(fake.Prompts) != 2 || !strings.Contains(fake.Prompts[0], "Other tabs of the sheet: «ДЗ-1А»") {
		t.Fatal(fake.Prompts)
	}
}

func TestAnAnswerTheModelCannotGiveSkipsTheTabUntilItChanges(t *testing.T) {
	l, fake, _ := setup(t)
	fake.Err = &llm.Error{Msg: "answer was cut at max_tokens", Bad: true}
	if spec := get(t, l, books.SheetID, "book", 18); !spec.Skip || !spec.Failed {
		t.Fatal(spec)
	}
	fake.Err = nil
	if spec := get(t, l, books.SheetID, "book", 18); !spec.Skip || len(fake.Prompts) != 1 {
		t.Fatal(spec)
	}
	if spec := get(t, l, books.SheetID, "classmate4", 18); spec.Skip { // no header known: any change asks
		t.Fatal(spec)
	}
}

func TestOtherFailuresAreTheCallers(t *testing.T) {
	l, fake, db := setup(t)
	fake.Err = llm.BudgetExceeded{}
	tab, rows := quizzes(books.Ivan, "book")
	if _, err := l.Get(books.SheetID, tab, rows, 18, nil); !llm.IsBudget(err) {
		t.Fatal(err)
	}
	if store.Has(db, "select 1 from kv") {
		t.Fatal("cached")
	}
}

func TestForgetDropsTheSheetsLayoutsOnly(t *testing.T) {
	l, fake, _ := setup(t)
	get(t, l, books.SheetID, "book", 18)
	get(t, l, books.SheetID+"_x", "book", 18)
	if l.Forget(books.SheetID) != 1 {
		t.Fatal("forget")
	}
	get(t, l, books.SheetID, "book", 18)
	get(t, l, books.SheetID+"_x", "book", 18)
	if len(fake.Prompts) != 3 {
		t.Fatal(len(fake.Prompts))
	}
}

func TestAWideTabGetsRoomForItsAnswer(t *testing.T) {
	l, fake, _ := setup(t)
	get(t, l, books.SheetID, "book", 18)
	row := map[string]string{}
	for i := 1; i <= 200; i++ {
		row[workbook.Column(i)] = fmt.Sprintf("Sep %d", i)
	}
	wide := &workbook.Tab{Title: "Даты", Top: []workbook.TopRow{{Number: 1, Cells: row}}}
	l.Get(books.SheetID, wide, []*workbook.Row{{Tab: wide, Number: 2, NameCol: "A",
		Cells: map[string]workbook.Value{"B": int64(5)}}}, 18, nil)
	if !reflect.DeepEqual(fake.MaxTokens, []int{MaxTokens, 200 * TokensPerWork}) {
		t.Fatal(fake.MaxTokens)
	}
}

func TestKeyIsThePythonOne(t *testing.T) {
	// sha256 of json.dumps(["ДЗ-1А", 3], ensure_ascii=False) as Python computed it
	if got := Key(books.SheetID, "ДЗ-1А", 18); got != "layout:"+books.SheetID+":"+pythonDigest+":18" {
		t.Fatal(got)
	}
}

func TestPromptSpellsOutCyrillicLetterOrder(t *testing.T) {
	if !strings.Contains(System, "в→C") || !strings.Contains(System, `"HW 1C"`) || !strings.Contains(System, `"ДЗ 1В"`) {
		t.Fatal("system")
	}
}

func TestPromptShowsTheRowsAboveAndTheRowWithoutTheName(t *testing.T) {
	found, _ := workbook.Find(load("book"), books.Ivan)
	prompt := Prompt(found[0], nil)
	if !strings.Contains(prompt, "row 3: A=101 | B=Образцов Нагляд Примерович") ||
		!strings.HasSuffix(prompt, "student row (row 5): A=101 | D=1 | E=0.7 | F=5.9 | G=1") ||
		strings.Contains(prompt, "ИВАНОВ") || strings.Contains(prompt, "row 5:") {
		t.Fatal(prompt)
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestExtractNormalisesToTenAndKeepsTheBonus(t *testing.T) {
	found, _ := workbook.Find(load("book"), books.Ivan)
	items := Extract(found[0], SpecOf(books.Spec(`Tab: "ДЗ-1А"`)), 0)
	if len(items) != 1 {
		t.Fatal(items)
	}
	it := items[0]
	if it.Label != "HW 1A" || !near(it.Value, 8.43) || it.Bonus == nil || *it.Bonus != 1 || it.Raw != 5.9 ||
		it.Max != 7 || it.Key != "ДЗ-1А|F|Балл" {
		t.Fatal(it)
	}
	if Budget("Calc") != 18 || Budget("Very long") != 13 || Budget(strings.Repeat("x", 20)) != 8 {
		t.Fatal("budget")
	}
}

func TestAWorkListedTwiceCountsOnce(t *testing.T) {
	found, _ := workbook.Find(load("book"), books.Ivan)
	seven := 7.0
	twice := Spec{Items: []Work{{Column: "F", Label: "HW 1A", Max: &seven}, {Column: "F", Label: "HW 1A again",
		Max: &seven}}}
	if items := Extract(found[0], twice, 0); len(items) != 1 || items[0].Label != "HW 1A" {
		t.Fatal(items)
	}
}

func TestTheSampleRowHasNumbers(t *testing.T) {
	parsed := load("book")
	empty := &workbook.Row{Tab: parsed[0], Number: 9, NameCol: "B", Cells: map[string]workbook.Value{"A": "—"}}
	found, _ := workbook.Find(parsed, books.Ivan)
	if Sample([]*workbook.Row{empty, found[0]}) != found[0] || Sample([]*workbook.Row{empty}) != empty {
		t.Fatal("sample")
	}
}

func TestLabelsComeInBothLanguages(t *testing.T) {
	_, rows := quizzes(books.Ivan, "book")
	ten := 10.0
	spec := Spec{HeaderRows: []int{1, 2, 3}, Items: []Work{
		{Column: "E", Label: "Quiz 1", LabelRu: " Квиз  1 ", Max: &ten},
		{Column: "G", Label: "Quiz 2", Max: &ten}}} // a layout from before label_ru
	items := Extract(rows[0], spec, 0)
	if items[0].Label != "Quiz 1" || items[0].LabelRu != "Квиз 1" || items[1].LabelRu != "" {
		t.Fatal(items)
	}
	if Label("Quiz 1", "Квиз 1", "ru") != "Квиз 1" || Label("Quiz 1", "Квиз 1", "en") != "Quiz 1" ||
		Label("Quiz 2", "", "ru") != "Quiz 2" {
		t.Fatal("label")
	}
}

func TestProblemNamesFromHeaders(t *testing.T) {
	for header, want := range map[string]string{"ДЗ 1 / №3 a)": "3a", "7 / 1а": "1а", "№ 5 b) / 10": "5b",
		"3 / 2": "2", "4*": "4*", "2 вопрос": "2 вопрос", "Защита: штраф": "Защита: штраф"} {
		if got := Problem(header); got != want {
			t.Errorf("%q: %q", header, got)
		}
	}
	for _, other := range []string{"Балл", "Сумма", "Оценка", "ДЗ", "Доп. лист 1", "КР1-О", "Номер доп. задачи",
		"Коллок"} {
		if got := Problem(other); got != "" {
			t.Errorf("%q: %q", other, got)
		}
	}
}

func cellsTab(title string, cells [][]workbook.Value) *workbook.Tab {
	tab := workbook.NewTab(title, cells)
	return tab
}

// gradebook: max points in row 1, problem numbers in row 2, then a blank copy of the list below the real one.
func gradebook() *workbook.Tab {
	i := func(v int64) workbook.Value { return v }
	return cellsTab("ДЗ-1", [][]workbook.Value{
		{"", "Max", i(2), i(1), nil, nil, nil, nil, nil},
		{"Группа", "Студент", "1", "2*", "Защита: штраф", "Балл", "Балл*", "Коллок", "Квиз"},
		{"261", "Иванов Иван", i(1), 0.5, nil, 7.5, i(1), i(9), i(0)},
		{"261", "Петрова Анна", i(2), i(0), i(-1), i(6), nil, i(8), i(0)},
		{"261", "Сидоров Пётр", i(0), i(0), nil, i(0), nil, i(7), i(5)},
		{"", "Смирнова Ольга", nil, nil, nil, nil, nil, nil, nil}})
}

func TestExtractGivesProblemsAndTheGroupsShare(t *testing.T) {
	tab := gradebook()
	ten, g := 10.0, "G"
	spec := Spec{HeaderRows: []int{1, 2}, Items: []Work{
		{Column: "F", Label: "HW 1", LabelRu: "ДЗ 1", Max: &ten, BonusColumn: &g},
		{Column: "I", Label: "Quiz", LabelRu: "Квиз", Max: &ten}}}
	rows, _ := workbook.Find([]*workbook.Tab{tab}, "Петрова Анна")
	items := Extract(rows[0], spec, 0)
	hw, quiz := items[0], items[1]
	if !reflect.DeepEqual(hw.Parts, [][2]string{{"1", "2"}, {"2*", "0"}, {"Защита: штраф", "-1"}}) ||
		!near(*hw.Graded, 0.5) {
		t.Fatal(hw.Parts, *hw.Graded)
	}
	if len(quiz.Parts) != 0 || !near(*quiz.Graded, 0.5) { // «Коллок» between them is no problem
		t.Fatal(quiz)
	}
	rows, _ = workbook.Find([]*workbook.Tab{tab}, "Иванов Иван")
	if g := Extract(rows[0], spec, 0)[1].Graded; !near(*g, 0.5) { // the blank copy is no student
		t.Fatal(*g)
	}
}

var stream = [][]workbook.Value{
	{nil, nil, int64(10)},
	{"Группа", "Студент", "Балл"},
	{int64(101), "Петров Пётр", int64(0)},
	{nil, "Сидорова Анна", int64(0)},
	{nil, "Орлов Олег", int64(0)},
	{nil, "Белова Белла", int64(0)},
	{int64(102), "Козлова Катя", int64(7)},
	{nil, "Иванов Иван", int64(0)},
	{nil, "Зайцев Захар", int64(9)},
	{nil, "Лисина Лиза", int64(0)},
}

func streamRow(cells [][]workbook.Value, number int) *workbook.Row {
	tab := &workbook.Tab{Title: "T", Cells: cells}
	return &workbook.Row{Tab: tab, Number: number, NameCol: "B", Cells: map[string]workbook.Value{"C": cells[number-1][2]}}
}

func TestGradedSharesCountTheOwnGroupOfAStream(t *testing.T) {
	if got := GradedShares(streamRow(stream, 8), []int{2}); !near(got["C"], 2.0/3) {
		t.Fatal(got)
	}
	if got := GradedShares(streamRow(stream, 4), []int{2}); got["C"] != 0 || len(got) != 1 {
		t.Fatal(got)
	}
}

func TestGradedSharesWithoutAGroupColumnCountTheWholeTab(t *testing.T) {
	var cells [][]workbook.Value
	for _, row := range stream {
		cells = append(cells, append([]workbook.Value{nil}, row[1:]...))
	}
	cells[1][0] = "Действительная группа" // not the group column
	if got := GradedShares(streamRow(cells, 8), []int{2}); !near(got["C"], 2.0/7) {
		t.Fatal(got)
	}
}

const pythonDigest = "51843716acbcbc6a95a9b543"
