import datetime as dt
import sqlite3

from gradebot.backups import KEEP, backup
from gradebot.store import Store

from .conftest import ZONE


def test_a_copy_a_day_after_the_hour_and_the_last_ones_kept(tmp_path):
    store = Store(tmp_path / "gradebot.db")
    store.put_json("answer", 42)
    folder = tmp_path / "backups"
    night = dt.datetime(2026, 11, 10, 0, 30, tzinfo=dt.UTC)       # 03:30 in Moscow: not yet
    assert backup(store, folder, night, ZONE) is None and not folder.exists()

    made = backup(store, folder, night + dt.timedelta(hours=1), ZONE)
    assert made is not None and made.name == "gradebot-2026-11-10.db"
    copy = sqlite3.connect(made)
    assert copy.execute("select value from kv where key = 'answer'").fetchone() == ("42",)
    copy.close()
    assert backup(store, folder, night + dt.timedelta(hours=5), ZONE) is None   # one a day

    for day in range(1, 20):
        backup(store, folder, night + dt.timedelta(days=day, hours=1), ZONE)
    kept = sorted(path.name for path in folder.iterdir())
    assert len(kept) == KEEP and kept[-1] == "gradebot-2026-11-29.db" and not list(folder.glob("*.part"))
    store.close()
