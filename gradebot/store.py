"""SQLite storage. Tables arrive with the stages that fill them: `create table if not exists` adds a later stage's
tables to an older database."""

import json
import sqlite3
from pathlib import Path

SCHEMA = """
create table if not exists kv (
    key text primary key,
    value text not null             -- JSON
);
create table if not exists invites (
    id integer primary key,
    code_hash text not null unique, -- sha256 of the normalised code; the code itself is never stored
    note text not null default '',  -- who the owner meant it for
    created_at text not null,
    expires_at text not null,
    used_by integer,                -- Telegram id of the user who came in with it
    used_at text,
    revoked_at text
);
create table if not exists users (
    tg_id integer primary key,
    tg_name text not null,          -- first and last name in Telegram
    username text,
    lang text not null,             -- en, ru
    student text,                   -- the full name as the sheets have it; null until /name
    quiet_from integer default 23,  -- quiet hours, local hours; both null: off
    quiet_to integer default 8,
    paused integer not null default 0,  -- the user blocked the bot
    joined_at text not null,
    invite_id integer references invites(id)
);
"""


class Store:
    def __init__(self, path: Path | str):
        self.db = sqlite3.connect(path)
        self.db.row_factory = sqlite3.Row
        self.db.execute("pragma foreign_keys = on")
        if str(path) != ":memory:":
            self.db.execute("pragma journal_mode = wal")
        self.db.executescript(SCHEMA)

    def close(self) -> None:
        self.db.close()

    def get_json(self, key: str, default=None):
        row = self.db.execute("select value from kv where key = ?", (key,)).fetchone()
        return default if row is None else json.loads(row["value"])

    def put_json(self, key: str, value) -> None:
        with self.db:
            self.db.execute("insert into kv(key, value) values(?, ?) "
                            "on conflict(key) do update set value = excluded.value",
                            (key, json.dumps(value, ensure_ascii=False)))
