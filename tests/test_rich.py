import pytest

from gradebot.rich import Doc, b, code, join, keyboard, link


def test_text_is_escaped_markup_is_not():
    doc = Doc().h("<Матан & Co>").p("a < b ", b("<x>"), " ", link("t.me", 'https://t.me/?a="1"&b'))
    rich = doc.rich()
    assert "<h3>&lt;Матан &amp; Co&gt;</h3>" in rich
    assert "<p>a &lt; b <b>&lt;x&gt;</b> <a href=\"https://t.me/?a=&quot;1&quot;&amp;b\">t.me</a></p>" in rich
    assert join("<", code("1")) == "&lt;<code>1</code>"


def test_rich_and_plain_renderings():
    doc = (Doc().h("Оценки").table(["Задание", "Оценка"], [["ДЗ 3", "8"], ["КР <1>", ""]])
           .quote("ошибка", expandable=True))
    assert doc.rich() == (
        "<h3>Оценки</h3><table bordered striped compact><tr><th>Задание</th><th>Оценка</th></tr>"
        "<tr><td>ДЗ 3</td><td>8</td></tr><tr><td>КР &lt;1&gt;</td><td></td></tr></table>"
        "<blockquote expandable>ошибка</blockquote>")
    assert doc.plain() == ("<b>Оценки</b>\n\n<i>Задание · Оценка</i>\nДЗ 3 · 8\nКР &lt;1&gt;\n\n"
                           "<blockquote expandable>ошибка</blockquote>")


def test_keyboard():
    assert keyboard([]) is None
    assert keyboard([[("Да", "dy:1"), ("Нет", "users")], []]) == {
        "inline_keyboard": [[{"text": "Да", "callback_data": "dy:1"}, {"text": "Нет", "callback_data": "users"}]]}
    with pytest.raises(ValueError):
        keyboard([[("x", "я" * 33)]])
