import sqlite3

from gradebot.store import Store


def test_an_older_database_gets_the_new_columns(tmp_path):
    path = tmp_path / "old.db"
    old = sqlite3.connect(path)
    old.executescript("""
        create table sheets (id integer primary key, sheet_id text not null unique, url text not null, title text,
                             course text not null default '', group_sheet integer not null default 0,
                             added_by integer not null, added_at text not null);
        create table subscriptions (tg_id integer not null, sheet integer not null, added_at text not null,
                                    primary key (tg_id, sheet));
        insert into sheets(sheet_id, url, added_by, added_at) values('abc', 'u', 1, 't');
    """)
    old.close()
    store = Store(path)
    columns = {row["name"] for row in store.db.execute("pragma table_info(subscriptions)")}
    assert {"baseline_at", "read_at", "error", "tabs"} <= columns
    assert store.db.execute("select told, error from sheets").fetchone()[0] == 0
    store.close()
    Store(path).close()  # a second start changes nothing


def test_delete_prefix(store):
    for key in ("layout:a_b:1", "layout:a_b:2", "layout:aXb:1"):
        store.put_json(key, 1)
    assert store.delete_prefix("layout:a_b:") == 2 and store.get_json("layout:aXb:1") == 1
