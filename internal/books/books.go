// Package books holds made-up grade sheets shaped like real ones, with made-up names, and a fake Haiku that lays them
// out: for tests only. The workbooks were written by openpyxl from the Python version's tests.
package books

import (
	"embed"
	"errors"
	"strings"
	"sync"

	"github.com/IvanIkra/gradebot/internal/llm"
)

// The names the users set: the quizzes tab has them short.
const (
	Ivan    = "Иванов Иван"
	Anna    = "Петрова Анна"
	SheetID = "1AbCdEfGhIjKlMnOpQrStUvWx"
	URL     = "https://docs.google.com/spreadsheets/d/" + SheetID + "/edit#gid=0"
)

//go:embed testdata/*.xlsx
var files embed.FS

// Book is a test workbook: "book" (the default), "namesake", "blank_copy", "inserted", "hw63_quiz9", "quiz9",
// "quiz6", "classmate4", "google", "broken".
func Book(name string) []byte {
	data, err := files.ReadFile("testdata/" + name + ".xlsx")
	if err != nil {
		panic(err)
	}
	return data
}

func f(v float64) *float64 { return &v }
func s(v string) *string   { return &v }

// Spec is the layout Haiku would give, by the tab and where its columns are in the header.
func Spec(prompt string) map[string]any {
	tab := strings.Split(prompt, `"`)[1]
	if tab == "ДЗ-1А" {
		return map[string]any{"skip": false, "reason": "", "header_rows": []any{1.0, 2.0}, "items": []any{
			map[string]any{"column": "F", "label": "HW 1A", "label_ru": "ДЗ 1А", "max": 7.0, "bonus_column": "G"}}}
	}
	quiz1, quiz2 := "E", "G"
	if strings.Contains(prompt, "C=Средний балл") {
		quiz1, quiz2 = "F", "H"
	}
	return map[string]any{"skip": false, "reason": "", "header_rows": []any{1.0, 2.0, 3.0, 99.0}, // 99: not shown
		"items": []any{
			map[string]any{"column": quiz1, "label": "Quiz 1", "label_ru": "Квиз 1", "max": 10.0, "bonus_column": nil},
			map[string]any{"column": quiz2, "label": "Quiz 2", "label_ru": "Квиз 2", "max": 10.0, "bonus_column": nil}}}
}

// FakeLLM lays out the test workbooks.
type FakeLLM struct {
	mu        sync.Mutex
	Prompts   []string
	MaxTokens []int
	Err       error
	Answer    func(task, prompt string) map[string]any // another answer than Spec
	Off       bool                                     // no key
}

func (l *FakeLLM) Available() bool { return !l.Off }

func (l *FakeLLM) JSON(task, system, prompt string, schema map[string]any, maxTokens int) (map[string]any, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if strings.Contains(system, "{budget}") {
		panic("the budget is not in the system prompt")
	}
	l.Prompts = append(l.Prompts, prompt)
	l.MaxTokens = append(l.MaxTokens, maxTokens)
	if l.Err != nil {
		return nil, l.Err
	}
	if l.Answer != nil {
		return l.Answer(task, prompt), nil
	}
	if task != "grades" {
		return nil, errors.New("FakeLLM: no answer for " + task)
	}
	return Spec(prompt), nil
}

var _ llm.Asker = (*FakeLLM)(nil)
var _, _ = f, s
