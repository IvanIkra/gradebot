"""The Google Sheets users follow. A sheet is one row for everyone who follows it, so it is downloaded and laid out
once (spec §5.1); `subscriptions` say who follows what.

A member's sheet the bot does not read yet is a request until the owner approves it (`access.MEMBER`); it counts
towards the user's limit while it waits.

Group sheets are the owner's, one set per group (`groups.py`). A sheet nobody follows and that is in no group is
dropped.
"""

import datetime as dt
import re
from collections.abc import Callable

from . import clock
from .store import Store

GOOGLE_SHEET = re.compile(r"^https://docs\.google\.com/spreadsheets/(?:u/\d+/)?d/([A-Za-z0-9_-]{20,80})")
COURSE_CHARS = 10       # the course in front of a work's label: "Calc HW 3" (23 characters for both)
COURSE_FROM_TITLE = 8   # characters of the sheet's title that make its course when the owner set none
SHEETS_PER_USER = 20
STALE_AFTER = dt.timedelta(hours=12)


class SheetError(ValueError):
    pass


def parse_url(url: str) -> tuple[str, str]:
    """(sheet id, the link to keep): the link up to the id, so the same sheet is one sheet whatever its tail."""
    found = GOOGLE_SHEET.match(url.strip())
    if found is None:
        raise SheetError("not a Google Sheets link")
    return found[1], f"https://docs.google.com/spreadsheets/d/{found[1]}"


def course(text: str) -> str:
    value = " ".join(text.split())
    if len(value) > COURSE_CHARS:
        raise SheetError(f"a course has at most {COURSE_CHARS} characters")
    return value


def course_of(sheet: dict) -> str:
    """The course in front of the sheet's works: the owner's, else the start of the title."""
    return sheet["course"] or (sheet["title"] or sheet["sheet_id"])[:COURSE_FROM_TITLE].strip()


def name(sheet: dict) -> str:
    """How the bot calls a sheet: its course, its title, or the end of its id while neither is known."""
    return sheet["course"] or sheet["title"] or f"…{sheet['sheet_id'][-6:]}"


class Sheets:
    def __init__(self, store: Store, now: Callable[[], dt.datetime] = clock.now_utc):
        self.db, self.now = store.db, now

    def upsert(self, url: str, by: int) -> int:
        sid, link = parse_url(url)
        row = self.db.execute("select id from sheets where sheet_id = ?", (sid,)).fetchone()
        if row is not None:
            return row["id"]
        return self.db.execute("insert into sheets(sheet_id, url, added_by, added_at) values(?, ?, ?, ?)",
                               (sid, link, by, clock.utc_iso(self.now()))).lastrowid

    def get(self, sheet: int) -> dict | None:
        row = self.db.execute("select * from sheets where id = ?", (sheet,)).fetchone()
        return dict(row) if row else None

    def by_url(self, url: str) -> dict | None:
        sid, _ = parse_url(url)
        row = self.db.execute("select * from sheets where sheet_id = ?", (sid,)).fetchone()
        return dict(row) if row else None

    # --- a user's sheets -------------------------------------------------------------------------

    def _count(self, tg_id: int) -> int:
        """The user's sheets and requests: the limit is on both."""
        return self.db.execute("select (select count(*) from subscriptions where tg_id = ?) + "
                               "(select count(*) from sheet_requests where tg_id = ?) n",
                               (tg_id, tg_id)).fetchone()["n"]

    def follow(self, tg_id: int, url: str) -> tuple[dict, bool]:
        """(the sheet, True when it is new to the user). SheetError for a bad link or too many sheets."""
        with self.db:
            sheet = self.upsert(url, tg_id)
            if self.db.execute("select 1 from subscriptions where tg_id = ? and sheet = ?", (tg_id, sheet)).fetchone():
                return self.get(sheet), False
            self.db.execute("delete from sheet_requests where tg_id = ? and sheet_id = ?",
                            (tg_id, self.get(sheet)["sheet_id"]))
            if self._count(tg_id) >= SHEETS_PER_USER:
                raise SheetError(f"at most {SHEETS_PER_USER} sheets")
            self.db.execute("insert into subscriptions(tg_id, sheet, added_at) values(?, ?, ?)",
                            (tg_id, sheet, clock.utc_iso(self.now())))
        return self.get(sheet), True

    def unfollow(self, tg_id: int, sheet: int) -> bool:
        with self.db:
            cur = self.db.execute("delete from subscriptions where tg_id = ? and sheet = ?", (tg_id, sheet))
            self._drop_orphans()
        return cur.rowcount == 1

    def followed(self, tg_id: int) -> list[dict]:
        """The user's sheets, with how their rows were read: read_at, user_error, tabs, baseline_at."""
        rows = self.db.execute("select s.*, u.read_at, u.error user_error, u.tabs, u.baseline_at from sheets s "
                               "join subscriptions u on u.sheet = s.id where u.tg_id = ? order by exists("
                               "select 1 from group_sheets g join users r on r.grp = g.grp where g.sheet = s.id "
                               "and r.tg_id = u.tg_id) desc, s.id", (tg_id,))
        return [dict(row) for row in rows]

    # --- requests: a member's sheets new to the bot ------------------------------------------------

    def ask(self, tg_id: int, url: str) -> tuple[dict, bool]:
        """(the request, True when it is new). SheetError for a bad link or too many sheets."""
        sid, link = parse_url(url)
        with self.db:
            row = self.db.execute("select * from sheet_requests where tg_id = ? and sheet_id = ?",
                                  (tg_id, sid)).fetchone()
            if row is not None:
                return dict(row), False
            if self._count(tg_id) >= SHEETS_PER_USER:
                raise SheetError(f"at most {SHEETS_PER_USER} sheets")
            rid = self.db.execute("insert into sheet_requests(tg_id, sheet_id, url, created_at) values(?, ?, ?, ?)",
                                  (tg_id, sid, link, clock.utc_iso(self.now()))).lastrowid
        return self.request(rid), True

    def request(self, rid: int) -> dict | None:
        row = self.db.execute("select * from sheet_requests where id = ?", (rid,)).fetchone()
        return dict(row) if row else None

    def requests(self, tg_id: int | None = None) -> list[dict]:
        """Waiting requests, of one user or of everyone, oldest first."""
        where, args = ("where tg_id = ?", (tg_id,)) if tg_id is not None else ("", ())
        return [dict(row) for row in self.db.execute(f"select * from sheet_requests {where} order by id", args)]

    def approve(self, rid: int) -> tuple[dict, dict] | None:
        """Follow the requested sheet: (the request, the sheet), or None when it is gone (handled, the user left)."""
        found = self.request(rid)
        if found is None:
            return None
        sheet, _ = self.follow(found["tg_id"], found["url"])  # drops the request
        return found, sheet

    def drop_request(self, rid: int, tg_id: int | None = None) -> dict | None:
        """Decline or cancel a request: the request, or None when it is gone (or is not this user's)."""
        found = self.request(rid)
        if found is None or (tg_id is not None and found["tg_id"] != tg_id):
            return None
        with self.db:
            self.db.execute("delete from sheet_requests where id = ?", (rid,))
        return found

    def followers(self, sheet: int) -> list[int]:
        return [row["tg_id"] for row in self.db.execute("select tg_id from subscriptions where sheet = ?", (sheet,))]

    # --- reading --------------------------------------------------------------------------------

    def readers(self, sheet: int) -> list[dict]:
        """The followers whose rows are looked for: those with a full name who have not blocked the bot."""
        rows = self.db.execute("select u.* from users u join subscriptions s on s.tg_id = u.tg_id "
                               "where s.sheet = ? and u.student is not null and not u.paused order by u.tg_id",
                               (sheet,))
        return [dict(row) for row in rows]

    def every(self) -> list[dict]:
        """Every sheet with the number of its followers: the owner's /status."""
        rows = self.db.execute("select s.*, (select count(*) from subscriptions u where u.sheet = s.id) followers "
                               "from sheets s order by exists(select 1 from group_sheets g where g.sheet = s.id) "
                               "desc, s.id")
        return [dict(row) for row in rows]

    def to_read(self) -> list[dict]:
        """Sheets with at least one reader: the others are not downloaded."""
        rows = self.db.execute("select * from sheets s where exists (select 1 from subscriptions u join users r "
                               "on r.tg_id = u.tg_id where u.sheet = s.id and r.student is not null and not r.paused) "
                               "order by id")
        return [dict(row) for row in rows]

    def reader(self, tg_id: int, sheet: int) -> str | None:
        """The full name a user's rows in a sheet are looked for by now; None when they no longer follow it."""
        row = self.db.execute("select u.student from users u join subscriptions s on s.tg_id = u.tg_id "
                              "where u.tg_id = ? and s.sheet = ?", (tg_id, sheet)).fetchone()
        return row["student"] if row else None

    def set_title(self, sheet: int, title: str) -> None:
        with self.db:
            self.db.execute("update sheets set title = ? where id = ?", (title, sheet))

    def read_ok(self, sheet: int) -> bool:
        """A good read; True when the sheet had been told as failing and is back."""
        row = self.db.execute("select told from sheets where id = ?", (sheet,)).fetchone()
        with self.db:
            self.db.execute("update sheets set ok_at = ?, error = null, error_since = null, told = 0 where id = ?",
                            (clock.utc_iso(self.now()), sheet))
        return bool(row and row["told"])

    def read_failed(self, sheet: int, error: str) -> str | None:
        """A failed read. The moment it has been failing since, when that makes STALE_AFTER just now: told once."""
        row = self.db.execute("select error_since, told from sheets where id = ?", (sheet,)).fetchone()
        if row is None:
            return None
        now = self.now()
        since = row["error_since"] or clock.utc_iso(now)
        stale = not row["told"] and now - clock.parse(since) >= STALE_AFTER
        with self.db:
            self.db.execute("update sheets set error = ?, error_since = ?, told = ? where id = ?",
                            (error, since, int(row["told"] or stale), sheet))
        return since if stale else None

    # --- the owner's -----------------------------------------------------------------------------

    def set_course(self, url: str, course_text: str) -> dict:
        sheet = self.by_url(url)
        if sheet is None:
            raise SheetError("nobody follows this sheet")
        with self.db:
            self.db.execute("update sheets set course = ? where id = ?", (course(course_text), sheet["id"]))
        return self.get(sheet["id"])

    def _drop_orphans(self) -> None:
        self.db.execute("delete from sheets where id not in (select sheet from subscriptions) "
                        "and id not in (select sheet from group_sheets)")

    def drop_orphans(self) -> None:
        """After a user or a group is deleted: the sheets nobody follows and no group has go too."""
        with self.db:
            self._drop_orphans()
