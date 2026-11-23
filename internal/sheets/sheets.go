// Package sheets keeps the Google Sheets users follow. A sheet is one row for everyone who follows it, so it is
// downloaded and laid out once; `subscriptions` say who follows what.
//
// A member's sheet the bot does not read yet is a request until the owner approves it (access.Member); it counts
// towards the user's limit while it waits.
//
// Group sheets are the owner's, one set per group (package groups). A sheet nobody follows and that is in no group
// is dropped.
package sheets

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/IvanIkra/gradebot/internal/access"
	"github.com/IvanIkra/gradebot/internal/clock"
	"github.com/IvanIkra/gradebot/internal/store"
)

// GoogleSheet matches a Google Sheets link and its id.
var GoogleSheet = regexp.MustCompile(`^https://docs\.google\.com/spreadsheets/(?:u/\d+/)?d/([A-Za-z0-9_-]{20,80})`)

const (
	CourseChars     = 10 // the course in front of a work's label: "Calc HW 3" (23 characters for both)
	CourseFromTitle = 8  // characters of the sheet's title that make its course when the owner set none
	PerUser         = 20
	StaleAfter      = 12 * time.Hour
	// Reader is a user whose rows are looked for (`users u`; the parameter is now): a full name, access, not
	// blocked the bot.
	Reader = "u.student is not null and not u.paused and (u.expires_at is null or u.expires_at > ?)"
)

// genericWords of a title say nothing about the course: "Ведомость Алгебра ИВТ 101" is the course "Алгебра".
var genericWords = map[string]bool{"ведомость": true, "успеваемость": true, "оценки": true, "журнал": true,
	"таблица": true, "баллы": true, "рейтинг": true, "итоги": true, "grades": true, "gradebook": true,
	"scores": true, "marks": true, "sheet": true, "results": true}

// Error is a bad link or course, or too many sheets.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// IsError tells a sheet error.
func IsError(err error) bool {
	var e *Error
	return errors.As(err, &e)
}

// Sheet is a row of sheets, with what some queries add.
type Sheet struct {
	ID         int64
	SheetID    string
	URL        string
	Title      string
	Course     string
	AddedBy    int64
	AddedAt    string
	OkAt       string
	Error      string
	ErrorSince string
	Told       bool
	// Followed: how the user's rows were read.
	ReadAt     string
	UserError  string
	Tabs       int
	BaselineAt string
	// Every, of a group: how many follow it.
	Followers int
}

// ParseURL gives (sheet id, the link to keep): the link up to the id, so the same sheet is one sheet whatever its
// tail.
func ParseURL(url string) (string, string, error) {
	found := GoogleSheet.FindStringSubmatch(strings.TrimSpace(url))
	if found == nil {
		return "", "", &Error{"not a Google Sheets link"}
	}
	return found[1], "https://docs.google.com/spreadsheets/d/" + found[1], nil
}

// A sheet's course is its short name: what the bot calls it in /sheets and on buttons, and its hashtag. The owner
// sets it with /rename; a user who brings a sheet new to the bot may give it with /sheet.

// Course checks a course: spaces squeezed, at most CourseChars characters.
func Course(text string) (string, error) {
	value := strings.Join(strings.Fields(text), " ")
	if len([]rune(value)) > CourseChars {
		return "", &Error{fmt.Sprintf("a course has at most %d characters", CourseChars)}
	}
	return value, nil
}

var (
	notWord = regexp.MustCompile(`[^\p{L}\p{N}_/]+`)
	letter  = regexp.MustCompile(`[\p{L}\p{Nl}\p{No}]`)
)

// CourseOf is the course in front of the sheet's works: the owner's, else whole words from the title (no group
// numbers, no "Ведомость") up to CourseFromTitle characters, or the start of the first such word.
func CourseOf(s Sheet) string {
	if s.Course != "" {
		return s.Course
	}
	var words []string
	for _, w := range notWord.Split(s.Title, -1) {
		if letter.MatchString(w) && !genericWords[strings.ToLower(w)] {
			words = append(words, w)
		}
	}
	out := ""
	for _, word := range words {
		next := strings.TrimSpace(out + " " + word)
		if len([]rune(next)) > CourseFromTitle {
			break
		}
		out = next
	}
	if out != "" {
		return out
	}
	start := s.Title
	if len(words) > 0 {
		start = words[0]
	} else if start == "" {
		start = s.SheetID
	}
	r := []rune(start)
	return strings.TrimSpace(string(r[:min(len(r), CourseFromTitle)]))
}

// Name is how the bot calls a sheet: its course, its title, or the end of its id while neither is known.
func Name(s Sheet) string {
	if s.Course != "" {
		return s.Course
	}
	if s.Title != "" {
		return s.Title
	}
	return "…" + s.SheetID[max(0, len(s.SheetID)-6):]
}

// Sheets keeps the sheets.
type Sheets struct {
	db  *store.Store
	Now clock.Now
}

// New makes Sheets.
func New(db *store.Store, now clock.Now) *Sheets {
	if now == nil {
		now = clock.NowUTC
	}
	return &Sheets{db: db, Now: now}
}

// Upsert gives the sheet's row id, adding it when it is new to the bot: then with courseText as its course.
func (s *Sheets) Upsert(url string, by int64, courseText string) (int64, error) {
	sid, link, err := ParseURL(url)
	if err != nil {
		return 0, err
	}
	if id, ok := store.Scalar[int64](s.db, "select id from sheets where sheet_id = ?", sid); ok {
		return id, nil
	}
	c, err := Course(courseText)
	if err != nil {
		return 0, err
	}
	return s.db.Insert("insert into sheets(sheet_id, url, course, added_by, added_at) values(?, ?, ?, ?, ?)", sid,
		link, c, by, clock.ISO(s.Now())), nil
}

// Get is a sheet by its row id.
func (s *Sheets) Get(sheet int64) (Sheet, bool) {
	return store.Get[Sheet](s.db, "select * from sheets where id = ?", sheet)
}

// ByURL is a sheet by its link.
func (s *Sheets) ByURL(url string) (Sheet, bool) {
	sid, _, err := ParseURL(url)
	if err != nil {
		return Sheet{}, false
	}
	return store.Get[Sheet](s.db, "select * from sheets where sheet_id = ?", sid)
}

// --- a user's sheets --------------------------------------------------------------------------------------------

// count is the user's sheets and requests: the limit is on both.
func (s *Sheets) count(tgID int64) int {
	n, _ := store.Scalar[int](s.db, "select (select count(*) from subscriptions where tg_id = ?) + "+
		"(select count(*) from sheet_requests where tg_id = ?) n", tgID, tgID)
	return n
}

// Follow gives (the sheet, true when it is new to the user). The course is set only for a sheet new to the bot:
// after that it is the owner's. An Error for a bad link or course, or too many sheets.
func (s *Sheets) Follow(tgID int64, url, courseText string) (Sheet, bool, error) {
	var sheet int64
	isNew := false
	err := s.db.TxErr(func() error {
		var err error
		if sheet, err = s.Upsert(url, tgID, courseText); err != nil {
			return err
		}
		if store.Has(s.db, "select 1 from subscriptions where tg_id = ? and sheet = ?", tgID, sheet) {
			return nil
		}
		row, _ := s.Get(sheet)
		s.db.Exec("delete from sheet_requests where tg_id = ? and sheet_id = ?", tgID, row.SheetID)
		if s.count(tgID) >= PerUser {
			return &Error{fmt.Sprintf("at most %d sheets", PerUser)}
		}
		s.db.Exec("insert into subscriptions(tg_id, sheet, added_at) values(?, ?, ?)", tgID, sheet,
			clock.ISO(s.Now()))
		isNew = true
		return nil
	})
	if err != nil {
		return Sheet{}, false, err
	}
	row, _ := s.Get(sheet)
	return row, isNew, nil
}

// Unfollow stops following a sheet.
func (s *Sheets) Unfollow(tgID, sheet int64) bool {
	var n int64
	s.db.Tx(func() {
		n = s.db.Exec("delete from subscriptions where tg_id = ? and sheet = ?", tgID, sheet)
		s.dropOrphans()
	})
	return n == 1
}

// Followed are the user's sheets, with how their rows were read: ReadAt, UserError, Tabs, BaselineAt.
func (s *Sheets) Followed(tgID int64) []Sheet {
	return store.Select[Sheet](s.db, "select s.*, u.read_at, u.error user_error, u.tabs, u.baseline_at "+
		"from sheets s join subscriptions u on u.sheet = s.id where u.tg_id = ? order by exists("+
		"select 1 from group_sheets g join users r on r.grp = g.grp where g.sheet = s.id "+
		"and r.tg_id = u.tg_id) desc, s.id", tgID)
}

// --- requests: a member's sheets new to the bot -----------------------------------------------------------------

// Request is a row of sheet_requests.
type Request struct {
	ID        int64
	TgID      int64
	SheetID   string
	URL       string
	Course    string
	CreatedAt string
}

// Ask gives (the request, true when it is new), with the course the sheet gets if it is approved. An Error for a
// bad link or course, or too many sheets.
func (s *Sheets) Ask(tgID int64, url, courseText string) (Request, bool, error) {
	sid, link, err := ParseURL(url)
	if err != nil {
		return Request{}, false, err
	}
	wanted, err := Course(courseText)
	if err != nil {
		return Request{}, false, err
	}
	if row, ok := store.Get[Request](s.db, "select * from sheet_requests where tg_id = ? and sheet_id = ?", tgID,
		sid); ok {
		return row, false, nil
	}
	if s.count(tgID) >= PerUser {
		return Request{}, false, &Error{fmt.Sprintf("at most %d sheets", PerUser)}
	}
	var rid int64
	s.db.Tx(func() {
		rid = s.db.Insert("insert into sheet_requests(tg_id, sheet_id, url, course, created_at) "+
			"values(?, ?, ?, ?, ?)", tgID, sid, link, wanted, clock.ISO(s.Now()))
	})
	row, _ := s.Request(rid)
	return row, true, nil
}

func (s *Sheets) Request(rid int64) (Request, bool) {
	return store.Get[Request](s.db, "select * from sheet_requests where id = ?", rid)
}

// Requests are the waiting requests, of one user or of everyone (0), oldest first.
func (s *Sheets) Requests(tgID int64) []Request {
	if tgID != 0 {
		return store.Select[Request](s.db, "select * from sheet_requests where tg_id = ? order by id", tgID)
	}
	return store.Select[Request](s.db, "select * from sheet_requests order by id")
}

// Approve follows the requested sheet: (the request, the sheet), or false when it is gone (handled, the user left).
func (s *Sheets) Approve(rid int64) (Request, Sheet, bool, error) {
	found, ok := s.Request(rid)
	if !ok {
		return Request{}, Sheet{}, false, nil
	}
	sheet, _, err := s.Follow(found.TgID, found.URL, found.Course) // drops the request
	if err != nil {
		return found, Sheet{}, true, err
	}
	return found, sheet, true, nil
}

// DropRequest declines or cancels a request: the request, or false when it is gone (or is not this user's; tgID 0
// is anyone's).
func (s *Sheets) DropRequest(rid, tgID int64) (Request, bool) {
	found, ok := s.Request(rid)
	if !ok || (tgID != 0 && found.TgID != tgID) {
		return Request{}, false
	}
	s.db.Tx(func() { s.db.Exec("delete from sheet_requests where id = ?", rid) })
	return found, true
}

// Followers are who follow a sheet.
func (s *Sheets) Followers(sheet int64) []int64 {
	return store.Column[int64](s.db, "select tg_id from subscriptions where sheet = ?", sheet)
}

// --- reading ----------------------------------------------------------------------------------------------------

// Readers are the followers whose rows are looked for: those with a full name and access who have not blocked the
// bot.
func (s *Sheets) Readers(sheet int64) []access.User {
	return store.Select[access.User](s.db, "select u.* from users u join subscriptions s "+
		"on s.tg_id = u.tg_id where s.sheet = ? and "+Reader+" order by u.tg_id", sheet, clock.ISO(s.Now()))
}

// Every is every sheet with the number of its followers: the owner's /status.
func (s *Sheets) Every() []Sheet {
	return store.Select[Sheet](s.db, "select s.*, (select count(*) from subscriptions u where u.sheet = s.id) "+
		"followers from sheets s order by exists(select 1 from group_sheets g where g.sheet = s.id) desc, s.id")
}

// ToRead are the sheets with at least one reader: the others are not downloaded.
func (s *Sheets) ToRead() []Sheet {
	return store.Select[Sheet](s.db, "select * from sheets s where exists (select 1 from subscriptions x join "+
		"users u on u.tg_id = x.tg_id where x.sheet = s.id and "+Reader+") order by id", clock.ISO(s.Now()))
}

// ReaderName is the full name a user's rows in a sheet are looked for by now; false when they no longer follow it.
func (s *Sheets) ReaderName(tgID, sheet int64) (string, bool) {
	return store.Scalar[string](s.db, "select u.student from users u join subscriptions s on s.tg_id = u.tg_id "+
		"where u.tg_id = ? and s.sheet = ?", tgID, sheet)
}

// Work is a work of the sheet's plan.
type Work struct {
	Sheet    int64
	Key      string
	Tab      string
	Label    string
	LabelRu  string
	Max      float64
	Position int
}

// SetPlan keeps the sheet's works as its laid-out tabs have them now, all of them in their order.
func (s *Sheets) SetPlan(sheet int64, works []Work) {
	s.db.Tx(func() {
		s.db.Exec("delete from sheet_works where sheet = ?", sheet)
		for n, w := range works {
			s.db.Exec("insert into sheet_works(sheet, key, tab, label, label_ru, max, position) "+
				"values(?, ?, ?, ?, ?, ?, ?)", sheet, w.Key, w.Tab, w.Label, store.Null(w.LabelRu), w.Max, n)
		}
	})
}

// Plan is the sheet's works in order.
func (s *Sheets) Plan(sheet int64) []Work {
	return store.Select[Work](s.db, "select * from sheet_works where sheet = ? order by position", sheet)
}

func (s *Sheets) SetTitle(sheet int64, title string) {
	s.db.Tx(func() { s.db.Exec("update sheets set title = ? where id = ?", title, sheet) })
}

// ReadOK records a good read; true when the sheet had been told as failing and is back.
func (s *Sheets) ReadOK(sheet int64) bool {
	told, ok := store.Scalar[bool](s.db, "select told from sheets where id = ?", sheet)
	s.db.Tx(func() {
		s.db.Exec("update sheets set ok_at = ?, error = null, error_since = null, told = 0 where id = ?",
			clock.ISO(s.Now()), sheet)
	})
	return ok && told
}

// ReadFailed records a failed read. The moment it has been failing since, when that makes StaleAfter just now:
// told once; "" otherwise.
func (s *Sheets) ReadFailed(sheet int64, error string) string {
	row, ok := store.Get[struct {
		ErrorSince string
		Told       bool
	}](s.db, "select error_since, told from sheets where id = ?", sheet)
	if !ok {
		return ""
	}
	now := s.Now()
	since := row.ErrorSince
	if since == "" {
		since = clock.ISO(now)
	}
	stale := !row.Told && now.Sub(clock.MustParse(since)) >= StaleAfter
	s.db.Tx(func() {
		s.db.Exec("update sheets set error = ?, error_since = ?, told = ? where id = ?", error, since,
			store.Bool(row.Told || stale), sheet)
	})
	if stale {
		return since
	}
	return ""
}

// --- the owner's ------------------------------------------------------------------------------------------------

// SetCourse sets a sheet's course.
func (s *Sheets) SetCourse(url, courseText string) (Sheet, error) {
	sheet, ok := s.ByURL(url)
	if !ok {
		return Sheet{}, &Error{"nobody follows this sheet"}
	}
	c, err := Course(courseText)
	if err != nil {
		return Sheet{}, err
	}
	s.db.Tx(func() { s.db.Exec("update sheets set course = ? where id = ?", c, sheet.ID) })
	row, _ := s.Get(sheet.ID)
	return row, nil
}

func (s *Sheets) dropOrphans() {
	s.db.Exec("delete from sheets where id not in (select sheet from subscriptions) " +
		"and id not in (select sheet from group_sheets)")
}

// DropOrphans: after a user or a group is deleted, the sheets nobody follows and no group has go too.
func (s *Sheets) DropOrphans() {
	s.db.Tx(s.dropOrphans)
}
