// Package access decides who may use the bot: invite codes from the owner, the users who came in with them, and a
// limit on wrong codes.
//
// A code is six characters from an alphabet without look-alikes (no 0/O, 1/I/L), shown as XXX-XXX: 31^6, close to
// a billion, against five wrong tries an hour from one Telegram id. Only its sha256 is stored; a code is compared
// without case, spaces and dashes, so «k7q f29» works too.
//
// A code lets in one person by default, or `uses` people, or any number (0) until it expires or is revoked. It may
// give access for a while: so many days from coming in, or until a date. When that ends the user is kept but the bot
// neither reads their sheets nor writes to them, until the owner prolongs the access or deletes them. The user hears
// of the end a day before and when it comes, with a button that asks the owner to prolong it; the owner hears
// nothing else.
package access

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"strings"
	"time"
	"unicode"

	"github.com/IvanIkra/gradebot/internal/clock"
	"github.com/IvanIkra/gradebot/internal/store"
)

const (
	Alphabet    = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	CodeLength  = 6
	NoteLength  = 40
	WrongLimit  = 5         // wrong codes from one Telegram id ...
	WrongWindow = time.Hour // ... within this long make the bot silent to it for as long
)

// open is a code that still lets people in; the parameter is now.
const open = "revoked_at is null and expires_at > ? and (uses = 0 or used < uses)"

// Ranks: what a user may do without the owner. A member's sheet that the bot does not read yet waits for approval;
// a trusted user's is followed at once. A sheet the bot already reads costs nothing new: anyone follows it. An admin
// is trusted and also runs their own group; in no group they are only trusted.
const (
	Member  = 0
	Trusted = 1
	Admin   = 2
)

// Normalise is a code without case, spaces and dashes.
func Normalise(code string) string {
	var b strings.Builder
	for _, ch := range strings.ToUpper(code) {
		if unicode.IsLetter(ch) || unicode.IsNumber(ch) {
			b.WriteRune(ch)
		}
	}
	return b.String()
}

// CodeHash is what the database keeps of a code.
func CodeHash(code string) string {
	sum := sha256.Sum256([]byte(Normalise(code)))
	return hex.EncodeToString(sum[:])
}

// NewCode is a random code, XXX-XXX.
func NewCode() string {
	raw := make([]byte, CodeLength)
	for n := range raw {
		k, err := rand.Int(rand.Reader, big.NewInt(int64(len(Alphabet))))
		if err != nil {
			panic(err)
		}
		raw[n] = Alphabet[k.Int64()]
	}
	return string(raw[:3]) + "-" + string(raw[3:])
}

// NewUser is who comes in with a code.
type NewUser struct {
	TgID     int64
	TgName   string
	Username string
	Lang     string
}

// User is a row of users; NULL text is "" and a NULL id 0.
type User struct {
	TgID         int64
	TgName       string
	Username     string
	Lang         string
	Student      string // the full name as the sheets have it; "" until /name
	QuietFrom    *int
	QuietTo      *int
	Paused       bool
	JoinedAt     string
	InviteID     int64
	Grp          int64
	GrpFixed     bool
	Rank         int
	ExpiresAt    string
	AccessTold   int
	ProlongAsked bool
	Note         string // Users(): the note of the invite
	GroupName    string // Users(): the name of the group
}

// Invite is a row of invites.
type Invite struct {
	ID          int64
	CodeHash    string
	Note        string
	CreatedAt   string
	ExpiresAt   string
	Uses        int
	Used        int
	UsedBy      int64
	UsedAt      string
	RevokedAt   string
	AccessDays  int
	AccessUntil string
	Grp         int64
	Rank        int
	GroupName   string
}

// Access keeps invites and users.
type Access struct {
	db         *store.Store
	InviteDays int
	Now        clock.Now
	wrong      map[int64][]time.Time
}

// New makes an Access.
func New(db *store.Store, inviteDays int, now clock.Now) *Access {
	if now == nil {
		now = clock.NowUTC
	}
	return &Access{db: db, InviteDays: inviteDays, Now: now, wrong: map[int64][]time.Time{}}
}

// --- invites ----------------------------------------------------------------------------------------------------

// Options of a new code. Uses 0 is any number of people.
type Options struct {
	Note        string
	Group       int64
	Uses        int
	Lifetime    time.Duration // INVITE_DAYS when 0
	AccessDays  int
	AccessUntil time.Time
	Rank        int
}

// Invite is a new code for one person: (invite id, the code).
func (a *Access) Invite(note string) (int64, string) {
	return a.InviteWith(Options{Note: note, Uses: 1})
}

// InviteWith is a new code: (invite id, the code). It lands its users in the group with the rank, lets in Uses
// people (0: any number), works for Lifetime and gives access for AccessDays from coming in or until AccessUntil
// (neither: for good). The code is shown once and never stored.
func (a *Access) InviteWith(o Options) (int64, string) {
	now := a.Now()
	lifetime := o.Lifetime
	if lifetime == 0 {
		lifetime = time.Duration(a.InviteDays) * 24 * time.Hour
	}
	note := []rune(strings.Join(strings.Fields(o.Note), " "))
	note = note[:min(len(note), NoteLength)]
	var days, until any
	if o.AccessDays != 0 {
		days = o.AccessDays
	}
	if !o.AccessUntil.IsZero() {
		until = clock.ISO(o.AccessUntil)
	}
	for {
		code := NewCode()
		var id int64
		err := a.db.TryTx(func() {
			id = a.db.Insert("insert into invites(code_hash, note, created_at, expires_at, grp, uses, access_days, "+
				"access_until, rank) values(?, ?, ?, ?, ?, ?, ?, ?, ?)", CodeHash(code), string(note), clock.ISO(now),
				clock.ISO(now.Add(lifetime)), store.NullID(o.Group), o.Uses, days, until, o.Rank)
		})
		if err == nil {
			return id, code
		}
		if !store.IsConstraint(err) { // the same code twice: try another one
			panic(err)
		}
	}
}

// OpenInvites are the codes that still let people in: not used up, not revoked and not expired, newest first.
func (a *Access) OpenInvites() []Invite {
	return store.Select[Invite](a.db, "select i.*, g.name group_name from invites i left join groups g "+
		"on g.id = i.grp where "+open+" order by i.id desc", clock.ISO(a.Now()))
}

// Revoke stops a code; false when it no longer worked.
func (a *Access) Revoke(inviteID int64) bool {
	var n int64
	a.db.Tx(func() {
		n = a.db.Exec("update invites set revoked_at = ? where id = ? and "+open, clock.ISO(a.Now()), inviteID,
			clock.ISO(a.Now()))
	})
	return n == 1
}

// Redeem registers the user with a valid code and counts them in it: the invite row, or false for a wrong code
// (counted).
func (a *Access) Redeem(code string, user NewUser) (Invite, bool) {
	moment := a.Now()
	now := clock.ISO(moment)
	var row Invite
	var found bool
	a.db.Tx(func() {
		row, found = store.Get[Invite](a.db, "select * from invites where code_hash = ? and "+open, CodeHash(code),
			now)
		if !found {
			return
		}
		a.db.Exec("update invites set used = used + 1, used_by = ?, used_at = ? where id = ?", user.TgID, now,
			row.ID)
		var expires any
		if row.AccessUntil != "" {
			expires = row.AccessUntil
		} else if row.AccessDays != 0 {
			expires = clock.ISO(moment.Add(time.Duration(row.AccessDays) * 24 * time.Hour))
		}
		rank := Member
		if row.Grp != 0 {
			rank = row.Rank
		}
		a.db.Exec("insert into users(tg_id, tg_name, username, lang, joined_at, invite_id, expires_at, rank) "+
			"values(?, ?, ?, ?, ?, ?, ?, ?)", user.TgID, user.TgName, store.Null(user.Username), user.Lang, now,
			row.ID, expires, rank)
	})
	if !found {
		a.countWrong(user.TgID)
		return Invite{}, false
	}
	delete(a.wrong, user.TgID)
	row.Used++
	row.UsedBy, row.UsedAt = user.TgID, now
	return row, true
}

// --- wrong codes ------------------------------------------------------------------------------------------------

func (a *Access) recentWrong(tgID int64) []time.Time {
	tries, since := a.wrong[tgID], a.Now().Add(-WrongWindow)
	for len(tries) > 0 && !tries[0].After(since) {
		tries = tries[1:]
	}
	a.wrong[tgID] = tries
	return tries
}

func (a *Access) countWrong(tgID int64) {
	a.wrong[tgID] = append(a.recentWrong(tgID), a.Now())
}

// Locked tells too many wrong codes lately: the bot does not answer this id.
func (a *Access) Locked(tgID int64) bool {
	return len(a.recentWrong(tgID)) >= WrongLimit
}

// --- users ------------------------------------------------------------------------------------------------------

// User is a user's row; false when they are not in.
func (a *Access) User(tgID int64) (User, bool) {
	return store.Get[User](a.db, "select * from users where tg_id = ?", tgID)
}

// Users are every user, with the note of their invite and the name of their group, by when they came in.
func (a *Access) Users() []User {
	return store.Select[User](a.db, "select u.*, i.note, g.name group_name from users u "+
		"left join invites i on i.id = u.invite_id left join groups g on g.id = u.grp order by u.joined_at")
}

// UpdateProfile keeps the last Telegram names the bot saw.
func (a *Access) UpdateProfile(tgID int64, tgName, username string) {
	a.db.Tx(func() {
		a.db.Exec("update users set tg_name = ?, username = ?, paused = 0 where tg_id = ?", tgName,
			store.Null(username), tgID)
	})
}

// SetStudent sets the full name; "" clears it. A new name means new rows in the sheets: the caller forgets the
// grades of the old one.
func (a *Access) SetStudent(tgID int64, student string) {
	a.db.Tx(func() { a.db.Exec("update users set student = ? where tg_id = ?", store.Null(student), tgID) })
}

func (a *Access) SetLang(tgID int64, lang string) {
	a.db.Tx(func() { a.db.Exec("update users set lang = ? where tg_id = ?", lang, tgID) })
}

// SetQuiet sets quiet hours; nil turns them off.
func (a *Access) SetQuiet(tgID int64, hours *[2]int) {
	var start, end any
	if hours != nil {
		start, end = hours[0], hours[1]
	}
	a.db.Tx(func() {
		a.db.Exec("update users set quiet_from = ?, quiet_to = ? where tg_id = ?", start, end, tgID)
	})
}

// --- access for a while -----------------------------------------------------------------------------------------

// Expired tells that the user's access has ended.
func (a *Access) Expired(user User) bool {
	return user.ExpiresAt != "" && user.ExpiresAt <= clock.ISO(a.Now())
}

// Prolong gives access for days more, counted from its end or from now if it ended; days 0: for good. The user
// row, false when there is no such user.
func (a *Access) Prolong(tgID int64, days int) (User, bool) {
	user, ok := a.User(tgID)
	if !ok {
		return User{}, false
	}
	var expires any
	if days != 0 {
		start := a.Now()
		if user.ExpiresAt != "" {
			if end := clock.MustParse(user.ExpiresAt); end.After(start) {
				start = end
			}
		}
		expires = clock.ISO(start.Add(time.Duration(days) * 24 * time.Hour))
	}
	a.db.Tx(func() {
		a.db.Exec("update users set expires_at = ?, access_told = 0, prolong_asked = 0 where tg_id = ?", expires,
			tgID)
	})
	return a.User(tgID)
}

// Ending gives (users whose access ends within `within` and who were not told, users whose access ended and who were
// not told). The caller tells them and marks it with Told.
func (a *Access) Ending(within time.Duration) ([]User, []User) {
	now, soon := clock.ISO(a.Now()), clock.ISO(a.Now().Add(within))
	near := store.Select[User](a.db, "select * from users where expires_at > ? and expires_at <= ? and "+
		"access_told < 1", now, soon)
	ended := store.Select[User](a.db, "select * from users where expires_at <= ? and access_told < 2", now)
	return near, ended
}

func (a *Access) AskedProlong(tgID int64) {
	a.db.Tx(func() { a.db.Exec("update users set prolong_asked = 1 where tg_id = ?", tgID) })
}

func (a *Access) Told(tgID int64, stage int) {
	a.db.Tx(func() { a.db.Exec("update users set access_told = ? where tg_id = ?", stage, tgID) })
}

func (a *Access) SetRank(tgID int64, rank int) bool {
	var n int64
	a.db.Tx(func() { n = a.db.Exec("update users set rank = ? where tg_id = ?", rank, tgID) })
	return n == 1
}

// Admins are the group's admins who hear from the bot: not paused, access not ended.
func (a *Access) Admins(gid int64) []User {
	if gid == 0 {
		return nil
	}
	return store.Select[User](a.db, "select * from users where grp = ? and rank >= ? and paused = 0 "+
		"and (expires_at is null or expires_at > ?) order by joined_at", gid, Admin, clock.ISO(a.Now()))
}

func (a *Access) SetPaused(tgID int64, paused bool) {
	a.db.Tx(func() { a.db.Exec("update users set paused = ? where tg_id = ?", store.Bool(paused), tgID) })
}

// Delete erases the user and everything kept for them (later tables cascade). Their invite row stays, used.
func (a *Access) Delete(tgID int64) bool {
	var n int64
	a.db.Tx(func() { n = a.db.Exec("delete from users where tg_id = ?", tgID) })
	return n == 1
}
