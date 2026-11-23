package profile

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/IvanIkra/gradebot/internal/access"
)

func TestStudent(t *testing.T) {
	for in, want := range map[string]string{"  Петров   Пётр ": "Петров Пётр",
		"Иванова-Петрова Анна-Мария Ивановна": "Иванова-Петрова Анна-Мария Ивановна", "O'Neil Kate": "O'Neil Kate",
		"Петров П.": "Петров П."} {
		if got, err := Student(in); err != nil || got != want {
			t.Error(in, got, err)
		}
	}
	for _, bad := range []string{"Петров", "Петров 123", "<b>Петров</b> Пётр", "Петров -Пётр",
		strings.Repeat("x ", 60)} {
		if _, err := Student(bad); err == nil {
			t.Error(bad)
		}
	}
}

func TestQuiet(t *testing.T) {
	if h, _ := Quiet("23-8"); *h != [2]int{23, 8} {
		t.Fatal(h)
	}
	if h, _ := Quiet(" 22:00 – 07:00 "); *h != [2]int{22, 7} {
		t.Fatal(h)
	}
	for _, off := range []string{"OFF", "выкл"} {
		if h, err := Quiet(off); h != nil || err != nil {
			t.Fatal(off)
		}
	}
	for _, bad := range []string{"24-8", "8-8", "night", "23"} {
		if _, err := Quiet(bad); err == nil {
			t.Error(bad)
		}
	}
}

func TestIsQuietAcrossMidnightAndWithinADay(t *testing.T) {
	zone, _ := time.LoadLocation("Europe/Moscow")
	at := func(hour int) time.Time { return time.Date(2026, 11, 9, hour, 30, 0, 0, zone) }
	hours := func(user access.User) []int {
		var out []int
		for h := range 24 {
			if IsQuiet(user, at(h), zone) {
				out = append(out, h)
			}
		}
		return out
	}
	pair := func(a, b int) access.User { return access.User{QuietFrom: &a, QuietTo: &b} }
	night := pair(23, 8)
	if got := hours(night); !reflect.DeepEqual(got, []int{0, 1, 2, 3, 4, 5, 6, 7, 23}) {
		t.Fatal(got)
	}
	if got := hours(pair(13, 15)); !reflect.DeepEqual(got, []int{13, 14}) {
		t.Fatal(got)
	}
	if IsQuiet(access.User{}, at(3), zone) {
		t.Fatal("off")
	}
	if !IsQuiet(night, time.Date(2026, 11, 9, 21, 0, 0, 0, time.UTC), zone) {
		t.Fatal("moscow")
	}
	if Hours(night) != "23:00–8:00" || Hours(access.User{}) != "" {
		t.Fatal(Hours(night))
	}
}
