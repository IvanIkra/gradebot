"""Time rules: the database keeps UTC in ISO 8601, so plain string comparison orders it."""

import datetime as dt


def now_utc() -> dt.datetime:
    return dt.datetime.now(dt.UTC)


def utc_iso(moment: dt.datetime) -> str:
    return moment.astimezone(dt.UTC).isoformat(timespec="seconds")


def parse(value: str) -> dt.datetime:
    return dt.datetime.fromisoformat(value)


def month_start(moment: dt.datetime, zone: dt.tzinfo) -> dt.datetime:
    """Local midnight of the first day of `moment`'s month, as UTC: the LLM limit is counted by local months."""
    local = moment.astimezone(zone)
    return dt.datetime(local.year, local.month, 1, tzinfo=zone).astimezone(dt.UTC)


def month(moment: dt.datetime, zone: dt.tzinfo) -> str:
    """'2026-11': the local month of a moment."""
    return moment.astimezone(zone).strftime("%Y-%m")
