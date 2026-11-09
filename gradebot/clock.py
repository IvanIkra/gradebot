"""Time rules: the database keeps UTC in ISO 8601, so plain string comparison orders it."""

import datetime as dt


def now_utc() -> dt.datetime:
    return dt.datetime.now(dt.UTC)


def utc_iso(moment: dt.datetime) -> str:
    return moment.astimezone(dt.UTC).isoformat(timespec="seconds")


def parse(value: str) -> dt.datetime:
    return dt.datetime.fromisoformat(value)
