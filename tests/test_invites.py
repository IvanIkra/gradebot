"""Codes for several people, how long a code works, and access for a while."""

import datetime as dt

import pytest

from gradebot.access import NewUser
from gradebot.owner import invite_args

from .conftest import OWNER, ZONE, buttons, callback, invite_code, message

PETR, ANNA, IRA = 2001, 2002, 2003
NOW = dt.datetime(2026, 11, 9, 12, 0, tzinfo=dt.UTC)


def args(text: str, groups: dict | None = None) -> dict:
    return invite_args(text, lambda word: (groups or {}).get(word.lower()), NOW, ZONE)


def test_invite_args():
    found = args("A1 x5 3д +30d Петя  с семинара", {"a1": {"id": 7}})
    assert found == {"group": {"id": 7}, "uses": 5, "lifetime": dt.timedelta(days=3), "access_days": 30,
                     "access_until": None, "note": "Петя с семинара"}
    assert args("x* 12h")["uses"] == 0 and args("x* 12h")["lifetime"] == dt.timedelta(hours=12)
    assert args("х3")["uses"] == 3                     # a Cyrillic х
    assert args("+31.12")["access_until"] == dt.datetime(2026, 12, 31, 23, 59, tzinfo=ZONE)
    assert args("+1.09")["access_until"].year == 2027  # gone this year: the next one
    assert args("+1.9.27")["access_until"] == dt.datetime(2027, 9, 1, 23, 59, tzinfo=ZONE)
    assert args("Петя")["uses"] == 1 and args("")["note"] == ""
    for bad in ("x0", "x1001", "0д", "400d", "+0д", "+31.02", "+1.09.2025"):
        with pytest.raises(ValueError):
            args(bad)


async def test_a_code_for_two(bot, api, access):
    await bot.handle(message(OWNER, "/invite x2 Семинар"))
    reply = api.last(OWNER)["text"]
    assert "Код «Семинар»: вошли 0 из 2" in reply and "доступ бессрочный" in reply
    code = invite_code(reply)
    await bot.handle(message(PETR, f"/start {code}"))
    assert "вошёл по коду «Семинар» (1/2)" in api.last(OWNER)["text"]
    await bot.handle(message(OWNER, "/codes"))
    assert '<td align="right">1/2</td>' in api.last(OWNER)["text"]
    await bot.handle(message(ANNA, f"/start {code}", first="Анна"))
    assert access.user(ANNA) is not None and access.open_invites() == []
    await bot.handle(message(IRA, f"/start {code}", first="Ира"))
    assert access.user(IRA) is None and "нужен код" in api.last(IRA)["text"]


def test_a_code_for_anyone_until_revoked(access, clock):
    invite_id, code = access.invite(uses=0, lifetime=dt.timedelta(hours=12))
    for tg_id in range(3000, 3010):
        assert access.redeem(code, NewUser(tg_id, "Тест", None, "ru")) is not None
    assert access.open_invites()[0]["used"] == 10
    assert access.revoke(invite_id)
    assert access.redeem(code, NewUser(3999, "Тест", None, "ru")) is None
    _, short = access.invite(uses=0, lifetime=dt.timedelta(hours=12))
    clock.advance(hours=12, seconds=1)
    assert access.redeem(short, NewUser(3999, "Тест", None, "ru")) is None


async def test_invite_help(bot, api):
    await bot.handle(message(OWNER, "/invite help"))
    assert "в любом порядке" in api.last(OWNER)["text"]
    await bot.handle(message(OWNER, "/invite x0"))
    assert "в любом порядке" in api.last(OWNER)["text"]


async def test_access_for_a_while(bot, api, access, sheets, clock):
    await bot.handle(message(OWNER, "/invite +30д Петя"))
    assert "доступ на 30 дн." in api.last(OWNER)["text"]
    await bot.handle(message(PETR, f"/start {invite_code(api.last(OWNER)['text'])}"))
    assert "Доступ открыт до <b>09.12 15:00</b>" in api.last(PETR)["text"]
    assert "доступ до 09.12 15:00" in api.last(OWNER)["text"]
    await bot.handle(message(PETR, "/name Петров Пётр"))
    sheets.follow(PETR, "https://docs.google.com/spreadsheets/d/1CalcExampleSheetId_0123456789abc")
    assert len(sheets.to_read()) == 1

    await bot.check_access()
    owner_count = len(api.sent(OWNER))
    clock.advance(days=29, hours=1)
    await bot.check_access()
    await bot.check_access()                              # told once
    warning = api.last(PETR)
    assert "Доступ к боту закончится <b>09.12 15:00</b>" in warning["text"]
    assert len([m for m in api.sent(PETR) if "закончится" in m["text"]]) == 1

    clock.advance(days=1)
    await bot.check_access()
    await bot.check_access()
    ended = api.last(PETR)
    assert "Доступ к боту закончился" in ended["text"] and buttons(ended) == {"Попросить продлить": "pr"}
    assert len(api.sent(OWNER)) == owner_count            # the owner hears nothing of it by itself
    assert sheets.to_read() == [] and sheets.readers(sheets.followed(PETR)[0]["id"]) == []
    await bot.handle(message(OWNER, "/status"))
    assert "<td>доступ закончился</td><td align=\"right\">1</td>" in api.last(OWNER)["text"]

    await bot.handle(callback(PETR, "pr"))                 # the user asks: now the owner hears, once
    assert api.calls[-1][1]["text"] == "Отправлено владельцу"
    request = api.last(OWNER)
    assert "просит продлить доступ, он закончился 09.12 15:00" in request["text"]
    assert buttons(request) == {"+7 дн.": f"ax:{PETR}.7", "+30 дн.": f"ax:{PETR}.30", "+90 дн.": f"ax:{PETR}.90",
                                "Бессрочно": f"ax:{PETR}.0", "Удалить": f"du:{PETR}"}
    await bot.handle(callback(PETR, "pr"))
    assert api.calls[-1][1]["text"] == "Уже попросил" and api.last(OWNER) == request

    await bot.handle(message(PETR, "/grades"))
    assert "Доступ к боту закончился" in api.last(PETR)["text"]
    await bot.handle(callback(PETR, "grades"))
    assert api.calls[-1][1]["text"] == "Доступ закончился"
    await bot.handle(message(OWNER, "/users"))
    assert "09.12 15:00 (закончился)" in api.last(OWNER)["text"]

    await bot.handle(callback(OWNER, buttons(request)["+7 дн."]))  # from the end, or from now when it has ended
    assert access.user(PETR)["expires_at"] == "2026-12-16T13:00:00+00:00"
    assert "Владелец продлил доступ: <b>до 16.12 16:00</b>" in api.last(PETR)["text"]
    assert len(sheets.to_read()) == 1
    await bot.handle(message(OWNER, "/users"))
    await bot.handle(callback(OWNER, buttons(api.last(OWNER))["Доступ: Пётр"]))
    view = api.last(OWNER)
    assert "Доступ Пётр (@petr_example): <b>16.12 16:00</b>" in view["text"]
    assert access.user(PETR)["prolong_asked"] == 0           # prolonged: may ask again next time
    await bot.handle(callback(OWNER, buttons(view)["Бессрочно"]))
    assert access.user(PETR)["expires_at"] is None and "бессрочно" in api.last(PETR)["text"]
    await bot.handle(callback(PETR, "pr"))
    assert api.calls[-1][1]["text"] == "Доступ и так бессрочный"


def test_codes_used_before_count_as_used(tmp_path):
    from gradebot.store import Store

    path = tmp_path / "old.db"
    store = Store(path)
    store.db.execute("insert into invites(code_hash, created_at, expires_at, used_by) values('h', 't', 'u', 1)")
    store.db.commit()
    store.db.execute("alter table invites drop column used")
    store.close()
    store = Store(path)
    assert tuple(store.db.execute("select uses, used from invites").fetchone()) == (1, 1)
    store.close()
