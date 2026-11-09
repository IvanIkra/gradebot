"""Reading the sheets.

Every interval (30 minutes unless the owner sets another with /interval) each sheet that has a reader (a follower
with a full name who has not blocked the bot) is downloaded as xlsx once, its title fetched once from the HTML
view, the workbook parsed once, and every reader's rows looked for in it. Each tab with a reader's row is laid out
once for all of them (layout.py), then each reader's grades are recorded (grades.py). A new follower or a new full
name asks for a read at once (`request`): only those sheets are read then, and the users who asked are told how it
went.

A sheet fails as a whole on the download, on a file that is no workbook (a sheet closed to the link gives a login
page) and on the layout (the network, the limit); its grades stay as they were, and it is read again at the next
poll. A reader whose row is on no tab fails alone; a tab with their name in two rows is left out for them, and they
are told so. A sheet failing for 12 hours is told
once to the owner and its followers, and once more when it reads again (`SheetRead.stale`, `back`).
"""

import asyncio
import html
import logging
import re
from collections.abc import Awaitable, Callable
from dataclasses import dataclass, field

import aiohttp

from . import layout as layouts
from . import workbook
from .grades import Grades
from .layout import Layouts
from .llm.budget import BudgetExceeded
from .llm.client import LLMError
from .sheets import Sheets, course_of
from .store import Store

log = logging.getLogger(__name__)

EXPORT_URL = "https://docs.google.com/spreadsheets/d/{id}/export?format=xlsx"
TITLE_URL = "https://docs.google.com/spreadsheets/d/{id}/htmlview"
INTERVAL = "poll.minutes"   # kv: how often the sheets are read, set by the owner
DEFAULT_MINUTES = 30
MIN_MINUTES, MAX_MINUTES = 5, 240
MINUTE = 60                 # seconds; tests make it shorter
TIMEOUT = 120               # seconds for a download

# Errors are kept as "<kind>: <detail>"; the kinds are what the bot explains to a user.
DOWNLOAD, CLOSED, LAYOUT, BUDGET = "download", "closed", "layout", "budget"   # of a sheet
NO_ROW, AMBIGUOUS = "no_row", "ambiguous"                                     # of a user's rows


def interval(store: Store) -> int:
    """Minutes between the reads of every sheet."""
    return store.get_json(INTERVAL, DEFAULT_MINUTES)


def set_interval(store: Store, minutes: int) -> None:
    """ValueError for minutes out of MIN_MINUTES..MAX_MINUTES."""
    if not MIN_MINUTES <= minutes <= MAX_MINUTES:
        raise ValueError(f"from {MIN_MINUTES} to {MAX_MINUTES} minutes")
    store.put_json(INTERVAL, minutes)


@dataclass
class UserRead:
    tg_id: int
    error: str | None = None    # NO_ROW, or AMBIGUOUS with the tabs left out (the others are read)
    tabs: int = 0               # with the user's row
    items: int = 0              # grades in the sheet now
    first: bool = False         # the baseline was made now


@dataclass
class SheetRead:
    sheet: dict
    error: str | None = None    # the sheet failed: "<kind>: <detail>"
    stale: str | None = None    # failing since this moment for 12 hours: told now
    back: bool = False          # read again after it was told as failing
    users: list[UserRead] = field(default_factory=list)
    unread: tuple[str, ...] = ()  # tabs the model could not lay out: skipped until their top rows change
    requested: frozenset[int] = frozenset()  # users who asked for this read and are waiting for its outcome


class Poller:
    def __init__(self, store: Store, sheets: Sheets, grades: Grades, layouts: Layouts):
        self.store, self.sheets, self.grades, self.layouts = store, sheets, grades, layouts
        self.on_read: Callable[[list[SheetRead]], Awaitable[None]] | None = None  # after every poll
        self._pending: dict[int, set[int]] = {}     # sheet -> users waiting for its outcome
        self._wake = asyncio.Event()

    def request(self, sheet: int, tg_id: int | None = None) -> None:
        """Read a sheet now, not at the next poll; `tg_id` is told how it went."""
        self._pending.setdefault(sheet, set()).update([tg_id] if tg_id is not None else [])
        self._wake.set()

    def relayout(self, sheet: dict) -> None:
        """Drop the sheet's cached layouts and read it again: the owner's /relayout."""
        self.layouts.forget(sheet["sheet_id"])
        self.request(sheet["id"])

    def rescheduled(self) -> None:
        """The interval changed: the next poll is counted from the last one with the new interval."""
        self._wake.set()

    async def run(self) -> None:
        """A poll of every sheet each interval, counted from the start of the last one; requested sheets between
        them. A read on request does not move the schedule."""
        loop = asyncio.get_running_loop()
        last = None
        while True:
            full = last is None or loop.time() - last >= interval(self.store) * MINUTE
            if full:
                last = loop.time()
            if full or self._pending:
                try:
                    await self.poll(full)
                except Exception:
                    log.exception("grades poll failed")
            wait = max(0.0, last + interval(self.store) * MINUTE - loop.time())
            try:  # cleared after waking, not before: a request during a poll gives one more poll
                await asyncio.wait_for(self._wake.wait(), wait)
            except TimeoutError:
                pass
            self._wake.clear()

    async def poll(self, full: bool = True) -> list[SheetRead]:
        """Every sheet with a reader, or only the requested ones; on_read gets what came of them."""
        requested, self._pending = self._pending, {}
        wanted = self.sheets.to_read()
        if not full:
            wanted = [sheet for sheet in wanted if sheet["id"] in requested]
        results = []
        if wanted:
            async with aiohttp.ClientSession(timeout=aiohttp.ClientTimeout(total=TIMEOUT)) as session:
                for sheet in wanted:
                    read = await self.read(session, sheet)
                    read.requested = frozenset(requested.get(sheet["id"], ()))
                    results.append(read)
        if results and self.on_read is not None:
            try:
                await self.on_read(results)
            except Exception:
                log.exception("telling the outcome of a poll failed")
        return results

    async def read(self, session: aiohttp.ClientSession, sheet: dict) -> SheetRead:
        sid = sheet["sheet_id"]
        try:
            async with session.get(EXPORT_URL.format(id=sid)) as response:
                response.raise_for_status()
                data = await response.read()
            if sheet["title"] is None:  # after the file: a sheet closed to the link has the title of a login page
                if title := await fetch_title(session, sid):
                    self.sheets.set_title(sheet["id"], title)
                    sheet = {**sheet, "title": title}
            tabs = await asyncio.to_thread(workbook.load, data)
        except aiohttp.ClientResponseError as e:  # its text carries the URL: the status alone
            return self._failed(sheet, f"{DOWNLOAD}: HTTP {e.status} {e.message}".strip())
        except workbook.NotWorkbook as e:
            return self._failed(sheet, f"{CLOSED}: {e}")
        except (aiohttp.ClientError, TimeoutError, OSError) as e:
            return self._failed(sheet, f"{DOWNLOAD}: {type(e).__name__} {e}".strip())

        # Every reader's rows; the layout of each tab one of them is in, once for all.
        users, rows, names = [], {}, {}
        for reader in self.sheets.readers(sheet["id"]):
            user = UserRead(reader["tg_id"])
            names[user.tg_id] = reader["student"]
            found, twice = workbook.find(tabs, reader["student"])
            if twice:
                user.error = f"{AMBIGUOUS}: {', '.join(twice)}"
            if found:
                rows[user.tg_id] = found
            elif not twice:
                user.error = NO_ROW
            users.append(user)
        budget = layouts.budget(course_of(sheet))
        specs = {}
        try:
            for tab in tabs:
                in_tab = [row for found in rows.values() for row in found if row.tab is tab]
                if in_tab:
                    specs[tab.title] = await self.layouts.get(sid, tab, in_tab, budget)
        except BudgetExceeded as e:
            return self._failed(sheet, f"{BUDGET}: {e}", users)
        except LLMError as e:
            return self._failed(sheet, f"{LAYOUT}: {e}", users)
        except Exception as e:  # one sheet must not stop the others
            log.exception("layout of sheet %s failed", sid)
            return self._failed(sheet, f"{LAYOUT}: {type(e).__name__}: {e}", users)

        # The database may have changed while the tabs were laid out: a reader who left the sheet, or has another
        # name now, gets nothing recorded from this read.
        for user in users:
            if self.sheets.reader(user.tg_id, sheet["id"]) != names[user.tg_id]:
                continue
            if found := rows.get(user.tg_id):
                items = []
                for row in found:
                    items += layouts.extract(row, specs[row.tab.title], len(items))
                user.first = self.grades.record(user.tg_id, sheet["id"], items, len(found), user.error)
                user.tabs, user.items = len(found), self.grades.count(user.tg_id, sheet["id"])
            else:
                self.grades.failed(user.tg_id, sheet["id"], user.error)
        unread = tuple(title for title, spec in specs.items() if spec.get("failed"))
        back = self.sheets.read_ok(sheet["id"])
        return SheetRead(self.sheets.get(sheet["id"]) or sheet, back=back, users=users, unread=unread)

    def _failed(self, sheet: dict, error: str, users: list[UserRead] | None = None) -> SheetRead:
        log.warning("sheet %s failed: %s", sheet["sheet_id"], error)
        stale = self.sheets.read_failed(sheet["id"], error)
        return SheetRead(sheet, error=error, stale=stale, users=users or [])


async def fetch_title(session: aiohttp.ClientSession, sid: str) -> str | None:
    """The sheet's title from its HTML view; None on any failure (asked again at the next read)."""
    try:
        async with session.get(TITLE_URL.format(id=sid)) as response:
            if response.status != 200:
                return None
            page = await response.text()
    except (aiohttp.ClientError, TimeoutError, UnicodeDecodeError):
        return None
    found = re.search(r"<title>(.*?)</title>", page, re.S)
    if found is None:
        return None
    title = html.unescape(found[1]).strip()
    for suffix in (" - Google Диск", " - Google Drive", " - Google Таблицы", " - Google Sheets"):
        title = title.removesuffix(suffix)
    title = title.strip()
    return title or None
