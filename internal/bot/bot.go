// Package bot is what the bot answers. Only private chats count; the bot stays silent in groups.
//
// Three kinds of senders: the owner (owner.go), users who came in with a code (users.go), and strangers, whom only a
// code lets in (here). Callback data is short and typed, `kind:arg`; each file lists its own kinds.
//
// The bot runs as the Python version's event loop did: one update, one poll's outcome or one look at the access at a
// time, under the service's lock. A failed call of the Bot API panics like the exception it was, and the loop that
// called in logs it.
package bot

import (
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/IvanIkra/gradebot/internal/access"
	"github.com/IvanIkra/gradebot/internal/clock"
	"github.com/IvanIkra/gradebot/internal/grades"
	"github.com/IvanIkra/gradebot/internal/groups"
	"github.com/IvanIkra/gradebot/internal/i18n"
	"github.com/IvanIkra/gradebot/internal/llm"
	"github.com/IvanIkra/gradebot/internal/poller"
	"github.com/IvanIkra/gradebot/internal/pyfmt"
	"github.com/IvanIkra/gradebot/internal/rich"
	"github.com/IvanIkra/gradebot/internal/sheets"
	"github.com/IvanIkra/gradebot/internal/store"
	"github.com/IvanIkra/gradebot/internal/telegram"
)

const (
	AccessCheck  = 10 * time.Minute // between looks at whose access ends
	AccessWarn   = 24 * time.Hour   // a user hears this long before their access ends
	NameReminder = 24 * time.Hour   // a user who came in and set no full name hears of it once after this
	reminded     = "remind.name"    // kv: the users told that their full name is missing
	ownerLangKey = "owner.lang"     // kv: the language of the owner's last message, for what the bot tells unasked
)

// Prolong are the days the owner's buttons add.
var Prolong = []int{7, 30, 90}

// Bot answers updates.
type Bot struct {
	API      *telegram.API
	Store    *store.Store
	Access   *access.Access
	Sheets   *sheets.Sheets
	Grades   *grades.Grades
	Groups   *groups.Groups
	Subjects *Subjects
	OwnerID  int64
	Zone     *time.Location
	Username string
	Now      clock.Now
	Poller   *poller.Poller // set by the service; without it nothing is read
	Budget   *llm.Budget    // set by the service: the owner's /status shows the spending
	Journal  *llm.Journal
	LLM      llm.Asker // Haiku, set by the service: /formula reads formulas with it

	Asks     map[int64]*Ask
	Drafts   map[int64]*Draft
	feedback map[int64][]time.Time
}

// New makes the bot.
func New(api *telegram.API, db *store.Store, a *access.Access, s *sheets.Sheets, ownerID int64, zone *time.Location,
	username string, now clock.Now) *Bot {
	if now == nil {
		now = clock.NowUTC
	}
	return &Bot{API: api, Store: db, Access: a, Sheets: s, Grades: grades.New(db, now),
		Groups: groups.New(db, s, now), Subjects: NewSubjects(db, now), OwnerID: ownerID, Zone: zone,
		Username: username, Now: now, Asks: map[int64]*Ask{}, Drafts: map[int64]*Draft{},
		feedback: map[int64][]time.Time{}}
}

// --- helpers ----------------------------------------------------------------------------------------------------

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func tr(lang, en, ru string) string { return i18n.Tr(lang, en, ru) }

func sf(format string, args ...any) string { return fmt.Sprintf(format, args...) }

// trimPy is Python's str.strip(): Unicode spaces and the file separators.
func trimPy(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) })
}

// isDigits is Python's str.isdigit().
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// allIn tells that every character of s is in chars.
func allIn(s, chars string) bool {
	for _, r := range s {
		if !strings.ContainsRune(chars, r) {
			return false
		}
	}
	return true
}

func atoi(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// partition is Python's str.partition.
func partition(s, sep string) (string, string, string) {
	before, after, found := strings.Cut(s, sep)
	if !found {
		return s, "", ""
	}
	return before, sep, after
}

func (b *Bot) send(chat int64, doc any) int64 {
	id, err := b.API.Send(chat, doc, nil, false)
	if err != nil {
		panic(err)
	}
	return id
}

func (b *Bot) edit(chat, messageID int64, doc any) {
	if err := b.API.Edit(chat, messageID, doc, nil); err != nil {
		panic(err)
	}
}

// Command is '/start@gradebot  K7QF-29XM' -> ('/start', 'K7QF-29XM'); plain text -> ("", text).
func Command(text string) (string, string) {
	text = trimPy(text)
	if !strings.HasPrefix(text, "/") {
		return "", text
	}
	head, _, rest := partition(text, " ")
	return strings.ToLower(strings.Split(head, "@")[0]), trimPy(rest)
}

func fullName(user telegram.User) string {
	var parts []string
	for _, p := range []string{user.FirstName, user.LastName} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return itoa(user.ID)
	}
	return strings.Join(parts, " ")
}

// OwnerLang is the language of the owner's last message.
func (b *Bot) OwnerLang() string {
	lang := "en"
	b.Store.GetJSON(ownerLangKey, &lang)
	return lang
}

// When is a moment in the bot's time zone; with lang, the zone named after it (a table names it in its header).
func (b *Bot) When(iso, lang string) string {
	text := clock.MustParse(iso).In(b.Zone).Format("02.01 15:04")
	if lang != "" {
		return text + " " + b.TZ(lang)
	}
	return text
}

// TZ is the bot's time zone as shown with times: MSK, or else its offset from UTC.
func (b *Bot) TZ(lang string) string {
	if b.Zone.String() == "Europe/Moscow" {
		return tr(lang, "MSK", "МСК")
	}
	_, offset := time.Now().In(b.Zone).Zone()
	hours := float64(offset) / 3600
	sign := "+"
	if hours < 0 || (hours == 0 && math.Signbit(hours)) {
		sign = "-"
	}
	return "UTC" + sign + pyfmt.G(math.Abs(hours), 6)
}

// Who is «Name (@username)» of a user row.
func Who(user access.User) rich.HTML {
	at := ""
	if user.Username != "" {
		at = " (@" + user.Username + ")"
	}
	return rich.Join(user.TgName, at)
}

// TellOwner: something the owner should act on or know at once, a message with a sound, under the digest of news.
func (b *Bot) TellOwner(doc *rich.Doc) {
	b.CloseNews("")
	if _, err := b.API.Send(b.OwnerID, doc, nil, false); err != nil {
		slog.Warn("telling the owner failed", "error", err)
	}
}

// Show redraws a message in place, or sends it as a new one when there is none to redraw (0).
func (b *Bot) Show(chat, messageID int64, doc any) {
	if messageID == 0 {
		b.send(chat, doc)
	} else {
		b.edit(chat, messageID, doc)
	}
}

// ReadSoon reads these sheets now; tgID (0: nobody) is told how it went.
func (b *Bot) ReadSoon(tgID int64, sheetIDs []int64) {
	if b.Poller != nil {
		for _, sheet := range sheetIDs {
			b.Poller.Request(sheet, tgID)
		}
	}
}

// Tell tells a user something unasked: the message id, or 0 when it failed. One who blocked the bot is paused.
func (b *Bot) Tell(user access.User, doc any, silent bool) int64 {
	id, err := b.API.Send(user.TgID, doc, nil, silent)
	if err != nil {
		slog.Info("telling a user failed", "user", user.TgID, "error", err)
		if e := telegram.AsError(err); e != nil && e.Blocked() {
			b.blocked(user)
		}
		return 0
	}
	return id
}

// blocked: the user blocked the bot; no reads, no messages until they write again, the owner hears of it once.
func (b *Bot) blocked(user access.User) {
	if user.Paused {
		return
	}
	b.Access.SetPaused(user.TgID, true)
	lang := b.OwnerLang()
	b.News([]any{Who(user), tr(lang, " blocked the bot: their sheets are paused until they write to it again.",
		": бот заблокирован, таблицы на паузе до нового сообщения боту.")}, b.deleteButton(user, lang))
	b.GroupNews(user.Grp, user.TgID, func(lang string) []any {
		return []any{Who(user), tr(lang, " blocked the bot: grades don't reach them", ": бот заблокирован, оценки не "+
			"доходят")}
	})
}

func (b *Bot) deleteButton(user access.User, lang string) rich.Button {
	return rich.BS(tr(lang, "Delete ", "Удалить ")+user.TgName, "du:"+itoa(user.TgID), "danger")
}

// --- access for a while -----------------------------------------------------------------------------------------

func ended(lang string) *rich.Doc {
	return rich.New().P(tr(lang, "Your access to the bot has ended: grades don't come any more. Your data is kept, "+
		"so once the owner prolongs it, everything goes on as before.",
		"Доступ к боту закончился: оценки больше не приходят. Данные сохранены — если владелец "+
			"продлит доступ, всё продолжится как было.")).Buttons(
		rich.BS(tr(lang, "Ask to prolong", "Попросить продлить"), "pr", "primary"))
}

// WatchAccess is one look, every few minutes: tell the users whose access ends within a day, or has ended, and
// remind the ones who never set their full name. The owner hears only when a user asks to prolong it; /status and
// /users show the rest.
func (b *Bot) WatchAccess() {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("checking access failed", "panic", r)
		}
	}()
	b.checkAccess()
	b.remindNames()
}

// remindNames: once, a day after coming in; without a full name no row is looked for, and nothing ever comes.
func (b *Bot) remindNames() {
	var list []int64
	b.Store.GetJSON(reminded, &list)
	told := map[int64]bool{}
	for _, id := range list {
		told[id] = true
	}
	since := clock.ISO(b.Now().Add(-NameReminder))
	for _, user := range b.Access.Users() {
		if user.Student != "" || told[user.TgID] || user.JoinedAt > since || user.Paused || b.Access.Expired(user) {
			continue
		}
		told[user.TgID] = true
		b.Store.PutJSON(reminded, sortedIDs(told))
		lang := user.Lang
		b.Tell(user, rich.New().P(
			tr(lang, "I can't find your grades yet: I need your full name exactly as the sheets have it. ",
				"Я пока не могу найти твои оценки: нужно ФИО — точно так, как в таблицах. "),
			rich.Code(tr(lang, "/name Surname Name", "/name Фамилия Имя"))), false)
	}
}

func sortedIDs(set map[int64]bool) []int64 {
	out := make([]int64, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func (b *Bot) checkAccess() {
	near, gone := b.Access.Ending(AccessWarn)
	for _, user := range near {
		b.Access.Told(user.TgID, 1)
		lang := user.Lang
		b.Tell(user, rich.New().P(tr(lang, "Your access to the bot ends ", "Доступ к боту закончится "),
			rich.Bold(b.When(user.ExpiresAt, lang)), ". ",
			tr(lang, "The owner may prolong it.", "Владелец может его продлить.")).Buttons(
			rich.B(tr(lang, "Ask to prolong", "Попросить продлить"), "pr")), false)
		b.GroupNews(user.Grp, user.TgID, func(lang string) []any {
			return []any{Who(user), tr(lang, ": access ends ", ": доступ закончится "), b.When(user.ExpiresAt, lang)}
		})
	}
	for _, user := range gone {
		b.Access.Told(user.TgID, 2)
		b.Tell(user, ended(user.Lang), false)
		b.GroupNews(user.Grp, user.TgID, func(lang string) []any {
			return []any{Who(user), tr(lang, ": access ended", ": доступ закончился")}
		})
	}
}

// askProlong: «Ask to prolong», the owner hears it once until the access is prolonged.
func (b *Bot) askProlong(user access.User) string {
	lang := user.Lang
	if user.ExpiresAt == "" {
		return tr(lang, "Your access is for good", "Доступ и так бессрочный")
	}
	if user.ProlongAsked {
		return tr(lang, "Already asked", "Просьба уже отправлена")
	}
	b.Access.AskedProlong(user.TgID)
	owner := b.OwnerLang()
	what := tr(owner, " asks to prolong the access, which ends ", ": просьба продлить доступ, он заканчивается ")
	if b.Access.Expired(user) {
		what = tr(owner, " asks to prolong the access, which ended ", ": просьба продлить доступ, он закончился ")
	}
	b.TellOwner(b.prolongDoc(user, owner, what).Buttons(
		rich.BS(tr(owner, "Delete", "Удалить"), "du:"+itoa(user.TgID), "danger")))
	return tr(lang, "Sent to the owner", "Отправлено владельцу")
}

// prolongDoc is «Name has access until …» with the buttons that prolong it.
func (b *Bot) prolongDoc(user access.User, lang, what string) *rich.Doc {
	return rich.New().P(Who(user), what, b.When(user.ExpiresAt, lang), ".").Buttons(prolongButtons(user, lang)...)
}

func prolongButtons(user access.User, lang string) []rich.Button {
	var out []rich.Button
	for _, n := range Prolong {
		out = append(out, rich.B("+"+i18n.Days(lang, n), sf("ax:%d.%d", user.TgID, n)))
	}
	return append(out, rich.BS(tr(lang, "For good", "Бессрочно"), sf("ax:%d.0", user.TgID), "success"))
}

// --- updates ----------------------------------------------------------------------------------------------------

// Handle answers one update.
func (b *Bot) Handle(update telegram.Update) {
	if msg := update.Message; msg != nil {
		if msg.Chat.Type == "private" && msg.From != nil {
			b.onMessage(msg)
		}
	} else if cb := update.CallbackQuery; cb != nil {
		b.onCallback(cb)
	}
}

func (b *Bot) onMessage(msg *telegram.Message) {
	sender, chat := *msg.From, msg.Chat.ID
	cmd, arg := Command(msg.Text)
	if sender.ID == b.OwnerID {
		b.CloseNews("")
		lang := i18n.Guess(sender.LanguageCode)
		if lang != b.OwnerLang() {
			b.Store.PutJSON(ownerLangKey, lang)
		}
		if cmd != "" {
			delete(b.Asks, chat)
		} else if b.Answer(chat, arg) {
			return
		}
		b.ownerCommand(chat, cmd, arg, lang)
		return
	}
	if user, ok := b.Access.User(sender.ID); ok {
		b.CloseNews(adminNews(user.TgID))
		if fullName(sender) != user.TgName || sender.Username != user.Username || user.Paused {
			b.Access.UpdateProfile(sender.ID, fullName(sender), sender.Username)
		}
		if b.Access.Expired(user) && cmd != "/stop" { // leaving works after the end too
			b.send(chat, ended(user.Lang))
			return
		}
		if cmd == "" && sheets.GoogleSheet.MatchString(arg) {
			if ask := b.Waiting(chat); ask == nil || ask.Step != "link" {
				cmd = "/sheet" // a link pasted while another question waits: follow it, the question goes
			}
		}
		if cmd != "" {
			delete(b.Asks, chat)
		} else if b.Answer(chat, arg) {
			return
		}
		b.userCommand(chat, user, cmd, arg)
		return
	}
	b.stranger(chat, sender, cmd, arg)
}

func (b *Bot) onCallback(cb *telegram.CallbackQuery) {
	sender, msg := cb.From, cb.Message
	note := ""
	if msg != nil {
		chat, messageID := msg.Chat.ID, msg.MessageID
		kind, _, arg := partition(cb.Data, ":")
		user, isUser := b.Access.User(sender.ID)
		switch {
		case (kind == "an" || kind == "ac") && (sender.ID == b.OwnerID || (isUser && !b.Access.Expired(user))):
			lang := user.Lang
			if sender.ID == b.OwnerID {
				lang = b.OwnerLang()
			}
			note = b.dialogCallback(chat, messageID, kind, arg, lang)
		case sender.ID == b.OwnerID:
			b.CloseNews("")
			where := messageID
			if b.isDigest(messageID) { // a digest stays as it is
				where = 0
			}
			note = b.ownerCallback(chat, where, kind, arg, b.OwnerLang())
		case isUser:
			b.CloseNews(adminNews(user.TgID))
			switch {
			case kind == "pr":
				note = b.askProlong(user)
			case b.Access.Expired(user) && kind != "stop" && kind != "stop!":
				note = tr(user.Lang, "Your access has ended", "Доступ закончился")
			default:
				note = b.userCallback(chat, messageID, user, kind, arg)
			}
		}
	}
	if err := b.API.Answer(cb.ID, note); err != nil {
		panic(err)
	}
}

// --- strangers --------------------------------------------------------------------------------------------------

// stranger: only a code lets one in, `/start CODE`, the t.me link (the same), or the code pasted alone.
func (b *Bot) stranger(chat int64, sender telegram.User, cmd, arg string) {
	tgID := sender.ID
	if b.Access.Locked(tgID) {
		return
	}
	lang := i18n.Guess(sender.LanguageCode)
	raw := access.Normalise(arg)
	pasted := cmd == "" && len([]rune(raw)) == access.CodeLength && allIn(raw, access.Alphabet)
	candidate := ""
	if cmd == "/start" || pasted { // «привет» is no code
		candidate = arg
	}
	if candidate == "" {
		b.send(chat, rich.New().P(tr(lang,
			"I send new grades from the Google Sheets of your courses as soon as they appear.",
			"Я присылаю новые оценки из таблиц Google твоих курсов, как только они появляются.")).P(tr(
			lang, "Access is by invite code: send me the code you got, or open the invite link.",
			"Вход по коду приглашения: пришли код, который тебе дали, или открой ссылку-приглашение.")))
		return
	}
	invite, ok := b.Access.Redeem(candidate, access.NewUser{TgID: tgID, TgName: fullName(sender),
		Username: sender.Username, Lang: lang})
	if !ok {
		if b.Access.Locked(tgID) {
			return
		}
		b.send(chat, tr(lang,
			"This code doesn't work: it is wrong, used up, expired or revoked. Check it, or ask for a new one.",
			"Этот код не подходит: он неверный, уже использован, истёк или отозван. Проверь его или попроси "+
				"новый."))
		return
	}
	group, hasGroup := b.Groups.Get(invite.Grp)
	followed := 0
	if hasGroup {
		_, added, _ := b.Groups.Join(tgID, group.ID)
		followed = len(added)
		b.Groups.Fix(tgID)
	}
	user, _ := b.Access.User(tgID)
	welcome := b.welcome(lang, group, hasGroup, followed, user.ExpiresAt)
	if _, ok := b.adminGroup(user); ok {
		welcome.P(tr(lang, "You are the group's admin: what you can do is in /help.",
			"Ты админ группы: что ты можешь — в /help."))
	}
	b.send(chat, welcome)
	if hasGroup || len(b.Groups.All()) == 0 { // else the full name is asked once the group is picked
		b.cmdName(chat, user, "")
	}
	note := ""
	if invite.Note != "" {
		note = " «" + invite.Note + "»"
	}
	owner := b.OwnerLang()
	count := ""
	if invite.Uses != 1 {
		uses := "∞"
		if invite.Uses != 0 {
			uses = itoa(int64(invite.Uses))
		}
		count = sf(" (%d/%s)", invite.Used, uses)
	}
	parts := []any{Who(user), tr(owner, " came in with the code", ": вход по коду"), note, count}
	if hasGroup {
		parts = append(parts, tr(owner, ", group ", ", группа "), group.Name)
	}
	if user.ExpiresAt != "" {
		parts = append(parts, tr(owner, ", access until ", ", доступ до "), b.When(user.ExpiresAt, owner))
	}
	b.News(parts, b.deleteButton(user, owner))
	b.GroupNews(user.Grp, tgID, func(lang string) []any {
		parts := []any{Who(user), tr(lang, " came in", ": вход по коду"), note}
		if user.ExpiresAt != "" {
			parts = append(parts, tr(lang, ", access until ", ", доступ до "), b.When(user.ExpiresAt, lang))
		}
		return parts
	})
	if _, ok := b.adminGroup(user); ok {
		b.adminMenu(user)
	}
}

// welcome is the first message: the user's group, or a choice of one when the code carried none, and the full name.
func (b *Bot) welcome(lang string, group groups.Group, hasGroup bool, followed int, expires string) *rich.Doc {
	doc := rich.New().H(tr(lang, "You're in", "Ты в деле")).P(tr(lang,
		"I'll send you new grades from the Google Sheets of your courses as soon as they appear.",
		"Я буду присылать новые оценки из таблиц Google твоих курсов, как только они появятся."))
	if expires != "" {
		doc.P(tr(lang, "Your access lasts until ", "Доступ открыт до "), rich.Bold(b.When(expires, lang)), ".")
	}
	if hasGroup {
		tail := ""
		if followed > 0 {
			tail = tr(lang, sf("You follow its shared sheets: %d. The list is in /sheets.", followed),
				sf("Её общие таблицы (%d) уже в твоих подписках. Список — в /sheets.", followed))
		}
		doc.P(tr(lang, "Your group: ", "Твоя группа: "), rich.Bold(group.Name), ". ", tail)
	} else if all := b.Groups.All(); len(all) > 0 {
		doc.P(tr(lang, "Which group are you in? I'll follow its shared sheets for you. You can change it later "+
			"with /group.",
			"В какой ты группе? Я подпишу тебя на её общие таблицы. Сменить группу можно потом: /group."))
		groupButtons(doc, all, "gj", lang)
	}
	return doc
}
