package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestOlderDatabaseGetsTheNewColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`
        create table sheets (id integer primary key, sheet_id text not null unique, url text not null, title text,
                             course text not null default '', group_sheet integer not null default 0,
                             added_by integer not null, added_at text not null);
        create table subscriptions (tg_id integer not null, sheet integer not null, added_at text not null,
                                    primary key (tg_id, sheet));
        insert into sheets(sheet_id, url, added_by, added_at) values('abc', 'u', 1, 't');`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	s := MustOpen(path)
	have := s.columns("subscriptions")
	for _, c := range []string{"baseline_at", "read_at", "error", "tabs"} {
		if !have[c] {
			t.Errorf("no %s", c)
		}
	}
	if told, _ := Scalar[int](s, "select told from sheets"); told != 0 {
		t.Error(told)
	}
	s.Close()
	MustOpen(path).Close() // a second start changes nothing
}

func TestDeletePrefix(t *testing.T) {
	s := MustOpen(":memory:")
	defer s.Close()
	for _, key := range []string{"layout:a_b:1", "layout:a_b:2", "layout:aXb:1"} {
		s.PutJSON(key, 1)
	}
	var v int
	if n := s.DeletePrefix("layout:a_b:"); n != 2 || !s.GetJSON("layout:aXb:1", &v) || v != 1 {
		t.Fatal(n, v)
	}
}

func TestUsersBeforeRanksAreTrusted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	s := MustOpen(path)
	s.Exec("insert into users(tg_id, tg_name, lang, joined_at) values(1, 'Тест', 'ru', 't')")
	s.Exec("alter table users drop column rank")
	s.Close()
	s = MustOpen(path)
	defer s.Close()
	if rank, _ := Scalar[int](s, "select rank from users"); rank != 1 {
		t.Fatal(rank)
	}
	s.Exec("insert into users(tg_id, tg_name, lang, joined_at) values(2, 'Новый', 'ru', 't')")
	if rank, _ := Scalar[int](s, "select rank from users where tg_id = 2"); rank != 0 {
		t.Fatal(rank)
	}
}

func TestGradesFromBeforeProblemsHaveNone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	s := MustOpen(path)
	s.Exec("alter table grade_items drop column parts")
	s.Close()
	s = MustOpen(path)
	defer s.Close()
	if !s.columns("grade_items")["parts"] {
		t.Fatal("no parts")
	}
}

func TestForeignKeysCascade(t *testing.T) {
	s := MustOpen(":memory:")
	defer s.Close()
	s.Exec("insert into users(tg_id, tg_name, lang, joined_at) values(1, 'Тест', 'ru', 't')")
	s.Exec("insert into sheets(sheet_id, url, added_by, added_at) values('a', 'u', 1, 't')")
	s.Exec("insert into subscriptions(tg_id, sheet, added_at) values(1, 1, 't')")
	s.Exec("delete from users")
	if n, _ := Scalar[int](s, "select count(*) from subscriptions"); n != 0 {
		t.Fatal("no cascade")
	}
}

func TestTxRollsBack(t *testing.T) {
	s := MustOpen(":memory:")
	defer s.Close()
	err := s.TryTx(func() {
		s.Exec("insert into kv(key, value) values('a', '1')")
		s.Exec("insert into kv(key, value) values('a', '2')")
	})
	if err == nil || !IsConstraint(err) || Has(s, "select 1 from kv") {
		t.Fatal(err)
	}
}
