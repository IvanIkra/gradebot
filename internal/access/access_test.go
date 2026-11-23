package access

import (
	"strings"
	"testing"
	"time"

	"github.com/IvanIkra/gradebot/internal/clock"
	"github.com/IvanIkra/gradebot/internal/store"
)

var (
	petr = NewUser{2001, "Пётр", "petr_example", "ru"}
	anna = NewUser{2002, "Анна", "", "ru"}
)

func setup(t *testing.T) (*Access, *store.Store, *clock.Fake) {
	db := store.MustOpen(":memory:")
	t.Cleanup(func() { db.Close() })
	c := clock.NewFake()
	return New(db, 7, c.Now), db, c
}

func TestCodeShapeAndNormalising(t *testing.T) {
	code := NewCode()
	if len(code) != 7 || code[3] != '-' || strings.ContainsAny(strings.ReplaceAll(code, "-", ""), "01ILO") {
		t.Fatal(code)
	}
	if Normalise(" k7q f-29 ") != "K7QF29" || CodeHash("k7q f29") != CodeHash("K7Q-F29") {
		t.Fatal("normalise")
	}
}

func TestRedeemOnce(t *testing.T) {
	a, db, _ := setup(t)
	id, code := a.Invite("Петя")
	for _, v := range store.Select[Invite](db, "select * from invites") {
		if strings.Contains(v.CodeHash+v.Note, Normalise(code)) {
			t.Fatal("the code is kept")
		}
	}
	inv, ok := a.Redeem(strings.ReplaceAll(strings.ToLower(code), "-", " "), petr)
	if !ok || inv.ID != id || inv.Note != "Петя" {
		t.Fatal(inv)
	}
	if u, _ := a.User(petr.TgID); u.InviteID != id {
		t.Fatal(u)
	}
	if _, ok := a.Redeem(code, anna); ok {
		t.Fatal("twice")
	}
	if _, ok := a.User(anna.TgID); ok || len(a.OpenInvites()) != 0 {
		t.Fatal("anna")
	}
}

func TestExpiredAndRevokedCodes(t *testing.T) {
	a, _, c := setup(t)
	_, old := a.Invite("")
	c.Advance(7*24*time.Hour + time.Second)
	if _, ok := a.Redeem(old, petr); ok {
		t.Fatal("expired")
	}
	revokedID, revoked := a.Invite("")
	if !a.Revoke(revokedID) || a.Revoke(revokedID) {
		t.Fatal("revoke")
	}
	if _, ok := a.Redeem(revoked, petr); ok {
		t.Fatal("revoked")
	}
	liveID, _ := a.Invite("Аня")
	if open := a.OpenInvites(); len(open) != 1 || open[0].ID != liveID {
		t.Fatal(open)
	}
}

func TestWrongCodesLockForAnHour(t *testing.T) {
	a, _, c := setup(t)
	for range WrongLimit - 1 {
		a.Redeem("AAAA-AAAA", petr)
	}
	if a.Locked(petr.TgID) {
		t.Fatal("early")
	}
	a.Redeem("AAAA-AAAA", petr)
	if !a.Locked(petr.TgID) || a.Locked(anna.TgID) {
		t.Fatal("lock")
	}
	c.Advance(time.Hour + time.Second)
	if a.Locked(petr.TgID) {
		t.Fatal("still")
	}
}

func TestUsersAndDelete(t *testing.T) {
	a, _, _ := setup(t)
	_, code := a.Invite("Петя")
	a.Redeem(code, petr)
	a.UpdateProfile(petr.TgID, "Пётр Петров", "")
	users := a.Users()
	if len(users) != 1 || users[0].TgName != "Пётр Петров" || users[0].Username != "" || users[0].Note != "Петя" {
		t.Fatal(users)
	}
	if !a.Delete(petr.TgID) || a.Delete(petr.TgID) || len(a.Users()) != 0 {
		t.Fatal("delete")
	}
}

func TestLongNoteIsCut(t *testing.T) {
	a, _, _ := setup(t)
	a.Invite(strings.Repeat("x", 100) + "   y")
	if n := len([]rune(a.OpenInvites()[0].Note)); n != 40 {
		t.Fatal(n)
	}
}
