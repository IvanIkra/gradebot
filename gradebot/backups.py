"""A copy of the database once a day: `data/backups/gradebot-<local date>.db`, the last KEEP of them.

The copy is SQLite's online backup, consistent while the bot runs, written to a temporary file first, so a copy that
is there is whole. It is made at the first look after BACKUP_HOUR local time, so a bot that was down at that hour
makes it when it starts. Copy the folder off the server now and then: a copy on the same disk does not survive the
disk.
"""

import asyncio
import datetime as dt
import logging
import sqlite3
from collections.abc import Callable
from pathlib import Path

from . import clock
from .store import Store

log = logging.getLogger(__name__)

BACKUP_HOUR = 4     # local time: the copy waits for the night's quiet
KEEP = 14
CHECK = 3600        # seconds between looks


def backup(store: Store, folder: Path, moment: dt.datetime, zone: dt.tzinfo) -> Path | None:
    """Today's copy when it is due and missing, and the old ones beyond KEEP dropped: the new file, or None."""
    local = moment.astimezone(zone)
    target = folder / f"gradebot-{local:%Y-%m-%d}.db"
    if local.hour < BACKUP_HOUR or target.exists():
        return None
    folder.mkdir(parents=True, exist_ok=True)
    partial = target.with_suffix(".part")
    copy = sqlite3.connect(partial)
    try:
        store.db.backup(copy)
    finally:
        copy.close()
    partial.replace(target)
    for old in sorted(folder.glob("gradebot-*.db"))[:-KEEP]:
        old.unlink()
    log.info("database copied to %s", target)
    return target


async def run(store: Store, folder: Path, zone: dt.tzinfo, now: Callable[[], dt.datetime] = clock.now_utc) -> None:
    while True:
        try:
            backup(store, folder, now(), zone)
        except Exception:
            log.exception("the database copy failed")
        await asyncio.sleep(CHECK)
