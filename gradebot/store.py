"""SQLite storage. Tables arrive with the stages that fill them: `create table if not exists` adds a later stage's
tables to an older database."""

import json
import sqlite3
from pathlib import Path

from . import clock

SCHEMA = """
create table if not exists kv (
    key text primary key,
    value text not null             -- JSON
);
create table if not exists groups (         -- groups of users, each with its shared sheets
    id integer primary key,
    name text not null unique collate nocase,  -- one word: commands take it in front of the rest
    created_at text not null
);
create table if not exists invites (
    id integer primary key,
    code_hash text not null unique, -- sha256 of the normalised code; the code itself is never stored
    note text not null default '',  -- who the owner meant it for
    created_at text not null,
    expires_at text not null,       -- the code works until then
    uses integer not null default 1,  -- how many people may come in with it; 0: any number until it expires
    used integer not null default 0,  -- how many came in
    used_by integer,                -- Telegram id of the last user who came in with it
    used_at text,
    revoked_at text,
    access_days integer,            -- who comes in has access for this many days from then ...
    access_until text,              -- ... or until then; both null: for good
    grp integer references groups(id) on delete set null  -- the group the user lands in; null: they pick one
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
    invite_id integer references invites(id),
    grp integer references groups(id) on delete set null,  -- the user's group: they follow its sheets
    grp_fixed integer not null default 0,  -- the group came with the invite code: only the owner moves the user
    rank integer not null default 0,  -- 0: a sheet new to the bot waits for the owner's approval; 1: trusted
    expires_at text,                -- the user's access ends then: no reads, no messages; null: for good
    access_told integer not null default 0,  -- 1: the end was told a day before, 2: the end was told
    prolong_asked integer not null default 0  -- the user asked the owner to prolong the access
);
create table if not exists sheets (
    id integer primary key,         -- short, for buttons
    sheet_id text not null unique,  -- the id in the Google link
    url text not null,
    title text,                     -- the sheet's title, known after the first read
    course text not null default '',  -- the course in front of a work's label, at most 10 characters
    added_by integer not null,      -- Telegram id
    added_at text not null,
    ok_at text,                     -- the last good read
    error text,                     -- why the last read failed: "<kind>: <detail>", see poller.explain
    error_since text,               -- failing since
    told integer not null default 0 -- failing for 12 hours was told to the owner and the followers
);
create table if not exists group_sheets (   -- the shared sheets of a group: everyone in it follows them
    grp integer not null references groups(id) on delete cascade,
    sheet integer not null references sheets(id) on delete cascade,
    primary key (grp, sheet)
);
create table if not exists sheet_requests (   -- sheets new to the bot that a user asked for, waiting for the owner
    id integer primary key,
    tg_id integer not null references users(tg_id) on delete cascade,
    sheet_id text not null,         -- the id in the Google link
    url text not null,
    course text not null default '',  -- the course the user gave: the sheet gets it if it is approved
    created_at text not null,
    unique (tg_id, sheet_id)
);
create table if not exists subscriptions (
    tg_id integer not null references users(tg_id) on delete cascade,
    sheet integer not null references sheets(id) on delete cascade,
    added_at text not null,
    baseline_at text,               -- the first read for this user: it records no new grades; null until then
    read_at text,                   -- the last read of the user's rows, good or not
    error text,                     -- no_row, ambiguous: the user's rows could not be told; null when fine
    tabs integer not null default 0,  -- tabs with the user's row at the last read
    primary key (tg_id, sheet)
);
create table if not exists grade_items (    -- the current grade of every work of a user in a sheet
    tg_id integer not null,
    sheet integer not null,
    key text not null,              -- <tab>|<column>|<its header>: a new label is no new grade
    tab text not null,
    header text not null,           -- the column's header: a work whose column moved is found by it
    label text not null,            -- the work without the course: "HW 1A"
    label_ru text,                  -- the same in Russian: "ДЗ 1А"; null for a layout from before it
    value real not null,            -- 0-10
    raw real not null,              -- the points as the sheet has them
    max real not null,              -- out of
    bonus real,
    position integer not null,      -- in sheet order
    gone integer not null default 0,  -- not in the sheet now (a hidden tab, a cleared cell): back with the same
                                      -- grade, it is no new grade
    primary key (tg_id, sheet, key),
    foreign key (tg_id, sheet) references subscriptions(tg_id, sheet) on delete cascade
);
create table if not exists grade_events (   -- new and changed grades, waiting to be sent while post_id is null
    id integer primary key,
    tg_id integer not null,
    sheet integer not null,
    key text not null,
    label text not null,
    label_ru text,
    value real not null,
    raw real not null,
    max real not null,
    bonus real,
    previous real,                  -- the grade before, null for a first one
    seen_at text not null,
    post_id integer,                -- the message in the user's chat; null while it waits
    foreign key (tg_id, sheet) references subscriptions(tg_id, sheet) on delete cascade
);
create index if not exists grade_events_user on grade_events(tg_id, sheet, seen_at);
create table if not exists layout_reports (  -- a user's complaints about a sheet's layout: one a day
    tg_id integer not null references users(tg_id) on delete cascade,
    sheet integer not null references sheets(id) on delete cascade,
    created_at text not null
);
create index if not exists layout_reports_user on layout_reports(tg_id, sheet, created_at);
create table if not exists llm_runs (       -- every Haiku call, a failed one too: tokens and cost
    id integer primary key,
    at text not null,               -- UTC
    task text not null,
    model text not null,
    ok integer not null,
    error text,
    input_tokens integer not null,
    output_tokens integer not null,
    cache_write integer not null,
    cache_read integer not null,
    cost_usd real not null,
    duration_s real not null
);
create index if not exists llm_runs_at on llm_runs(at);
"""

# (table, column, definition[, what to fill the old rows with]): columns a later stage added to a table an earlier one
# created.
UPGRADES = (
    ("sheets", "ok_at", "text"),
    ("sheets", "error", "text"),
    ("sheets", "error_since", "text"),
    ("sheets", "told", "integer not null default 0"),
    ("subscriptions", "baseline_at", "text"),
    ("subscriptions", "read_at", "text"),
    ("subscriptions", "error", "text"),
    ("subscriptions", "tabs", "integer not null default 0"),
    ("users", "rank", "integer not null default 0", "update users set rank = 1"),  # who was in before ranks is trusted
    ("users", "grp", "integer references groups(id) on delete set null"),
    ("users", "grp_fixed", "integer not null default 0"),
    ("users", "expires_at", "text"),
    ("users", "access_told", "integer not null default 0"),
    ("users", "prolong_asked", "integer not null default 0"),
    ("invites", "uses", "integer not null default 1"),
    ("invites", "used", "integer not null default 0", "update invites set used = 1 where used_by is not null"),
    ("invites", "access_days", "integer"),
    ("invites", "access_until", "text"),
    ("invites", "grp", "integer references groups(id) on delete set null"),
    ("sheet_requests", "course", "text not null default ''"),
    ("grade_items", "label_ru", "text"),
    ("grade_events", "label_ru", "text"),
)
FIRST_GROUP = "group"   # the group the shared sheets of a database from before groups go to, with every user


class Store:
    def __init__(self, path: Path | str):
        self.db = sqlite3.connect(path)
        self.db.row_factory = sqlite3.Row
        self.db.execute("pragma foreign_keys = on")
        if str(path) != ":memory:":
            self.db.execute("pragma journal_mode = wal")
        self._upgrade()
        self.db.executescript(SCHEMA)
        self._to_groups()

    def _upgrade(self) -> None:
        """Add the columns an older database lacks, before the schema's indexes may need them."""
        for table, column, definition, *fill in UPGRADES:
            have = {row["name"] for row in self.db.execute(f"pragma table_info({table})")}
            if have and column not in have:
                self.db.execute(f"alter table {table} add column {column} {definition}")
                for sql in fill:
                    self.db.execute(sql)
        self.db.commit()

    def _to_groups(self) -> None:
        """Before groups there was one set of shared sheets, `sheets.group_sheet`: they become the sheets of one
        group with every user in it, and the column goes."""
        if "group_sheet" not in {row["name"] for row in self.db.execute("pragma table_info(sheets)")}:
            return
        with self.db:
            if self.db.execute("select 1 from sheets where group_sheet = 1").fetchone():
                gid = self.db.execute("insert into groups(name, created_at) values(?, ?)",
                                      (FIRST_GROUP, clock.utc_iso(clock.now_utc()))).lastrowid
                self.db.execute("insert into group_sheets(grp, sheet) select ?, id from sheets where group_sheet = 1",
                                (gid,))
                self.db.execute("update users set grp = ?", (gid,))
            self.db.execute("alter table sheets drop column group_sheet")

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

    def delete_prefix(self, prefix: str) -> int:
        """kv keys that start with `prefix`, by the start of the key rather than LIKE: ids may have "_" in them."""
        with self.db:
            return self.db.execute("delete from kv where substr(key, 1, ?) = ?", (len(prefix), prefix)).rowcount
