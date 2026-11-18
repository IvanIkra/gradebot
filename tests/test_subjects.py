"""Subjects: sheets of one course read as one section, a group's run by its admin and the owner, a user's own."""

import pytest

from gradebot.layout import Item
from gradebot.subjects import SubjectError

from .conftest import OWNER, buttons, callback, message
from .test_admins import come_in, say
from .test_sheets import ALG, CALC

ADA, BOB = 3001, 3002   # Ada runs A1, Bob is in it
COLLOQ = "https://docs.google.com/spreadsheets/d/1ColloqExampleSheetId_0123456789a/edit"


def item(key, label, value, position=0):
    return Item(key, "T", key, label, value, value, 10, None, position)


@pytest.fixture
async def a1(bot, api, access, sheets):
    """Group A1 with Ada its admin and Bob in it, both with full names, and three shared sheets: (group, sheets)."""
    group = bot.groups.create("A1")
    await come_in(bot, api, ADA, "A1 админ", "Ада")
    await come_in(bot, api, BOB, "A1", "Боб")
    access.set_student(ADA, "Адова Ада")
    access.set_student(BOB, "Бобов Боб")
    shared = {}
    for url, title in ((CALC, "Матанализ ДЗ"), (COLLOQ, "Матанализ колок"), (ALG, "Алгебра")):
        sheet, _ = bot.groups.add_sheet(group["id"], url, OWNER)
        sheets.set_title(sheet["id"], title)
        shared[title] = sheet["id"]
    return group, shared


def test_names_and_scopes(bot, a1):
    group, shared = a1
    subjects = bot.subjects
    calc = subjects.create("Матан", grp=group["id"])
    with pytest.raises(SubjectError):
        subjects.create("матан", grp=group["id"])          # the same name in the same scope
    with pytest.raises(SubjectError):
        subjects.create("Математический анализ", grp=group["id"])  # longer than a hashtag may be
    mine = subjects.create("Матан", tg_id=BOB)                # another scope: fine
    other = subjects.create("Анализ", grp=group["id"])
    subjects.put(calc["id"], shared["Матанализ ДЗ"])
    subjects.put(other["id"], shared["Матанализ ДЗ"])         # moves it within the group's subjects
    subjects.put(mine["id"], shared["Матанализ ДЗ"])          # a user's own scope is apart
    assert subjects.sheets_of(calc["id"]) == [] and subjects.sheets_of(other["id"]) == [shared["Матанализ ДЗ"]]
    assert subjects.sheets_of(mine["id"]) == [shared["Матанализ ДЗ"]]
    bot.groups.delete(group["id"])
    assert subjects.get(other["id"]) is None and subjects.get(mine["id"]) is not None


def test_units_own_subject_wins(bot, access, sheets, a1):
    group, shared = a1
    calc = bot.subjects.create("Матан", grp=group["id"])
    bot.subjects.put(calc["id"], shared["Матанализ ДЗ"])
    bot.subjects.put(calc["id"], shared["Матанализ колок"])
    bob = access.user(BOB)
    units = bot.subjects.units(bob, sheets.followed(BOB))
    assert [(u.name, [s["title"] for s in u.sheets]) for u in units] == [
        ("Матан", ["Матанализ ДЗ", "Матанализ колок"]), ("Алгебра", ["Алгебра"])]
    mine = bot.subjects.create("Колок", tg_id=BOB)
    bot.subjects.put(mine["id"], shared["Матанализ колок"])
    units = bot.subjects.units(bob, sheets.followed(BOB))
    assert [u.name for u in units] == ["Матан", "Колок", "Алгебра"]
    assert [u.name for u in bot.subjects.units(access.user(ADA), sheets.followed(ADA))] == ["Матан", "Алгебра"]


async def test_the_admin_joins_two_sheets_and_a_member_sees_one_section(bot, api, a1, clock):
    group, shared = a1
    await bot.handle(say(ADA, "/subjects"))
    await bot.handle(callback(ADA, buttons(api.last(ADA))["Новый предмет группы"]))
    await bot.handle(say(ADA, "Матан"))
    card = api.last(ADA)
    assert "Предмет Матан" in card["text"]
    await bot.handle(callback(ADA, buttons(card)["+ Матанализ ДЗ"]))
    await bot.handle(callback(ADA, buttons(api.last(ADA))["+ Матанализ колок"]))
    card = api.last(ADA)
    assert "− Матанализ ДЗ" in buttons(card) and "− Матанализ колок" in buttons(card)

    calc, colloq = shared["Матанализ ДЗ"], shared["Матанализ колок"]
    bot.grades.record(BOB, calc, [item("hw1", "ДЗ 1", 7)], 1)
    bot.grades.record(BOB, colloq, [], 0)
    clock.advance(minutes=30)
    bot.grades.record(BOB, colloq, [item("c1", "Колок", 9)], 1)
    await bot.send_grades()
    news = api.last(BOB)["text"]
    assert news.count("<h3>") == 1 and "#Матан" in news and "Колок" in news and "Матанализ ДЗ</a>" in news

    await bot.handle(say(BOB, "/grades"))
    view = api.last(BOB)["text"]
    assert "Матан · оценок: 2 · новых: 1" in view and "Алгебра · оценок: 0" in view
    assert {"Сообщить об ошибке: Матанализ ДЗ", "Сообщить об ошибке: Матанализ колок"} <= set(buttons(api.last(BOB)))


async def test_a_member_cannot_run_the_groups_subjects_but_has_own(bot, api, a1):
    group, shared = a1
    bot.subjects.create("Матан", grp=group["id"])
    await bot.handle(say(BOB, "/subjects"))
    view = api.last(BOB)
    assert "Группа A1" in view["text"] and "Матан" in view["text"]
    assert f"sj:n.g{group['id']}" not in buttons(view).values()
    await bot.handle(callback(BOB, f"sj:n.g{group['id']}"))
    assert BOB not in bot.asks                                   # no question asked

    await bot.handle(callback(BOB, buttons(view)["Новый свой предмет"]))
    await bot.handle(say(BOB, "Мой"))
    mine = bot.subjects.scope(tg_id=BOB)[0]
    await bot.handle(callback(BOB, f"sj:p.{mine['id']}.{shared['Алгебра']}"))
    assert bot.subjects.sheets_of(mine["id"]) == [shared["Алгебра"]]
    other, _ = bot.sheets.follow(ADA, "https://docs.google.com/spreadsheets/d/1OtherExampleSheetId_0123456789ab/e")
    await bot.handle(callback(BOB, f"sj:p.{mine['id']}.{other['id']}"))   # not a sheet Bob follows
    assert bot.subjects.sheets_of(mine["id"]) == [shared["Алгебра"]]


async def test_the_owner_runs_a_groups_subjects(bot, api, a1):
    group, shared = a1
    await bot.handle(callback(OWNER, "sj:h"))
    assert buttons(api.last(OWNER))["A1"] == f"sj:l.g{group['id']}"
    await bot.handle(callback(OWNER, f"sj:n.g{group['id']}"))
    await bot.handle(message(OWNER, "Алг"))
    [subject] = bot.subjects.scope(grp=group["id"])
    await bot.handle(callback(OWNER, f"sj:p.{subject['id']}.{shared['Алгебра']}"))
    await bot.handle(callback(OWNER, f"sj:r.{subject['id']}"))
    await bot.handle(message(OWNER, "Алгебра"))
    assert bot.subjects.get(subject["id"])["name"] == "Алгебра"
    await bot.handle(callback(OWNER, f"sj:d.{subject['id']}"))
    await bot.handle(callback(OWNER, buttons(api.last(OWNER))["Да, удалить"]))
    assert bot.subjects.get(subject["id"]) is None
