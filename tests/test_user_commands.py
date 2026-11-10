import pytest

from gradebot.access import TRUSTED
from gradebot.i18n import days

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
    assert "Записал: <b>Петров Пётр</b>" in api.last(PETR)["text"] and "просто вставь" in api.last(PETR)["text"]
    note = api.last(OWNER)
    assert "Пётр (@petr_example): ФИО <b>Петров Пётр</b>" in note["text"]
    await bot.handle(message(PETR, "/name Петров Пётр"))
    assert "уже записано" in api.last(PETR)["text"]
    await bot.handle(message(PETR, "/name Петров Пётр Петрович"))
    assert "(было Петров Пётр)" in api.last(OWNER)["text"]

    await bot.handle(callback(OWNER, buttons(note)["Сбросить ФИО: Пётр"]))
    assert access.user(PETR)["student"] is None
    assert "ФИО <b>Петров Пётр Петрович</b> у Пётр (@petr_example) сброшено" in api.last(OWNER)["text"]
    assert "Владелец сбросил твоё ФИО" in api.last(PETR)["text"]
    await bot.handle(message(PETR, "/name"))
    assert "Как ты записан в таблицах?" in api.last(PETR)["text"]


async def test_start_again_says_you_are_in(bot, api, petr):
    for text in ("/start", "/start ABC-DEF"):
        await bot.handle(message(PETR, text))
        assert "Ты уже в списке" in api.last(PETR)["text"]


async def test_sheets_follow_and_unfollow(bot, api, sheets, access, petr):
    access.set_rank(PETR, TRUSTED)
    await bot.handle(message(PETR, "/sheets"))
    assert "Таблиц пока нет" in api.last(PETR)["text"]
    await bot.handle(message(PETR, "/sheet https://example.com/x"))
    assert "не ссылка на таблицу Google" in api.last(PETR)["text"]
    await bot.handle(message(PETR, CALC))                       # the answer to the link asked again
    reply = api.last(PETR)["text"]
    assert "Подписал на таблицу." in reply and "нужно ФИО" in reply
    await bot.handle(message(PETR, f"/sheet {CALC}"))
    assert "уже в твоих подписках" in api.last(PETR)["text"]

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
    assert buttons(api.last(PETR)) == {"English": "lg:en", "Русский ✓": "lg:ru"}
    await bot.handle(callback(PETR, "lg:en"))
    assert access.user(PETR)["lang"] == "en" and "Language: English" in api.last(PETR)["text"]
    await bot.handle(message(PETR, "/help"))
    assert "follow a Google Sheet" in api.last(PETR)["text"]
    await bot.handle(message(PETR, "/lang RU"))
    assert access.user(PETR)["lang"] == "ru"


async def test_stop_drops_own_sheets(bot, api, sheets, access, petr):
    access.set_rank(PETR, TRUSTED)
    await bot.handle(message(PETR, f"/sheet {CALC}"))
    sheet_id = sheets.followed(PETR)[0]["id"]
    await bot.handle(callback(PETR, "stop!"))
    assert sheets.get(sheet_id) is None


async def test_only_the_owner_names_a_sheet(bot, api, sheets, access, petr):
    await bot.handle(message(PETR, "/help"))
    assert "/sheet &lt;ссылка&gt;</td>" in api.last(PETR)["text"] and "просто прислать ссылку" in \
        api.last(PETR)["text"]
    await bot.handle(message(PETR, "/sheet"))
    assert "Пришли ссылку на таблицу" in api.last(PETR)["text"]
    await bot.handle(callback(PETR, "ac", bot.asks[PETR].message_id))
    assert "Отменено" in api.last(PETR)["text"] and PETR not in bot.asks

    await bot.handle(message(PETR, f"/sheet {ALG} Алгебра"))   # words after the link are no name
    request = api.last(OWNER)
    assert "Алгебра" not in request["text"]
    await bot.handle(callback(OWNER, buttons(request)["Одобрить"]))
    approved = api.last(OWNER)
    assert not sheets.by_url(ALG)["course"] and "Одобрено" in approved["text"]
    await bot.handle(callback(OWNER, buttons(approved)["Задать имя"]))
    await bot.handle(message(OWNER, "Алгебра"))
    assert sheets.by_url(ALG)["course"] == "Алгебра" and "Имя: <b>Алгебра</b>" in api.last(OWNER)["text"]

    await bot.handle(message(OWNER, f"/rename {ALG} Алг"))
    assert "Имя: <b>Алг</b>" in api.last(OWNER)["text"]
    await bot.handle(message(OWNER, "/rename"))
    assert buttons(api.last(OWNER))["Алг"] == f"an:#{sheets.by_url(ALG)['id']}"
    await press(bot, OWNER, f"#{sheets.by_url(ALG)['id']}")
    await press(bot, OWNER)                                     # «Title from Google»
    assert "Короткого имени нет" in api.last(OWNER)["text"] and "из первых слов" in api.last(OWNER)["text"]


async def test_a_link_pasted_alone_follows_the_sheet(bot, api, sheets, access, petr):
    access.set_rank(PETR, TRUSTED)
    assert bot.asks[PETR].step == "student"             # the full name is asked after coming in
    await bot.handle(message(PETR, CALC))               # a link instead: it is followed, not taken for a name
    assert [s["url"] for s in sheets.followed(PETR)] == [sheets.by_url(CALC)["url"]]
    assert access.user(PETR)["student"] is None


async def test_add_a_sheet_from_the_list(bot, api, petr):
    await bot.handle(message(PETR, "/sheets"))
    assert "просто вставь её сюда" in api.last(PETR)["text"]
    await bot.handle(callback(PETR, buttons(api.last(PETR))["Добавить таблицу"]))
    assert "Пришли ссылку на таблицу" in api.last(PETR)["text"] and bot.asks[PETR].step == "link"


def test_days_in_words():
    assert [days("ru", n) for n in (1, 3, 7, 11, 21, 22, 30)] == \
        ["1 день", "3 дня", "7 дней", "11 дней", "21 день", "22 дня", "30 дней"]
    assert (days("en", 1), days("en", 30)) == ("1 day", "30 days")


async def test_a_user_without_a_full_name_is_reminded_once(bot, api, access, petr, clock):
    await bot.remind_names()
    count = len(api.sent(PETR))
    clock.advance(days=1, minutes=1)
    await bot.remind_names()
    await bot.remind_names()
    assert len(api.sent(PETR)) == count + 1 and "нужно ФИО" in api.last(PETR)["text"]


async def test_feedback_reaches_the_owner_who_answers(bot, api, petr):
    await bot.handle(message(PETR, "/feedback"))
    assert "Что передать владельцу" in api.last(PETR)["text"]
    await bot.handle(message(PETR, "Не вижу оценки за <КР2>"))
    assert "Ответ придёт сюда" in api.last(PETR)["text"]
    note = api.last(OWNER)
    assert "Пётр (@petr_example) пишет:" in note["text"] and "Не вижу оценки за &lt;КР2&gt;" in note["text"]
    [to_owner] = [params for method, params in api.calls if method == "sendRichMessage"
                  and params["chat_id"] == OWNER and "пишет:" in params["rich_message"]["html"]]
    assert "disable_notification" not in to_owner  # a message that needs the owner comes with a sound

    await bot.handle(callback(OWNER, buttons(note)["Ответить"]))
    assert "Ответ для Пётр (@petr_example)" in api.last(OWNER)["text"]
    await bot.handle(message(OWNER, "Скоро починю"))
    assert "Ответ владельца:" in api.last(PETR)["text"] and "Скоро починю" in api.last(PETR)["text"]
    assert "Отправлено: Пётр" in api.last(OWNER)["text"]


async def test_feedback_has_a_daily_limit(bot, api, petr, clock):
    for n in range(5):
        await bot.handle(message(PETR, f"/feedback идея {n}"))
    await bot.handle(message(PETR, "/feedback ещё одна"))
    assert "На сегодня хватит" in api.last(PETR)["text"] and "ещё одна" not in api.last(OWNER)["text"]
    clock.advance(days=1, minutes=1)
    await bot.handle(message(PETR, "/feedback ещё одна"))
    assert "ещё одна" in api.last(OWNER)["text"]
