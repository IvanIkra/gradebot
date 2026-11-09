"""The owner's news: one silent digest, edited as news comes, until the owner looks or something needs them."""

from gradebot.news import DIGESTS, NEWS
from gradebot.rich import Doc
from gradebot.telegram import TelegramError

from .conftest import OWNER, buttons, callback, message

PETR, ANNA = 2001, 2002


async def come_in(bot, api, tg_id: int, first: str) -> None:
    await bot.handle(message(OWNER, "/invite"))
    await bot.handle(callback(OWNER, "an:", bot.asks[OWNER].message_id))  # one person
    while OWNER in bot.asks:
        await bot.handle(callback(OWNER, "an:", bot.asks[OWNER].message_id))
    code = api.last(OWNER)["text"].split("<code>")[1][:7]
    await bot.handle(message(tg_id, f"/start {code}", first=first, username=None))


def owner_calls(api) -> list[tuple[str, dict]]:
    return [(method, params) for method, params in api.calls
            if params.get("chat_id") == OWNER and method in ("sendRichMessage", "editMessageText")]


async def test_news_gathers_in_one_silent_message(bot, api):
    await come_in(bot, api, PETR, "Пётр")
    start = len(owner_calls(api))
    await bot.handle(message(PETR, "/name Петров Пётр", username=None))
    await bot.handle(message(ANNA, "/start", username=None))   # a stranger is no news

    (sent, first), (edited, second) = owner_calls(api)[start - 1:]
    assert sent == "sendRichMessage" and first["disable_notification"] is True
    assert edited == "editMessageText" and second["message_id"] == bot.store.get_json(NEWS)["message_id"]
    text = api.last(OWNER)["text"]
    assert "Новости · 2" in text and "вход по коду" in text and "ФИО <b>Петров Пётр</b>" in text
    assert set(buttons(api.last(OWNER))) == {"Удалить Пётр", "Сбросить ФИО: Пётр"}


async def test_the_owner_looking_or_a_request_starts_a_new_digest(bot, api, access):
    await come_in(bot, api, PETR, "Пётр")
    await bot.handle(message(OWNER, "/status"))                  # the owner has seen the chat
    await bot.handle(message(PETR, "/name Петров Пётр", username=None))
    assert api.last(OWNER)["method"] == "sendRichMessage" and "Новости ·" not in api.last(OWNER)["text"]

    await bot.tell_owner(Doc().p("a request"))                   # a message that needs the owner, with a sound
    await bot.handle(message(PETR, "/stop", username=None))
    await bot.handle(callback(PETR, "stop!"))
    last = api.last(OWNER)
    assert last["method"] == "sendRichMessage" and "через /stop" in last["text"] and "ФИО" not in last["text"]


async def test_a_button_in_a_digest_answers_in_a_new_message(bot, api, access):
    await come_in(bot, api, PETR, "Пётр")
    digest = api.last(OWNER)
    [digest_id] = bot.store.get_json(DIGESTS)
    await bot.handle(callback(OWNER, buttons(digest)["Удалить Пётр"], message_id=digest_id))
    method, params = owner_calls(api)[-1]
    assert method == "sendRichMessage" and "Удалить Пётр?" in params["rich_message"]["html"]  # the digest stays


async def test_a_digest_that_cannot_be_edited_is_started_anew(bot, api):
    await come_in(bot, api, PETR, "Пётр")
    api.fail["editMessageText"] = TelegramError("editMessageText", 400, "Bad Request: message to edit not found")
    await bot.handle(message(PETR, "/name Петров Пётр", username=None))
    assert api.calls[-1][0] == "sendRichMessage" and "Новости ·" not in api.last(OWNER)["text"]
