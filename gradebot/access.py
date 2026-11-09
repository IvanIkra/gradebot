"""Who may use the bot: one-time invite codes from the owner, the users who came in with them, and a limit on wrong
codes.

A code is six characters from an alphabet without look-alikes (no 0/O, 1/I/L), shown as XXX-XXX: 31^6, close to
a billion, against five wrong tries an hour from one Telegram id. Only its sha256 is stored; a code is compared without
case, spaces and dashes, so «k7q f29» works too.

A code lets in one person by default, or `uses` people, or any number (0) until it expires or is revoked. It may give
access for a while: so many days from coming in, or until a date. When that ends the user is kept but the bot neither
reads their sheets nor writes to them, until the owner prolongs the access or deletes them.
"""

import datetime as dt
import hashlib
import secrets
import sqlite3
from collections import defaultdict, deque
from collections.abc import Callable
from dataclasses import dataclass

from . import clock
from .store import Store

ALPHABET = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
CODE_LENGTH = 6
NOTE_LENGTH = 40
WRONG_LIMIT = 5                     # wrong codes from one Telegram id ...
WRONG_WINDOW = dt.timedelta(hours=1)  # ... within this long make the bot silent to it for as long

# A code that still lets people in; the parameter is now.
OPEN = "revoked_at is null and expires_at > ? and (uses = 0 or used < uses)"

# Ranks: what a user may do without the owner. A member's sheet that the bot does not read yet waits for the owner's
# approval; a trusted user's is followed at once. A sheet the bot already reads costs nothing new: anyone follows it.
MEMBER, TRUSTED = 0, 1


def normalise(code: str) -> str:
    return "".join(ch for ch in code.upper() if ch.isalnum())


def code_hash(code: str) -> str:
    return hashlib.sha256(normalise(code).encode()).hexdigest()


def new_code() -> str:
    raw = "".join(secrets.choice(ALPHABET) for _ in range(CODE_LENGTH))
    return f"{raw[:3]}-{raw[3:]}"


@dataclass(frozen=True)
class NewUser:
    tg_id: int
    tg_name: str
    username: str | None
    lang: str


class Access:
    def __init__(self, store: Store, invite_days: int = 7, now: Callable[[], dt.datetime] = clock.now_utc):
        self.db, self.invite_days, self.now = store.db, invite_days, now
        self._wrong: dict[int, deque[dt.datetime]] = defaultdict(deque)

    # --- invites ------------------------------------------------------------------------------

    def invite(self, note: str = "", group: int | None = None, uses: int = 1, lifetime: dt.timedelta | None = None,
               access_days: int | None = None, access_until: dt.datetime | None = None) -> tuple[int, str]:
        """A new code: (invite id, the code). It lands its users in `group`, lets in `uses` people (0: any number),
        works for `lifetime` (INVITE_DAYS by default) and gives access for `access_days` from coming in or until
        `access_until` (neither: for good). The code is shown once and never stored."""
        now = self.now()
        while True:
            code = new_code()
            try:
                with self.db:
                    cur = self.db.execute(
                        "insert into invites(code_hash, note, created_at, expires_at, grp, uses, access_days, "
                        "access_until) values(?, ?, ?, ?, ?, ?, ?, ?)",
                        (code_hash(code), " ".join(note.split())[:NOTE_LENGTH], clock.utc_iso(now),
                         clock.utc_iso(now + (lifetime or dt.timedelta(days=self.invite_days))), group, uses,
                         access_days, clock.utc_iso(access_until) if access_until else None))
                return cur.lastrowid, code
            except sqlite3.IntegrityError:  # the same code twice: try another one
                continue

    def open_invites(self) -> list[dict]:
        """Codes that still let people in: not used up, not revoked and not expired, newest first."""
        rows = self.db.execute(f"select i.*, g.name group_name from invites i left join groups g on g.id = i.grp "
                               f"where {OPEN} order by i.id desc", (clock.utc_iso(self.now()),))
        return [dict(row) for row in rows]

    def revoke(self, invite_id: int) -> bool:
        with self.db:
            cur = self.db.execute(f"update invites set revoked_at = ? where id = ? and {OPEN}",
                                  (clock.utc_iso(self.now()), invite_id, clock.utc_iso(self.now())))
        return cur.rowcount == 1

    def redeem(self, code: str, user: NewUser) -> dict | None:
        """Register the user with a valid code and count them in it: the invite row, or None for a wrong code
        (counted)."""
        moment = self.now()
        now = clock.utc_iso(moment)
        with self.db:
            row = self.db.execute(f"select * from invites where code_hash = ? and {OPEN}",
                                  (code_hash(code), now)).fetchone()
            if row is None:
                self._count_wrong(user.tg_id)
                return None
            self.db.execute("update invites set used = used + 1, used_by = ?, used_at = ? where id = ?",
                            (user.tg_id, now, row["id"]))
            expires = row["access_until"] or (clock.utc_iso(moment + dt.timedelta(days=row["access_days"]))
                                              if row["access_days"] else None)
            self.db.execute("insert into users(tg_id, tg_name, username, lang, joined_at, invite_id, expires_at) "
                            "values(?, ?, ?, ?, ?, ?, ?)",
                            (user.tg_id, user.tg_name, user.username, user.lang, now, row["id"], expires))
        self._wrong.pop(user.tg_id, None)
        return {**dict(row), "used": row["used"] + 1, "used_by": user.tg_id, "used_at": now}

    # --- wrong codes ---------------------------------------------------------------------------

    def _recent_wrong(self, tg_id: int) -> deque[dt.datetime]:
        tries, since = self._wrong[tg_id], self.now() - WRONG_WINDOW
        while tries and tries[0] <= since:
            tries.popleft()
        return tries

    def _count_wrong(self, tg_id: int) -> None:
        self._recent_wrong(tg_id).append(self.now())

    def locked(self, tg_id: int) -> bool:
        """Too many wrong codes lately: the bot does not answer this id."""
        return len(self._recent_wrong(tg_id)) >= WRONG_LIMIT

    # --- users ---------------------------------------------------------------------------------

    def user(self, tg_id: int) -> dict | None:
        row = self.db.execute("select * from users where tg_id = ?", (tg_id,)).fetchone()
        return dict(row) if row else None

    def users(self) -> list[dict]:
        rows = self.db.execute("select u.*, i.note, g.name group_name from users u "
                               "left join invites i on i.id = u.invite_id left join groups g on g.id = u.grp "
                               "order by u.joined_at")
        return [dict(row) for row in rows]

    def update_profile(self, tg_id: int, tg_name: str, username: str | None) -> None:
        """Telegram names change: keep the last ones the bot saw."""
        with self.db:
            self.db.execute("update users set tg_name = ?, username = ?, paused = 0 where tg_id = ?",
                            (tg_name, username, tg_id))

    def set_student(self, tg_id: int, student: str | None) -> None:
        """A new name means new rows in the sheets: the caller forgets the grades of the old one."""
        with self.db:
            self.db.execute("update users set student = ? where tg_id = ?", (student, tg_id))

    def set_lang(self, tg_id: int, lang: str) -> None:
        with self.db:
            self.db.execute("update users set lang = ? where tg_id = ?", (lang, tg_id))

    def set_quiet(self, tg_id: int, hours: tuple[int, int] | None) -> None:
        start, end = hours or (None, None)
        with self.db:
            self.db.execute("update users set quiet_from = ?, quiet_to = ? where tg_id = ?", (start, end, tg_id))

    # --- access for a while -----------------------------------------------------------------------

    def expired(self, user: dict) -> bool:
        return bool(user.get("expires_at")) and user["expires_at"] <= clock.utc_iso(self.now())

    def prolong(self, tg_id: int, days: int | None) -> dict | None:
        """Access for `days` more, counted from its end or from now if it ended; None: for good. The user row."""
        user = self.user(tg_id)
        if user is None:
            return None
        if days is None:
            expires = None
        else:
            start = max(self.now(), clock.parse(user["expires_at"])) if user["expires_at"] else self.now()
            expires = clock.utc_iso(start + dt.timedelta(days=days))
        with self.db:
            self.db.execute("update users set expires_at = ?, access_told = 0 where tg_id = ?", (expires, tg_id))
        return self.user(tg_id)

    def ending(self, within: dt.timedelta) -> tuple[list[dict], list[dict]]:
        """(users whose access ends within `within` and who were not told, users whose access ended and who were
        not told). The caller tells them and marks it with `told`."""
        now, soon = clock.utc_iso(self.now()), clock.utc_iso(self.now() + within)
        near = self.db.execute("select * from users where expires_at > ? and expires_at <= ? and access_told < 1",
                               (now, soon)).fetchall()
        ended = self.db.execute("select * from users where expires_at <= ? and access_told < 2", (now,)).fetchall()
        return [dict(row) for row in near], [dict(row) for row in ended]

    def told(self, tg_id: int, stage: int) -> None:
        with self.db:
            self.db.execute("update users set access_told = ? where tg_id = ?", (stage, tg_id))

    def set_rank(self, tg_id: int, rank: int) -> bool:
        with self.db:
            cur = self.db.execute("update users set rank = ? where tg_id = ?", (rank, tg_id))
        return cur.rowcount == 1

    def set_paused(self, tg_id: int, paused: bool) -> None:
        with self.db:
            self.db.execute("update users set paused = ? where tg_id = ?", (int(paused), tg_id))

    def delete(self, tg_id: int) -> bool:
        """Erase the user and everything kept for them (later tables cascade). Their invite row stays, used."""
        with self.db:
            cur = self.db.execute("delete from users where tg_id = ?", (tg_id,))
        return cur.rowcount == 1
