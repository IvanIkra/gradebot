"""/formula: Haiku's flat answer as a tree, the check before keeping it, the group's formula and one's own."""

import pytest

from gradebot.formula import FormulaError
from gradebot.formulas import to_tree
from gradebot.layout import Planned

from .conftest import OWNER, buttons, callback, message
from .test_subjects import ADA, BOB, a1, say  # noqa: F401

PLAN = [Planned("T|B|1", "ДЗ", "HW 1", "ДЗ 1", 10), Planned("T|C|2", "ДЗ", "HW 2", "ДЗ 2", 10),
        Planned("K|B|1", "КР", "Test 1", "КР 1", 10)]
PLAN_ROWS = [{"key": w.key, "tab": w.tab, "label": w.label, "label_ru": w.label_ru, "max": w.max} for w in PLAN]


def node(nid, kind, name="", **fields) -> dict:
    """A node as the schema has it: every field, the unused ones empty."""
    return {"id": nid, "type": kind, "name": name, "agg": "mean", "k": None, "count": None, "works": [], "match": [],
            "terms": [], "mode": "half_up", "digits": 0, "args": [], "value": None, "cond": None, "then": "",
            "else": "", **fields}


ANSWER = {"found": True, "reason": "", "final": "f", "nodes": [
    node("hw", "part", "ДЗ", agg="best", k=1, works=[{"work": "w1", "weight": None}, {"work": "w2", "weight": None}],
         match=["ДЗ"]),
    node("kr", "part", "КР", works=[{"work": "w3", "weight": None}], match=["КР"]),
    node("ex", "part", "Экз", match=["Экз"]),
    node("acc", "sum", "Накоп", terms=[{"weight": 0.5, "node": "hw", "bonus": False},
                                       {"weight": 0.5, "node": "kr", "bonus": False}]),
    node("mix", "sum", terms=[{"weight": 0.6, "node": "acc", "bonus": False},
                              {"weight": 0.4, "node": "ex", "bonus": False}]),
    node("r", "round", args=["mix"]),
    node("f", "if", "Итог", cond={"node": "ex", "op": "<", "value": 4}, then="ex", **{"else": "r"}),
]}


class FormulaLLM:
    available = True

    def __init__(self, answer=ANSWER):
        self.answer, self.prompts = answer, []

    async def json(self, task, system, prompt, schema, *, max_tokens):
        assert task == "formula"
        self.prompts.append(prompt)
        return self.answer


def test_the_flat_answer_becomes_a_tree():
    tree = to_tree(ANSWER, PLAN_ROWS)
    assert set(tree["nodes"]) == {"Накоп"} and tree["final"]["type"] == "if"
    part = tree["nodes"]["Накоп"]["terms"][0]["node"]
    assert part["agg"] == "best" and [w["key"] for w in part["works"]] == ["T|B|1", "T|C|2"]


@pytest.mark.parametrize("answer", [
    {**ANSWER, "found": False, "reason": "this is a timetable"},
    {**ANSWER, "final": "nope"},
    {**ANSWER, "nodes": [node("a", "sum", "A", terms=[{"weight": 1, "node": "b", "bonus": False}]),
                         node("b", "sum", "B", terms=[{"weight": 1, "node": "a", "bonus": False}])], "final": "a"},
])
def test_answers_that_are_no_formula(answer):
    with pytest.raises(FormulaError):
        to_tree(answer, PLAN_ROWS)


@pytest.fixture
def matan(bot, sheets, a1):  # noqa: F811
    """The group's subject «Матан» of two sheets, laid out: its id."""
    group, shared = a1
    subject = bot.subjects.create("Матан", grp=group["id"])
    bot.subjects.put(subject["id"], shared["Матанализ ДЗ"])
    bot.subjects.put(subject["id"], shared["Матанализ колок"])
    sheets.set_plan(shared["Матанализ ДЗ"], PLAN[:2])
    sheets.set_plan(shared["Матанализ колок"], PLAN[2:])
    bot.llm = FormulaLLM()
    return subject["id"]


async def test_the_admin_sets_the_groups_formula(bot, api, matan):
    await bot.handle(say(ADA, "/formula"))
    await bot.handle(callback(ADA, buttons(api.last(ADA))["Матан"]))
    view = api.last(ADA)
    assert "Формулы пока нет" in view["text"]
    await bot.handle(callback(ADA, buttons(view)["Задать для группы"]))
    await bot.handle(say(ADA, "Итог = округл(0.6·Накоп + 0.4·Экз), экзамен ниже 4 — итог равен экзамену"))
    assert "w1: «ДЗ 1» (HW 1), tab «ДЗ», sheet «Матанализ ДЗ», max 10" in bot.llm.prompts[0]
    draft = api.last(ADA)["text"]
    assert "Проверь формулу · Матан" in draft and "Накоп = 0.5·ДЗ (лучшие 1) + 0.5·КР" in draft
    assert "Итог = если Экз &lt; 4, то Экз, иначе округл(0.6·Накоп + 0.4·Экз)" in draft
    assert "<td><b>ДЗ</b></td><td>ДЗ 1, ДЗ 2</td>" in draft and "в таблицах пока нет работ" in draft
    assert "для всей группы" in draft
    await bot.handle(callback(ADA, buttons(api.last(ADA))["Верно"]))
    assert bot.subjects.subject_formula(matan)["tree"]["final"]["type"] == "if"

    await bot.handle(say(BOB, "/formula"))
    await bot.handle(callback(BOB, buttons(api.last(BOB))["Матан"]))
    view = api.last(BOB)
    assert "Формула предмета группы" in view["text"] and "Задать для группы" not in buttons(view)
    await bot.handle(callback(BOB, f"fm:e.{matan}.s"))     # not Bob's to set
    assert BOB not in bot.asks


async def test_a_member_keeps_an_own_formula(bot, api, matan):
    await bot.handle(callback(BOB, f"fm:s.{matan}"))
    await bot.handle(callback(BOB, buttons(api.last(BOB))["Своя формула"]))
    await bot.handle(say(BOB, "Итог = 0.5·ДЗ + 0.5·КР"))
    assert "только для тебя" in api.last(BOB)["text"]
    await bot.handle(callback(BOB, "fm:ok"))
    assert bot.subjects.own_formula(BOB, matan) is not None and bot.subjects.subject_formula(matan) is None
    assert bot.subjects.formula_for(BOB, matan)["own"] and bot.subjects.formula_for(ADA, matan) is None
    assert "Твоя формула" in api.last(BOB)["text"]
    await bot.handle(callback(BOB, buttons(api.last(BOB))["Сбросить свою"]))
    assert bot.subjects.own_formula(BOB, matan) is None


async def test_a_lone_sheet_becomes_a_subject_of_ones_own(bot, api, matan, a1):  # noqa: F811
    _, shared = a1
    await bot.handle(say(BOB, "/formula"))
    await bot.handle(callback(BOB, buttons(api.last(BOB))["Алгебра"]))
    [own] = bot.subjects.scope(tg_id=BOB)
    assert bot.subjects.sheets_of(own["id"]) == [shared["Алгебра"]] and "Формула · " in api.last(BOB)["text"]


async def test_what_cannot_be_read(bot, api, matan):
    bot.llm = FormulaLLM({**ANSWER, "found": False, "reason": "no formula here"})
    await bot.handle(callback(ADA, f"fm:e.{matan}.s"))
    await bot.handle(say(ADA, "привет"))
    assert "Не получилось разобрать формулу: no formula here" in api.last(ADA)["text"]
    await bot.handle(callback(ADA, "fm:ok"))
    assert bot.subjects.subject_formula(matan) is None
    bot.llm = None
    await bot.handle(callback(ADA, f"fm:e.{matan}.s"))
    await bot.handle(say(ADA, "Итог = ДЗ"))
    assert "Разбор формул выключен" in api.last(ADA)["text"]


async def test_the_owner_sets_it_from_the_subjects_card(bot, api, matan):
    await bot.handle(callback(OWNER, f"sj:o.{matan}"))
    await bot.handle(callback(OWNER, buttons(api.last(OWNER))["Формула"]))
    await bot.handle(callback(OWNER, buttons(api.last(OWNER))["Задать для группы"]))
    await bot.handle(message(OWNER, "Итог = 0.5·ДЗ + 0.5·КР"))
    await bot.handle(callback(OWNER, "fm:ok"))
    assert bot.subjects.subject_formula(matan) is not None
