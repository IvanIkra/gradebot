"""A group's admin: made by the owner, runs only their own group, within limits."""

import pytest

from gradebot.access import ADMIN, MEMBER, TRUSTED
from gradebot.owner import limit_args

from .conftest import OWNER, buttons, callback, invite_code, message, press
from .test_sheets import ALG, CALC

ADA, BOB, EVA = 3001, 3002, 3003     # Ada runs A1, Bob is in A1, Eva is in B2
NAMES = {ADA: "Ада", BOB: "Боб", EVA: "Ева"}


def say(tg_id: int, text: str) -> dict:
    return message(tg_id, text, first=NAMES[tg_id], username=None)


async def come_in(bot, api, tg_id: int, args: str, first: str) -> None:
    await bot.handle(message(OWNER, f"/invite {args}"))
    await bot.handle(message(tg_id, f"/start {invite_code(api.last(OWNER)['text'])}", first=first, username=None))


@pytest.fixture
async def groups(bot, api, access):
    a1, b2 = bot.groups.create("A1"), bot.groups.create("B2")
    await come_in(bot, api, ADA, "A1 админ", "Ада")
    await come_in(bot, api, BOB, "A1", "Боб")
    await come_in(bot, api, EVA, "B2", "Ева")
    return a1, b2


def commands_set(api, chat: int) -> list[str]:
    """The commands of the last menu set for a chat."""
    menus = [params for method, params in api.calls
             if method == "setMyCommands" and params["scope"].get("chat_id") == chat]
    return [c["command"] for c in menus[-1]["commands"]] if menus else []


async def test_an_admins_code(bot, api, access, groups):
    a1, _ = groups
    ada = access.user(ADA)
    assert (ada["grp"], ada["rank"]) == (a1["id"], ADMIN) and access.user(BOB)["rank"] == MEMBER
    assert "Ты админ группы" in api.sent(ADA)[0]["text"]
    assert "invite" in commands_set(api, ADA) and not commands_set(api, BOB)
    await bot.handle(message(OWNER, "/invite админ"))           # an admin's code needs a group
    assert "Код админа — только в группу" in api.last(OWNER)["text"]


async def test_the_owner_makes_and_unmakes_an_admin(bot, api, access, groups):
    await bot.handle(callback(OWNER, f"uc:{BOB}"))
    assert buttons(api.last(OWNER))["Сделать админом группы"] == f"ra:{BOB}"
    await bot.handle(callback(OWNER, f"ra:{BOB}"))
    assert access.user(BOB)["rank"] == ADMIN and "Владелец сделал тебя админом группы <b>A1</b>" in \
        api.last(BOB)["text"]
    assert "groupsheet" in commands_set(api, BOB)
    await bot.handle(callback(OWNER, f"ra:{BOB}"))
    assert access.user(BOB)["rank"] == TRUSTED and "больше не админ" in api.last(BOB)["text"]
    assert "groupsheet" not in commands_set(api, BOB)


async def test_moved_or_group_gone_no_admin(bot, api, access, groups):
    _, b2 = groups
    await bot.handle(callback(OWNER, f"um:{ADA}.{b2['id']}"))
    assert access.user(ADA)["rank"] == TRUSTED and "invite" not in commands_set(api, ADA)
    await bot.handle(callback(OWNER, f"ra:{ADA}"))              # the admin of B2 now
    bot.groups.delete(b2["id"])
    assert access.user(ADA)["rank"] == TRUSTED


async def test_admin_codes_within_limits(bot, api, access, groups, clock):
    a1, b2 = groups
    before = len(access.open_invites())
    await bot.handle(say(ADA, "/invite x50"))
    assert "не больше 30 человек" in api.last(ADA)["text"]
    await bot.handle(say(ADA, "/invite 10д"))
    assert "не больше 7 дней" in api.last(ADA)["text"] and len(access.open_invites()) == before
    await bot.handle(say(ADA, "/invite x5 2д админ курс"))  # «админ» is only a word of the note here
    reply = api.last(ADA)["text"]
    assert "Код «админ курс» в группу <b>A1</b>: вошли 0 из 5" in reply and invite_code(reply)
    invite = access.open_invites()[0]
    assert (invite["grp"], invite["rank"], invite["uses"]) == (a1["id"], MEMBER, 5)
    assert "Ада, админ <b>A1</b>, создал(а) код «админ курс» на 5 чел." in api.last(OWNER)["text"]

    access.prolong(ADA, 10)                                      # access for 10 days: codes give no more
    await bot.handle(say(ADA, "/invite +30д"))
    assert access.open_invites()[0]["access_until"] == access.user(ADA)["expires_at"]


async def test_admin_invite_step_by_step(bot, api, access, groups):
    await bot.handle(say(ADA, "/invite"))
    question = api.last(ADA)
    assert "Сколько человек" in question["text"] and "Сколько угодно" not in buttons(question)
    await press(bot, ADA, "x10")
    assert set(buttons(api.last(ADA))) >= {"1 день", "7 дней"} and "30 дней" not in buttons(api.last(ADA))
    for value in ("", "", "Новенькие"):
        await press(bot, ADA, value) if value == "" else await bot.handle(say(ADA, value))
    assert "Код «Новенькие» в группу <b>A1</b>: вошли 0 из 10" in api.last(ADA)["text"]


async def test_admin_codes_of_their_group_only(bot, api, access, groups):
    await bot.handle(message(OWNER, "/invite B2 x3 чужой"))
    await bot.handle(message(OWNER, "/invite A1 x3 свой"))
    await bot.handle(message(OWNER, "/invite A1 админ второй админ"))
    await bot.handle(say(ADA, "/codes"))
    view = api.last(ADA)["text"]
    assert "свой" in view and "чужой" not in view and "второй админ" not in view
    theirs = next(inv for inv in access.open_invites() if inv["note"] == "чужой")
    await bot.handle(callback(ADA, f"av:{theirs['id']}"))
    assert any(inv["id"] == theirs["id"] for inv in access.open_invites())
    mine = next(inv for inv in access.open_invites() if inv["note"] == "свой")
    await bot.handle(callback(ADA, f"aq:{mine['id']}"))
    assert buttons(api.last(ADA))["Да, отозвать"] == f"av:{mine['id']}"
    await bot.handle(callback(ADA, f"av:{mine['id']}"))
    assert all(inv["id"] != mine["id"] for inv in access.open_invites())


async def test_admin_sees_their_group(bot, api, access, groups):
    await bot.handle(say(ADA, "/users"))
    view = api.last(ADA)
    assert "Боб" in view["text"] and "Ева" not in view["text"] and buttons(view)["Боб"] == f"mc:{BOB}"
    await bot.handle(callback(ADA, f"mc:{BOB}"))
    card = api.last(ADA)
    assert "<h3>Боб</h3>" in card["text"] and "обычный" in card["text"]
    assert not any(data.startswith(("du:", "rk:", "ae:")) for data in buttons(card).values())
    await bot.handle(callback(ADA, f"mc:{EVA}"))                 # not in the group: the list again
    assert "Ева" not in api.last(ADA)["text"] and "Группа A1" in api.last(ADA)["text"]


async def test_members_cannot_act_as_admins(bot, api, groups):
    await bot.handle(say(BOB, "/invite x5"))
    assert "Это не команда" in api.last(BOB)["text"]
    await bot.handle(callback(BOB, f"mc:{ADA}"))
    assert api.calls[-1][1]["text"] == "Это может только админ группы"


async def test_admin_shared_sheets(bot, api, access, sheets, groups):
    a1, b2 = groups
    await bot.handle(say(ADA, f"/groupsheet {CALC}"))
    assert "общая таблица группы <b>A1</b>" in api.last(ADA)["text"]
    assert {s["url"] for s in bot.groups.sheets_of(a1["id"])} == {sheets.by_url(CALC)["url"]}
    assert sheets.by_url(CALC)["id"] in {s["id"] for s in sheets.followed(BOB)}
    assert "добавил(а) общую таблицу" in api.last(OWNER)["text"]
    sheet = sheets.by_url(CALC)["id"]
    await bot.handle(callback(ADA, f"gr:{b2['id']}.{sheet}"))     # another group's: nothing
    assert bot.groups.sheets_of(a1["id"])
    await bot.handle(callback(ADA, f"gr:{a1['id']}.{sheet}"))
    assert not bot.groups.sheets_of(a1["id"]) and "убрал(а) общую таблицу" in api.last(OWNER)["text"]


async def test_admin_approves_members_sheets(bot, api, access, sheets, groups):
    await bot.handle(say(BOB, f"/sheet {ALG}"))
    assert "владелец или админ группы" in api.last(BOB)["text"]
    asked = api.last(ADA)
    assert "просьба подписать на таблицу" in asked["text"]
    await bot.handle(callback(ADA, buttons(asked)["Одобрить"]))
    assert "Одобрено" in api.last(ADA)["text"] and "Задать имя" not in buttons(api.last(ADA))
    assert "Админ группы одобрил таблицу" in api.last(BOB)["text"]
    assert sheets.by_url(ALG)["id"] in {s["id"] for s in sheets.followed(BOB)}
    assert "админ группы, одобрил(а)" in api.last(OWNER)["text"]

    await bot.handle(say(EVA, f"/sheet {CALC}"))             # B2 has no admin: only the owner hears
    request = sheets.requests(EVA)[0]
    await bot.handle(callback(ADA, f"sd:{request['id']}"))
    assert sheets.requests(EVA)


async def test_group_news_as_the_admin_likes(bot, api, access, groups):
    news = [m for m in api.sent(ADA) if "Группа A1" in m["text"]]
    assert news and "Боб: вход по коду" in news[-1]["text"] and buttons(news[-1])["Настроить"] == "nm"
    await bot.handle(say(BOB, "/name Бобров Боб"))
    digest = api.last(ADA)
    assert digest["method"] == "editMessageText" and "Боб: ФИО <b>Бобров Боб</b>" in digest["text"]
    assert "Ева" not in digest["text"]                           # B2's news are not A1's

    await bot.handle(say(ADA, "/news"))
    assert "Сейчас: <b>тихой сводкой</b>" in api.last(ADA)["text"]
    await bot.handle(callback(ADA, "nw:each"))
    await bot.handle(say(BOB, "/stop"))
    await bot.handle(callback(BOB, "stop!"))
    each = api.last(ADA)
    assert each["method"] in ("sendRichMessage", "sendMessage") and "вышел(а) из бота" in each["text"]

    await bot.handle(callback(ADA, "nw:off"))
    count = len(api.sent(ADA))
    await come_in(bot, api, 3004, "A1", "Вера")
    assert len(api.sent(ADA)) == count


async def test_the_help_of_an_admin(bot, api, groups):
    await bot.handle(say(ADA, "/help"))
    assert "Админ группы A1" in api.last(ADA)["text"]
    await bot.handle(say(BOB, "/help"))
    assert "Админ группы" not in api.last(BOB)["text"]


def test_limit_args():
    assert limit_args("x50 14д") == {"uses": 50, "days": 14}
    assert limit_args("x*") == {"uses": 0} and limit_args("10") == {"days": 10}
    for bad in ("", "x0", "x1001", "0д", "400d", "много"):
        with pytest.raises(ValueError):
            limit_args(bad)


async def test_the_owner_sets_admins_limits(bot, api, access, groups):
    await bot.handle(message(OWNER, "/limits"))
    view = api.last(OWNER)
    assert "<b>до 30 человек</b>" in view["text"] and "до <b>7 дней</b>" in view["text"]
    assert buttons(view)["50"] == "al:u.50" and "✓ 30" in buttons(view) and "✓ 7 дней" in buttons(view)
    await bot.handle(callback(OWNER, "al:u.5"))
    assert bot.limits() == (5, 7) and "✓ 5" not in buttons(api.last(OWNER))   # not a button: set anyway
    await bot.handle(say(ADA, "/invite x10"))
    assert "не больше 5 человек" in api.last(ADA)["text"] and "до 5 человек" in api.last(ADA)["text"]

    await bot.handle(message(OWNER, "/limits"))
    await bot.handle(message(OWNER, "много"))                    # wrong: asked again
    assert "Например x50 или x*" in api.last(OWNER)["text"]
    await bot.handle(message(OWNER, "x* 14д"))
    assert bot.limits() == (0, 14) and "сколько угодно человек" in api.last(OWNER)["text"]
    await bot.handle(say(ADA, "/invite x* 10д"))
    assert "без ограничения" in api.last(ADA)["text"] and invite_code(api.last(ADA)["text"])
    await bot.handle(callback(OWNER, "al:d.0"))
    assert bot.limits() == (0, 14)
