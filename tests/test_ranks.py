"""Ranks: a member's sheet new to the bot waits for the owner's approval; a trusted user's does not."""

import pytest

from gradebot.access import MEMBER, TRUSTED
from gradebot.sheets import SHEETS_PER_USER, SheetError

from .conftest import OWNER, buttons, callback, invite_code, message, press
from .test_sheets import ALG, CALC

PETR, ANNA = 2001, 2002


@pytest.fixture
async def petr(bot, api, access):
    await bot.handle(message(OWNER, "/invite Петя"))
    await bot.handle(message(PETR, f"/start {invite_code(api.last(OWNER)['text'])}"))
    await bot.handle(message(PETR, "/name Петров Пётр"))
    return access.user(PETR)


async def test_a_new_sheet_of_a_member_waits_for_approval(bot, api, sheets, access, petr):
    assert petr["rank"] == MEMBER
    await bot.handle(message(PETR, f"/sheet {CALC}"))
    await press(bot, PETR)
    assert "сначала её одобрит владелец" in api.last(PETR)["text"]
    assert sheets.followed(PETR) == [] and sheets.by_url(CALC) is None
    request = api.last(OWNER)
    assert "просьба подписать на таблицу" in request["text"]
    await bot.handle(message(PETR, f"/sheet {CALC}"))
    assert "уже ждёт одобрения" in api.last(PETR)["text"]
    await bot.handle(message(PETR, "/sheets"))
    assert "Ждут одобрения владельца" in api.last(PETR)["text"]

    await bot.handle(callback(OWNER, buttons(request)["Одобрить"]))
    assert "Одобрено:" in api.last(OWNER)["text"]
    assert "Владелец одобрил таблицу" in api.last(PETR)["text"] and "Сейчас прочитаю" in api.last(PETR)["text"]
    assert [s["sheet_id"] for s in sheets.followed(PETR)] == [sheets.by_url(CALC)["sheet_id"]]
    assert sheets.requests() == []
    await bot.handle(callback(OWNER, buttons(request)["Одобрить"]))
    assert api.calls[-1] == ("answerCallbackQuery", {"callback_query_id": "cb1", "text": "Уже решено"})


async def test_a_sheet_the_bot_reads_needs_no_approval(bot, api, sheets, access, petr):
    bot.groups.add_sheet(bot.groups.create("A1")["id"], ALG, OWNER)  # a group's sheet: Petr is in no group
    await bot.handle(message(PETR, f"/sheet {ALG}"))
    assert "Подписал на" in api.last(PETR)["text"] and len(sheets.followed(PETR)) == 1


async def test_decline_and_cancel(bot, api, sheets, petr):
    await bot.handle(message(PETR, f"/sheet {CALC} Матан"))
    await bot.handle(callback(OWNER, buttons(api.last(OWNER))["Отклонить"]))
    assert "Отклонено:" in api.last(OWNER)["text"]
    assert "Владелец отклонил таблицу" in api.last(PETR)["text"]
    assert sheets.requests() == [] and sheets.by_url(CALC) is None

    await bot.handle(message(PETR, f"/sheet {ALG}"))
    await press(bot, PETR)
    await bot.handle(message(PETR, "/sheets"))
    await bot.handle(callback(PETR, buttons(api.last(PETR))["Отменить заявку 1"]))
    assert sheets.requests() == [] and "Таблиц пока нет" in api.last(PETR)["text"]


async def test_the_owner_trusts_a_user_in_users(bot, api, sheets, access, petr):
    await bot.handle(message(OWNER, "/users"))
    listing = api.last(OWNER)
    assert "обычный" in listing["text"]
    await bot.handle(callback(OWNER, buttons(listing)["Доверять: Пётр"]))
    assert access.user(PETR)["rank"] == TRUSTED and "доверенный" in api.last(OWNER)["text"]
    await bot.handle(message(PETR, f"/sheet {CALC}"))
    await press(bot, PETR)
    assert "Подписал на" in api.last(PETR)["text"] and sheets.requests() == []
    await bot.handle(callback(OWNER, buttons(api.last(OWNER))["Обычный: Пётр"]))
    assert access.user(PETR)["rank"] == MEMBER


async def test_requests_count_towards_the_limit(sheets, access, petr):
    for n in range(SHEETS_PER_USER - 1):
        sheets.ask(PETR, f"https://docs.google.com/spreadsheets/d/{'x' * 20}{n:04d}")
    sheets.follow(PETR, CALC)
    with pytest.raises(SheetError):
        sheets.ask(PETR, ALG)
    approved = sheets.approve(sheets.requests(PETR)[0]["id"])  # a request becomes a sheet within the limit
    assert approved is not None and len(sheets.followed(PETR)) == 2
