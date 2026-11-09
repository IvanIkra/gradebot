import pytest

from .conftest import OWNER, buttons, callback, invite_code, message
from .test_sheets import ALG, CALC

PETR = 2001


@pytest.fixture
async def petr(bot, api, access):
    await bot.handle(message(OWNER, "/invite Петя"))
    await bot.handle(message(PETR, f"/start {invite_code(api.last(OWNER)['text'])}"))
    return access.user(PETR)


async def test_name_tells_the_owner_and_can_be_reset(bot, api, access, petr):
    await bot.handle(message(PETR, "/name петров"))
    assert "не похоже на ФИО" in api.last(PETR)["text"]
    await bot.handle(message(PETR, "/name  Петров   Пётр "))
    assert access.user(PETR)["student"] == "Петров Пётр"
    assert "Записал: <b>Петров Пётр</b>" in api.last(PETR)["text"] and "/sheet" in api.last(PETR)["text"]
    note = api.last(OWNER)
    assert "Пётр (@petr_example) указал ФИО <b>Петров Пётр</b>" in note["text"]
    await bot.handle(message(PETR, "/name Петров Пётр"))
    assert "уже записано" in api.last(PETR)["text"]
    await bot.handle(message(PETR, "/name Петров Пётр Петрович"))
    assert "(было Петров Пётр)" in api.last(OWNER)["text"]

    await bot.handle(callback(OWNER, buttons(note)["Сбросить"]))
    assert access.user(PETR)["student"] is None
    assert "ФИО <b>Петров Пётр Петрович</b> у Пётр (@petr_example) сброшено" in api.last(OWNER)["text"]
    assert "Владелец сбросил твоё ФИО" in api.last(PETR)["text"]
    await bot.handle(message(PETR, "/name"))
    assert "ФИО не указано" in api.last(PETR)["text"]


async def test_sheets_follow_and_unfollow(bot, api, sheets, petr):
    await bot.handle(message(PETR, "/sheets"))
    assert "Таблиц пока нет" in api.last(PETR)["text"]
    await bot.handle(message(PETR, "/sheet https://example.com/x"))
    assert "не ссылка на таблицу Google" in api.last(PETR)["text"]
    await bot.handle(message(PETR, f"/sheet {CALC}"))
    reply = api.last(PETR)["text"]
    assert "Подписал на <b>…789abc</b>" in reply and "нужно ФИО" in reply
    await bot.handle(message(PETR, f"/sheet {CALC}"))
    assert "уже подписан" in api.last(PETR)["text"]

    await bot.handle(message(PETR, "/sheets"))
    listing = api.last(PETR)
    assert "Твои таблицы · 1" in listing["text"] and "своя" in listing["text"]
    await bot.handle(callback(PETR, buttons(listing)["Отписаться: …789abc"]))
    assert sheets.followed(PETR) == []
    assert "Таблиц пока нет" in api.last(PETR)["text"]


async def test_group_sheets_for_new_and_old_users(bot, api, sheets, petr, access):
    await bot.handle(message(OWNER, f"/groupsheet {CALC} Матан"))
    assert "Общая таблица <b>Матан</b>. Подписано пользователей: 1." in api.last(OWNER)["text"]
    await bot.handle(message(OWNER, "/groupsheet not-a-link"))
    assert "не ссылка" in api.last(OWNER)["text"]

    await bot.handle(message(OWNER, "/invite Аня"))
    await bot.handle(message(2002, f"/start {invite_code(api.last(OWNER)['text'])}", first="Анна"))
    assert "общие таблицы группы: 1" in api.last(2002)["text"]
    assert [s["course"] for s in sheets.followed(2002)] == ["Матан"]

    await bot.handle(message(OWNER, f"/course {CALC} Матан-2"))
    assert "Курс: <b>Матан-2</b>" in api.last(OWNER)["text"]
    await bot.handle(message(OWNER, f"/course {ALG} Алг"))
    assert "никто не подписан" in api.last(OWNER)["text"]

    await bot.handle(message(OWNER, "/groupsheet"))
    listing = api.last(OWNER)
    assert "Общие таблицы · 1" in listing["text"] and "<td>2</td>" in listing["text"]
    await bot.handle(callback(OWNER, buttons(listing)["Убрать из общих: Матан-2"]))
    assert sheets.group() == [] and len(sheets.followed(PETR)) == 1
    await bot.handle(message(OWNER, "/users"))
    assert "<td>1</td>" in api.last(OWNER)["text"]


async def test_quiet_hours(bot, api, access, petr):
    await bot.handle(message(PETR, "/quiet"))
    view = api.last(PETR)
    assert "Тихие часы: <b>23:00–8:00</b>" in view["text"]
    assert buttons(view) == {"Выключить": "qt:off"}
    await bot.handle(callback(PETR, "qt:off"))
    assert (access.user(PETR)["quiet_from"], access.user(PETR)["quiet_to"]) == (None, None)
    assert buttons(api.last(PETR)) == {"23:00–8:00": "qt:23-8"}
    await bot.handle(message(PETR, "/quiet 22-7"))
    assert "<b>22:00–7:00</b>" in api.last(PETR)["text"]
    await bot.handle(message(PETR, "/quiet ночью"))
    assert "/quiet 23-8" in api.last(PETR)["text"]
    assert access.user(PETR)["quiet_from"] == 22


async def test_language(bot, api, access, petr):
    await bot.handle(message(PETR, "/lang"))
    assert buttons(api.last(PETR)) == {"English": "lg:en", "Русский": "lg:ru"}
    await bot.handle(callback(PETR, "lg:en"))
    assert access.user(PETR)["lang"] == "en" and "Language: English" in api.last(PETR)["text"]
    await bot.handle(message(PETR, "/help"))
    assert "follow a Google Sheet" in api.last(PETR)["text"]
    await bot.handle(message(PETR, "/lang RU"))
    assert access.user(PETR)["lang"] == "ru"


async def test_stop_drops_own_sheets(bot, api, sheets, petr):
    await bot.handle(message(PETR, f"/sheet {CALC}"))
    sheet_id = sheets.followed(PETR)[0]["id"]
    await bot.handle(callback(PETR, "stop!"))
    assert sheets.get(sheet_id) is None
