// Package profile checks what a user sets about themselves: the full name the sheets have, quiet hours and the
// language.
package profile

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/IvanIkra/gradebot/internal/access"
)

const NameChars = 100

// nameWord is letters, inner hyphens and apostrophes; "А." too.
var nameWord = regexp.MustCompile(`^[\p{L}\p{Nl}\p{No}]+(?:[-'’][\p{L}\p{Nl}\p{No}]+)*\.?$`)

// QuietDefault is the quiet hours a new user has.
var QuietDefault = [2]int{23, 8}

// Error is what the user wrote wrong.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// Student is '  Петров   Пётр ' -> 'Петров Пётр'. Two words at least: one would match every namesake in a sheet.
func Student(text string) (string, error) {
	words := strings.Fields(text)
	value := strings.Join(words, " ")
	ok := len([]rune(value)) <= NameChars && len(words) >= 2
	for _, w := range words {
		ok = ok && nameWord.MatchString(w)
	}
	if !ok {
		return "", &Error{"a full name: two words or more, letters only"}
	}
	return value, nil
}

var hoursRe = regexp.MustCompile(`^(\d{1,2})(?::00)?\s*[-–—]\s*(\d{1,2})(?::00)?$`)

// Quiet reads '23-8', '22:00–07:00' as (from, to); 'off' is nil.
func Quiet(text string) (*[2]int, error) {
	value := strings.ToLower(strings.TrimSpace(text))
	switch value {
	case "off", "выкл", "нет", "no":
		return nil, nil
	}
	found := hoursRe.FindStringSubmatch(value)
	if found == nil {
		return nil, &Error{"hours as 23-8, or off"}
	}
	start, _ := strconv.Atoi(found[1])
	end, _ := strconv.Atoi(found[2])
	if start > 23 || end > 23 || start == end {
		return nil, &Error{"hours from 0 to 23, the start and the end differ"}
	}
	return &[2]int{start, end}, nil
}

// IsQuiet tells the user's quiet hours at moment: messages then go without a sound.
func IsQuiet(user access.User, moment time.Time, zone *time.Location) bool {
	if user.QuietFrom == nil || user.QuietTo == nil {
		return false
	}
	start, end, hour := *user.QuietFrom, *user.QuietTo, moment.In(zone).Hour()
	if start < end {
		return start <= hour && hour < end
	}
	return hour >= start || hour < end
}

// Hours is the quiet hours as shown: "23:00–8:00", or "" when off.
func Hours(user access.User) string {
	if user.QuietFrom == nil || user.QuietTo == nil {
		return ""
	}
	return fmt.Sprintf("%d:00–%d:00", *user.QuietFrom, *user.QuietTo)
}
