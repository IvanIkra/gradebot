"""Commands asked for step by step: a command from the menu comes without arguments."""

from .conftest import OWNER, buttons, callback, invite_code, message, press
from .test_sheets import ALG, CALC

PETR = 2001


async def test_invite_step_by_step(bot, api, access):
    a1 = bot.groups.create("A1")["id"]
    await bot.handle(message(OWNER, "/invite"))
    question = api.last(OWNER)
    assert "Какая группа?" in question["text"]
    assert buttons(question) == {"A1": f"an:{a1}", "Без группы: выберет сам": "an:", "Отмена": "ac"}
    await press(bot, OWNER, str(a1))
    assert "Сколько человек" in api.last(OWNER)["text"]
    await bot.handle(message(OWNER, "много"))                       # wrong: asked again with the reason
    assert "Число от 1 до 1000" in api.last(OWNER)["text"] and "Сколько человек" in api.last(OWNER)["text"]
    await bot.handle(message(OWNER, "3"))
    assert "Сколько действует код" in api.last(OWNER)["text"]
    await press(bot, OWNER)                                         # the default lifetime
    assert "На сколько открыть доступ" in api.last(OWNER)["text"]
    await bot.handle(message(OWNER, "31.12"))
    assert "Пометка для себя" in api.last(OWNER)["text"]
    await bot.handle(message(OWNER, "семинар"))
    reply = api.last(OWNER)["text"]
    assert "Код «семинар» в группу <b>A1</b>: вошли 0 из 3, действует до 16.11 15:00, доступ до 31.12 23:59" in reply
    [invite] = access.open_invites()
    assert (invite["grp"], invite["uses"], invite["access_until"]) == (a1, 3, "2026-12-31T20:59:00+00:00")
    assert invite_code(reply) and OWNER not in bot.asks


async def test_a_command_or_an_hour_drops_the_question(bot, api, clock, access):
    await bot.handle(message(OWNER, "/invite"))
    question = api.last(OWNER)
    await bot.handle(message(OWNER, "/codes"))                      # another command: the question is gone
    await bot.handle(message(OWNER, "5"))
    assert "Команды владельца" in api.last(OWNER)["text"]
    await bot.handle(callback(OWNER, "an:x5", question["chat_id"]))
    assert api.calls[-1][1]["text"] == "Этот вопрос уже закрыт"

    await bot.handle(message(OWNER, "/invite"))
    clock.advance(hours=1, seconds=1)
    await bot.handle(message(OWNER, "5"))
    assert "Команды владельца" in api.last(OWNER)["text"] and access.open_invites() == []


async def test_typed_arguments_run_at_once(bot, api, access):
    await bot.handle(message(OWNER, "/invite Петя"))
    assert invite_code(api.last(OWNER)["text"]) and OWNER not in bot.asks


async def test_name_asked_for(bot, api, access):
    await bot.handle(message(OWNER, "/invite Петя"))
    await bot.handle(message(PETR, f"/start {invite_code(api.last(OWNER)['text'])}"))
    await bot.handle(message(PETR, "/name"))
    assert "ФИО не указано" in api.last(PETR)["text"] and "Напиши его точно так" in api.last(PETR)["text"]
    await bot.handle(message(PETR, "петров"))
    assert "не похоже на ФИО" in api.last(PETR)["text"]
    await bot.handle(message(PETR, "Петров Пётр"))
    assert access.user(PETR)["student"] == "Петров Пётр"


async def test_groups_and_their_sheets_by_buttons(bot, api, sheets):
    await bot.handle(message(OWNER, "/groups"))
    await bot.handle(callback(OWNER, buttons(api.last(OWNER))["Новая группа"]))
    await bot.handle(message(OWNER, "A1"))
    made = api.last(OWNER)
    assert "Группа <b>A1</b> создана" in made["text"]
    await bot.handle(callback(OWNER, buttons(made)["Добавить таблицу"]))
    assert "Пришли ссылку" in api.last(OWNER)["text"]
    await bot.handle(message(OWNER, CALC))
    assert "Название из Google" in buttons(api.last(OWNER))
    await bot.handle(message(OWNER, "Матан"))
    assert "Общая таблица <b>Матан</b> группы <b>A1</b>" in api.last(OWNER)["text"]

    await bot.handle(message(OWNER, "/groups"))
    await bot.handle(callback(OWNER, buttons(api.last(OWNER))["Переименовать A1"]))
    await bot.handle(message(OWNER, "two words"))
    assert "одно слово" in api.last(OWNER)["text"]
    await bot.handle(message(OWNER, "B2"))
    assert "Переименована: <b>B2</b>" in api.last(OWNER)["text"]

    bot.groups.create("C3")
    await bot.handle(message(OWNER, "/groupsheet"))
    await bot.handle(callback(OWNER, buttons(api.last(OWNER))["Добавить таблицу"]))
    assert "Какая группа?" in api.last(OWNER)["text"]
    await bot.handle(message(OWNER, "c3"))
    await bot.handle(message(OWNER, CALC))
    assert "Оставить «Матан»" in buttons(api.last(OWNER))           # the sheet has a short name already
    await press(bot, OWNER)
    assert sheets.by_url(CALC)["course"] == "Матан"
    assert [s["url"] for s in bot.groups.sheets_of(bot.groups.find("C3")["id"])] == [sheets.by_url(CALC)["url"]]


async def test_relayout_by_a_button(bot, api, sheets):
    bot.groups.add_sheet(bot.groups.create("A1")["id"], ALG, OWNER, "Алг")
    await bot.handle(message(OWNER, "/relayout"))
    assert buttons(api.last(OWNER))["Алг"] == f"an:#{sheets.by_url(ALG)['id']}"
    await press(bot, OWNER, f"#{sheets.by_url(ALG)['id']}")
    assert "Разметка <b>Алг</b> сброшена" in api.last(OWNER)["text"]
