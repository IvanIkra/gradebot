"""/goal and the outlook line: in words, in /grades, and under new grades only with a goal."""

import pytest

from gradebot.formula import Outlook
from gradebot.formulas import to_tree
from gradebot.goals import outlook_text
from gradebot.layout import Item

from .conftest import buttons, callback
from .test_formulas import ANSWER, a1, matan  # noqa: F401
from .test_subjects import BOB, say


@pytest.mark.parametrize(("view", "text"), [
    (Outlook(7, 7, 7, [], 8), "Итог: 7 · цель 8 не набрана"),
    (Outlook(9, 9, 9, ["Экз"], 8), "Итог уже не зависит от оставшегося: 9 · цель 8 набрана"),
    (Outlook(2, 10, 8, ["ДЗ", "Экз"], 8, 7.4),
     "Итог будет от 2 до 10 · прогноз 8 · до 8 нужно в среднем 7.4 на оставшемся"),
    (Outlook(3, 9, None, ["Экз"], 6, 5.75), "Итог будет от 3 до 9 · до 6 нужно 5.8 за «Экз»"),
    (Outlook(6, 9, 8, ["Экз"], 6), "Итог будет от 6 до 9 · прогноз 8 · цель 6 уже гарантирована"),
    (Outlook(2, 7, 5, ["Экз"], 8), "Итог будет от 2 до 7 · прогноз 5 · цель 8 недостижима"),
    (Outlook(2, 7, 5, ["Экз"]), "Итог будет от 2 до 7 · прогноз 5"),
])
def test_in_words(view, text):
    assert outlook_text(view, "ru") == text


def item(key, tab, label, value):
    return Item(key, tab, key, label, value, value, 10, None, 0, label)


@pytest.fixture
def graded(bot, matan, a1):  # noqa: F811
    """The group's formula for Матан, Bob's homework 8 and test 6: (subject, the two sheets)."""
    _, shared = a1
    bot.subjects.set_formula(matan, "…", to_tree(ANSWER, bot.subjects.plan(matan)))
    calc, colloq = shared["Матанализ ДЗ"], shared["Матанализ колок"]
    bot.grades.record(BOB, calc, [item("T|B|1", "ДЗ", "ДЗ 1", 8)], 1)
    bot.grades.record(BOB, colloq, [item("K|B|1", "КР", "КР 1", 6)], 1)
    return matan, calc, colloq


async def test_a_goal_and_what_it_needs(bot, api, graded):
    sid, _, _ = graded
    await bot.handle(say(BOB, "/goal"))
    await bot.handle(callback(BOB, buttons(api.last(BOB))["Матан"]))
    view = api.last(BOB)
    assert "Цели пока нет" in view["text"] and "Итог будет от 0 до 9 · прогноз 7" in view["text"]
    await bot.handle(callback(BOB, buttons(view)["8"]))
    view = api.last(BOB)
    assert bot.subjects.goal(BOB, sid) == 8 and "Твоя цель: <b>8</b>" in view["text"]
    # ДЗ: the best of 8 and x; КР 6; the exam x: round(0.6·(0.5·x + 3) + 0.4·x) reaches 8 from 8.2
    assert "до 8 нужно в среднем 8.2 на оставшемся" in view["text"]

    await bot.handle(say(BOB, "/grades"))
    grades = api.last(BOB)
    assert "Итог будет от 0 до 9 · прогноз 7 · до 8 нужно в среднем 8.2" in grades["text"]
    assert buttons(grades)["Цель"] == f"gl:s.{sid}" and buttons(grades)["Формула"] == f"fm:s.{sid}"

    await bot.handle(callback(BOB, buttons(view)["Без цели"]))
    assert bot.subjects.goal(BOB, sid) is None


async def test_new_grades_carry_the_line_only_with_a_goal(bot, api, graded, clock):
    sid, calc, _ = graded
    clock.advance(minutes=30)
    bot.grades.record(BOB, calc, [item("T|B|1", "ДЗ", "ДЗ 1", 9)], 1)
    await bot.send_grades()
    assert "Итог будет" not in api.last(BOB)["text"]
    bot.subjects.set_goal(BOB, sid, 7)
    clock.advance(minutes=30)
    bot.grades.record(BOB, calc, [item("T|B|1", "ДЗ", "ДЗ 1", 10)], 1)
    await bot.send_grades()
    assert "до 7 нужно" in api.last(BOB)["text"]


async def test_a_goal_without_a_formula_asks_for_one(bot, api, matan):  # noqa: F811
    bot.subjects.set_goal(BOB, matan, 8)
    await bot.handle(callback(BOB, f"gl:s.{matan}"))
    assert "чтобы её считать, предмету нужна формула: /formula" in api.last(BOB)["text"]
