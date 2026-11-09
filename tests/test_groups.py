"""Groups: each with its shared sheets, picked at registration or carried by the code, changed with /group."""

import sqlite3

import pytest

from gradebot.access import NewUser
from gradebot.groups import GroupError, Groups
from gradebot.store import FIRST_GROUP, Store

from .conftest import OWNER, buttons, callback, invite_code, message
from .test_sheets import ALG, CALC

PETR, ANNA, IRA = 2001, 2002, 2003
GEO = "https://docs.google.com/spreadsheets/d/1GeometryExampleSheetId_01234567"


@pytest.fixture
def groups(store, sheets, clock) -> Groups:
    return Groups(store, sheets, clock)


def user(access, tg_id: int, first: str = "Тест") -> None:
    _, code = access.invite()
    access.redeem(code, NewUser(tg_id, first, None, "ru"))


def test_names(groups):
    a1 = groups.create("A1")
    assert groups.find("a1")["id"] == a1["id"]
    for bad in ("", "two words", "x" * 21, "/a", "https://docs"):
        with pytest.raises(GroupError):
            groups.create(bad)
    with pytest.raises(GroupError):
        groups.create("a1")
    b2 = groups.create("B2")
    with pytest.raises(GroupError):
        groups.rename(b2["id"], "A1")
    assert groups.rename(b2["id"], "B3")["name"] == "B3"


def test_each_group_has_its_sheets(groups, sheets, access):
    for tg_id in (PETR, ANNA, IRA):
        user(access, tg_id)
    a1, b2 = groups.create("A1")["id"], groups.create("B2")["id"]
    groups.join(PETR, a1)
    groups.join(ANNA, b2)
    calc, added = groups.add_sheet(a1, CALC, OWNER, "Матан")
    assert added == 1 and calc["course"] == "Матан"
    alg, _ = groups.add_sheet(b2, ALG, OWNER)
    groups.add_sheet(b2, CALC, OWNER)           # a sheet in two groups
    assert [s["id"] for s in sheets.followed(PETR)] == [calc["id"]]
    assert sorted(s["id"] for s in sheets.followed(ANNA)) == sorted([calc["id"], alg["id"]])
    assert sheets.followed(IRA) == []
    assert [(g["name"], g["users"], g["sheets"]) for g in groups.all()] == [("A1", 1, 1), ("B2", 1, 2)]

    # Moving: the old group's sheets go, except those the new group has too; an own sheet stays.
    own, _ = sheets.follow(PETR, GEO)
    left, new = groups.join(PETR, b2)
    assert (left, new) == (0, [alg["id"]])     # Calc is in both groups: kept
    assert sorted(s["id"] for s in sheets.followed(PETR)) == sorted([calc["id"], alg["id"], own["id"]])
    left, new = groups.join(PETR, a1)
    assert (left, new) == (1, []) and alg["id"] not in {s["id"] for s in sheets.followed(PETR)}
    assert groups.join(PETR, a1) == (0, [])    # the same group: nothing changes
    left, _ = groups.join(PETR, None)
    assert left == 1 and [s["id"] for s in sheets.followed(PETR)] == [own["id"]]
    with pytest.raises(GroupError):
        groups.join(PETR, 999)

    # A sheet taken from a group stays with its followers; deleting a group keeps its users' sheets.
    assert groups.remove_sheet(b2, alg["id"])
    assert sheets.get(alg["id"]) is not None and sheets.followers(alg["id"]) == [ANNA]
    assert groups.delete(b2)
    assert access.user(ANNA)["grp"] is None and len(sheets.followed(ANNA)) == 2
    sheets.unfollow(ANNA, alg["id"])
    assert sheets.get(alg["id"]) is None        # in no group, nobody follows it


async def test_a_code_with_a_group(bot, api, access, sheets):
    gid = bot.groups.create("A1")["id"]
    bot.groups.add_sheet(gid, CALC, OWNER, "Матан")
    await bot.handle(message(OWNER, "/invite a1 Петя"))
    reply = api.last(OWNER)["text"]
    assert "Код «Петя» в группу <b>A1</b>" in reply
    await bot.handle(message(OWNER, "/codes"))
    assert "<td>A1</td>" in api.last(OWNER)["text"]
    await bot.handle(message(PETR, f"/start {invite_code(reply)}"))
    welcome = api.last(PETR)
    assert "Твоя группа: <b>A1</b>" in welcome["text"] and "общие таблицы: 1" in welcome["text"]
    assert not any(data.startswith("gj:") for data in buttons(welcome).values())
    assert "вошёл по коду «Петя», группа A1" in api.last(OWNER)["text"]
    assert access.user(PETR)["grp"] == gid and len(sheets.followed(PETR)) == 1


async def test_a_code_without_a_group_asks_for_one(bot, api, access, sheets):
    a1, b2 = bot.groups.create("A1")["id"], bot.groups.create("B2")["id"]
    bot.groups.add_sheet(b2, ALG, OWNER)
    await bot.handle(message(OWNER, "/invite Петя"))
    assert "в группу" not in api.last(OWNER)["text"]
    await bot.handle(message(PETR, f"/start {invite_code(api.last(OWNER)['text'])}"))
    welcome = api.last(PETR)
    assert "В какой ты группе?" in welcome["text"]
    assert buttons(welcome) == {"A1": f"gj:{a1}", "B2": f"gj:{b2}", "Без группы": "gj:0"}
    await bot.handle(callback(PETR, f"gj:{b2}"))
    edited = api.last(PETR)
    assert edited["method"] == "editMessageText" and "Твоя группа: <b>B2</b>" in edited["text"]
    assert "/name" in edited["text"] and access.user(PETR)["grp"] == b2
    assert "Пётр (@petr_example) теперь в группе <b>B2</b>" in api.last(OWNER)["text"]
    assert [s["url"] for s in sheets.followed(PETR)] == [sheets.by_url(ALG)["url"]]


async def test_no_groups_no_question(bot, api):
    await bot.handle(message(OWNER, "/invite"))
    await bot.handle(message(PETR, f"/start {invite_code(api.last(OWNER)['text'])}"))
    assert "группе" not in api.last(PETR)["text"] and buttons(api.last(PETR)) == {}


async def test_group_command(bot, api, access, sheets):
    a1, b2 = bot.groups.create("A1")["id"], bot.groups.create("B2")["id"]
    bot.groups.add_sheet(a1, CALC, OWNER)
    user(access, PETR, "Пётр")
    await bot.handle(message(PETR, "/group"))
    view = api.last(PETR)
    assert "Твоя группа: не выбрана" in view["text"] and buttons(view) == {"A1": f"gp:{a1}", "B2": f"gp:{b2}"}
    await bot.handle(callback(PETR, f"gp:{a1}"))
    view = api.last(PETR)
    assert "Твоя группа: <b>A1</b>" in view["text"] and "подписал на: 1" in view["text"]
    assert buttons(view) == {"B2": f"gp:{b2}", "Без группы": "gp:0"}
    await bot.handle(callback(PETR, "gp:0"))
    assert access.user(PETR)["grp"] is None and sheets.followed(PETR) == []
    assert "left the group <b>A1</b>" in api.last(OWNER)["text"]  # no word from the owner yet: English
    await bot.handle(callback(PETR, "gp:999"))
    assert api.calls[-1] == ("answerCallbackQuery", {"callback_query_id": "cb1"})


async def test_the_owner_manages_groups(bot, api, access, sheets):
    await bot.handle(message(OWNER, "/groups"))
    assert "Групп пока нет" in api.last(OWNER)["text"]
    await bot.handle(message(OWNER, "/groups A1"))
    assert "Группа <b>A1</b> создана" in api.last(OWNER)["text"]
    await bot.handle(message(OWNER, "/groups a1"))
    assert "Такая группа уже есть" in api.last(OWNER)["text"]
    await bot.handle(message(OWNER, "/groups A1 A2"))
    assert "Переименована: <b>A2</b>" in api.last(OWNER)["text"]
    await bot.handle(message(OWNER, "/groups Z9 Z8"))
    assert "Такой группы нет" in api.last(OWNER)["text"]

    user(access, PETR, "Пётр")
    gid = bot.groups.find("A2")["id"]
    bot.groups.join(PETR, gid)
    await bot.handle(message(OWNER, f"/groupsheet {CALC} Матан"))   # one group: it may be left out
    assert "Общая таблица <b>Матан</b> группы <b>A2</b>. Подписано пользователей: 1." in api.last(OWNER)["text"]
    await bot.handle(message(OWNER, "/groups B1"))
    await bot.handle(message(OWNER, f"/groupsheet {ALG}"))
    assert "Какой группе?" in api.last(OWNER)["text"] and "A2, B1" in api.last(OWNER)["text"]
    await bot.handle(message(OWNER, "/groupsheet B1 not-a-link"))
    assert "не ссылка" in api.last(OWNER)["text"]
    await bot.handle(message(OWNER, "/users"))
    assert "<td>A2</td>" in api.last(OWNER)["text"]

    await bot.handle(message(OWNER, "/groupsheet"))
    listing = api.last(OWNER)
    assert "Группа A2 · 1" in listing["text"] and "Группа B1 · 0" in listing["text"]
    await bot.handle(callback(OWNER, buttons(listing)["Убрать из общих: Матан"]))
    assert bot.groups.sheets_of(gid) == [] and len(sheets.followed(PETR)) == 1

    await bot.handle(message(OWNER, "/groups"))
    view = api.last(OWNER)
    await bot.handle(callback(OWNER, buttons(view)["Таблицы: A2"]))
    assert "Общих таблиц пока нет" in api.last(OWNER)["text"]
    await bot.handle(callback(OWNER, buttons(view)["Удалить A2"]))
    confirm = api.last(OWNER)
    assert "Удалить группу <b>A2</b>?" in confirm["text"]
    await bot.handle(callback(OWNER, buttons(confirm)["Да, удалить"]))
    assert bot.groups.find("A2") is None and access.user(PETR)["grp"] is None


def test_shared_sheets_from_before_groups_become_one_group(tmp_path):
    path = tmp_path / "old.db"
    store = Store(path)
    for tg_id in (1, 2):
        store.db.execute("insert into users(tg_id, tg_name, lang, joined_at) values(?, 'Тест', 'ru', 't')", (tg_id,))
    for sid in ("a" * 20, "b" * 20):
        store.db.execute("insert into sheets(sheet_id, url, added_by, added_at) values(?, 'u', 1, 't')", (sid,))
    store.db.commit()
    store.close()
    old = sqlite3.connect(path)
    old.executescript("alter table sheets add column group_sheet integer not null default 0; "
                      "update sheets set group_sheet = 1 where sheet_id like 'a%';")
    old.close()
    store = Store(path)
    [group] = store.db.execute("select * from groups").fetchall()
    assert group["name"] == FIRST_GROUP
    assert [row[0] for row in store.db.execute("select sheet from group_sheets")] == [1]
    assert [row[0] for row in store.db.execute("select grp from users")] == [group["id"]] * 2
    assert "group_sheet" not in {row["name"] for row in store.db.execute("pragma table_info(sheets)")}
    store.close()
    Store(path).close()
