"""The formula tree: every case of spec §13.2, checked, evaluated and told in words; the outlook of a goal."""

import pytest

from gradebot.formula import Formula, FormulaError, check, describe, outlook, rounded, warnings


def plan(*keys: str, maximum: float = 10) -> list[dict]:
    return [{"key": key, "label": key, "label_ru": key.upper(), "max": maximum} for key in keys]


def graded(**values) -> dict[str, dict]:
    return {key: {"value": value, "bonus": None, "max": 10} for key, value in values.items()}


def part(name: str, *keys: str, agg: str = "mean", **more) -> dict:
    return {"type": "part", "name": name, "agg": agg, "works": [{"key": k} for k in keys], **more}


def total(*terms, bonus=()) -> dict:
    return {"type": "sum", "terms": [{"weight": w, "node": n} for w, n in terms] +
            [{"weight": w, "node": n, "bonus": True} for w, n in bonus]}


def ref(name: str) -> dict:
    return {"type": "ref", "name": name}


def rnd(node: dict, mode: str = "half_up", digits: int = 0) -> dict:
    return {"type": "round", "mode": mode, "digits": digits, "node": node}


def when(node: dict, op: str, value: float, then: dict, else_: dict) -> dict:
    return {"type": "if", "cond": {"node": node, "op": op, "value": value}, "then": then, "else": else_}


def formula(final: dict, works: list[dict], grades: dict, **named) -> Formula:
    return Formula(check({"nodes": named, "final": final}), works, grades)


# --- the cases of the spec ---------------------------------------------------------------------------


def test_weights_and_means():
    f = formula(total((0.3, part("ДЗ", "h1", "h2")), (0.7, part("Экз", "ex"))), plan("h1", "h2", "ex"),
                graded(h1=6, h2=8, ex=9))
    assert f.value(0) == pytest.approx(0.3 * 7 + 0.7 * 9) and f.open_parts() == []


def test_nested_named_nodes():
    f = formula(total((0.6, ref("Накоп")), (0.4, part("Экз", "ex"))), plan("h1", "t1", "ex"),
                graded(h1=8, t1=6, ex=5), Накоп=total((0.5, part("ДЗ", "h1")), (0.5, part("КР", "t1"))))
    assert f.value(0) == pytest.approx(0.6 * 7 + 0.4 * 5)


def test_best_k_of_n_with_works_not_graded_yet():
    f = formula(part("ДЗ", "h1", "h2", "h3", agg="best", k=2), plan("h1", "h2", "h3"), graded(h1=4, h2=9))
    assert f.value(0) == pytest.approx(6.5)     # 9 and 4: the third one at 0 is the worst
    assert f.value(10) == pytest.approx(9.5)    # 10 and 9
    assert f.open_parts() == ["ДЗ"]


def test_own_weights_of_works_and_points():
    weighted = {"type": "part", "name": "КР", "agg": "weighted",
                "works": [{"key": "t1", "weight": 0.4}, {"key": "t2", "weight": 0.6}]}
    assert formula(weighted, plan("t1", "t2"), graded(t1=5, t2=10)).value(0) == pytest.approx(8)
    works = plan("a") + [{"key": "b", "label": "b", "label_ru": "B", "max": 30}]
    by_points = formula(part("Баллы", "a", "b", agg="points"), works, graded(a=10, b=5))
    assert by_points.value(0) == pytest.approx((10 * 10 + 5 * 30) / 40)


@pytest.mark.parametrize(("value", "mode", "digits", "expected"), [
    (7.5, "half_up", 0, 8), (6.5, "half_up", 0, 7), (7.49, "half_up", 0, 7), (7.9999999999, "floor", 0, 8),
    (7.01, "ceil", 0, 8), (7.0000000001, "ceil", 0, 7), (7.25, "half_up", 1, 7.3), (7.99, "floor", 1, 7.9)])
def test_rounding(value, mode, digits, expected):
    assert rounded(value, mode, digits) == pytest.approx(expected)


def blocking_exam() -> dict:
    """Итог = экзамен, если он ниже 4, иначе round(0.6·Накоп + 0.4·Экз)."""
    exam = part("Экз", "ex")
    return when(exam, "<", 4, exam, rnd(total((0.6, ref("Накоп")), (0.4, exam))))


def test_a_blocking_exam():
    named = {"Накоп": part("ДЗ", "h1")}
    assert formula(blocking_exam(), plan("h1", "ex"), graded(h1=9, ex=3.5), **named).value(0) == 3.5
    assert formula(blocking_exam(), plan("h1", "ex"), graded(h1=9, ex=4), **named).value(0) == 7   # 5.4 + 1.6


def test_an_exemption():
    """Накоп ≥ 8: the exam may be skipped, the final grade is the accumulated one."""
    exemption = when(ref("Накоп"), ">=", 8, rnd(ref("Накоп")), rnd(total((0.5, ref("Накоп")), (0.5, part("Экз")))))
    f = formula(exemption, plan("h1"), graded(h1=8.5), Накоп=part("ДЗ", "h1"))
    view = outlook(f, goal=9)
    assert view.settled and view.low == 9 and view.reached and view.open == ["Экз"]


def test_a_bonus_on_top_and_a_cap():
    capped = {"type": "min", "nodes": [{"type": "const", "value": 10},
                                       total((1, part("Работы", "w")), bonus=[(0.1, part("Активность", "act"))])]}
    assert formula(capped, plan("w", "act"), graded(w=9, act=8)).value(0) == pytest.approx(9.8)
    assert formula(capped, plan("w", "act"), graded(w=10, act=10)).value(0) == 10


def test_the_better_of_two_ways():
    exam, acc = part("Экз", "ex"), part("Накоп", "h1")
    better = {"type": "max", "nodes": [exam, total((0.5, exam), (0.5, acc))]}
    assert formula(better, plan("ex", "h1"), graded(ex=6, h1=10)).value(0) == 8
    assert formula(better, plan("ex", "h1"), graded(ex=9, h1=5)).value(0) == 9


def test_a_bonus_column_adds_to_its_work_up_to_10():
    grades = {"h1": {"value": 9, "bonus": 2, "max": 10}, "h2": {"value": 6, "bonus": 1, "max": 10}}
    assert formula(part("ДЗ", "h1", "h2"), plan("h1", "h2"), grades).value(0) == pytest.approx(8.5)


def test_a_part_with_no_works_yet_and_a_count_from_the_formula():
    f = formula(total((0.5, part("ДЗ", "h1", count=4)), (0.5, part("Экз"))), plan("h1"), graded(h1=8))
    assert len(f.works("ДЗ")) == 4 and len(f.works("Экз")) == 1
    assert f.value(10) == pytest.approx(0.5 * (8 + 30) / 4 + 5)


def test_works_the_sheet_no_longer_has_are_left_out():
    f = formula(part("ДЗ", "h1", "gone"), plan("h1"), graded(h1=7))
    assert [w.key for w in f.works("ДЗ")] == ["h1"] and f.value(0) == 7


# --- the goal ----------------------------------------------------------------------------------------


def test_the_need_range_and_forecast():
    f = formula(rnd(total((0.3, part("ДЗ", "h1", "h2", "h3")), (0.7, part("Экз", "ex")))),
                plan("h1", "h2", "h3", "ex"), graded(h1=7, h2=9))
    view = outlook(f, goal=8)
    assert (view.low, view.high) == (2, 10)                 # round(0.3·16/3), round(0.3·26/3 + 7)
    assert view.need == pytest.approx(7.4)                  # 1.6 + 0.8·7.4 = 7.52 → 8; 7.3 gives 7.44 → 7
    assert view.forecast == round(0.3 * 8 + 0.7 * 8)        # the left homework at 8, the exam at the mean too
    assert not view.reached and not view.out_of_reach and view.open == ["ДЗ", "Экз"]


def test_reached_and_out_of_reach():
    f = formula(total((0.5, part("A", "a")), (0.5, part("B", "b"))), plan("a", "b"), graded(a=2))
    assert outlook(f, goal=7).out_of_reach and outlook(f, goal=7).need is None
    assert outlook(f, goal=1).reached and outlook(f, goal=4).need == pytest.approx(6)


def test_need_over_a_blocking_exam_that_makes_the_grade_go_down():
    """Накоп 2: an exam of 3.9 is a final 3.9, an exam of 4 is round(1.2 + 1.6) = 3. From 5.8 it is 4."""
    f = formula(blocking_exam(), plan("h1", "ex"), graded(h1=2), Накоп=part("ДЗ", "h1"))
    assert f.value(3.9) == pytest.approx(3.9) and f.value(4) == 3
    assert outlook(f, goal=4).need == pytest.approx(5.8)   # round(1.2 + 0.4·5.8) = round(3.52) = 4


def test_nothing_graded_has_no_forecast():
    view = outlook(formula(part("ДЗ", "h1"), plan("h1"), {}), goal=6)
    assert view.forecast is None and view.need == 6


# --- checking and words ------------------------------------------------------------------------------


@pytest.mark.parametrize("tree", [
    {"final": {"type": "magic"}},
    {"final": ref("Накоп")},
    {"nodes": {"A": ref("B"), "B": ref("A")}, "final": ref("A")},
    {"final": part("ДЗ", agg="best")},
    {"final": {"type": "sum", "terms": [{"weight": "half", "node": part("ДЗ")}]}},
    {"final": {"type": "if", "cond": {"node": part("Экз"), "op": "≈", "value": 4}, "then": part("Экз"),
               "else": part("Экз")}},
    {"nodes": {}},
])
def test_bad_trees(tree):
    with pytest.raises(FormulaError):
        check(tree)


def test_in_words():
    tree = check({"nodes": {"Накоп": total((0.5, part("ДЗ", agg="best", k=4)), (0.5, part("КР")))},
                  "final": blocking_exam()})
    assert describe(tree, "ru") == [
        "Накоп = 0.5·ДЗ (лучшие 4) + 0.5·КР",
        "Итог = если Экз < 4, то Экз, иначе округл(0.6·Накоп + 0.4·Экз)"]
    assert describe(tree, "en")[1] == "Final = if Экз < 4: Экз, else round(0.6·Накоп + 0.4·Экз)"


def test_warnings():
    tree = check({"nodes": {"Накоп": total((0.5, part("ДЗ", "h1")), (0.4, part("КР", "t1")))},
                  "final": total((0.6, ref("Накоп")), (0.4, part("Экз")), bonus=[(0.1, part("Доп", "x"))])})
    assert warnings(tree, plan("h1", "t1", "lab")) == [
        ("weights", "Накоп", 0.9), ("empty", "Экз", None), ("empty", "Доп", None), ("outside", "LAB", None)]


def test_a_new_work_joins_the_part_its_label_matches():
    tree = check({"final": total((0.5, part("ДЗ", "h1", match=["ДЗ"])), (0.5, part("КР", "t1", match=["КР"])))})
    works = [{"key": "h1", "label": "HW 1", "label_ru": "ДЗ 1", "max": 10},
             {"key": "t1", "label": "Test 1", "label_ru": "КР 1", "max": 10},
             {"key": "h2", "label": "HW 2", "label_ru": "дз 2", "max": 10},       # added after the formula
             {"key": "x", "label": "Lab", "label_ru": "Лаба", "max": 10}]
    f = Formula(tree, works, graded(h1=6, h2=8, t1=5))
    assert [w.key for w in f.works("ДЗ")] == ["h1", "h2"] and f.value(0) == pytest.approx(0.5 * 7 + 0.5 * 5)
    assert warnings(tree, works) == [("outside", "Лаба", None)]
