"""A work the sheets of one subject repeat (the group's sheet and the lecturer's gradebook): shown, told and counted
once; and the layout that leaves out a gradebook's copies of works with tabs of their own."""

import pytest

from gradebot import layout, workbook
from gradebot.formula import parts
from gradebot.layout import Item, Planned
from gradebot.subjects import drop_copies, merge_copies

from .books import IVAN, SHEET_ID
from .books import workbook as book
from .test_subjects import BOB, a1, say  # noqa: F401


def grade(sheet, key, label, value, bonus=None, new=False) -> dict:
    return {"sheet": sheet, "key": key, "tab": key.split("|")[0], "label": label, "label_ru": label,
            "value": value, "bonus": bonus, "new": new}


def test_copies_are_the_same_label_and_grade_elsewhere():
    items = [grade(1, "ДЗ|B|1", "ДЗ 1", 9), grade(1, "ДЗ|C|2", "ДЗ 2", 8),
             grade(2, "262|D|x", "дз1", 9, new=True),     # the gradebook's copy: spaces and case aside
             grade(2, "262|E|y", "ДЗ 2", 7),              # another grade: both are shown
             grade(1, "ДЗ|F|z", "ДЗ 3", 5), grade(1, "ДЗ|G|w", "ДЗ 3", 5)]  # one tab: not copies
    kept, copies = drop_copies(items)
    assert [i["key"] for i in copies] == ["262|D|x"] and kept[0]["new"]       # the copy's «new» passes on
    assert [i["key"] for i in kept] == ["ДЗ|B|1", "ДЗ|C|2", "262|E|y", "ДЗ|F|z", "ДЗ|G|w"]
    kept, copies = drop_copies([grade(2, "262|D|x", "ДЗ 1", 9)], known=[grade(1, "ДЗ|B|1", "ДЗ 1", 9)])
    assert kept == [] and len(copies) == 1


def test_a_plan_has_each_label_once():
    plan = [{"key": "1:ДЗ|B|1", "sheet": 1, "tab": "ДЗ", "label": "HW 1", "label_ru": "ДЗ 1"},
            {"key": "2:262|D|x", "sheet": 2, "tab": "262", "label": "HW 1", "label_ru": "ДЗ 1"},
            {"key": "2:262|E|y", "sheet": 2, "tab": "262", "label": "Test 1", "label_ru": "КР 1"}]
    kept, aliases = merge_copies(plan)
    assert [w["key"] for w in kept] == ["1:ДЗ|B|1", "2:262|E|y"] and aliases == {"2:262|D|x": "1:ДЗ|B|1"}


@pytest.fixture
def matan(bot, sheets, a1):  # noqa: F811
    """«Матан» of the group's sheet (ДЗ 1, КР 1) and the gradebook (its tab «262» with ДЗ 1 again)."""
    group, shared = a1
    own, book_ = shared["Матанализ ДЗ"], shared["Матанализ колок"]
    subject = bot.subjects.create("Матан", grp=group["id"])
    bot.subjects.put(subject["id"], own)
    bot.subjects.put(subject["id"], book_)
    sheets.set_plan(own, [Planned("ДЗ|B|1", "ДЗ", "HW 1", "ДЗ 1", 10), Planned("ДЗ|C|2", "ДЗ", "HW 2", "ДЗ 2", 10),
                          Planned("КР|B|1", "КР", "Test 1", "КР 1", 10)])
    sheets.set_plan(book_, [Planned("262|D|x", "262", "HW 1", "ДЗ 1", 10)])
    return subject["id"], own, book_


def item(key, label, value):
    return Item(key, key.split("|")[0], key, label, value, value, 10, None, 0, label)


async def test_grades_and_news_show_a_copy_once(bot, api, matan, clock):
    sid, own, book_ = matan
    bot.grades.record(BOB, own, [item("ДЗ|B|1", "ДЗ 1", 9)], 1)
    bot.grades.record(BOB, book_, [], 0)
    clock.advance(minutes=30)
    bot.grades.record(BOB, book_, [item("262|D|x", "ДЗ 1", 9)], 1)     # the gradebook catches up a day later
    await bot.send_grades()
    assert not any("ДЗ 1" in m["text"] for m in api.sent(BOB))        # nothing new: a copy of a known grade
    assert bot.grades.waiting(BOB) == []

    await bot.handle(say(BOB, "/grades"))
    assert api.last(BOB)["text"].count("ДЗ 1") == 1 and "Матан · оценок: 1" in api.last(BOB)["text"]

    clock.advance(minutes=30)
    bot.grades.record(BOB, book_, [item("262|D|x", "ДЗ 1", 7)], 1)     # the gradebook says otherwise: both shown
    await bot.send_grades()
    assert "ДЗ 1" in api.last(BOB)["text"]
    await bot.handle(say(BOB, "/grades"))
    assert api.last(BOB)["text"].count("ДЗ 1") == 2


def test_a_formula_counts_a_copy_once(bot, matan):
    sid, own, book_ = matan
    bot.subjects.set_formula(sid, "…", {"nodes": {}, "final": {
        "type": "part", "name": "ДЗ", "agg": "mean", "match": [],
        "works": [{"key": f"{own}:ДЗ|B|1"}, {"key": f"{book_}:262|D|x"}, {"key": f"{own}:ДЗ|C|2"}]}})
    bot.grades.record(BOB, own, [], 0)
    bot.grades.record(BOB, book_, [item("262|D|x", "ДЗ 1", 9)], 1)   # graded only in the gradebook yet
    formula = bot.subject_formula_of(bot.access.user(BOB), sid)
    assert [w.key for w in formula.works("ДЗ")] == [f"{own}:ДЗ|B|1", f"{own}:ДЗ|C|2"]
    assert formula.value(5) == pytest.approx(7)                        # (9 + 5) / 2, not (9 + 9 + 5) / 3
    assert [p["name"] for p in parts(formula.tree)] == ["ДЗ"]


# --- the layout ------------------------------------------------------------------------------------


async def test_the_layout_sees_the_other_tabs_and_asks_again_when_they_change(layouts, llm):
    tabs = workbook.load(book())
    found, _ = workbook.find(tabs, IVAN)
    quizzes = next(row for row in found if row.tab.title == "Квизы")
    await layouts.get(SHEET_ID, quizzes.tab, [quizzes], 18, ["ДЗ-1А"])
    assert "Other tabs of the sheet: «ДЗ-1А»" in llm.prompts[-1]
    await layouts.get(SHEET_ID, quizzes.tab, [quizzes], 18, ["ДЗ-1А"])
    assert len(llm.prompts) == 1                                        # cached
    await layouts.get(SHEET_ID, quizzes.tab, [quizzes], 18, ["ДЗ-1А", "Коллоквиум 1"])
    assert len(llm.prompts) == 2 and "«Коллоквиум 1»" in llm.prompts[-1]
    assert "A column whose work has its own tab is not a graded work" in " ".join(layout.LAYOUT_SYSTEM.split())
