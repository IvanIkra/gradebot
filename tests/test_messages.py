"""Stage 4: grades as rich messages, /grades, /sheets, /status and the buttons inside messages."""

import datetime as dt

import pytest

from gradebot import cards, rich
from gradebot.layout import Item
from gradebot.llm.budget import Budget
from gradebot.llm.journal import Journal
from gradebot.rich import Doc
from gradebot.telegram import TelegramError

from .books import URL
from .conftest import OWNER, FakeApi, add_user, buttons, callback, message

IVAN = 1


def item(key, label, value, raw=None, mx=10.0, bonus=None, position=0):
    return Item(key, "T", key, label, value, raw if raw is not None else value, mx, bonus, position)


@pytest.fixture
def followed(bot, access, sheets):
    """Ivan follows a sheet titled «Матанализ», read once (the baseline): its id."""
    add_user(access, IVAN, "Иванов Иван")
    sheet, _ = sheets.follow(IVAN, URL)
    sheets.set_title(sheet["id"], "Матанализ")
    bot.grades.record(IVAN, sheet["id"], [item("k1", "HW 1", 7, raw=3.5, mx=5)], 1)
    return sheet["id"]


def news(bot, sheet, clock, *items):
    clock.advance(minutes=30)
    bot.grades.record(IVAN, sheet, list(items), 1)


# --- rich ------------------------------------------------------------------------------------------

def test_details_buttons_and_alignment():
    inner = Doc().table(["Задание", "Оценка"], [[rich.mark("ДЗ 1"), rich.b("8")]], [None, "right"])
    doc = Doc().details("Матан · 1", inner.buttons(("Неверно", "wr:1", "link")), open_=True).buttons(
        ("Обновить", "rf"), ("Открыть", "https://example.com/?a=1&b=2"))
    assert doc.rich() == (
        '<details open><summary>Матан · 1</summary><table bordered striped compact><tr><th>Задание</th>'
        '<th align="right">Оценка</th></tr><tr><td><mark>ДЗ 1</mark></td><td align="right"><b>8</b></td></tr>'
        '</table><tg-button-row><tg-button type="callback_data" data="wr:1" style="link">Неверно</tg-button>'
        '</tg-button-row></details><tg-button-row><tg-button type="callback_data" data="rf">Обновить</tg-button>'
        '<tg-button type="url" url="https://example.com/?a=1&amp;b=2">Открыть</tg-button></tg-button-row>')
    assert doc.count() == 6  # details, table, two rows, a row of buttons inside, one outside
    assert "<u>ДЗ 1</u>" in doc.plain() and "tg-button" not in doc.plain()
    assert [len(row) for row in doc.button_rows()] == [1, 2]
    with pytest.raises(ValueError):
        Doc().buttons(*[("x", "y")] * 9)
    with pytest.raises(ValueError):
        Doc().buttons(("x", "y", "blue"))


def test_limits(monkeypatch):
    doc = Doc().table([], [["x"]] * 10)
    assert doc.fits()
    monkeypatch.setattr(rich, "MAX_BLOCKS", 10)
    assert not doc.fits()


async def test_without_rich_messages_the_buttons_go_to_the_keyboard():
    api = FakeApi(rich=False)
    await api.send(5, Doc().p("hi").buttons(("Да", "yes", "danger"), ("Сайт", "https://example.com")))
    method, params = api.calls[-1]
    assert method == "sendMessage" and params["reply_markup"] == {"inline_keyboard": [
        [{"text": "Да", "callback_data": "yes"}, {"text": "Сайт", "url": "https://example.com"}]]}
    await api.edit(5, 9, Doc().p("hi").buttons(("Да", "yes")), {"inline_keyboard": [[{"text": "A",
                                                                                       "callback_data": "a"}]]})
    assert [len(row) for row in api.calls[-1][1]["reply_markup"]["inline_keyboard"]] == [1, 1]


# --- cards -----------------------------------------------------------------------------------------

def test_hashtag_and_numbers():
    assert cards.hashtag("Calc 2") == "#Calc_2" and cards.hashtag("2024") == "" and cards.hashtag("") == ""
    assert cards.score(8.43) == "8.4" and cards.score(9.0) == "9" and cards.score(9.96) == "10"
    assert cards.points(5.9) == "5.9" and cards.points(7.0) == "7"


def event(id_, key, value, previous, bonus=None):
    return {"id": id_, "key": key, "label": key, "value": value, "raw": value, "max": 10, "bonus": bonus,
            "previous": previous}


def test_collapse_keeps_the_latest_with_the_first_previous():
    rows, dropped = cards.collapse([event(1, "a", 6, 5), event(2, "b", 7, None), event(3, "a", 8, 6),
                                    event(4, "c", 4, 5), event(5, "c", 5, 4)])
    assert [(r["key"], r["value"], r["previous"], r["ids"]) for r in rows] == [("a", 8, 5, [1, 3]),
                                                                                ("b", 7, None, [2])]
    assert dropped == [4, 5]  # c went back to 5: no news


def test_news_is_split_when_it_does_not_fit(monkeypatch):
    sheet = {"id": 1, "sheet_id": "x" * 25, "url": URL, "title": "Матанализ", "course": "Calc"}
    rows = [{**event(n, f"k{n}", 8, None), "ids": [n]} for n in range(12)]
    [(doc, ids)] = cards.news_messages([(sheet, rows), (sheet, rows[:1])], "ru")
    assert ids == [*range(12), 0] and doc.rich().count("<h3>") == 2
    monkeypatch.setattr(rich, "MAX_BLOCKS", 12)
    messages = cards.news_messages([(sheet, rows)], "ru")
    assert len(messages) > 1 and all(doc.fits() for doc, _ in messages)
    assert sorted(i for _, ids in messages for i in ids) == list(range(12))
    assert all(buttons({"text": doc.rich(), "keyboard": None}) == {"Все оценки": "grades"} for doc, _ in messages)


# --- sending ---------------------------------------------------------------------------------------

async def test_new_grades_come_in_one_message_and_quietly_at_night(bot, api, followed, clock):
    news(bot, followed, clock, item("k1", "HW 1", 9, raw=4.5, mx=5), item("k2", "Quiz 1", 6, position=1))
    clock.moment = dt.datetime(2026, 11, 9, 21, 0, tzinfo=dt.UTC)  # midnight in Moscow
    await bot.send_grades()
    [msg] = api.sent(IVAN)
    assert "HW 1" in msg["text"] and "Quiz 1" in msg["text"] and "4.5 из 5" in msg["text"]
    assert api.calls[-1][1]["disable_notification"] is True
    assert bot.grades.waiting() == []


async def test_a_failure_keeps_the_grades_for_the_next_poll(bot, api, followed, clock, access):
    news(bot, followed, clock, item("k1", "HW 1", 9))
    api.fail_chat[IVAN] = TelegramError("sendRichMessage", 500, "Internal Server Error")
    await bot.send_grades()
    assert len(bot.grades.waiting()) == 1 and not access.user(IVAN)["paused"]
    del api.fail_chat[IVAN]
    await bot.send_grades()
    assert bot.grades.waiting() == [] and len(api.sent(IVAN)) == 1


async def test_a_user_who_blocked_the_bot_is_paused_and_the_owner_told(bot, api, followed, clock, access, sheets):
    news(bot, followed, clock, item("k1", "HW 1", 9))
    api.fail_chat[IVAN] = TelegramError("sendRichMessage", 403, "Forbidden: bot was blocked by the user")
    await bot.send_grades()
    assert access.user(IVAN)["paused"] and len(bot.grades.waiting()) == 1
    del api.fail_chat[IVAN]
    owner = api.last(OWNER)
    assert "бот заблокирован" in owner["text"] or "blocked the bot" in owner["text"]
    assert f"du:{IVAN}" in buttons(owner).values() and owner["method"] == "sendRichMessage"
    await bot.send_grades()
    assert sheets.to_read() == [] and len(api.sent(IVAN)) == 0  # paused: neither read nor told
    await bot.handle(message(IVAN, "/grades"))                  # back: unpaused, the grades go next time
    await bot.send_grades()
    assert bot.grades.waiting() == []


# --- /grades -----------------------------------------------------------------------------------

async def test_grades_view_marks_the_new_ones_and_refreshes_in_place(bot, api, followed, clock):
    await bot.handle(message(IVAN, "/grades"))
    view = api.last(IVAN)
    assert "<details><summary>Матанализ · оценок: 1</summary>" in view["text"]  # nothing new: closed
    assert "<td>HW 1</td>" in view["text"] and "3.5 из 5" in view["text"]
    assert buttons(view) == {"Неверно": f"wr:{followed}", "Обновить": "rf"}

    news(bot, followed, clock, item("k1", "HW 1", 7, raw=3.5, mx=5), item("k2", "Quiz 1", 9, position=1))
    await bot.handle(callback(IVAN, "rf", message_id=321))
    edited = api.last(IVAN)
    assert edited["method"] == "editMessageText" and api.calls[-2][1]["message_id"] == 321
    assert "<details open><summary>Матанализ · оценок: 2 · новых: 1</summary>" in edited["text"]
    assert "<mark>Quiz 1</mark>" in edited["text"] and "<mark><b>9</b></mark>" in edited["text"]

    await bot.handle(callback(IVAN, "grades"))  # «All grades» under a notification: a new message
    assert api.last(IVAN)["method"] == "sendRichMessage"


async def test_grades_view_tells_what_is_wrong(bot, api, access, sheets):
    add_user(access, IVAN, None)
    await bot.handle(message(IVAN, "/grades"))
    assert "Сначала ФИО" in api.last(IVAN)["text"]
    access.set_student(IVAN, "Иванов Иван")
    sheet, _ = sheets.follow(IVAN, URL)
    await bot.handle(message(IVAN, "/grades"))
    assert "Ещё не прочитана" in api.last(IVAN)["text"] and "Неверно" not in api.last(IVAN)["text"]
    bot.grades.failed(IVAN, sheet["id"], "no_row")
    await bot.handle(message(IVAN, "/grades"))
    assert "твоей строки нет ни на одной вкладке" in api.last(IVAN)["text"]


async def test_a_long_grades_view_folds_the_old_tables(bot, api, followed, clock, monkeypatch):
    news(bot, followed, clock, item("k1", "HW 1", 7, raw=3.5, mx=5), item("k2", "Quiz 1", 9, position=1))
    other, _ = bot.sheets.follow(IVAN, "https://docs.google.com/spreadsheets/d/2ZyXwVuTsRqPoNmLkJiHgFeDc")
    bot.grades.record(IVAN, other["id"], [item(f"o{n}", f"Lab {n}", 8, position=n) for n in range(30)], 1)
    monkeypatch.setattr(rich, "MAX_BLOCKS", 30)
    await bot.handle(message(IVAN, "/grades"))
    text = api.last(IVAN)["text"]
    assert "<mark>Quiz 1</mark>" in text and "Lab 1" not in text and "только там, где есть новые" in text


async def test_a_complaint_reaches_the_owner_once_a_day(bot, api, followed, clock):
    calls = []
    bot.poller = type("P", (), {"relayout": lambda self, sheet: calls.append(sheet["id"]),
                                "request": lambda self, sheet, tg_id=None: None})()
    await bot.handle(callback(IVAN, f"wr:{followed}"))
    assert ("answerCallbackQuery", {"callback_query_id": "cb1", "text": "Отправлено владельцу"}) in api.calls
    owner = api.last(OWNER)
    assert "says the layout of" in owner["text"] and "<td>T</td><td>HW 1 (k1)</td>" in owner["text"]
    assert buttons(owner) == {"Lay out again": f"rl:{followed}", "Open the sheet": URL.split("/edit")[0]}
    await bot.handle(callback(IVAN, f"wr:{followed}"))
    assert api.calls[-1][1]["text"] == "Уже отправлено сегодня" and api.last(OWNER) == owner
    clock.advance(days=1, minutes=1)
    await bot.handle(callback(IVAN, f"wr:{followed}"))
    assert sum("says the layout of" in m["text"] for m in api.sent(OWNER)) == 2

    await bot.handle(callback(OWNER, f"rl:{followed}"))
    assert calls == [followed]
    await bot.handle(callback(IVAN, "wr:999"))  # not their sheet
    assert api.calls[-1][1].get("text") is None


# --- /sheets ---------------------------------------------------------------------------------------

async def test_sheets_view_and_check(bot, api, followed):
    requested = []
    bot.poller = type("P", (), {"request": lambda self, sheet, tg_id=None: requested.append((sheet, tg_id))})()
    await bot.handle(message(IVAN, "/sheets"))
    view = api.last(IVAN)
    assert "<th>Прочитана</th>" in view["text"] and "<td>09.11 15:00</td>" in view["text"]
    assert '<td align="right">1</td>' in view["text"] and "Ошибка" not in view["text"]
    assert buttons(view) == {"Проверить: Матанализ": f"ck:{followed}", "Отписаться: Матанализ": f"us:{followed}"}
    assert f'data="us:{followed}" style="danger"' in view["text"]
    await bot.handle(callback(IVAN, f"ck:{followed}"))
    assert requested == [(followed, IVAN)]
    await bot.handle(callback(IVAN, "ck:999"))
    assert requested == [(followed, IVAN)]


# --- /status ---------------------------------------------------------------------------------------

async def test_status(bot, api, followed, store, clock, sheets):
    await bot.handle(message(OWNER, "/status"))
    text = api.last(OWNER)["text"]
    assert "Таблицы · 1" in text or "Sheets · 1" in text
    assert "Not connected" in text or "Не подключён" in text

    journal = Journal(store)
    bot.budget = Budget(journal, store, 1.0, bot.zone, now=clock)
    journal.add({"at": "2026-11-09T10:00:00+00:00", "task": "grades", "model": "m", "ok": False,
                 "input_tokens": 0, "output_tokens": 0, "cache_write": 0, "cache_read": 0, "cost_usd": 0.012,
                 "duration_s": 1})
    sheets.read_failed(followed, "closed: not a workbook (BadZipFile)")
    await bot.handle(message(OWNER, "/status", lang="en"))
    text = api.last(OWNER)["text"]
    assert "<td>failing</td>" in text and "<blockquote expandable>Матанализ: closed: not a workbook" in text
    assert '<td align="right">$0.012 / $1.00</td><td>calls 1, failed 1</td>' in text
    assert buttons(api.last(OWNER)) == {"Refresh": "st"}
    await bot.handle(callback(OWNER, "st", message_id=42))
    assert api.last(OWNER)["method"] == "editMessageText"
