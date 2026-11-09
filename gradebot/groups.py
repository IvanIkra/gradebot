"""Groups of users and their shared sheets (spec §4.2). The owner makes groups and gives each its sheets; a user is in
one group or none, picked at registration and changed with /group. A group that came with the invite code is fixed:
only the owner moves such a user (`fixed`).

Everyone in a group follows its sheets: when they join it and when the owner adds a sheet to it. A user may still
unfollow a group sheet. Leaving a group unfollows its sheets, except those the new group has too; the grades of
those sheets go with the subscriptions. A sheet may be in several groups. A sheet that is in no group and that
nobody follows is dropped (`Sheets.drop_orphans`).
"""

import datetime as dt
from collections.abc import Callable

from . import clock
from .sheets import Sheets, course
from .store import Store

NAME_CHARS = 20


class GroupError(ValueError):
    pass


def group_name(text: str) -> str:
    """One word: /invite and /groupsheet take a group's name in front of the rest."""
    value = text.strip()
    if not value or len(value.split()) != 1 or len(value) > NAME_CHARS or value.startswith(("/", "http")):
        raise GroupError(f"a group's name is one word of at most {NAME_CHARS} characters")
    return value


class Groups:
    def __init__(self, store: Store, sheets: Sheets, now: Callable[[], dt.datetime] = clock.now_utc):
        self.db, self.sheets, self.now = store.db, sheets, now

    def all(self) -> list[dict]:
        """Every group with its users and sheets counted, by name."""
        rows = self.db.execute("select g.*, (select count(*) from users u where u.grp = g.id) users, "
                               "(select count(*) from group_sheets s where s.grp = g.id) sheets "
                               "from groups g order by g.name collate nocase")
        return [dict(row) for row in rows]

    def get(self, gid: int | None) -> dict | None:
        row = self.db.execute("select * from groups where id = ?", (gid,)).fetchone() if gid else None
        return dict(row) if row else None

    def find(self, name: str) -> dict | None:
        row = self.db.execute("select * from groups where name = ? collate nocase", (name.strip(),)).fetchone()
        return dict(row) if row else None

    def create(self, name: str) -> dict:
        name = group_name(name)
        if self.find(name) is not None:
            raise GroupError("a group with this name exists")
        with self.db:
            gid = self.db.execute("insert into groups(name, created_at) values(?, ?)",
                                  (name, clock.utc_iso(self.now()))).lastrowid
        return self.get(gid)

    def rename(self, gid: int, name: str) -> dict:
        name = group_name(name)
        found = self.find(name)
        if found is not None and found["id"] != gid:
            raise GroupError("a group with this name exists")
        with self.db:
            self.db.execute("update groups set name = ? where id = ?", (name, gid))
        return self.get(gid)

    def delete(self, gid: int) -> bool:
        """Its users are in no group then and keep their sheets; codes that carried it carry none."""
        with self.db:
            cur = self.db.execute("delete from groups where id = ?", (gid,))
        self.sheets.drop_orphans()
        return cur.rowcount == 1

    # --- sheets ------------------------------------------------------------------------------------

    def sheets_of(self, gid: int) -> list[dict]:
        rows = self.db.execute("select s.*, (select count(*) from subscriptions u where u.sheet = s.id) followers "
                               "from sheets s join group_sheets g on g.sheet = s.id where g.grp = ? order by s.id",
                               (gid,))
        return [dict(row) for row in rows]

    def add_sheet(self, gid: int, url: str, by: int, course_text: str | None = None) -> tuple[dict, int]:
        """Give a group a sheet and have everyone in it follow it: (the sheet, how many users started to)."""
        with self.db:
            sheet = self.sheets.upsert(url, by)
            self.db.execute("insert or ignore into group_sheets(grp, sheet) values(?, ?)", (gid, sheet))
            if course_text is not None:
                self.db.execute("update sheets set course = ? where id = ?", (course(course_text), sheet))
            cur = self.db.execute("insert or ignore into subscriptions(tg_id, sheet, added_at) "
                                  "select tg_id, ?, ? from users where grp = ?",
                                  (sheet, clock.utc_iso(self.now()), gid))
        return self.sheets.get(sheet), cur.rowcount

    def remove_sheet(self, gid: int, sheet: int) -> bool:
        """No longer the group's: who joins it won't get it, who follows it keeps it."""
        with self.db:
            cur = self.db.execute("delete from group_sheets where grp = ? and sheet = ?", (gid, sheet))
        self.sheets.drop_orphans()
        return cur.rowcount == 1

    # --- users -------------------------------------------------------------------------------------

    def fix(self, tg_id: int) -> None:
        """The user's group came with their invite code: they don't change it themselves."""
        with self.db:
            self.db.execute("update users set grp_fixed = 1 where tg_id = ?", (tg_id,))

    @staticmethod
    def fixed(user: dict) -> bool:
        """Only the owner moves the user: their group came with the code and is still there."""
        return bool(user["grp_fixed"] and user["grp"])

    def join(self, tg_id: int, gid: int | None) -> tuple[int, list[int]]:
        """Move a user to a group, or to none: (sheets of the old group unfollowed, the new group's sheets they
        started to follow)."""
        row = self.db.execute("select grp from users where tg_id = ?", (tg_id,)).fetchone()
        if row is None:
            return 0, []
        gid = gid or None
        if row["grp"] == gid:
            return 0, []
        if gid is not None and self.get(gid) is None:
            raise GroupError("no such group")
        with self.db:
            left = 0
            if row["grp"] is not None:
                left = self.db.execute(
                    "delete from subscriptions where tg_id = ? and sheet in (select sheet from group_sheets "
                    "where grp = ?) and sheet not in (select sheet from group_sheets where grp = ?)",
                    (tg_id, row["grp"], gid or 0)).rowcount
            self.db.execute("update users set grp = ? where tg_id = ?", (gid, tg_id))
            new = [r["sheet"] for r in self.db.execute(
                "select sheet from group_sheets where grp = ? and sheet not in "
                "(select sheet from subscriptions where tg_id = ?) order by sheet", (gid or 0, tg_id))]
            self.db.executemany("insert into subscriptions(tg_id, sheet, added_at) values(?, ?, ?)",
                                [(tg_id, sheet, clock.utc_iso(self.now())) for sheet in new])
        self.sheets.drop_orphans()
        return left, new
