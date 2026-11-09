
import pytest

from gradebot.layout import Item

from .conftest import add_user


def item(key, label, value, bonus=None, position=0, tab="T", header=None):
    return Item(key, tab, header or key, label, value, value, 10, bonus, position)


@pytest.fixture
def two(access, sheets):
    """Two users following one sheet: (sheet id, first user, second user)."""
    add_user(access, 1, "Иванов Иван")
    add_user(access, 2, "Петрова Анна")
    sheet, _ = sheets.follow(1, "https://docs.google.com/spreadsheets/d/1AbCdEfGhIjKlMnOpQrStUvWx")
    sheets.follow(2, sheet["url"])
    return sheet["id"], 1, 2


def test_first_read_is_a_baseline_then_changes_are_new(grades, two, clock):
    sheet, ivan, _ = two
    assert grades.record(ivan, sheet, [item("k1", "HW 1A", 8.43), item("k2", "Quiz 1", 0)], 2)
    assert grades.waiting() == []
    assert [i["label"] for i in grades.items(ivan, sheet)] == ["HW 1A"]  # the zero is a placeholder

    clock.advance(days=1)
    assert not grades.record(ivan, sheet, [item("k1", "HW 1A", 9.0), item("k2", "Quiz 1", 8, position=1),
                                           item("k3", "Quiz 2", 0, position=2)], 2)
    events = {e["label"]: e for e in grades.waiting(ivan)}
    assert set(events) == {"HW 1A", "Quiz 1"}
    assert events["HW 1A"]["previous"] == 8.43 and events["Quiz 1"]["previous"] is None
    assert [i["new"] for i in grades.items(ivan, sheet)] == [True, True]

    grades.sent([events["HW 1A"]["id"]], 77)
    assert [e["label"] for e in grades.waiting(ivan)] == ["Quiz 1"]
    clock.advance(days=8)
    assert grades.waiting() == [] and not any(i["new"] for i in grades.items(ivan, sheet))  # out of the window


def test_users_of_one_sheet_are_apart(grades, two, clock):
    sheet, ivan, anna = two
    assert grades.record(ivan, sheet, [item("k1", "HW", 8)], 1)
    assert grades.record(anna, sheet, [item("k1", "HW", 6)], 1)  # her own baseline
    clock.advance(hours=1)
    grades.record(anna, sheet, [item("k1", "HW", 7)], 1)
    assert [(e["tg_id"], e["value"]) for e in grades.waiting()] == [(anna, 7)]
    assert grades.items(ivan, sheet)[0]["value"] == 8 and grades.count(anna, sheet) == 1


def test_a_grade_that_leaves_and_comes_back(grades, two, clock):
    sheet, ivan, _ = two
    grades.record(ivan, sheet, [item("k1", "HW", 8), item("k2", "Quiz", 7, position=1)], 2)
    clock.advance(hours=1)
    grades.record(ivan, sheet, [item("k1", "HW", 8)], 1)   # the quiz tab hidden for a while
    assert [i["label"] for i in grades.items(ivan, sheet)] == ["HW"]
    grades.record(ivan, sheet, [item("k1", "HW", 8), item("k2", "Quiz", 7, position=1)], 2)
    assert grades.waiting() == []                           # back with the same grade: no news
    grades.record(ivan, sheet, [item("k1", "HW", 8)], 1)
    grades.record(ivan, sheet, [item("k1", "HW", 8), item("k2", "Quiz", 9, position=1)], 2)
    assert [(e["label"], e["value"], e["previous"]) for e in grades.waiting()] == [("Quiz", 9, 7)]


def test_a_moved_column_is_no_new_grade(grades, two, clock):
    sheet, ivan, _ = two
    grades.record(ivan, sheet, [item("T|E|Квиз 1", "Quiz 1", 8, header="Квиз 1")], 1)
    clock.advance(hours=1)
    grades.record(ivan, sheet, [item("T|F|Квиз 1", "Quiz 1", 8, header="Квиз 1")], 1)
    assert grades.waiting() == [] and [i["key"] for i in grades.items(ivan, sheet)] == ["T|F|Квиз 1"]
    grades.record(ivan, sheet, [item("T|F|Квиз 1", "Quiz 1", 9, header="Квиз 1")], 1)
    clock.advance(hours=1)
    grades.record(ivan, sheet, [item("T|E|Квиз 1", "Quiz 1", 9, header="Квиз 1")], 1)  # moved back with its news
    assert [(e["key"], e["value"]) for e in grades.waiting()] == [("T|E|Квиз 1", 9)]


def test_forget_makes_the_next_read_a_baseline(grades, two, clock):
    sheet, ivan, anna = two
    grades.record(ivan, sheet, [item("k1", "HW", 8)], 1)
    grades.record(anna, sheet, [item("k1", "HW", 8)], 1)
    grades.failed(ivan, sheet, "no_row")
    grades.forget(ivan)
    assert grades.items(ivan, sheet) == [] and grades.items(anna, sheet)
    assert grades.record(ivan, sheet, [item("k1", "HW", 5)], 1) and grades.waiting() == []


def test_grades_go_with_the_subscription_and_the_user(grades, two, sheets, access):
    sheet, ivan, anna = two
    grades.record(ivan, sheet, [item("k1", "HW", 8)], 1)
    grades.record(anna, sheet, [item("k1", "HW", 8)], 1)
    sheets.unfollow(ivan, sheet)
    access.delete(anna)
    assert grades.db.execute("select count(*) from grade_items").fetchone()[0] == 0
