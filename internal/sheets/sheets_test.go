package sheets_test

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/IvanIkra/gradebot/internal/access"
	"github.com/IvanIkra/gradebot/internal/clock"
	"github.com/IvanIkra/gradebot/internal/groups"
	"github.com/IvanIkra/gradebot/internal/sheets"
	"github.com/IvanIkra/gradebot/internal/store"
)

const (
	calc = "https://docs.google.com/spreadsheets/d/1CalcExampleSheetId_0123456789abc/edit#gid=0"
	alg  = "https://docs.google.com/spreadsheets/u/1/d/1AlgebraExampleSheetId-0123456789/edit"
	petr = int64(2001)
	anna = int64(2002)
)

func setup(t *testing.T) (*sheets.Sheets, *access.Access, *store.Store) {
	db := store.MustOpen(":memory:")
	t.Cleanup(func() { db.Close() })
	c := clock.NewFake()
	a := access.New(db, 7, c.Now)
	for _, u := range []access.NewUser{{TgID: petr, TgName: "Пётр", Lang: "ru"}, {TgID: anna, TgName: "Анна",
		Lang: "ru"}} {
		_, code := a.Invite("")
		a.Redeem(code, u)
	}
	return sheets.New(db, c.Now), a, db
}

func TestParseURL(t *testing.T) {
	sid, link, err := sheets.ParseURL(calc)
	if err != nil || sid != "1CalcExampleSheetId_0123456789abc" ||
		link != "https://docs.google.com/spreadsheets/d/1CalcExampleSheetId_0123456789abc" {
		t.Fatal(sid, link, err)
	}
	if sid, _, _ := sheets.ParseURL(alg); sid != "1AlgebraExampleSheetId-0123456789" {
		t.Fatal(sid)
	}
	for _, bad := range []string{"https://example.com/spreadsheets/d/1CalcExampleSheetId_0123456789abc",
		"http://docs.google.com/spreadsheets/d/1CalcExampleSheetId_0123456789abc", "calc"} {
		if _, _, err := sheets.ParseURL(bad); !sheets.IsError(err) {
			t.Error(bad)
		}
	}
}

func TestFollowOneSheetForAll(t *testing.T) {
	s, _, _ := setup(t)
	sheet, isNew, _ := s.Follow(petr, calc, "")
	if !isNew || sheets.Name(sheet) != "…789abc" {
		t.Fatal(sheet)
	}
	again, isNew, _ := s.Follow(anna, strings.Replace(calc, "#gid=0", "?usp=sharing", 1), "")
	if !isNew || again.ID != sheet.ID {
		t.Fatal(again)
	}
	f := s.Followers(sheet.ID)
	slices.Sort(f)
	if !reflect.DeepEqual(f, []int64{petr, anna}) {
		t.Fatal(f)
	}
	if _, isNew, _ := s.Follow(petr, calc, ""); isNew {
		t.Fatal("twice")
	}
}

func TestUnfollowDropsSheetsNobodyFollows(t *testing.T) {
	s, _, _ := setup(t)
	sheet, _, _ := s.Follow(petr, calc, "")
	if !s.Unfollow(petr, sheet.ID) {
		t.Fatal("unfollow")
	}
	if _, ok := s.Get(sheet.ID); ok || s.Unfollow(petr, sheet.ID) {
		t.Fatal("kept")
	}
}

func TestLimit(t *testing.T) {
	s, _, db := setup(t)
	for n := range sheets.PerUser {
		if _, _, err := s.Follow(petr, fmt.Sprintf("https://docs.google.com/spreadsheets/d/%s%04d",
			strings.Repeat("x", 20), n), ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.Follow(petr, calc, ""); !sheets.IsError(err) {
		t.Fatal(err)
	}
	if n, _ := store.Scalar[int](db, "select count(*) from sheets"); n != sheets.PerUser {
		t.Fatal("the refused sheet stayed", n)
	}
}

func TestCourses(t *testing.T) {
	s, _, _ := setup(t)
	s.Follow(petr, calc, "")
	if sheet, _ := s.SetCourse(calc, "  Мат  ан "); sheet.Course != "Мат ан" {
		t.Fatal(sheet)
	}
	if _, err := s.SetCourse(calc, strings.Repeat("x", 11)); !sheets.IsError(err) {
		t.Fatal(err)
	}
	if _, err := s.SetCourse(alg, "Алг"); !sheets.IsError(err) {
		t.Fatal(err)
	}
}

func TestDeletingAUserDropsTheirOwnSheets(t *testing.T) {
	s, a, db := setup(t)
	g := groups.New(db, s, nil)
	group, _ := g.Create("A1")
	g.Join(petr, group.ID)
	g.Join(anna, group.ID)
	own, _, _ := s.Follow(petr, calc, "")
	shared, _, _ := g.AddSheet(group.ID, alg, 1000, nil)
	a.Delete(petr)
	s.DropOrphans()
	if _, ok := s.Get(own.ID); ok {
		t.Fatal("own kept")
	}
	if _, ok := s.Get(shared.ID); !ok || !reflect.DeepEqual(s.Followers(shared.ID), []int64{anna}) {
		t.Fatal("shared")
	}
}

func TestACourseFromTheTitleTakesWholeWords(t *testing.T) {
	of := func(title, course string) string {
		return sheets.CourseOf(sheets.Sheet{Course: course, Title: title, SheetID: "1AbCdEfGhIjKlMnOpQrStUvWx"})
	}
	for _, c := range [][3]string{
		{"Ведомость Алгебра ИВТ 101", "", "Алгебра"},
		{"Физика ФИИТ 105", "", "Физика"},
		{"ТВ ИВТ 25/26 - Успеваемость", "", "ТВ ИВТ"},
		{"Дискретная математика", "", "Дискретн"},
		{"Алгебра", "Алг", "Алг"},
		{"", "", "1AbCdEfG"},
	} {
		if got := of(c[0], c[1]); got != c[2] {
			t.Errorf("%q: %q", c[0], got)
		}
	}
}
