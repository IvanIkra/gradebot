import pytest

from gradebot.access import NewUser
from gradebot.sheets import SHEETS_PER_USER, SheetError, name, parse_url

CALC = "https://docs.google.com/spreadsheets/d/1CalcExampleSheetId_0123456789abc/edit#gid=0"
ALG = "https://docs.google.com/spreadsheets/u/1/d/1AlgebraExampleSheetId-0123456789/edit"
PETR, ANNA = 2001, 2002


@pytest.fixture
def users(access):
    for tg_id, first in ((PETR, "Пётр"), (ANNA, "Анна")):
        _, code = access.invite()
        access.redeem(code, NewUser(tg_id, first, None, "ru"))


def test_parse_url():
    assert parse_url(CALC) == ("1CalcExampleSheetId_0123456789abc",
                               "https://docs.google.com/spreadsheets/d/1CalcExampleSheetId_0123456789abc")
    assert parse_url(ALG)[0] == "1AlgebraExampleSheetId-0123456789"
    for bad in ("https://example.com/spreadsheets/d/1CalcExampleSheetId_0123456789abc",
                "http://docs.google.com/spreadsheets/d/1CalcExampleSheetId_0123456789abc", "calc"):
        with pytest.raises(SheetError):
            parse_url(bad)


def test_follow_one_sheet_for_all(sheets, users):
    sheet, new = sheets.follow(PETR, CALC)
    assert new and name(sheet) == "…789abc"
    again, new = sheets.follow(ANNA, CALC.replace("#gid=0", "?usp=sharing"))
    assert new and again["id"] == sheet["id"]  # new to Anna, the same sheet
    assert sorted(sheets.followers(sheet["id"])) == [PETR, ANNA]
    assert sheets.follow(PETR, CALC)[1] is False


def test_unfollow_drops_sheets_nobody_follows(sheets, users):
    sheet, _ = sheets.follow(PETR, CALC)
    assert sheets.unfollow(PETR, sheet["id"])
    assert sheets.get(sheet["id"]) is None
    assert not sheets.unfollow(PETR, sheet["id"])


def test_limit(sheets, users):
    for n in range(SHEETS_PER_USER):
        sheets.follow(PETR, f"https://docs.google.com/spreadsheets/d/{'x' * 20}{n:04d}")
    with pytest.raises(SheetError):
        sheets.follow(PETR, CALC)


def test_group_sheets(sheets, users, access):
    sheet, added = sheets.add_group(CALC, 1000, "Матан")
    assert added == 2 and sheet["course"] == "Матан" and sheet["group_sheet"] == 1
    assert sheets.unfollow(ANNA, sheet["id"])      # a group sheet can be unfollowed and stays
    assert sheets.get(sheet["id"]) is not None
    _, code = access.invite()
    access.redeem(code, NewUser(2003, "Ира", None, "ru"))
    assert sheets.follow_group(2003) == 1
    assert [s["followers"] for s in sheets.group()] == [2]
    assert sheets.remove_group(sheet["id"])
    assert sheets.follow_group(2004) == 0
    assert sorted(sheets.followers(sheet["id"])) == [PETR, 2003]


def test_courses(sheets, users):
    sheets.follow(PETR, CALC)
    assert sheets.set_course(CALC, "  Мат  ан ")["course"] == "Мат ан"
    with pytest.raises(SheetError):
        sheets.set_course(CALC, "x" * 11)
    with pytest.raises(SheetError):
        sheets.set_course(ALG, "Алг")


def test_deleting_a_user_drops_their_own_sheets(sheets, users, access):
    own, _ = sheets.follow(PETR, CALC)
    group, _ = sheets.add_group(ALG, 1000)
    access.delete(PETR)
    sheets.drop_orphans()
    assert sheets.get(own["id"]) is None and sheets.get(group["id"]) is not None
    assert sheets.followers(group["id"]) == [ANNA]
