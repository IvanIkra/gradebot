// Package store is the SQLite storage. Tables arrive with the stages that fill them: `create table if not exists`
// adds a later stage's tables to an older database.
//
// One connection serves the whole service, as the event loop of the Python version had: foreign keys and the
// transaction in progress belong to a connection. A database error is a bug, so the helpers panic on one; the loops
// that handle updates and read sheets recover and log it, as an exception was logged before.
package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/IvanIkra/gradebot/internal/clock"
	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const schema = `
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
    grp integer references groups(id) on delete set null,  -- the group the user lands in; null: they pick one
    rank integer not null default 0  -- the rank the user gets: 2 makes them the group's admin
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
    rank integer not null default 0,  -- 0: a sheet new to the bot waits for approval; 1: trusted; 2: also the
                                    -- admin of their group
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
create table if not exists sheet_works (    -- every work the laid-out tabs of a sheet have, graded or not yet
    sheet integer not null references sheets(id) on delete cascade,
    key text not null,              -- as grade_items.key
    tab text not null,
    label text not null,
    label_ru text,
    max real not null,
    position integer not null,      -- in sheet order
    primary key (sheet, key)
);
create table if not exists group_sheets (   -- the shared sheets of a group: everyone in it follows them
    grp integer not null references groups(id) on delete cascade,
    sheet integer not null references sheets(id) on delete cascade,
    primary key (grp, sheet)
);
create table if not exists subjects (     -- sheets of one course read as one: a group's, or a user's own
    id integer primary key,
    grp integer references groups(id) on delete cascade,      -- run by the group's admin and the owner ...
    tg_id integer references users(tg_id) on delete cascade,  -- ... or by this user, for the sheets they follow
    name text not null,             -- its hashtag too, at most 10 characters
    formula text,                   -- how its grade is made, as the teacher writes it; null: none given
    tree text,                      -- the formula as the formula package evaluates it (JSON)
    created_at text not null
);
create table if not exists subject_sheets (   -- in a scope (a group, a user) a sheet is in one subject at most
    subject integer not null references subjects(id) on delete cascade,
    sheet integer not null references sheets(id) on delete cascade,
    primary key (subject, sheet)
);
create table if not exists subject_formulas (   -- a user's own formula of a subject: it wins over the subject's
    tg_id integer not null references users(tg_id) on delete cascade,
    subject integer not null references subjects(id) on delete cascade,
    formula text not null,
    tree text not null,
    primary key (tg_id, subject)
);
create table if not exists subject_goals (  -- the grade a user aims at in a subject
    tg_id integer not null references users(tg_id) on delete cascade,
    subject integer not null references subjects(id) on delete cascade,
    goal integer not null,          -- 4 to 10
    primary key (tg_id, subject)
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
    error text,                     -- no_row, or ambiguous: tabs with the name in two rows; null when fine
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
    parts text,                     -- the work's problems in the row, JSON [[number, points]]; null: none
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
`

// upgrade is a column a later stage added to a table an earlier one created, and what fills the old rows.
type upgrade struct{ table, column, definition, fill string }

var upgrades = []upgrade{
	{"sheets", "ok_at", "text", ""},
	{"sheets", "error", "text", ""},
	{"sheets", "error_since", "text", ""},
	{"sheets", "told", "integer not null default 0", ""},
	{"subscriptions", "baseline_at", "text", ""},
	{"subscriptions", "read_at", "text", ""},
	{"subscriptions", "error", "text", ""},
	{"subscriptions", "tabs", "integer not null default 0", ""},
	{"users", "rank", "integer not null default 0", "update users set rank = 1"}, // who was in before ranks is trusted
	{"users", "grp", "integer references groups(id) on delete set null", ""},
	{"users", "grp_fixed", "integer not null default 0", ""},
	{"users", "expires_at", "text", ""},
	{"users", "access_told", "integer not null default 0", ""},
	{"users", "prolong_asked", "integer not null default 0", ""},
	{"invites", "uses", "integer not null default 1", ""},
	{"invites", "used", "integer not null default 0", "update invites set used = 1 where used_by is not null"},
	{"invites", "access_days", "integer", ""},
	{"invites", "access_until", "text", ""},
	{"invites", "grp", "integer references groups(id) on delete set null", ""},
	{"invites", "rank", "integer not null default 0", ""},
	{"sheet_requests", "course", "text not null default ''", ""},
	{"grade_items", "label_ru", "text", ""},
	{"grade_events", "label_ru", "text", ""},
	{"subjects", "tree", "text", ""},
	{"grade_items", "parts", "text", ""},
}

// FirstGroup is the group the shared sheets of a database from before groups go to, with every user.
const FirstGroup = "group"

type querier interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
}

// Store is the database.
type Store struct {
	DB   *sql.DB
	tx   *sql.Tx
	Path string
}

// Open opens (and creates or upgrades) the database at path; ":memory:" for tests.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	if path == ":memory:" {
		dsn = "file::memory:?_pragma=foreign_keys(1)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)
	s := &Store{DB: db, Path: path}
	if path != ":memory:" {
		if _, err := db.Exec("pragma journal_mode = wal"); err != nil {
			db.Close()
			return nil, err
		}
	}
	if err := s.upgrade(); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.toGroups(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// MustOpen is Open for tests.
func MustOpen(path string) *Store {
	s, err := Open(path)
	if err != nil {
		panic(err)
	}
	return s
}

func (s *Store) columns(table string) map[string]bool {
	have := map[string]bool{}
	for _, name := range Column[string](s, "select name from pragma_table_info(?)", table) {
		have[name] = true
	}
	return have
}

// upgrade adds the columns an older database lacks, before the schema's indexes may need them.
func (s *Store) upgrade() error {
	for _, u := range upgrades {
		have := s.columns(u.table)
		if len(have) > 0 && !have[u.column] {
			if _, err := s.DB.Exec(fmt.Sprintf("alter table %s add column %s %s", u.table, u.column,
				u.definition)); err != nil {
				return err
			}
			if u.fill != "" {
				if _, err := s.DB.Exec(u.fill); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// toGroups: before groups there was one set of shared sheets, `sheets.group_sheet`: they become the sheets of one
// group with every user in it, and the column goes.
func (s *Store) toGroups() error {
	if !s.columns("sheets")["group_sheet"] {
		return nil
	}
	s.Tx(func() {
		if Has(s, "select 1 from sheets where group_sheet = 1") {
			gid := s.Insert("insert into groups(name, created_at) values(?, ?)", FirstGroup,
				clock.ISO(clock.NowUTC()))
			s.Exec("insert into group_sheets(grp, sheet) select ?, id from sheets where group_sheet = 1", gid)
			s.Exec("update users set grp = ?", gid)
		}
		s.Exec("alter table sheets drop column group_sheet")
	})
	return nil
}

// Close closes the database.
func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) q() querier {
	if s.tx != nil {
		return s.tx
	}
	return s.DB
}

// Tx runs fn in one transaction: committed when it returns, rolled back when it panics. A Tx inside a Tx is part of
// the outer one, as `with db:` blocks were.
func (s *Store) Tx(fn func()) {
	if s.tx != nil {
		fn()
		return
	}
	tx, err := s.DB.Begin()
	if err != nil {
		panic(err)
	}
	s.tx = tx
	done := false
	defer func() {
		s.tx = nil
		if !done {
			_ = tx.Rollback()
		}
	}()
	fn()
	if err := tx.Commit(); err != nil {
		panic(err)
	}
	done = true
}

// TryTx is Tx that gives back the panic of fn as an error, rolled back; a constraint failure is the usual one.
func (s *Store) TryTx(fn func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(error); ok {
				err = e
				return
			}
			panic(r)
		}
	}()
	s.Tx(fn)
	return nil
}

// Exec runs a statement and gives the rows it changed.
func (s *Store) Exec(query string, args ...any) int64 {
	res, err := s.q().Exec(query, args...)
	if err != nil {
		panic(fmt.Errorf("%w\n%s", err, query))
	}
	n, _ := res.RowsAffected()
	return n
}

// TryExec runs a statement and gives back its error: a constraint failure the caller expects.
func (s *Store) TryExec(query string, args ...any) (int64, error) {
	res, err := s.q().Exec(query, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// Insert runs an insert and gives the new row's id.
func (s *Store) Insert(query string, args ...any) int64 {
	res, err := s.q().Exec(query, args...)
	if err != nil {
		panic(fmt.Errorf("%w\n%s", err, query))
	}
	id, _ := res.LastInsertId()
	return id
}

// IsConstraint tells a constraint failure (unique, foreign key) from other errors.
func IsConstraint(err error) bool {
	var e *sqlite.Error
	if errors.As(err, &e) {
		return e.Code()&0xff == sqlite3.SQLITE_CONSTRAINT
	}
	return false
}

// Select reads rows into structs: a column goes to the field tagged `db:"name"`, or named the same ignoring case
// and underscores. Columns with no field are skipped.
func Select[T any](s *Store, query string, args ...any) []T {
	rows, err := s.q().Query(query, args...)
	if err != nil {
		panic(fmt.Errorf("%w\n%s", err, query))
	}
	defer rows.Close()
	names, _ := rows.Columns()
	var out []T
	var skip any
	for rows.Next() {
		var item T
		v := reflect.ValueOf(&item).Elem()
		targets := make([]any, len(names))
		for n, name := range names {
			if field, ok := fieldOf(v, name); ok {
				targets[n] = field.Addr().Interface()
			} else {
				targets[n] = &skip
			}
		}
		if err := rows.Scan(targets...); err != nil {
			panic(fmt.Errorf("%w\n%s", err, query))
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		panic(err)
	}
	return out
}

func fieldOf(v reflect.Value, column string) (reflect.Value, bool) {
	t := v.Type()
	want := strings.ReplaceAll(strings.ToLower(column), "_", "")
	for n := 0; n < t.NumField(); n++ {
		f := t.Field(n)
		if tag := f.Tag.Get("db"); tag == column || (tag == "" && strings.ToLower(f.Name) == want) {
			return v.Field(n), true
		}
	}
	return reflect.Value{}, false
}

// Get reads one row; false when there is none.
func Get[T any](s *Store, query string, args ...any) (T, bool) {
	rows := Select[T](s, query, args...)
	if len(rows) == 0 {
		var zero T
		return zero, false
	}
	return rows[0], true
}

// Column reads the first column of every row.
func Column[T any](s *Store, query string, args ...any) []T {
	rows, err := s.q().Query(query, args...)
	if err != nil {
		panic(fmt.Errorf("%w\n%s", err, query))
	}
	defer rows.Close()
	names, _ := rows.Columns()
	var out []T
	for rows.Next() {
		var value T
		targets := make([]any, len(names))
		targets[0] = &value
		for n := 1; n < len(names); n++ {
			targets[n] = new(any)
		}
		if err := rows.Scan(targets...); err != nil {
			panic(fmt.Errorf("%w\n%s", err, query))
		}
		out = append(out, value)
	}
	if err := rows.Err(); err != nil {
		panic(err)
	}
	return out
}

// Scalar reads the first column of the first row; false when there is no row.
func Scalar[T any](s *Store, query string, args ...any) (T, bool) {
	values := Column[T](s, query, args...)
	if len(values) == 0 {
		var zero T
		return zero, false
	}
	return values[0], true
}

// Has tells whether the query gives a row.
func Has(s *Store, query string, args ...any) bool {
	_, ok := Scalar[any](s, query, args...)
	return ok
}

// GetJSON reads a kv value into out; false when the key is not there.
func (s *Store) GetJSON(key string, out any) bool {
	value, ok := Scalar[string](s, "select value from kv where key = ?", key)
	if !ok {
		return false
	}
	if err := json.Unmarshal([]byte(value), out); err != nil {
		panic(fmt.Errorf("kv %s: %w", key, err))
	}
	return true
}

// PutJSON writes a kv value.
func (s *Store) PutJSON(key string, value any) {
	s.Tx(func() {
		s.Exec("insert into kv(key, value) values(?, ?) on conflict(key) do update set value = excluded.value",
			key, string(Marshal(value)))
	})
}

// DeleteKey removes a kv value.
func (s *Store) DeleteKey(key string) {
	s.Tx(func() { s.Exec("delete from kv where key = ?", key) })
}

// DeletePrefix removes kv keys that start with prefix, by the start of the key rather than LIKE: ids may have "_"
// in them.
func (s *Store) DeletePrefix(prefix string) int64 {
	var n int64
	s.Tx(func() {
		n = s.Exec("delete from kv where substr(key, 1, ?) = ?", len([]rune(prefix)), prefix)
	})
	return n
}

// Marshal is JSON with the text as it is: no \u escapes of non-ASCII letters or of <, > and &.
func Marshal(value any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		panic(err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

// Bool is SQLite's 0 and 1.
func Bool(b bool) int {
	if b {
		return 1
	}
	return 0
}
