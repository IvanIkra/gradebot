"""Grades of every user in every sheet they follow, and the new ones among them, one user at a time.

A grade's key is its place: tab, column and the column's header, not its label, so a new label is no new grade. The
first read of a sheet for a user is the baseline: it fills grade_items and records nothing. After it, a work new to
the sheet, or a changed grade or bonus, is a new grade (grade_events), unless it is a placeholder zero (a zero before
any real grade means "not graded yet"). A grade that leaves the sheet (a tab hidden, the row gone from one tab, a
cell cleared) is kept as gone, so its return with the same grade is no new grade, and with another one it is new with
the grade before. A column moved by one inserted before it keeps its grade: matched by tab, header and grade among
the works that left in the same read.

Grades live as long as the subscription: unfollowing a sheet, leaving the bot or being deleted erases them (foreign
keys), and a new full name erases all of the user's (`forget`), so the next read is a baseline again.
"""

import datetime as dt
from collections.abc import Callable

from . import clock
from .layout import Item
from .store import Store

WINDOW = dt.timedelta(days=7)   # new grades wait this long to be sent, and are marked new this long


class Grades:
    def __init__(self, store: Store, now: Callable[[], dt.datetime] = clock.now_utc):
        self.db, self.now = store.db, now

    def record(self, tg_id: int, sheet: int, items: list[Item], tabs: int) -> bool:
        """Store a user's grades in a sheet and the new ones among them; True for the baseline (the first read).
        The subscription must exist."""
        db = self.db
        sub = db.execute("select baseline_at from subscriptions where tg_id = ? and sheet = ?",
                         (tg_id, sheet)).fetchone()
        first = sub["baseline_at"] is None
        prev = {row["key"]: dict(row) for row in db.execute(
            "select * from grade_items where tg_id = ? and sheet = ?", (tg_id, sheet))}
        keys = {item.key for item in items}
        # A column inserted before others shifts their letters and so their keys: a work that leaves one key in this
        # read and is found under another with the same tab, header and grade is the same work, not a new grade.
        shifted: dict[tuple, list[dict]] = {}
        for row in prev.values():
            if row["key"] not in keys and not row["gone"]:
                shifted.setdefault((row["tab"], row["header"], row["value"], row["bonus"]), []).append(row)
        now, seen, moved = clock.utc_iso(self.now()), set(), set()
        with db:
            for item in items:
                old = prev.get(item.key)
                if old is None and (same := shifted.get((item.tab, item.header, item.value, item.bonus))):
                    old = same.pop()
                    moved.add(old["key"])
                    db.execute("update grade_events set key = ? where tg_id = ? and sheet = ? and key = ?",
                               (item.key, tg_id, sheet, old["key"]))
                if item.value == 0 and not item.bonus and (old is None or old["value"] == 0):
                    continue  # a placeholder for "not graded yet"
                seen.add(item.key)
                db.execute(
                    "insert into grade_items(tg_id, sheet, key, tab, header, label, label_ru, value, raw, max, bonus, "
                    "position) values(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) on conflict(tg_id, sheet, key) do update set "
                    "label = excluded.label, label_ru = excluded.label_ru, value = excluded.value, raw = excluded.raw, "
                    "max = excluded.max, bonus = excluded.bonus, position = excluded.position, gone = 0",
                    (tg_id, sheet, item.key, item.tab, item.header, item.label, item.label_ru or None, item.value,
                     item.raw, item.max, item.bonus, item.position))
                changed = old is None or old["value"] != item.value or (old["bonus"] or 0) != (item.bonus or 0)
                if changed and not first:
                    db.execute("insert into grade_events(tg_id, sheet, key, label, label_ru, value, raw, max, bonus, "
                               "previous, seen_at) values(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
                               (tg_id, sheet, item.key, item.label, item.label_ru or None, item.value, item.raw,
                                item.max, item.bonus,
                                old["value"] if old else None, now))
            # A moved work lives under its new key now; the others that left may come back.
            db.executemany("delete from grade_items where tg_id = ? and sheet = ? and key = ?",
                           [(tg_id, sheet, key) for key in moved])
            db.executemany("update grade_items set gone = 1 where tg_id = ? and sheet = ? and key = ?",
                           [(tg_id, sheet, key) for key in set(prev) - seen - moved])
            db.execute("update subscriptions set baseline_at = coalesce(baseline_at, ?), read_at = ?, error = null, "
                       "tabs = ? where tg_id = ? and sheet = ?", (now, now, tabs, tg_id, sheet))
        return first

    def failed(self, tg_id: int, sheet: int, error: str) -> None:
        """The user's rows could not be told (no_row, ambiguous): the grades stay as they were."""
        with self.db:
            self.db.execute("update subscriptions set read_at = ?, error = ? where tg_id = ? and sheet = ?",
                            (clock.utc_iso(self.now()), error, tg_id, sheet))

    def forget(self, tg_id: int, sheet: int | None = None) -> None:
        """A user's grades, of one sheet or all: the next read is a baseline again (a new full name, a reset)."""
        where, args = ("tg_id = ?", [tg_id]) if sheet is None else ("tg_id = ? and sheet = ?", [tg_id, sheet])
        with self.db:
            for table in ("grade_items", "grade_events"):
                self.db.execute(f"delete from {table} where {where}", args)
            self.db.execute(f"update subscriptions set baseline_at = null, read_at = null, error = null, tabs = 0 "
                            f"where {where}", args)

    # --- what the user sees ------------------------------------------------------------------------

    def count(self, tg_id: int, sheet: int) -> int:
        return self.db.execute("select count(*) n from grade_items where tg_id = ? and sheet = ? and not gone",
                               (tg_id, sheet)).fetchone()["n"]

    def items(self, tg_id: int, sheet: int) -> list[dict]:
        """A user's grades in a sheet in sheet order, the ones new within the window marked."""
        fresh = {row["key"] for row in self.db.execute(
            "select distinct key from grade_events where tg_id = ? and sheet = ? and seen_at >= ?",
            (tg_id, sheet, self._since()))}
        rows = self.db.execute("select * from grade_items where tg_id = ? and sheet = ? and not gone "
                               "order by position", (tg_id, sheet))
        return [{**dict(row), "new": row["key"] in fresh} for row in rows]

    def waiting(self, tg_id: int | None = None) -> list[dict]:
        """New grades not sent yet, within the window, oldest first; of one user or of everyone."""
        where, args = ("and tg_id = ?", [tg_id]) if tg_id is not None else ("", [])
        rows = self.db.execute(f"select * from grade_events where post_id is null and seen_at >= ? {where} "
                               "order by id", [self._since(), *args])
        return [dict(row) for row in rows]

    def sent(self, event_ids: list[int], msg_id: int) -> None:
        with self.db:
            self.db.executemany("update grade_events set post_id = ? where id = ?",
                                [(msg_id, event_id) for event_id in event_ids])

    def _since(self) -> str:
        return clock.utc_iso(self.now() - WINDOW)
