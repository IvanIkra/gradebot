package rich

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTextIsEscapedMarkupIsNot(t *testing.T) {
	doc := New().H("<Матан & Co>").P("a < b ", Bold("<x>"), " ", Link("t.me", `https://t.me/?a="1"&b`))
	r := doc.Rich()
	if !strings.Contains(r, "<h3>&lt;Матан &amp; Co&gt;</h3>") {
		t.Error(r)
	}
	if !strings.Contains(r, `<p>a &lt; b <b>&lt;x&gt;</b> <a href="https://t.me/?a=&quot;1&quot;&amp;b">t.me</a></p>`) {
		t.Error(r)
	}
	if j := Join("<", Code("1")); j != "&lt;<code>1</code>" {
		t.Error(j)
	}
}

func TestRichAndPlainRenderings(t *testing.T) {
	doc := New().H("Оценки").Table([]string{"Задание", "Оценка"}, [][]string{{"ДЗ 3", "8"}, {"КР <1>", ""}}, nil).
		Quote("ошибка", true)
	want := "<h3>Оценки</h3><table bordered striped compact><tr><th>Задание</th><th>Оценка</th></tr>" +
		"<tr><td>ДЗ 3</td><td>8</td></tr><tr><td>КР &lt;1&gt;</td><td></td></tr></table>" +
		"<blockquote expandable>ошибка</blockquote>"
	if got := doc.Rich(); got != want {
		t.Errorf("rich:\n%s\n%s", got, want)
	}
	want = "<b>Оценки</b>\n\n<i>Задание · Оценка</i>\nДЗ 3 · 8\nКР &lt;1&gt;\n\n<blockquote expandable>ошибка</blockquote>"
	if got := doc.Plain(); got != want {
		t.Errorf("plain:\n%s\n%s", got, want)
	}
}

func TestKeyboard(t *testing.T) {
	if Keyboard(nil) != nil {
		t.Error("not nil")
	}
	got, _ := json.Marshal(Keyboard([][]Button{{B("Да", "dy:1"), B("Нет", "users")}, {}}))
	want := `{"inline_keyboard":[[{"callback_data":"dy:1","text":"Да"},{"callback_data":"users","text":"Нет"}]]}`
	if string(got) != want {
		t.Error(string(got))
	}
	defer func() {
		if recover() == nil {
			t.Error("no panic")
		}
	}()
	Keyboard([][]Button{{B("x", strings.Repeat("я", 33))}})
}
