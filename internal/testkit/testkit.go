// Package testkit sets up what tests share: an in-memory database, a fake clock, users who came in with a code.
package testkit

import (
	"testing"
	"time"

	"github.com/IvanIkra/gradebot/internal/access"
	"github.com/IvanIkra/gradebot/internal/clock"
	"github.com/IvanIkra/gradebot/internal/sheets"
	"github.com/IvanIkra/gradebot/internal/store"
)

// Owner is the owner's Telegram id in tests.
const Owner = int64(1000)

// Zone is the bot's time zone in tests.
var Zone = mustZone("Europe/Moscow")

func mustZone(name string) *time.Location {
	zone, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return zone
}

// Env is a database with the stores on it.
type Env struct {
	DB     *store.Store
	Clock  *clock.Fake
	Access *access.Access
	Sheets *sheets.Sheets
}

// New makes an Env closed when the test ends.
func New(t testing.TB) *Env {
	db := store.MustOpen(":memory:")
	t.Cleanup(func() { db.Close() })
	c := clock.NewFake()
	return &Env{DB: db, Clock: c, Access: access.New(db, 7, c.Now), Sheets: sheets.New(db, c.Now)}
}

// AddUser is a user who came in with a fresh code and set a full name ("" for none).
func (e *Env) AddUser(tgID int64, student, lang, name string) access.User {
	if lang == "" {
		lang = "ru"
	}
	if name == "" {
		name = "Тест"
	}
	_, code := e.Access.Invite("")
	e.Access.Redeem(code, access.NewUser{TgID: tgID, TgName: name, Lang: lang})
	if student != "" {
		e.Access.SetStudent(tgID, student)
	}
	user, _ := e.Access.User(tgID)
	return user
}

// Days is n days.
func Days(n float64) time.Duration { return time.Duration(n * 24 * float64(time.Hour)) }
