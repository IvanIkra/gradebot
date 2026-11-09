import pytest

from gradebot.access import TRUSTED

from .conftest import OWNER, buttons, callback, invite_code, message, press
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


async def test_sheets_follow_and_unfollow(bot, api, sheets, access, petr):
    access.set_rank(PETR, TRUSTED)
    await bot.handle(message(PETR, "/sheets"))
    assert "Таблиц пока нет" in api.last(PETR)["text"]
    await bot.handle(message(PETR, "/sheet https://example.com/x"))
    assert "не ссылка на таблицу Google" in api.last(PETR)["text"]
    await bot.handle(message(PETR, CALC))                       # the answer to the link asked again
    assert "Короткое имя таблицы" in api.last(PETR)["text"]
    await press(bot, PETR)                                      # «Title from Google»
    reply = api.last(PETR)["text"]
    assert "Подписал на <b>…789abc</b>" in reply and "нужно ФИО" in reply
    await bot.handle(message(PETR, f"/sheet {CALC}"))
    assert "уже подписан" in api.last(PETR)["text"]

    await bot.handle(message(PETR, "/sheets"))
    listing = api.last(PETR)
    assert "Твои таблицы · 1" in listing["text"] and "ещё нет" in listing["text"]  # not read yet
    await bot.handle(callback(PETR, buttons(listing)["Отписаться: …789abc"]))
    assert sheets.followed(PETR) == []
    assert "Таблиц пока нет" in api.last(PETR)["text"]


async def test_quiet_hours(bot, api, access, petr):
    await bot.handle(message(PETR, "/quiet"))
    view = api.last(PETR)
    assert "Тихие часы: <b>23:00–8:00</b>" in view["text"]
    assert buttons(view) == {"Выключить": "qt:off"}
    await bot.handle(message(PETR, "21-6"))                     # other hours, typed after /quiet
    assert "<b>21:00–6:00</b>" in api.last(PETR)["text"]
    await bot.handle(message(PETR, "/quiet"))
    await bot.handle(callback(PETR, "qt:off"))
    assert (access.user(PETR)["quiet_from"], access.user(PETR)["quiet_to"]) == (None, None)
    assert buttons(api.last(PETR)) == {"23:00–8:00": "qt:23-8"}
    await bot.handle(message(PETR, "/quiet 22-7"))
    assert "<b>22:00–7:00</b>" in api.last(PETR)["text"]
    await bot.handle(message(PETR, "/quiet ночью"))
    assert "Часы вида 23-8 или off" in api.last(PETR)["text"]
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


async def test_stop_drops_own_sheets(bot, api, sheets, access, petr):
    access.set_rank(PETR, TRUSTED)
    await bot.handle(message(PETR, f"/sheet {CALC}"))
    await press(bot, PETR)
    sheet_id = sheets.followed(PETR)[0]["id"]
    await bot.handle(callback(PETR, "stop!"))
    assert sheets.get(sheet_id) is None


async def test_a_course_for_a_sheet_new_to_the_bot(bot, api, sheets, access, petr):
    await bot.handle(message(PETR, "/help"))
    assert "/sheet &lt;ссылка&gt; [имя]" in api.last(PETR)["text"] and "название таблицы в Google" in \
        api.last(PETR)["text"]
    await bot.handle(message(PETR, "/sheet"))
    assert "Пришли ссылку на таблицу" in api.last(PETR)["text"]
    await bot.handle(message(PETR, CALC))
    assert "Без имени бот берёт название таблицы в Google" in api.last(PETR)["text"]
    await bot.handle(callback(PETR, "ac", bot.asks[PETR].message_id))
    assert "Отменено" in api.last(PETR)["text"] and PETR not in bot.asks
    access.set_rank(PETR, TRUSTED)
    await bot.handle(message(PETR, f"/sheet {CALC} Слишком длинный"))
    assert "В коротком имени не больше 10 символов" in api.last(PETR)["text"] and sheets.by_url(CALC) is None
    await bot.handle(message(PETR, f"/sheet {CALC} Матан"))
    assert "Подписал на <b>Матан</b>" in api.last(PETR)["text"] and sheets.by_url(CALC)["course"] == "Матан"

    await bot.handle(message(OWNER, "/invite Аня"))
    await bot.handle(message(2002, f"/start {invite_code(api.last(OWNER)['text'])}", first="Анна"))
    await bot.handle(message(2002, f"/sheet {CALC} Анализ"))  # the bot reads it already: the course stays
    assert "имя остаётся прежним: <b>Матан</b>" in api.last(2002)["text"]
    assert sheets.by_url(CALC)["course"] == "Матан"

    await bot.handle(message(2002, f"/sheet {ALG} Алгебра"))   # a member: the course waits with the request
    request = api.last(OWNER)
    assert ", имя <b>Алгебра</b>" in request["text"]
    await bot.handle(callback(OWNER, buttons(request)["Одобрить"]))
    assert sheets.by_url(ALG)["course"] == "Алгебра"

    await bot.handle(message(OWNER, f"/rename {ALG} Алг"))
    assert "Имя: <b>Алг</b>" in api.last(OWNER)["text"]
    await bot.handle(message(OWNER, "/rename"))
    assert buttons(api.last(OWNER))["Алг"] == f"an:#{sheets.by_url(ALG)['id']}"
    await press(bot, OWNER, f"#{sheets.by_url(ALG)['id']}")
    await press(bot, OWNER)                                     # «Title from Google»
    assert "Короткого имени нет" in api.last(OWNER)["text"] and "первые 8 символов" in api.last(OWNER)["text"]
