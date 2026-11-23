package backups

import (
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/IvanIkra/gradebot/internal/store"
)

func TestACopyADayAfterTheHourAndTheLastOnesKept(t *testing.T) {
	dir := t.TempDir()
	db := store.MustOpen(filepath.Join(dir, "gradebot.db"))
	defer db.Close()
	db.PutJSON("answer", 42)
	folder := filepath.Join(dir, "backups")
	zone, _ := time.LoadLocation("Europe/Moscow")
	night := time.Date(2026, 11, 10, 0, 30, 0, 0, time.UTC) // 03:30 in Moscow: not yet
	if made, _ := Backup(db, folder, night, zone); made != "" {
		t.Fatal(made)
	}
	if _, err := os.Stat(folder); err == nil {
		t.Fatal("folder made")
	}
	made, err := Backup(db, folder, night.Add(time.Hour), zone)
	if err != nil || filepath.Base(made) != "gradebot-2026-11-10.db" {
		t.Fatal(made, err)
	}
	copied, _ := sql.Open("sqlite", made)
	var value string
	if err := copied.QueryRow("select value from kv where key = 'answer'").Scan(&value); err != nil || value != "42" {
		t.Fatal(value, err)
	}
	copied.Close()
	if again, _ := Backup(db, folder, night.Add(5*time.Hour), zone); again != "" { // one a day
		t.Fatal(again)
	}
	for day := 1; day < 20; day++ {
		Backup(db, folder, night.Add(time.Duration(day)*24*time.Hour+time.Hour), zone)
	}
	entries, _ := os.ReadDir(folder)
	var kept []string
	for _, e := range entries {
		kept = append(kept, e.Name())
	}
	sort.Strings(kept)
	if len(kept) != Keep || kept[len(kept)-1] != "gradebot-2026-11-29.db" {
		t.Fatal(kept)
	}
}
