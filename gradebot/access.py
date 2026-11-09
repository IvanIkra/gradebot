"""Who may use the bot: one-time invite codes from the owner, the users who came in with them, and a limit on wrong
codes.

A code is eight characters from an alphabet without look-alikes (no 0/O, 1/I/L), shown as XXXX-XXXX. Only its
sha256 is stored; a code is compared without case, spaces and dashes, so «k7qf 29xm» works too.
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
CODE_LENGTH = 8
NOTE_LENGTH = 40
WRONG_LIMIT = 5                     # wrong codes from one Telegram id ...
WRONG_WINDOW = dt.timedelta(hours=1)  # ... within this long make the bot silent to it for as long

# Ranks: what a user may do without the owner. A member's sheet that the bot does not read yet waits for the owner's
# approval; a trusted user's is followed at once. A sheet the bot already reads costs nothing new: anyone follows it.
MEMBER, TRUSTED = 0, 1


def normalise(code: str) -> str:
    return "".join(ch for ch in code.upper() if ch.isalnum())


def code_hash(code: str) -> str:
    return hashlib.sha256(normalise(code).encode()).hexdigest()


def new_code() -> str:
    raw = "".join(secrets.choice(ALPHABET) for _ in range(CODE_LENGTH))
    return f"{raw[:4]}-{raw[4:]}"


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

    def invite(self, note: str = "") -> tuple[int, str]:
        """A new code: (invite id, the code). The code is shown once and never stored."""
        now = self.now()
        while True:
            code = new_code()
            try:
                with self.db:
                    cur = self.db.execute(
                        "insert into invites(code_hash, note, created_at, expires_at) values(?, ?, ?, ?)",
                        (code_hash(code), " ".join(note.split())[:NOTE_LENGTH], clock.utc_iso(now),
                         clock.utc_iso(now + dt.timedelta(days=self.invite_days))))
                return cur.lastrowid, code
            except sqlite3.IntegrityError:  # the same code twice: try another one
                continue

    def open_invites(self) -> list[dict]:
        """Codes not used, not revoked and not expired, newest first."""
        rows = self.db.execute("select * from invites where used_by is null and revoked_at is null and expires_at > ? "
                               "order by id desc", (clock.utc_iso(self.now()),))
        return [dict(row) for row in rows]

    def revoke(self, invite_id: int) -> bool:
        with self.db:
            cur = self.db.execute("update invites set revoked_at = ? where id = ? and used_by is null "
                                  "and revoked_at is null", (clock.utc_iso(self.now()), invite_id))
        return cur.rowcount == 1

    def redeem(self, code: str, user: NewUser) -> dict | None:
        """Register the user with a valid code and use it up: the invite row, or None for a wrong code (counted)."""
        now = clock.utc_iso(self.now())
        with self.db:
            row = self.db.execute("select * from invites where code_hash = ? and used_by is null "
                                  "and revoked_at is null and expires_at > ?", (code_hash(code), now)).fetchone()
            if row is None:
                self._count_wrong(user.tg_id)
                return None
            self.db.execute("update invites set used_by = ?, used_at = ? where id = ?", (user.tg_id, now, row["id"]))
            self.db.execute("insert into users(tg_id, tg_name, username, lang, joined_at, invite_id) "
                            "values(?, ?, ?, ?, ?, ?)",
                            (user.tg_id, user.tg_name, user.username, user.lang, now, row["id"]))
        self._wrong.pop(user.tg_id, None)
        return {**dict(row), "used_by": user.tg_id, "used_at": now}

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
        rows = self.db.execute("select u.*, i.note from users u left join invites i on i.id = u.invite_id "
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
