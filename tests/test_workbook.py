import pytest

from gradebot import layout, workbook
from gradebot.workbook import NotWorkbook

from .books import ANNA, IVAN, spec
from .books import workbook as book


def tabs(**kwargs):
    return workbook.load(book(**kwargs))


def test_load_keeps_visible_tabs_and_their_top():
    found = {tab.title: tab for tab in tabs()}
    assert set(found) == {"ДЗ-1А", "Квизы"}  # the hidden tab is skipped
    hw = found["ДЗ-1А"]
    assert [n for n, _ in hw.top] == [1, 2, 3, 4, 5]
    assert hw.header([2])[0]["F"] == "Балл" and hw.column_header("F", [1, 2]) == "Балл"


def test_find_rows_of_several_students_in_one_parse():
    parsed = tabs()
    ivan, _ = workbook.find(parsed, IVAN)
    anna, _ = workbook.find(parsed, ANNA)
    assert [(r.tab.title, r.number, r.name_col) for r in ivan] == [("ДЗ-1А", 5, "B"), ("Квизы", 5, "A")]
    assert [(r.tab.title, r.number) for r in anna] == [("ДЗ-1А", 4), ("Квизы", 6)]
    assert ivan[0].cells["F"] == 5.9 and "B" not in ivan[0].cells  # no name among the cells
    assert workbook.find(parsed, "Никто Никтоевич") == ([], [])


def test_a_name_in_two_rows_of_a_tab_leaves_that_tab_out():
    rows, twice = workbook.find(tabs(namesake=True), IVAN)
    assert twice == ["ДЗ-1А"] and [r.tab.title for r in rows] == ["Квизы"]
    rows, twice = workbook.find(tabs(namesake=True), ANNA)  # others are fine
    assert twice == [] and len(rows) == 2


def test_a_blank_copy_of_the_list_is_no_match():
    rows, twice = workbook.find(tabs(blank_copy=True), IVAN)
    assert twice == [] and [(r.tab.title, r.number) for r in rows] == [("ДЗ-1А", 5), ("Квизы", 5)]


def test_name_matching_handles_case_and_yo():
    assert workbook.matches("ИВАНОВ  Иван Сергеевич", ["иванов", "иван"])
    assert not workbook.matches("Иванова Ирина", ["иванов", "иван"])
    assert not workbook.matches("Иванов", ["иванов", "иван"])
    assert workbook.norm("Пётр,") == "петр"
    assert workbook.column(0) == "A" and workbook.column(26) == "AA"


def test_not_a_workbook():
    with pytest.raises(NotWorkbook):
        workbook.load(b"<html>Sign in</html>")


def test_prompt_shows_the_rows_above_and_the_row_without_the_name():
    hw = workbook.find(tabs(), IVAN)[0][0]
    prompt = layout.prompt(hw)
    assert "row 3: A=101 | B=Образцов Нагляд Примерович" in prompt  # the model tells the header from students
    assert prompt.endswith("student row (row 5): A=101 | D=1 | E=0.7 | F=5.9 | G=1")
    assert "ИВАНОВ" not in prompt and "row 5:" not in prompt


def test_extract_normalises_to_ten_and_keeps_the_bonus():
    hw = workbook.find(tabs(), IVAN)[0][0]
    [item] = layout.extract(hw, spec('Tab: "ДЗ-1А"'))
    assert item.label == "HW 1A" and item.value == pytest.approx(8.43) and item.bonus == 1
    assert (item.raw, item.max, item.key) == (5.9, 7, "ДЗ-1А|F|Балл")
    assert layout.budget("Calc") == 18 and layout.budget("Very long") == 13 and layout.budget("x" * 20) == 8


def test_a_work_listed_twice_counts_once():
    hw = workbook.find(tabs(), IVAN)[0][0]
    twice = {"skip": False, "items": [{"column": "F", "label": "HW 1A", "max": 7, "bonus_column": None},
                                      {"column": "F", "label": "HW 1A again", "max": 7, "bonus_column": None}]}
    assert [i.label for i in layout.extract(hw, twice)] == ["HW 1A"]


def test_the_sample_row_has_numbers():
    parsed = tabs()
    empty = workbook.Row(parsed[0], 9, "B", {"A": "—"})
    ivan = workbook.find(parsed, IVAN)[0][0]
    assert layout.sample([empty, ivan]) is ivan and layout.sample([empty]) is empty
