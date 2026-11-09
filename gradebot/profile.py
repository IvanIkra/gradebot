"""What a user sets about themselves: the full name the sheets have, quiet hours and the language."""

import datetime as dt
import re

NAME_CHARS = 100
NAME_WORD = re.compile(r"^[^\W\d_]+(?:[-'’][^\W\d_]+)*\.?$")  # letters, inner hyphens and apostrophes; "А." too
QUIET_DEFAULT = (23, 8)


class ProfileError(ValueError):
    pass


def student(text: str) -> str:
    """'  Петров   Пётр ' -> 'Петров Пётр'. Two words at least: one would match every namesake in a sheet."""
    value = " ".join(text.split())
    words = value.split()
    if len(value) > NAME_CHARS or len(words) < 2 or not all(NAME_WORD.match(w) for w in words):
        raise ProfileError("a full name: two words or more, letters only")
    return value


def quiet(text: str) -> tuple[int, int] | None:
    """'23-8', '22:00–07:00' -> (from, to); 'off' -> None. ProfileError otherwise."""
    value = text.strip().lower()
    if value in ("off", "выкл", "нет", "no"):
        return None
    found = re.fullmatch(r"(\d{1,2})(?::00)?\s*[-–—]\s*(\d{1,2})(?::00)?", value)
    if found is None:
        raise ProfileError("hours as 23-8, or off")
    start, end = int(found[1]), int(found[2])
    if not (0 <= start <= 23 and 0 <= end <= 23) or start == end:
        raise ProfileError("hours from 0 to 23, the start and the end differ")
    return start, end


def is_quiet(user: dict, moment: dt.datetime, zone: dt.tzinfo) -> bool:
    """Within the user's quiet hours at `moment`: messages then go without a sound (spec §5.4)."""
    start, end = user.get("quiet_from"), user.get("quiet_to")
    if start is None or end is None:
        return False
    hour = moment.astimezone(zone).hour
    return start <= hour < end if start < end else hour >= start or hour < end


def hours(user: dict) -> str:
    start, end = user.get("quiet_from"), user.get("quiet_to")
    return "" if start is None or end is None else f"{start}:00–{end}:00"
