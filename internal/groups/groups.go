// Package groups keeps groups of users and their shared sheets. The owner makes groups and gives each its sheets; a
// user is in one group or none, picked at registration and changed with /group. A group that came with the invite
// code is fixed: only the owner moves such a user.
//
// Everyone in a group follows its sheets: when they join it and when the owner adds a sheet to it. A user may still
// unfollow a group sheet. Leaving a group unfollows its sheets, except those the new group has too; the grades of
// those sheets go with the subscriptions. A sheet may be in several groups. A sheet that is in no group and that
// nobody follows is dropped (Sheets.DropOrphans).
package groups

import (
	"errors"
	"fmt"
	"strings"

	"github.com/IvanIkra/gradebot/internal/access"
	"github.com/IvanIkra/gradebot/internal/clock"
	"github.com/IvanIkra/gradebot/internal/sheets"
	"github.com/IvanIkra/gradebot/internal/store"
)

const NameChars = 20

// Error is a bad group name, or no such group.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// IsError tells a group error.
func IsError(err error) bool {
	var e *Error
	return errors.As(err, &e)
}

// Name checks a group's name: one word, since /invite and /groupsheet take it in front of the rest.
func Name(text string) (string, error) {
	value := strings.TrimSpace(text)
	if value == "" || len(strings.Fields(value)) != 1 || len([]rune(value)) > NameChars ||
		strings.HasPrefix(value, "/") || strings.HasPrefix(value, "http") {
		return "", &Error{fmt.Sprintf("a group's name is one word of at most %d characters", NameChars)}
	}
	return value, nil
}

// Group is a row of groups; All counts its users and sheets.
type Group struct {
	ID        int64
	Name      string
	CreatedAt string
	Users     int
	Sheets    int
}

// Groups keeps the groups.
type Groups struct {
	db     *store.Store
	sheets *sheets.Sheets
	Now    clock.Now
}

// New makes Groups.
func New(db *store.Store, s *sheets.Sheets, now clock.Now) *Groups {
	if now == nil {
		now = clock.NowUTC
	}
	return &Groups{db: db, sheets: s, Now: now}
}

// All is every group with its users and sheets counted, by name.
func (g *Groups) All() []Group {
	return store.Select[Group](g.db, "select g.*, (select count(*) from users u where u.grp = g.id) users, "+
		"(select count(*) from group_sheets s where s.grp = g.id) sheets from groups g order by g.name collate nocase")
}

// Get is a group; false for 0 or a group that is gone.
func (g *Groups) Get(gid int64) (Group, bool) {
	if gid == 0 {
		return Group{}, false
	}
	return store.Get[Group](g.db, "select * from groups where id = ?", gid)
}

// Find is a group by its name, without case.
func (g *Groups) Find(name string) (Group, bool) {
	return store.Get[Group](g.db, "select * from groups where name = ? collate nocase", strings.TrimSpace(name))
}

// Create makes a group.
func (g *Groups) Create(name string) (Group, error) {
	name, err := Name(name)
	if err != nil {
		return Group{}, err
	}
	if _, ok := g.Find(name); ok {
		return Group{}, &Error{"a group with this name exists"}
	}
	var gid int64
	g.db.Tx(func() {
		gid = g.db.Insert("insert into groups(name, created_at) values(?, ?)", name, clock.ISO(g.Now()))
	})
	row, _ := g.Get(gid)
	return row, nil
}

// Rename renames a group.
func (g *Groups) Rename(gid int64, name string) (Group, error) {
	name, err := Name(name)
	if err != nil {
		return Group{}, err
	}
	if found, ok := g.Find(name); ok && found.ID != gid {
		return Group{}, &Error{"a group with this name exists"}
	}
	g.db.Tx(func() { g.db.Exec("update groups set name = ? where id = ?", name, gid) })
	row, _ := g.Get(gid)
	return row, nil
}

// Delete removes a group: its users are in no group then and keep their sheets; codes that carried it carry none.
func (g *Groups) Delete(gid int64) bool {
	var n int64
	g.db.Tx(func() {
		g.db.Exec("update users set rank = min(rank, 1) where grp = ?", gid) // no group to run
		n = g.db.Exec("delete from groups where id = ?", gid)
	})
	g.sheets.DropOrphans()
	return n == 1
}

// --- sheets -----------------------------------------------------------------------------------------------------

// SheetsOf are a group's sheets with their followers counted.
func (g *Groups) SheetsOf(gid int64) []sheets.Sheet {
	return store.Select[sheets.Sheet](g.db, "select s.*, (select count(*) from subscriptions u where u.sheet = "+
		"s.id) followers from sheets s join group_sheets g on g.sheet = s.id where g.grp = ? order by s.id", gid)
}

// AddSheet gives a group a sheet and has everyone in it follow it: (the sheet, how many users started to). A nil
// course leaves the sheet's course as it is.
func (g *Groups) AddSheet(gid int64, url string, by int64, courseText *string) (sheets.Sheet, int, error) {
	var sheet, n int64
	err := g.db.TxErr(func() error {
		var err error
		if sheet, err = g.sheets.Upsert(url, by, ""); err != nil {
			return err
		}
		g.db.Exec("insert or ignore into group_sheets(grp, sheet) values(?, ?)", gid, sheet)
		if courseText != nil {
			c, err := sheets.Course(*courseText)
			if err != nil {
				return err
			}
			g.db.Exec("update sheets set course = ? where id = ?", c, sheet)
		}
		n = g.db.Exec("insert or ignore into subscriptions(tg_id, sheet, added_at) select tg_id, ?, ? from users "+
			"where grp = ?", sheet, clock.ISO(g.Now()), gid)
		return nil
	})
	if err != nil {
		return sheets.Sheet{}, 0, err
	}
	row, _ := g.sheets.Get(sheet)
	return row, int(n), nil
}

// RemoveSheet: no longer the group's; who joins it won't get it, who follows it keeps it.
func (g *Groups) RemoveSheet(gid, sheet int64) bool {
	var n int64
	g.db.Tx(func() { n = g.db.Exec("delete from group_sheets where grp = ? and sheet = ?", gid, sheet) })
	g.sheets.DropOrphans()
	return n == 1
}

// --- users ------------------------------------------------------------------------------------------------------

// Fix: the user's group came with their invite code, they don't change it themselves.
func (g *Groups) Fix(tgID int64) {
	g.db.Tx(func() { g.db.Exec("update users set grp_fixed = 1 where tg_id = ?", tgID) })
}

// Fixed tells that only the owner moves the user: their group came with the code and is still there.
func Fixed(user access.User) bool {
	return user.GrpFixed && user.Grp != 0
}

// Join moves a user to a group, or to none (0): (sheets of the old group unfollowed, the new group's sheets they
// started to follow).
func (g *Groups) Join(tgID, gid int64) (int, []int64, error) {
	old, ok := store.Scalar[int64](g.db, "select grp from users where tg_id = ?", tgID)
	if !ok || old == gid {
		return 0, nil, nil
	}
	if gid != 0 {
		if _, ok := g.Get(gid); !ok {
			return 0, nil, &Error{"no such group"}
		}
	}
	var left int64
	var added []int64
	g.db.Tx(func() {
		if old != 0 {
			left = g.db.Exec("delete from subscriptions where tg_id = ? and sheet in (select sheet from "+
				"group_sheets where grp = ?) and sheet not in (select sheet from group_sheets where grp = ?)", tgID,
				old, gid)
		}
		// an admin runs the group they are in: moved to another one they are only trusted (the first group, from
		// the code, keeps the rank the code gave)
		g.db.Exec("update users set rank = case when grp is null then rank else min(rank, 1) end, grp = ? "+
			"where tg_id = ?", store.NullID(gid), tgID)
		added = store.Column[int64](g.db, "select sheet from group_sheets where grp = ? and sheet not in "+
			"(select sheet from subscriptions where tg_id = ?) order by sheet", gid, tgID)
		for _, sheet := range added {
			g.db.Exec("insert into subscriptions(tg_id, sheet, added_at) values(?, ?, ?)", tgID, sheet,
				clock.ISO(g.Now()))
		}
	})
	g.sheets.DropOrphans()
	return int(left), added, nil
}
