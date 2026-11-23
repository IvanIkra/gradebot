package workbook

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	Ivan = "Иванов Иван"
	Anna = "Петрова Анна"
)

func load(t *testing.T, name string) []*Tab {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name + ".xlsx")
	if err != nil {
		t.Fatal(err)
	}
	tabs, err := Load(data)
	if err != nil {
		t.Fatal(err)
	}
	return tabs
}

func typeName(v Value) string {
	switch v.(type) {
	case int64, BigInt:
		return "int"
	case float64:
		return "float"
	case string:
		return "str"
	case bool:
		return "bool"
	case time.Time:
		return "datetime"
	case TimeOfDay:
		return "time"
	case Duration:
		return "timedelta"
	}
	return "?"
}

// TestSameAsOpenpyxl compares every test workbook with what openpyxl and the Python text() made of it.
func TestSameAsOpenpyxl(t *testing.T) {
	raw, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden map[string][]struct {
		Title string
		Cells [][][3]string
		Top   [][2]json.RawMessage
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	for name, want := range golden {
		tabs := load(t, name)
		if len(tabs) != len(want) {
			t.Fatalf("%s: %d tabs, want %d", name, len(tabs), len(want))
		}
		for n, tab := range tabs {
			w := want[n]
			if tab.Title != w.Title {
				t.Errorf("%s: tab %q, want %q", name, tab.Title, w.Title)
			}
			var got [][][3]string
			for _, row := range tab.Cells {
				line := [][3]string{}
				for j, c := range row {
					if c != nil {
						line = append(line, [3]string{Column(j), typeName(c), Text(c)})
					}
				}
				got = append(got, line)
			}
			for len(got) < len(w.Cells) {
				got = append(got, [][3]string{})
			}
			wantCells := w.Cells
			for i := range wantCells {
				if wantCells[i] == nil {
					wantCells[i] = [][3]string{}
				}
			}
			if !reflect.DeepEqual(got, wantCells) {
				t.Errorf("%s/%s cells:\n%v\nwant\n%v", name, tab.Title, got, wantCells)
			}
			var top [][2]any
			for _, row := range tab.Top {
				top = append(top, [2]any{row.Number, row.Cells})
			}
			var wantTop [][2]any
			for _, pair := range w.Top {
				var number int
				var cells map[string]string
				_ = json.Unmarshal(pair[0], &number)
				_ = json.Unmarshal(pair[1], &cells)
				wantTop = append(wantTop, [2]any{number, cells})
			}
			if !reflect.DeepEqual(top, wantTop) {
				t.Errorf("%s/%s top:\n%v\nwant\n%v", name, tab.Title, top, wantTop)
			}
		}
	}
}

func TestLoadKeepsVisibleTabsAndTheirTop(t *testing.T) {
	tabs := load(t, "book")
	if len(tabs) != 2 || tabs[0].Title != "ДЗ-1А" || tabs[1].Title != "Квизы" { // the hidden tab is skipped
		t.Fatal(tabs)
	}
	hw := tabs[0]
	var numbers []int
	for _, r := range hw.Top {
		numbers = append(numbers, r.Number)
	}
	if !reflect.DeepEqual(numbers, []int{1, 2, 3, 4, 5}) || hw.Header([]int{2})[0]["F"] != "Балл" ||
		hw.ColumnHeader("F", []int{1, 2}) != "Балл" {
		t.Fatal(numbers)
	}
}

type found struct {
	tab    string
	number int
	col    string
}

func where(rows []*Row) []found {
	var out []found
	for _, r := range rows {
		out = append(out, found{r.Tab.Title, r.Number, r.NameCol})
	}
	return out
}

func TestFindRowsOfSeveralStudentsInOneParse(t *testing.T) {
	parsed := load(t, "book")
	ivan, _ := Find(parsed, Ivan)
	anna, _ := Find(parsed, Anna)
	if got := where(ivan); !reflect.DeepEqual(got, []found{{"ДЗ-1А", 5, "B"}, {"Квизы", 5, "A"}}) {
		t.Fatal(got)
	}
	if got := where(anna); !reflect.DeepEqual(got, []found{{"ДЗ-1А", 4, "B"}, {"Квизы", 6, "A"}}) {
		t.Fatal(got)
	}
	if _, has := ivan[0].Cells["B"]; ivan[0].Cells["F"] != 5.9 || has { // no name among the cells
		t.Fatal(ivan[0].Cells)
	}
	if rows, twice := Find(parsed, "Никто Никтоевич"); rows != nil || twice != nil {
		t.Fatal(rows, twice)
	}
}

func TestANameInTwoRowsOfATabLeavesThatTabOut(t *testing.T) {
	rows, twice := Find(load(t, "namesake"), Ivan)
	if !reflect.DeepEqual(twice, []string{"ДЗ-1А"}) || len(rows) != 1 || rows[0].Tab.Title != "Квизы" {
		t.Fatal(rows, twice)
	}
	rows, twice = Find(load(t, "namesake"), Anna) // others are fine
	if twice != nil || len(rows) != 2 {
		t.Fatal(rows, twice)
	}
}

func TestABlankCopyOfTheListIsNoMatch(t *testing.T) {
	rows, twice := Find(load(t, "blank_copy"), Ivan)
	if twice != nil || !reflect.DeepEqual(where(rows), []found{{"ДЗ-1А", 5, "B"}, {"Квизы", 5, "A"}}) {
		t.Fatal(where(rows), twice)
	}
}

func TestNameMatchingHandlesCaseAndYo(t *testing.T) {
	name := []string{"иванов", "иван"}
	if !Matches("ИВАНОВ  Иван Сергеевич", name) || Matches("Иванова Ирина", name) || Matches("Иванов", name) {
		t.Fatal("matches")
	}
	if Norm("Пётр,") != "петр" || Column(0) != "A" || Column(26) != "AA" || Index("AA") != 26 {
		t.Fatal("norm")
	}
}

func TestNotAWorkbook(t *testing.T) {
	var e *NotWorkbookError
	if _, err := Load([]byte("<html>Sign in</html>")); !errors.As(err, &e) {
		t.Fatal(err)
	}
}

func TestATabThatCannotBeParsed(t *testing.T) {
	data, _ := os.ReadFile("testdata/broken.xlsx")
	var e *BrokenWorkbookError
	if _, err := Load(data); !errors.As(err, &e) {
		t.Fatal(err)
	}
}

func TestDateFormats(t *testing.T) {
	for code, want := range map[string]bool{"dd.mm.yyyy": true, `0.0"d"`: false, "[h]:mm": true, "0.00": false,
		"[Red]0.00": false, `_(* #,##0_)`: false, "General": false, "mm:ss": true, `\d0`: false} {
		if isDateFormat(code) != want {
			t.Errorf("%s", code)
		}
	}
	if !strings.Contains(durationText(36*time.Hour), "1 day, 12:00:00") {
		t.Error(durationText(36 * time.Hour))
	}
}
