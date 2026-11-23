// Package backups makes a copy of the database once a day: data/backups/gradebot-<local date>.db, the last Keep of
// them.
//
// The copy is SQLite's VACUUM INTO, consistent while the bot runs, written to a temporary file first, so a copy that
// is there is whole. It is made at the first look after Hour local time, so a bot that was down at that hour makes
// it when it starts. Copy the folder off the server now and then: a copy on the same disk does not survive the disk.
package backups

import (
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/IvanIkra/gradebot/internal/store"
)

const (
	Hour  = 4 // local time: the copy waits for the night's quiet
	Keep  = 14
	Check = time.Hour // between looks
)

// Backup makes today's copy when it is due and missing, and drops the old ones beyond Keep: the new file, or "".
func Backup(db *store.Store, folder string, moment time.Time, zone *time.Location) (string, error) {
	local := moment.In(zone)
	target := filepath.Join(folder, "gradebot-"+local.Format("2006-01-02")+".db")
	if local.Hour() < Hour {
		return "", nil
	}
	if _, err := os.Stat(target); err == nil {
		return "", nil
	}
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return "", err
	}
	partial := target[:len(target)-len(".db")] + ".part"
	_ = os.Remove(partial)
	if _, err := db.TryExec("vacuum into ?", partial); err != nil {
		_ = os.Remove(partial)
		return "", err
	}
	if err := os.Rename(partial, target); err != nil {
		return "", err
	}
	old, _ := filepath.Glob(filepath.Join(folder, "gradebot-*.db"))
	sort.Strings(old)
	if len(old) > Keep {
		for _, path := range old[:len(old)-Keep] {
			_ = os.Remove(path)
		}
	}
	slog.Info("database copied", "to", target)
	return target, nil
}
