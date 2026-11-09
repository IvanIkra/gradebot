"""Reading the sheets end to end: a local server stands in for Google, FakeApi for Telegram, FakeLLM for Haiku."""

import pytest
from aiohttp import web

from gradebot import poller as poller_module
from gradebot.llm.budget import BudgetExceeded
from gradebot.poller import Poller

from .books import ANNA, IVAN, URL
from .books import workbook as book
from .conftest import OWNER, add_user, buttons, callback, message


@pytest.fixture
async def google(aiohttp_server, monkeypatch):
    """`state["data"]` is the workbook, or an HTTP status to answer with."""
    state = {"data": book(), "titles": 0, "exports": 0}

    async def export(request):
        state["exports"] += 1
        if isinstance(state["data"], int):
            return web.Response(status=state["data"])
        return web.Response(body=state["data"])

    async def title(request):
        state["titles"] += 1
        return web.Response(text="<html><title>Матанализ &quot;1&quot; - Google Диск</title></html>",
                            content_type="text/html")

    app = web.Application()
    app.router.add_get("/d/{id}/export", export)
    app.router.add_get("/d/{id}/htmlview", title)
    server = await aiohttp_server(app)
    base = f"http://{server.host}:{server.port}"
    monkeypatch.setattr(poller_module, "EXPORT_URL", base + "/d/{id}/export")
    monkeypatch.setattr(poller_module, "TITLE_URL", base + "/d/{id}/htmlview")
    return state


@pytest.fixture
def poller(bot, store, sheets, layouts):
    p = Poller(store, sheets, bot.grades, layouts)
    bot.poller, p.on_read = p, bot.after_read
    return p


@pytest.fixture
def two(access, sheets):
    add_user(access, 1, IVAN)
    add_user(access, 2, ANNA, lang="en")
    sheet, _ = sheets.follow(1, URL)
    sheets.follow(2, URL)
    return sheet["id"]


async def test_one_download_and_one_layout_for_every_follower(google, poller, two, bot, api, llm, clock):
    results = await poller.poll()
    assert google["exports"] == 1 and google["titles"] == 1 and len(llm.prompts) == 2  # a layout per tab
    [read] = results
    assert read.sheet["title"] == 'Матанализ "1"' and read.error is None
    assert [(u.tg_id, u.tabs, u.items, u.first) for u in read.users] == [(1, 2, 2, True), (2, 2, 2, True)]
    assert "подключена: вкладок 2, оценок 2" in api.last(1)["text"]          # Quiz 2 is a placeholder zero
    assert "Матанали" in api.last(1)["text"]                                 # the course from the title
    assert "connected: tabs 2, grades 2" in api.last(2)["text"]
    assert bot.grades.waiting() == []

    google["data"] = book(hw=6.3, quiz=9)
    clock.advance(hours=1)
    sent = len(api.sent())
    await poller.poll()
    assert google["titles"] == 1 and len(llm.prompts) == 2  # the layout is cached
    [news] = api.sent()[sent:]                              # one message, to Ivan only
    assert news["chat_id"] == 1 and news["method"] == "sendRichMessage"
    assert f'<h3><a href="{URL.split("/edit")[0]}">Матанализ "1"</a> #Матанали</h3>' in news["text"]
    assert ('<tr><td>ДЗ 1А</td><td align="right"><b>9</b> +1</td><td align="right">6.3 из 7</td>'
            '<td align="right">8.4</td></tr>') in news["text"]
    assert '<tr><td>Квиз 1</td><td align="right"><b>9</b></td><td align="right"></td><td align="right">8</td>' \
        in news["text"]
    assert buttons(news) == {"Все оценки": "grades"}
    assert ("disable_notification", True) not in api.calls[-1][1].items()  # 15:00 in Moscow: not quiet
    assert bot.grades.waiting() == []
    await poller.poll()
    assert len(api.sent()) == sent + 1  # nothing twice


async def test_a_request_reads_only_that_sheet_and_tells_the_user(google, poller, two, bot, api, sheets, access):
    add_user(access, 3, "Сидорова Мария")
    other, _ = sheets.follow(3, "https://docs.google.com/spreadsheets/d/2ZyXwVuTsRqPoNmLkJiHgFeDc")
    await bot.handle(message(3, "/sheet " + URL))
    assert "Сейчас прочитаю" in api.last(3)["text"]
    [read] = await poller.poll(full=False)
    assert read.sheet["id"] == two and read.requested == {3} and google["exports"] == 1
    assert "подключена: вкладок 1, оценок 1" in api.last(3)["text"]  # her row is in the quizzes tab only


async def test_a_new_name_forgets_the_grades_and_reads_again(google, poller, two, bot, api, clock):
    await poller.poll()
    google["data"] = book(quiz=9)
    await bot.handle(message(1, "/name Петрова Анна"))
    assert "Сейчас прочитаю твои таблицы" in api.sent(1)[-1]["text"] and bot.grades.items(1, two) == []
    [read] = await poller.poll(full=False)
    assert read.requested == {1} and bot.grades.waiting() == []  # a baseline for the new name, no news
    assert "подключена" in api.last(1)["text"]
    assert [i["value"] for i in bot.grades.items(1, two)] == [10, 9]  # her grades now


async def test_errors_of_a_user_and_of_a_sheet(google, poller, two, bot, api, access, sheets):
    google["data"] = book(namesake=True)
    add_user(access, 3, "Никто Никтоевич")
    sheets.follow(3, URL)
    poller.request(two, 1)
    poller.request(two, 3)
    [read] = await poller.poll(full=False)
    assert {u.tg_id: u.error for u in read.users} == {1: "ambiguous: ДЗ-1А", 2: None, 3: "no_row"}
    assert "ФИО подходит к нескольким строкам на вкладках: ДЗ-1А" in api.last(1)["text"]
    assert "твоей строки нет ни на одной вкладке" in api.last(3)["text"]
    row = sheets.db.execute("select error, baseline_at from subscriptions where tg_id = 1").fetchone()
    assert (row["error"], row["baseline_at"]) == ("ambiguous: ДЗ-1А", None)

    google["data"] = b"<html>sign in</html>"
    poller.request(two, 2)
    [read] = await poller.poll(full=False)
    assert read.error.startswith("closed:") and "closed: open it to anyone with the link" in api.last(2)["text"]
    assert sheets.get(two)["error"].startswith("closed:")


async def test_a_sheet_failing_for_12_hours_is_told_once_and_its_return_too(google, poller, two, bot, api, clock,
                                                                            sheets):
    await poller.poll()
    google["data"] = 404
    await poller.poll()
    assert sheets.get(two)["error"] == "download: HTTP 404 Not Found"
    sent = len(api.sent())
    clock.advance(hours=13)
    await poller.poll()
    await poller.poll()
    told = api.sent()[sent:]
    assert sorted(m["chat_id"] for m in told) == [1, 2, OWNER]
    assert "не читается с 09.11 15:00" in api.last(1)["text"] and "cannot be read since" in api.last(2)["text"]
    assert "download: HTTP 404" in api.last(OWNER)["text"]

    google["data"] = book()
    await poller.poll()
    assert "снова читается" in api.last(1)["text"] and len(api.sent()) == sent + 6
    assert sheets.get(two)["error"] is None and bot.grades.waiting() == []


async def test_a_failed_layout_records_nothing_until_the_next_poll(google, poller, two, bot, llm, sheets):
    llm.error = BudgetExceeded("the monthly LLM limit is spent")
    [read] = await poller.poll()
    assert read.error.startswith("budget:") and bot.grades.items(1, two) == []
    llm.error = None
    [read] = await poller.poll()
    assert read.error is None and len(bot.grades.items(1, two)) == 2


async def test_nobody_to_read_for(google, poller, access, sheets):
    add_user(access, 1, None)          # no full name yet
    sheets.follow(1, URL)
    add_user(access, 2, ANNA)
    sheets.follow(2, URL)
    access.set_paused(2, True)         # blocked the bot
    assert await poller.poll() == [] and google["exports"] == 0


async def test_relayout_drops_the_cache(google, poller, two, bot, api, llm):
    await poller.poll()
    await bot.handle(message(OWNER, "/relayout " + URL))
    assert "сброшена" in api.last(OWNER)["text"]
    await poller.poll(full=False)
    assert len(llm.prompts) == 4
    await bot.handle(message(OWNER, "/relayout https://docs.google.com/spreadsheets/d/3QwErTyUiOpAsDfGhJkLzXcVb"))
    assert "/relayout" in api.last(OWNER)["text"]


async def test_a_group_sheet_is_read_for_everyone_at_once(google, poller, bot, api, access):
    add_user(access, 1, IVAN)
    bot.groups.join(1, bot.groups.create("A1")["id"])
    await bot.handle(message(OWNER, f"/groupsheet {URL} Calc"))
    [read] = await poller.poll(full=False)
    assert read.requested == set() and [u.first for u in read.users] == [True]
    assert "Calc" in api.last(1)["text"] and "подключена" in api.last(1)["text"]


async def test_the_owner_hears_about_the_llm_limit(bot, api):
    import asyncio

    from gradebot.llm.budget import Level

    bot.budget_notice(Level.STOP, 1.02, 1.0)
    await asyncio.sleep(0)
    assert "monthly LLM limit is spent" in api.last(OWNER)["text"] and "$1.02 / $1.00" in api.last(OWNER)["text"]


async def test_a_name_changed_during_the_read_records_nothing(google, poller, two, bot, access, layouts):
    get = layouts.get

    async def slow(*args):
        access.set_student(1, "Сидорова Мария")  # /name while Haiku thinks
        return await get(*args)

    layouts.get = slow
    [read] = await poller.poll()
    assert bot.grades.items(1, two) == [] and bot.grades.items(2, two)


async def test_the_owner_sets_the_interval(bot, api, poller, store):
    from gradebot.poller import interval

    await bot.handle(message(OWNER, "/interval"))
    view = api.last(OWNER)
    assert "раз в <b>30 мин</b>" in view["text"]
    assert buttons(view) == {"10 мин": "iv:10", "15 мин": "iv:15", "60 мин": "iv:60"}
    await bot.handle(callback(OWNER, "iv:15", message_id=77))
    assert interval(store) == 15 and api.last(OWNER)["method"] == "editMessageText"
    assert "iv:30" in api.last(OWNER)["text"]
    await bot.handle(message(OWNER, "/interval 20"))
    assert interval(store) == 20
    for wrong in ("/interval 2", "/interval 500", "/interval often"):
        await bot.handle(message(OWNER, wrong))
        assert "Минуты от 5 до 240" in api.last(OWNER)["text"] and interval(store) == 20
    await bot.handle(callback(OWNER, "iv:1"))
    assert interval(store) == 20
    await bot.handle(message(1, "/interval 5"))  # a stranger: no
    assert interval(store) == 20


async def test_a_new_interval_applies_at_once_and_requests_keep_the_schedule(poller, store, monkeypatch):
    import asyncio

    from gradebot import poller as module

    monkeypatch.setattr(module, "MINUTE", 0.02)    # 30 "minutes" is 0.6 s
    polls = []

    async def poll(full=True):
        polls.append(full)
        poller._pending = {}
        return []

    poller.poll = poll
    task = asyncio.create_task(poller.run())
    await asyncio.sleep(0.1)
    assert polls == [True]                      # the first poll at once, the next in 0.6 s
    poller.request(1)
    await asyncio.sleep(0.05)
    assert polls == [True, False]               # the request alone, the schedule kept
    module.set_interval(store, 5)               # 0.1 s
    poller.rescheduled()
    await asyncio.sleep(0.3)
    task.cancel()
    assert polls[:2] == [True, False] and polls.count(True) >= 3
