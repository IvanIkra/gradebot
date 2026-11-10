from gradebot.access import WRONG_LIMIT
from gradebot.bot import command
from gradebot.telegram import TelegramError

from .conftest import OWNER, buttons, callback, invite_code, message

PETR, ANNA = 2001, 2002


async def invite(bot, api, note: str = "Петя") -> str:
    await bot.handle(message(OWNER, f"/invite {note}"))
    return invite_code(api.last(OWNER)["text"])


def test_command():
    assert command("/start@gradebot_test_bot  K7QF-29XM ") == ("/start", "K7QF-29XM")
    assert command("/USERS") == ("/users", "")
    assert command("K7QF-29XM") == ("", "K7QF-29XM")


async def test_stranger_needs_a_code(bot, api):
    await bot.handle(message(PETR, "привет"))
    assert "Вход по коду приглашения" in api.last(PETR)["text"]
    await bot.handle(message(ANNA, "/start", lang="en"))
    assert "invite code" in api.last(ANNA)["text"]
    await bot.handle(message(PETR, "/start", chat_type="group"))
    assert len(api.sent(PETR)) == 1


async def test_invite_and_come_in(bot, api, access):
    code = await invite(bot, api)
    text = api.last(OWNER)["text"]
    assert "«Петя»" in text and "16.11 15:00" in text
    assert f"<code>https://t.me/gradebot_test_bot?start={code}</code>" in text   # a tap copies it
    assert buttons(api.last(OWNER)) == {
        "Отправить в чат": f"https://t.me/share/url?url=https%3A%2F%2Ft.me%2Fgradebot_test_bot%3Fstart%3D{code}"}

    await bot.handle(message(PETR, f"/start {code}"))
    assert "Как ты записан в таблицах?" in api.last(PETR)["text"] and bot.asks[PETR].step == "student"
    assert access.user(PETR)["lang"] == "ru"
    note = api.last(OWNER)
    assert "Пётр (@petr_example): вход по коду «Петя»" in note["text"]
    assert buttons(note) == {"Удалить Пётр": f"du:{PETR}"}

    await bot.handle(message(ANNA, f"/start {code}"))
    assert access.user(ANNA) is None and "Этот код не подходит" in api.last(ANNA)["text"]


async def test_pasted_code_works(bot, api, access):
    code = await invite(bot, api)
    await bot.handle(message(PETR, code.lower().replace("-", ""), lang="en"))
    assert access.user(PETR)["lang"] == "en"
    assert "You're in" in api.sent(PETR)[-2]["text"]


async def test_too_many_wrong_codes_silence_the_bot(bot, api):
    for _ in range(WRONG_LIMIT):
        await bot.handle(message(PETR, "/start AAAA-AAAA"))
    count = len(api.sent(PETR))
    await bot.handle(message(PETR, "/start AAAA-AAAA"))
    await bot.handle(message(PETR, "hello"))
    assert len(api.sent(PETR)) == count == WRONG_LIMIT - 1


async def test_names_are_escaped(bot, api):
    code = await invite(bot, api, note="<i>x</i>")
    await bot.handle(message(PETR, f"/start {code}", first="<b>Пётр</b>", username=None))
    text = api.last(OWNER)["text"]
    assert "&lt;b&gt;Пётр&lt;/b&gt;" in text and "&lt;i&gt;x&lt;/i&gt;" in text and "<b>Пётр" not in text


async def test_codes_and_revoke(bot, api, access):
    await bot.handle(message(OWNER, "/codes"))
    assert "Действующих кодов нет" in api.last(OWNER)["text"]
    await invite(bot, api, "Аня")
    await bot.handle(message(OWNER, "/codes"))
    listing = api.last(OWNER)
    assert "<table" in listing["text"] and "Аня" in listing["text"]
    await bot.handle(callback(OWNER, buttons(listing)["Отозвать Аня"]))
    confirm = api.last(OWNER)
    assert "Отозвать код <b>Аня</b>?" in confirm["text"] and len(access.open_invites()) == 1
    await bot.handle(callback(OWNER, buttons(confirm)["Да, отозвать"]))
    assert access.open_invites() == []
    edited = api.last(OWNER)
    assert edited["method"] == "editMessageText" and "Действующих кодов нет" in edited["text"]
    assert ("answerCallbackQuery", {"callback_query_id": "cb1", "text": "Отозван"}) in api.calls


async def test_owner_deletes_a_user(bot, api, access):
    await bot.handle(message(PETR, f"/start {await invite(bot, api)}"))
    await bot.handle(message(OWNER, "/users"))
    listing = api.last(OWNER)
    assert "Пётр (@petr_example)" in listing["text"]
    await bot.handle(callback(OWNER, buttons(listing)["Пётр"]))
    card = api.last(OWNER)
    assert "Петя" in card["text"] and "обычный" in card["text"]
    await bot.handle(callback(OWNER, buttons(card)["Удалить"]))
    confirm = api.last(OWNER)
    assert "Удалить Пётр (@petr_example)?" in confirm["text"]
    await bot.handle(callback(OWNER, buttons(confirm)["Отмена"]))
    assert access.user(PETR) is not None and "« Пользователи" in api.last(OWNER)["text"]  # back to the card
    await bot.handle(callback(OWNER, f"dy:{PETR}"))
    assert access.user(PETR) is None
    assert "закрыл тебе доступ" in api.last(PETR)["text"]
    assert "Пользователей пока нет" in api.last(OWNER)["text"]
    await bot.handle(message(PETR, "/help"))
    assert "Вход по коду приглашения" in api.last(PETR)["text"]


async def test_deleting_a_user_who_blocked_the_bot(bot, api, access):
    await bot.handle(message(PETR, f"/start {await invite(bot, api)}"))
    api.fail["sendRichMessage"] = TelegramError("sendRichMessage", 403, "Forbidden: bot was blocked by the user")
    api.fail["sendMessage"] = api.fail["sendRichMessage"]
    await bot.handle(callback(OWNER, f"dy:{PETR}"))
    assert access.user(PETR) is None


async def test_user_stops(bot, api, access):
    await bot.handle(message(PETR, f"/start {await invite(bot, api)}"))
    await bot.handle(message(PETR, "/stop"))
    ask = api.last(PETR)
    await bot.handle(callback(PETR, buttons(ask)["Отмена"]))
    assert access.user(PETR) is not None and "Остаёмся" in api.last(PETR)["text"]
    await bot.handle(callback(PETR, buttons(ask)["Да, удалить"]))
    assert access.user(PETR) is None
    assert "Выход из бота через /stop: Пётр (@petr_example)" in api.last(OWNER)["text"]


async def test_strangers_callbacks_do_nothing(bot, api, access):
    await bot.handle(message(PETR, f"/start {await invite(bot, api)}"))
    await bot.handle(callback(ANNA, f"dy:{PETR}"))
    await bot.handle(callback(PETR, f"dy:{PETR}"))
    assert access.user(PETR) is not None
    assert api.calls[-1] == ("answerCallbackQuery", {"callback_query_id": "cb1"})


async def test_owner_language_follows_their_client(bot, api):
    await bot.handle(message(OWNER, "/help", lang="en"))
    assert "Owner's commands" in api.last(OWNER)["text"]
    assert bot.owner_lang == "en"
    await bot.handle(message(OWNER, "/help", lang="ru"))
    assert "Команды владельца" in api.last(OWNER)["text"]
