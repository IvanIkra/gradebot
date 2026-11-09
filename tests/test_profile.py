import datetime as dt

import pytest

from gradebot.profile import ProfileError, hours, is_quiet, quiet, student

from .conftest import ZONE


def test_student():
    assert student("  Петров   Пётр ") == "Петров Пётр"
    assert student("Иванова-Петрова Анна-Мария Ивановна") == "Иванова-Петрова Анна-Мария Ивановна"
    assert student("O'Neil Kate") == "O'Neil Kate"
    assert student("Петров П.") == "Петров П."
    for bad in ("Петров", "Петров 123", "<b>Петров</b> Пётр", "Петров -Пётр", "x " * 60):
        with pytest.raises(ProfileError):
            student(bad)


def test_quiet():
    assert quiet("23-8") == (23, 8)
    assert quiet(" 22:00 – 07:00 ") == (22, 7)
    assert quiet("OFF") is None and quiet("выкл") is None
    for bad in ("24-8", "8-8", "night", "23"):
        with pytest.raises(ProfileError):
            quiet(bad)


def at(hour: int) -> dt.datetime:
    return dt.datetime(2026, 11, 9, hour, 30, tzinfo=ZONE)


def test_is_quiet_across_midnight_and_within_a_day():
    night = {"quiet_from": 23, "quiet_to": 8}
    assert [h for h in range(24) if is_quiet(night, at(h), ZONE)] == [0, 1, 2, 3, 4, 5, 6, 7, 23]
    day = {"quiet_from": 13, "quiet_to": 15}
    assert [h for h in range(24) if is_quiet(day, at(h), ZONE)] == [13, 14]
    assert not is_quiet({"quiet_from": None, "quiet_to": None}, at(3), ZONE)
    assert is_quiet(night, dt.datetime(2026, 11, 9, 21, 0, tzinfo=dt.UTC), ZONE)  # 00:00 in Moscow
    assert hours(night) == "23:00–8:00" and hours({"quiet_from": None, "quiet_to": None}) == ""
