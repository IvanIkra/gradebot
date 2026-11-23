package bot

import (
	"errors"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/IvanIkra/gradebot/internal/access"
	"github.com/IvanIkra/gradebot/internal/clock"
	"github.com/IvanIkra/gradebot/internal/groups"
	"github.com/IvanIkra/gradebot/internal/i18n"
	"github.com/IvanIkra/gradebot/internal/poller"
	"github.com/IvanIkra/gradebot/internal/rich"
	"github.com/IvanIkra/gradebot/internal/sheets"
	"github.com/IvanIkra/gradebot/internal/store"
)

// The owner's commands: invite codes, users, groups and their sheets, sheets' short names and layouts.
//
// Callbacks: `rq:<invite>` asks to revoke a code and `rv:<invite>` revokes it, `codes` and `users` redraw the
// lists, `up:<page>` turns a page of users, `ua` drops the search, `uc:<user>` opens a user's card, `du:<user>` asks
// to delete a user and `dy:<user>` deletes them, `rk:<user>` trusts a user or stops trusting them, `ae:<user>`
// offers to prolong a user's access and `ax:<user>.<days>` prolongs it (0: for good), `ug:<user>` offers the groups
// to move a user to and `um:<user>.<group>` moves them (group 0: none), `ra:<user>` makes a user their group's admin
// or takes it back, `nr:<user>` resets a user's full name, `sa:<request>` and `sd:<request>` approve and decline a
// user's sheet, `groups` redraws the groups, `gs:<group>` shows a group's sheets, `gr:<group>.<sheet>` takes a sheet
// from a group, `gd:<group>` asks to delete a group and `gx:<group>` deletes it, `rn:<sheet>` asks for a sheet's
// short name, `rl:<sheet>` lays a sheet out again, `st` redraws /status, `iv:<minutes>` sets how often the sheets
// are read, `fa:<user>` answers a user's /feedback, `al:u.<people>` and `al:d.<days>` set the shared limits of the
// groups' admins' codes, `al:<user>` shows an admin's own, `al:<user>.u.<people>` and `al:<user>.d.<days>` set
// them, `al:<user>.r` drops them for the shared ones; `sj:…` are /subjects', `fm:…` a subject's formula.

const (
	usersViewKey = "owner.users" // kv: {"page", "query"} of the users list, kept while the owner pages through it
	usersPage    = 10
	NewGroup     = "+" // the answer of the "New group" button under /invite's question of the group
	soon         = 3 * 24 * time.Hour
	MaxUses      = 1000
	MaxDays      = 365
)

var intervals = []int{10, 15, 30, 60} // the buttons of /interval; any other is typed

// matches is `/users <text>`: the text in the user's names, @username, full name, the note of their code or their
// group.
func matches(user access.User, query string) bool {
	fold := func(text string) string { return strings.ReplaceAll(strings.ToLower(text), "ё", "е") }
	where := strings.Join([]string{fold(user.TgName), fold(user.Username), fold(user.Student), fold(user.Note),
		fold(user.GroupName)}, " ")
	for _, word := range strings.Fields(fold(query)) {
		if !strings.Contains(where, strings.TrimLeft(word, "@")) {
			return false
		}
	}
	return true
}

type usersState struct {
	Page  int    `json:"page"`
	Query string `json:"query"`
}

func (b *Bot) ownerCommand(chat int64, cmd, arg, lang string) {
	switch cmd {
	case "/invite":
		b.cmdInvite(chat, arg, lang)
	case "/codes":
		b.send(chat, b.codesView(lang))
	case "/users":
		b.Store.PutJSON(usersViewKey, usersState{Query: strings.Join(strings.Fields(arg), " ")})
		b.send(chat, b.usersView(lang))
	case "/groups":
		b.cmdGroups(chat, arg, lang)
	case "/groupsheet":
		b.cmdGroupsheet(chat, arg, lang)
	case "/rename":
		b.cmdRename(chat, arg, lang)
	case "/relayout":
		b.cmdRelayout(chat, arg, lang)
	case "/status":
		b.send(chat, b.statusView(lang))
	case "/interval":
		b.cmdInterval(chat, arg, lang)
	case "/limits":
		b.cmdLimits(chat, arg, lang, 0)
	case "/subjects":
		b.cmdSubjects(chat, nil, arg, lang)
	default:
		b.send(chat, b.ownerHelp(lang))
	}
}

// ownerCallback: messageID is the message to redraw, 0 to answer in a new one.
func (b *Bot) ownerCallback(chat, messageID int64, kind, arg, lang string) string {
	var number int64
	if isDigits(arg) {
		number = atoi(arg)
	}
	switch kind {
	case "sj":
		return b.subjectsCallback(chat, messageID, nil, arg, lang)
	case "fm":
		return b.formulaCallback(chat, messageID, nil, arg, lang)
	case "rq":
		if number != 0 {
			b.Show(chat, messageID, b.revokeView(number, lang))
		}
	case "rv":
		if number != 0 {
			note := tr(lang, "Already used or revoked", "Уже использован или отозван")
			if b.Access.Revoke(number) {
				note = tr(lang, "Revoked", "Отозван")
			}
			b.Show(chat, messageID, b.codesView(lang))
			return note
		}
	case "codes":
		b.Show(chat, messageID, b.codesView(lang))
	case "users":
		b.Show(chat, messageID, b.usersView(lang))
	case "up":
		if isDigits(arg) {
			var state usersState
			b.Store.GetJSON(usersViewKey, &state)
			state.Page = int(atoi(arg))
			b.Store.PutJSON(usersViewKey, state)
			b.Show(chat, messageID, b.usersView(lang))
		}
	case "ua":
		b.Store.PutJSON(usersViewKey, usersState{})
		b.Show(chat, messageID, b.usersView(lang))
	case "fa":
		if number != 0 {
			return b.answerFeedback(chat, number, lang)
		}
	case "uc":
		if number != 0 {
			b.Show(chat, messageID, b.userCard(number, lang))
		}
	case "du":
		if number != 0 {
			b.Show(chat, messageID, b.deleteView(number, lang))
		}
	case "dy":
		if number != 0 {
			note := b.deleteUser(number, lang)
			b.Show(chat, messageID, b.usersView(lang))
			return note
		}
	case "rk":
		if number != 0 {
			user, ok := b.Access.User(number)
			if !ok {
				return ""
			}
			rank := access.Trusted
			if user.Rank >= access.Trusted {
				rank = access.Member
			}
			b.Access.SetRank(number, rank)
			if user.Rank >= access.Admin {
				now, _ := b.Access.User(number)
				b.adminMenu(now)
			}
			b.Show(chat, messageID, b.userCard(number, lang))
			if user.Rank < access.Trusted {
				return tr(lang, "Trusted", "Доверенный")
			}
			return tr(lang, "Member", "Обычный")
		}
	case "ra":
		if number != 0 {
			user, ok := b.Access.User(number)
			if !ok {
				return ""
			}
			if _, done := b.setAdmin(number, user.Rank < access.Admin); !done {
				return ""
			}
			b.Show(chat, messageID, b.userCard(number, lang))
			if user.Rank < access.Admin {
				return tr(lang, "Admin", "Админ")
			}
			return tr(lang, "Trusted", "Доверенный")
		}
	case "ae":
		if number != 0 {
			user, ok := b.Access.User(number)
			if !ok {
				return ""
			}
			b.Show(chat, messageID, b.accessView(user, lang))
		}
	case "ax":
		who, _, days := partition(arg, ".")
		if !isDigits(who) || !isDigits(days) {
			return ""
		}
		return b.prolong(chat, messageID, atoi(who), int(atoi(days)), lang)
	case "ug":
		if number != 0 {
			b.Show(chat, messageID, b.moveView(number, lang))
		}
	case "um":
		who, _, gid := partition(arg, ".")
		if !isDigits(who) || !isDigits(gid) {
			return ""
		}
		note := b.moveUser(atoi(who), atoi(gid), lang)
		b.Show(chat, messageID, b.userCard(atoi(who), lang))
		return note
	case "sa":
		if number != 0 {
			return b.approve(chat, messageID, number, lang, nil)
		}
	case "sd":
		if number != 0 {
			return b.decline(chat, messageID, number, lang, nil)
		}
	case "nr":
		if number != 0 {
			return b.resetName(chat, messageID, number, lang)
		}
	case "groups":
		b.Show(chat, messageID, b.groupsView(lang))
	case "gsa":
		data := Data{}
		if number != 0 {
			data["group"] = arg
		}
		b.addGroupSheet(chat, data, lang)
	case "gn":
		b.Flow(chat, lang, []Step{b.groupNameStep(lang, 0)}, func(data Data) { b.makeGroup(chat, data, lang) }, nil)
	case "gm":
		if number != 0 {
			b.Flow(chat, lang, []Step{b.groupNameStep(lang, number)}, func(data Data) {
				b.renameGroup(chat, number, data["name"], lang)
			}, nil)
		}
	case "gs":
		if number != 0 {
			b.Show(chat, messageID, b.groupView(lang, number, nil, true))
		}
	case "gr":
		gid, _, sheet := partition(arg, ".")
		if !isDigits(gid) || !isDigits(sheet) {
			return ""
		}
		b.Groups.RemoveSheet(atoi(gid), atoi(sheet))
		b.Show(chat, messageID, b.groupView(lang, atoi(gid), nil, true))
	case "gd":
		if number != 0 {
			b.Show(chat, messageID, b.deleteGroupView(number, lang))
		}
	case "gx":
		if number != 0 {
			done := b.Groups.Delete(number)
			b.Show(chat, messageID, b.groupsView(lang))
			if done {
				return tr(lang, "Deleted", "Удалена")
			}
		}
	case "rn":
		if number != 0 {
			if sheet, ok := b.Sheets.Get(number); ok {
				b.cmdRename(chat, sheet.URL, lang)
			}
		}
	case "rl":
		if number != 0 {
			sheet, ok := b.Sheets.Get(number)
			if !ok || b.Poller == nil {
				return ""
			}
			b.Poller.Relayout(sheet)
			return tr(lang, "Laying it out again", "Размечаю заново")
		}
	case "st":
		b.Show(chat, messageID, b.statusView(lang))
		return tr(lang, "Updated", "Обновлено")
	case "al":
		parts := strings.Split(arg, ".")
		var who int64
		if len(parts) > 0 && isDigits(parts[0]) {
			who = atoi(parts[0])
			parts = parts[1:]
		}
		if arg == "" {
			parts = nil
		}
		if who != 0 {
			if user, ok := b.Access.User(who); !ok || user.Rank < access.Admin {
				return tr(lang, "Not an admin", "Не админ")
			}
			if len(parts) == 0 {
				b.cmdLimits(chat, "", lang, who)
				return ""
			}
		}
		switch {
		case who != 0 && len(parts) == 1 && parts[0] == "r":
			b.dropOwnLimits(who)
		case len(parts) == 2 && (parts[0] == "u" || parts[0] == "d") && isDigits(parts[1]) &&
			(atoi(parts[1]) != 0 || parts[0] == "u"):
			n := int(atoi(parts[1]))
			if parts[0] == "u" {
				b.setLimits(&n, nil, who)
			} else {
				b.setLimits(nil, &n, who)
			}
		default:
			return ""
		}
		b.Show(chat, messageID, b.limitsView(lang, who))
		return tr(lang, "Saved", "Сохранено")
	case "iv":
		if number != 0 {
			if !b.setInterval(int(number)) {
				return ""
			}
			b.Show(chat, messageID, b.intervalView(lang))
			return tr(lang, "Saved", "Сохранено")
		}
	}
	return ""
}

func (b *Bot) ownerHelp(lang string) *rich.Doc {
	return rich.New().H(tr(lang, "Owner's commands", "Команды владельца")).Table(nil, [][]string{
		{"/invite " + tr(lang, "[group] [x5] [3d] [+30d] [note]", "[группа] [x5] [3д] [+30д] [пометка]"),
			tr(lang, "an invite code: for how many people, how long it works, how long the access lasts; /invite help "+
				"explains", "код приглашения: на сколько человек, сколько действует, на сколько доступ; подробно — "+
				"/invite help")},
		{"/codes", tr(lang, "open codes, revoke", "действующие коды, отзыв")},
		{"/users " + tr(lang, "[text]", "[текст]"), tr(lang, "users, a search; a user's card: rank, group, access, delete",
			"пользователи, поиск; карточка: ранг, группа, доступ, удаление")},
		{"/groups " + tr(lang, "[name [new name]]", "[имя [новое имя]]"), tr(lang, "groups: a new one, a new name; delete",
			"группы: новая, переименовать; удаление")},
		{"/groupsheet " + tr(lang, "[group] [link [short name]]", "[группа] [ссылка [имя]]"),
			tr(lang, "a group's shared sheets: everyone in it follows them", "общие таблицы группы: на них подписаны все в ней")},
		{"/rename " + tr(lang, "<link> [short name]", "<ссылка> [имя]"),
			tr(lang, "a sheet's short name and hashtag, up to 10 characters, for everyone who follows it; without one, "+
				"its title in Google", "короткое имя и хэштег таблицы, до 10 символов, у всех её подписчиков; без имени — "+
				"название таблицы в Google")},
		{"/subjects " + tr(lang, "[group]", "[группа]"), tr(lang, "a group's subjects: several sheets of one course as one",
			"предметы группы: несколько таблиц одного курса как одна")},
		{"/relayout " + tr(lang, "<link>", "<ссылка>"), tr(lang, "lay a sheet's tabs out again",
			"разметить вкладки таблицы заново")},
		{"/status", tr(lang, "sheets, errors, users, Haiku spending", "таблицы, ошибки, пользователи, расход Haiku")},
		{"/interval " + tr(lang, "[minutes]", "[минуты]"), tr(lang, "how often the sheets are read",
			"как часто читать таблицы")},
		{"/limits " + tr(lang, "[x50|x*] [14d]", "[x50|x*] [14д]"), tr(lang,
			"what a group admin's code may: how many people, how many days",
			"что может код админа группы: сколько человек, сколько дней")},
	}, nil).P(asksHint(lang))
}

// --- invites ----------------------------------------------------------------------------------------------------

// InviteArgs are the arguments of /invite.
type InviteArgs struct {
	Group       *groups.Group
	Uses        int
	Lifetime    time.Duration // 0: INVITE_DAYS
	AccessDays  int
	AccessUntil time.Time
	Rank        int
	Note        string
}

var (
	usesRe     = regexp.MustCompile(`^[xх×](\d+|\*)$`)
	lifetimeRe = regexp.MustCompile(`^(\d+)([dдhч])$`)
	accessRe   = regexp.MustCompile(`^\+(\d+)[dд]$`)
	untilRe    = regexp.MustCompile(`^\+(\d{1,2})\.(\d{1,2})(?:\.(\d{2}|\d{4}))?$`)
	daysRe     = regexp.MustCompile(`^(\d+)[dд]?$`)
	units      = map[string]int{"d": 24, "д": 24, "h": 1, "ч": 1}
)

type argError string

func (e argError) Error() string { return string(e) }

// validDate is a local date at 23:59, or false for one the calendar has not (31.02).
func validDate(year, month, day int, zone *time.Location) (time.Time, bool) {
	t := time.Date(year, time.Month(month), day, 23, 59, 0, 0, zone)
	return t, t.Year() == year && int(t.Month()) == month && t.Day() == day
}

// InviteArgsOf reads `/invite` arguments in any order: a group's name (the first word that names one), `x5` or `x*`
// people, `3d` or `12h` the code works, `+30d` or `+31.12[.2027]` of access, with adminWord «admin» for an admin's
// code; the other words are the note. An error for a number out of range or a date gone.
func InviteArgsOf(text string, findGroup func(string) (groups.Group, bool), now time.Time, zone *time.Location,
	adminWord bool) (InviteArgs, error) {
	out := InviteArgs{Uses: 1, Rank: access.Member}
	var note []string
	for _, word := range strings.Fields(text) {
		low := strings.ToLower(word)
		if out.Group == nil {
			if group, ok := findGroup(word); ok {
				out.Group = &group
				continue
			}
		}
		if adminWord && (low == "admin" || low == "админ") {
			out.Rank = access.Admin
		} else if found := usesRe.FindStringSubmatch(low); found != nil {
			if found[1] == "*" {
				out.Uses = 0
			} else {
				n, err := strconv.Atoi(found[1])
				if err != nil || n == 0 || n > MaxUses {
					return out, argError(word)
				}
				out.Uses = n
			}
		} else if found := lifetimeRe.FindStringSubmatch(low); found != nil {
			n, err := strconv.Atoi(found[1])
			hours := n * units[found[2]]
			if err != nil || hours < 1 || hours > MaxDays*24 {
				return out, argError(word)
			}
			out.Lifetime = time.Duration(hours) * time.Hour
		} else if found := accessRe.FindStringSubmatch(low); found != nil {
			n, err := strconv.Atoi(found[1])
			if err != nil || n < 1 || n > 10*MaxDays {
				return out, argError(word)
			}
			out.AccessDays = n
		} else if found := untilRe.FindStringSubmatch(low); found != nil {
			day, _ := strconv.Atoi(found[1])
			month, _ := strconv.Atoi(found[2])
			local := now.In(zone)
			year := local.Year()
			if found[3] != "" {
				year, _ = strconv.Atoi(found[3])
				if len(found[3]) == 2 {
					year += 2000
				}
			}
			end, ok := validDate(year, month, day, zone)
			if !ok {
				return out, argError(word)
			}
			if found[3] == "" && !end.After(local) {
				if end, ok = validDate(year+1, month, day, zone); !ok {
					return out, argError(word)
				}
			}
			if !end.After(local) {
				return out, argError(word)
			}
			out.AccessUntil = end
		} else {
			note = append(note, word)
		}
	}
	out.Note = strings.Join(note, " ")
	return out, nil
}

func noGroup(string) (groups.Group, bool) { return groups.Group{}, false }

// limitArgs reads `/limits` arguments: `x50` or `x*` people, `14d` days; an error for anything else or nothing.
func limitArgs(text string) (*int, *int, error) {
	var uses, days *int
	for _, word := range strings.Fields(strings.ToLower(text)) {
		if found := usesRe.FindStringSubmatch(word); found != nil {
			n := 0
			if found[1] != "*" {
				var err error
				if n, err = strconv.Atoi(found[1]); err != nil || n == 0 || n > MaxUses {
					return nil, nil, argError(word)
				}
			}
			uses = &n
		} else if found := daysRe.FindStringSubmatch(word); found != nil {
			n, err := strconv.Atoi(found[1])
			if err != nil || n < 1 || n > MaxDays {
				return nil, nil, argError(word)
			}
			days = &n
		} else {
			return nil, nil, argError(word)
		}
	}
	if uses == nil && days == nil {
		return nil, nil, argError(text)
	}
	return uses, days, nil
}

// cmdInvite is `/invite [group] [x5|x*] [3d|12h] [+30d|+31.12] [note]`, in any order: what is not typed is the
// default. Without arguments each is asked for, with buttons.
func (b *Bot) cmdInvite(chat int64, arg, lang string) {
	switch strings.ToLower(trimPy(arg)) {
	case "help", "?", "помощь":
		b.send(chat, b.inviteHelp(lang))
		return
	case "":
		b.Flow(chat, lang, b.inviteSteps(lang, nil), func(data Data) { b.inviteAsked(chat, data, lang) }, nil)
		return
	}
	args, err := InviteArgsOf(arg, b.Groups.Find, b.Now(), b.Zone, true)
	if err != nil {
		b.send(chat, b.inviteHelp(lang))
		return
	}
	if args.Rank >= access.Admin && args.Group == nil {
		b.send(chat, tr(lang, "An admin's code goes to a group: /invite <group> admin",
			"Код админа — только в группу: /invite <группа> админ"))
		return
	}
	b.send(chat, b.makeInvite(args, lang))
}

// inviteSteps are the questions of /invite; admin: of a group admin, within their limits (admins.go).
func (b *Bot) inviteSteps(lang string, admin *access.User) []Step {
	token := func(kind string, fix func(string) string) func(string, Data) (string, error) {
		return func(value string, _ Data) (string, error) {
			word := fix(strings.ReplaceAll(strings.ToLower(trimPy(value)), " ", ""))
			found, err := InviteArgsOf(word, noGroup, b.Now(), b.Zone, false)
			if err != nil {
				return "", err
			}
			ok := found.Note == ""
			switch kind {
			case "lifetime":
				ok = ok && found.Lifetime != 0
			case "access_any":
				ok = ok && (found.AccessDays != 0 || !found.AccessUntil.IsZero())
			}
			if !ok { // not this kind of word
				return "", argError(value)
			}
			return word, nil
		}
	}
	plain := func(text string) func(Data) *rich.Doc { return func(Data) *rich.Doc { return rich.New().P(text) } }
	people := tr(lang, "A number from 1 to 1000, or x* for any number.", "Число от 1 до 1000 или x* — сколько угодно.")
	days := tr(lang, "Like 3d or 12h, up to 365 days.", "Например 3д или 12ч, до 365 дней.")
	accessText := tr(lang, "Like +30d, or a date: +31.12.", "Например +30д или дата: +31.12.")
	anyUses := []Choice{{"1", "x1"}, {"5", "x5"}, {"10", "x10"}, {"30", "x30"}, {tr(lang, "Any number",
		"Сколько угодно"), "x*"}}
	var uses []Choice
	var lifetimes []int
	forever := tr(lang, "For good", "Бессрочно")
	if admin != nil {
		most, longest := b.limits(admin.TgID)
		if most != 0 {
			people = tr(lang, sf("A number from 1 to %d.", most), sf("Число от 1 до %d.", most))
			for _, n := range sortedSet(append(below([]int{1, 5, 10}, most), most)) {
				uses = append(uses, Choice{itoa(int64(n)), sf("x%d", n)})
			}
		} else {
			uses = anyUses
		}
		days = tr(lang, "Like 3d or 12h, up to "+i18n.Days("en", longest)+".",
			"Например 3д или 12ч, до "+i18n.Days("ru", longest)+".")
		lifetimes = sortedSet(append(below([]int{1, 3, b.Access.InviteDays}, longest), longest))
		if admin.ExpiresAt != "" {
			forever = tr(lang, "As long as mine", "Как у меня")
		}
	} else {
		uses = anyUses
		lifetimes = sortedSet([]int{1, 3, b.Access.InviteDays, 30})
	}
	newGroup := b.groupNameStep(lang, 0)
	newGroup.Name = "new_group"
	newGroup.When = func(data Data) bool { return data["group"] == NewGroup }
	d := tr(lang, "d", "д")
	return []Step{
		b.groupStep(lang, tr(lang, "No group: they pick one", "Без группы: выберет сам"), true),
		newGroup,
		{Name: "rank", Prompt: plain(tr(lang, "Who comes in with the code?", "Кто входит по коду?")),
			Check: func(value string, _ Data) (string, error) {
				if v := strings.ToLower(trimPy(value)); v != "admin" && v != "админ" {
					return "", argError(value)
				}
				return "admin", nil
			},
			Error: func(lang string, _ error) string {
				return tr(lang, "A button, please.", "Выбери кнопкой.")
			},
			Choices: func(Data) []Choice {
				return []Choice{{tr(lang, "The group's admin", "Админ группы"), "admin"}}
			},
			Skip: func(Data) string { return tr(lang, "Members", "Участники") },
			When: func(data Data) bool { return data["group"] != "" }},
		{Name: "people", Prompt: plain(tr(lang, "How many people may come in with the code? ",
			"Сколько человек может войти по коду? ") + people),
			Check: token("uses", func(v string) string {
				if strings.HasPrefix(v, "x") || strings.HasPrefix(v, "х") {
					return v
				}
				return "x" + v
			}),
			Error:   func(string, error) string { return people },
			Choices: func(Data) []Choice { return uses }},
		{Name: "lifetime", Prompt: plain(tr(lang, "How long does the code work? ", "Сколько действует код? ") + days),
			Check: token("lifetime", func(v string) string {
				if isDigits(v) {
					return v + d
				}
				return v
			}),
			Error: func(string, error) string { return days },
			Choices: func(Data) []Choice {
				var out []Choice
				for _, n := range lifetimes {
					out = append(out, Choice{i18n.Days(lang, n), sf("%dd", n)})
				}
				return out
			}},
		{Name: "access", Prompt: plain(tr(lang, "How long does the access last? ", "На сколько открыть доступ? ") +
			accessText),
			Check: token("access_any", func(v string) string {
				switch {
				case strings.HasPrefix(v, "+"):
					return v
				case isDigits(v):
					return "+" + v + d
				}
				return "+" + v
			}),
			Error: func(string, error) string { return accessText },
			Choices: func(Data) []Choice {
				return []Choice{{tr(lang, "30 days", "30 дней"), "+30d"}, {tr(lang, "90 days", "90 дней"), "+90d"}}
			},
			Skip: func(Data) string { return forever }},
		{Name: "note", Prompt: plain(tr(lang, "A note for yourself: who the code is for.", "Пометка для себя: для кого код.")),
			Skip: func(Data) string { return tr(lang, "No note", "Без пометки") }},
	}
}

func below(values []int, limit int) []int {
	var out []int
	for _, v := range values {
		if v < limit {
			out = append(out, v)
		}
	}
	return out
}

func sortedSet(values []int) []int {
	seen := map[int]bool{}
	var out []int
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Ints(out)
	return out
}

func (b *Bot) inviteAsked(chat int64, data Data, lang string) {
	args, _ := InviteArgsOf(data["people"]+" "+data["lifetime"], noGroup, b.Now(), b.Zone, false)
	if data["access"] != "" {
		got, _ := InviteArgsOf(data["access"], noGroup, b.Now(), b.Zone, false)
		args.AccessDays, args.AccessUntil = got.AccessDays, got.AccessUntil
	}
	args.Note = data["note"]
	args.Rank = access.Member
	if data["rank"] == "admin" {
		args.Rank = access.Admin
	}
	made := rich.New()
	if data["group"] == NewGroup {
		group, err := b.Groups.Create(data["new_group"])
		if err != nil { // made by another way while the questions were asked
			group, _ = b.Groups.Find(data["new_group"])
		}
		args.Group = &group
		made.P(tr(lang, "Group ", "Группа "), rich.Bold(group.Name), tr(lang, " is made.", " создана."))
	} else if data["group"] != "" {
		if group, ok := b.Groups.Get(atoi(data["group"])); ok {
			args.Group = &group
		}
	}
	b.send(chat, made.Extend(b.makeInvite(args, lang)))
}

func (b *Bot) makeInvite(args InviteArgs, lang string) *rich.Doc {
	var gid int64
	if args.Group != nil {
		gid = args.Group.ID
	}
	id, code := b.Access.InviteWith(access.Options{Note: args.Note, Group: gid, Uses: args.Uses,
		Lifetime: args.Lifetime, AccessDays: args.AccessDays, AccessUntil: args.AccessUntil, Rank: args.Rank})
	var invite access.Invite
	for _, inv := range b.Access.OpenInvites() {
		if inv.ID == id {
			invite = inv
		}
	}
	kind := tr(lang, "Code", "Код")
	if args.Rank >= access.Admin {
		kind = tr(lang, "Admin's code", "Код админа")
	}
	note := ""
	if args.Note != "" {
		note = " «" + invite.Note + "»"
	}
	parts := []any{kind, note}
	if args.Group != nil {
		parts = append(parts, tr(lang, " to the group ", " в группу "), rich.Bold(args.Group.Name))
	}
	parts = append(parts, ": ", people(invite, lang), tr(lang, ", works until ", ", действует до "),
		b.When(invite.ExpiresAt, lang), tr(lang, ", access ", ", доступ "), b.term(invite, lang), ":")
	doc := rich.New().P(parts...)
	doc.P(rich.Code(code))
	if b.Username == "" {
		return doc.P(rich.Italic(tr(lang, "Tap the code to copy it. It is shown only this once: only its hash is kept.",
			"Нажми на код, чтобы скопировать. Он показан только сейчас: хранится лишь его хэш.")))
	}
	link := "https://t.me/" + b.Username + "?start=" + code
	doc.P(rich.Code(link)) // monospace: a tap copies it
	doc.P(rich.Italic(tr(lang, "Tap the link to copy it: it lets one in with a tap. The code is shown only this once: "+
		"only its hash is kept.",
		"Нажми на ссылку, чтобы скопировать: по ней входят в одно касание. Код показан только сейчас: хранится лишь "+
			"его хэш.")))
	return doc.Buttons(rich.B(tr(lang, "Send to a chat", "Отправить в чат"),
		"https://t.me/share/url?url="+url.QueryEscape(link)))
}

// groupStep is a group: a button for each, or its name typed. Asked only when there are groups, or with isNew: then
// a button for a new group too, whose name the step after it asks for.
func (b *Bot) groupStep(lang, skip string, isNew bool) Step {
	return Step{Name: "group",
		Prompt: func(Data) *rich.Doc { return rich.New().P(tr(lang, "Which group?", "Какая группа?")) },
		Check: func(value string, _ Data) (string, error) {
			if isNew && value == NewGroup {
				return value, nil
			}
			var group groups.Group
			var ok bool
			if isDigits(value) {
				group, ok = b.Groups.Get(atoi(value))
			} else {
				group, ok = b.Groups.Find(value)
			}
			if !ok {
				return "", argError(value)
			}
			return itoa(group.ID), nil
		},
		Error: func(lang string, _ error) string {
			return tr(lang, "No such group.", "Такой группы нет.")
		},
		Choices: func(Data) []Choice {
			var out []Choice
			for _, g := range b.Groups.All() {
				out = append(out, Choice{g.Name, itoa(g.ID)})
			}
			if isNew {
				out = append(out, Choice{tr(lang, "New group", "Новая группа"), NewGroup})
			}
			return out
		},
		Skip: func(Data) string { return skip },
		When: func(Data) bool { return isNew || len(b.Groups.All()) > 0 }}
}

// sheetStep is a sheet the bot reads: a button for each, or its link.
func (b *Bot) sheetStep(lang string) Step {
	return Step{Name: "link",
		Prompt: func(Data) *rich.Doc {
			return rich.New().P(tr(lang, "Which sheet? A button, or its link.", "Какая таблица? Кнопка или ссылка."))
		},
		Check: func(value string, _ Data) (string, error) {
			var s sheets.Sheet
			var ok bool
			if strings.HasPrefix(value, "#") && isDigits(value[1:]) {
				s, ok = b.Sheets.Get(atoi(value[1:]))
			} else {
				s, ok = b.Sheets.ByURL(value)
			}
			if !ok {
				return "", argError(value)
			}
			return s.URL, nil
		},
		Error: func(lang string, _ error) string {
			return tr(lang, "The bot reads no such sheet.", "Бот не читает такую таблицу.")
		},
		Choices: func(Data) []Choice {
			var out []Choice
			every := b.Sheets.Every()
			for _, s := range every[:min(len(every), 24)] {
				out = append(out, Choice{sheets.Name(s), "#" + itoa(s.ID)})
			}
			return out
		}}
}

func (b *Bot) shortNameStep(lang string, skip func(Data) string) Step {
	return Step{Name: "name",
		Prompt: func(Data) *rich.Doc {
			return rich.New().P(tr(lang, "A short name, up to 10 characters; it is the sheet's hashtag too. ",
				"Короткое имя, до 10 символов; оно же хэштег таблицы. "), noCourse(lang))
		},
		Check: func(value string, _ Data) (string, error) { return sheets.Course(value) },
		Error: func(lang string, _ error) string {
			return tr(lang, "A short name has at most 10 characters.", "В коротком имени не больше 10 символов.")
		},
		Skip: skip}
}

func (b *Bot) inviteHelp(lang string) *rich.Doc {
	rows := [][]string{
		{"x5", tr(lang, "up to 5 people; x* any number until it expires or is revoked; one by default",
			"до 5 человек; x* — сколько угодно, пока код не истёк или не отозван; без этого — один")},
		{tr(lang, "3d, 12h", "3д, 12ч"), tr(lang, sf("how long the code works; %d days by default", b.Access.InviteDays),
			"сколько действует код; по умолчанию "+i18n.Days(lang, b.Access.InviteDays))},
		{tr(lang, "+30d, +31.12", "+30д, +31.12"), tr(lang,
			"access for 30 days from coming in, or until 31.12; for good by default",
			"доступ на 30 дней после входа или до 31.12; по умолчанию бессрочно")},
		{tr(lang, "group", "группа"), tr(lang, "the user lands in it and only you move them",
			"пользователь сразу в ней, переводишь только ты")},
		{tr(lang, "admin", "админ"), tr(lang, "with a group: who comes in is the group's admin",
			"вместе с группой: вошедший становится админом группы")},
	}
	return rich.New().P(rich.Code(tr(lang, "/invite [group] [x5] [3d] [+30d] [note]",
		"/invite [группа] [x5] [3д] [+30д] [пометка]")), " — ", tr(lang, "in any order:", "в любом порядке:")).
		Table(nil, rows, nil).P(rich.Code(tr(lang, "/invite A1 x* 2d +31.12 seminar", "/invite A1 x* 2д +31.12 семинар")))
}

// people is «one person», «3 of 5 people», «3 people, no limit».
func people(invite access.Invite, lang string) string {
	switch invite.Uses {
	case 1:
		return tr(lang, "one person", "на одного")
	case 0:
		return tr(lang, sf("any number of people (in: %d)", invite.Used), sf("без ограничения (вошли: %d)", invite.Used))
	}
	return tr(lang, sf("%d of %d people in", invite.Used, invite.Uses), sf("вошли %d из %d", invite.Used, invite.Uses))
}

func (b *Bot) term(invite access.Invite, lang string) string {
	if invite.AccessDays != 0 {
		return tr(lang, "for ", "на ") + i18n.Days(lang, invite.AccessDays)
	}
	if invite.AccessUntil != "" {
		return tr(lang, "until ", "до ") + b.When(invite.AccessUntil, lang)
	}
	return tr(lang, "for good", "бессрочный")
}

func usesText(invite access.Invite) string {
	uses := "∞"
	if invite.Uses != 0 {
		uses = itoa(int64(invite.Uses))
	}
	return sf("%d/%s", invite.Used, uses)
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func (b *Bot) codesView(lang string) *rich.Doc {
	invites := b.Access.OpenInvites()
	if len(invites) == 0 {
		return rich.New().P(tr(lang, "No open codes. ", "Действующих кодов нет. "), "/invite")
	}
	var rows [][]string
	for _, inv := range invites {
		rows = append(rows, []string{orDash(inv.Note), orDash(inv.GroupName), usesText(inv), b.term(inv, lang),
			b.When(inv.ExpiresAt, "")})
	}
	doc := rich.New().H(tr(lang, "Open codes", "Действующие коды")).Table([]string{tr(lang, "Note", "Пометка"),
		tr(lang, "Group", "Группа"), tr(lang, "People", "Люди"), tr(lang, "Access", "Доступ"),
		tr(lang, "Expires", "Истекает") + ", " + b.TZ(lang)}, rows, []string{"", "", "right", "", ""})
	revoke := tr(lang, "Revoke ", "Отозвать ")
	for _, inv := range invites[:min(len(invites), 30)] {
		label := inv.Note
		if label == "" {
			label = b.When(inv.CreatedAt, lang)
		}
		doc.Buttons(rich.BS(revoke+label, "rq:"+itoa(inv.ID), "danger"))
	}
	return doc
}

func (b *Bot) revokeView(inviteID int64, lang string) *rich.Doc {
	for _, inv := range b.Access.OpenInvites() {
		if inv.ID == inviteID {
			note := inv.Note
			if note == "" {
				note = b.When(inv.CreatedAt, lang)
			}
			return rich.New().P(tr(lang, "Revoke the code ", "Отозвать код "), rich.Bold(note), "?").Buttons(
				rich.BS(tr(lang, "Yes, revoke", "Да, отозвать"), "rv:"+itoa(inviteID), "danger"),
				rich.B(tr(lang, "Cancel", "Отмена"), "codes"))
		}
	}
	return b.codesView(lang)
}

// --- users ------------------------------------------------------------------------------------------------------

// usersView is a page of users, newest first, those that match the search of `/users <text>` when there is one; a
// button for each opens their card.
func (b *Bot) usersView(lang string) *rich.Doc {
	var state usersState
	b.Store.GetJSON(usersViewKey, &state)
	everyone := b.Access.Users()
	slices.Reverse(everyone)
	users := everyone
	if state.Query != "" {
		users = nil
		for _, u := range everyone {
			if matches(u, state.Query) {
				users = append(users, u)
			}
		}
	}
	pages := max(1, (len(users)+usersPage-1)/usersPage)
	page := min(state.Page, pages-1)
	shown := users[min(len(users), page*usersPage):min(len(users), (page+1)*usersPage)]
	title := tr(lang, "Users", "Пользователи") + sf(" · %d", len(users))
	if state.Query != "" {
		title += " · «" + state.Query + "»"
	}
	doc := rich.New().H(title)
	if len(shown) == 0 {
		if state.Query != "" {
			doc.P(tr(lang, "Nobody found. ", "Никого не нашлось. "), "/invite")
			return doc.Buttons(rich.B(tr(lang, "Everyone", "Все"), "ua"))
		}
		return doc.P(tr(lang, "No users yet. ", "Пользователей пока нет. "), "/invite")
	}
	var rows [][]any
	for _, u := range shown {
		rows = append(rows, []any{Who(u), orDash(u.Student), orDash(u.GroupName), b.until(u, lang, false)})
	}
	doc.TableHTML([]any{tr(lang, "Telegram", "Telegram"), tr(lang, "Full name", "ФИО"), tr(lang, "Group", "Группа"),
		tr(lang, "Access until", "Доступ до") + ", " + b.TZ(lang)}, rows, nil)
	for start := 0; start < len(shown); start += 2 {
		var row []rich.Button
		for _, u := range shown[start:min(len(shown), start+2)] {
			row = append(row, rich.B(u.TgName, "uc:"+itoa(u.TgID)))
		}
		doc.Buttons(row...)
	}
	if pages > 1 {
		var row []rich.Button
		if page > 0 {
			row = append(row, rich.B("◀", sf("up:%d", page-1)))
		}
		row = append(row, rich.B(sf("%d / %d", page+1, pages), sf("up:%d", page)))
		if page < pages-1 {
			row = append(row, rich.B("▶", sf("up:%d", page+1)))
		}
		doc.Buttons(row...)
	}
	if state.Query != "" {
		doc.Buttons(rich.B(tr(lang, "Everyone", "Все"), "ua"))
	}
	return doc.P(rich.Italic(tr(lang, "Search: /users and a name, a full name, a @username, a code's note or a group.",
		"Поиск: /users и имя, ФИО, @ник, пометка кода или группа.")))
}

func (b *Bot) userRow(tgID int64) (access.User, bool) {
	for _, u := range b.Access.Users() {
		if u.TgID == tgID {
			return u, true
		}
	}
	return access.User{}, false
}

// userCard is everything about one user, and what the owner may do with them.
func (b *Bot) userCard(tgID int64, lang string) *rich.Doc {
	user, ok := b.userRow(tgID)
	if !ok {
		return b.usersView(lang)
	}
	rows := [][]any{
		{"Telegram", Who(user)},
		{tr(lang, "Full name", "ФИО"), orDash(user.Student)},
		{tr(lang, "Group", "Группа"), b.groupCell(user, lang)},
		{tr(lang, "Rank", "Ранг"), rankText(user, lang)},
	}
	if user.Rank >= access.Admin && user.Grp != 0 {
		shared := ""
		if !b.hasOwnLimits(tgID) {
			shared = tr(lang, " (shared)", " (общие)")
		}
		rows = append(rows, []any{tr(lang, "Codes' limits", "Лимиты кодов"), b.limitsText(lang, tgID) + shared})
	}
	rows = append(rows,
		[]any{tr(lang, "Joined", "С нами с"), b.When(user.JoinedAt, lang)},
		[]any{tr(lang, "Access until", "Доступ до"), b.until(user, lang, true)},
		[]any{tr(lang, "Code", "Код"), orDash(user.Note)},
		[]any{tr(lang, "Subjects", "Предметы"), b.subjectNames(user)})
	if user.Paused {
		rows = append(rows, []any{tr(lang, "State", "Состояние"), tr(lang, "blocked the bot", "заблокировал(а) бота")})
	}
	doc := rich.New().H(user.TgName).TableHTML(nil, rows, nil)
	doc.P(rich.Italic(tr(lang, "A member's sheet that the bot doesn't read yet waits for your approval; a trusted "+
		"user's doesn't. A group's admin is trusted and runs the group: codes to it, its users, its shared sheets, its "+
		"members' requests.",
		"Новую для бота таблицу обычного пользователя ты сначала одобряешь, доверенного — нет. Админ группы — "+
			"доверенный, который ведёт свою группу: коды в неё, участники, общие таблицы, заявки.")))
	trust := tr(lang, "Trust", "Доверять")
	if user.Rank >= access.Trusted {
		trust = tr(lang, "Make a member", "Сделать обычным")
	}
	row := []rich.Button{rich.B(trust, "rk:"+itoa(tgID))}
	if len(b.Groups.All()) > 0 {
		row = append(row, rich.B(tr(lang, "Group", "Группа"), "ug:"+itoa(tgID)))
	}
	doc.Buttons(append(row, rich.B(tr(lang, "Access", "Доступ"), "ae:"+itoa(tgID)))...)
	if user.Grp != 0 {
		text := tr(lang, "Make the group's admin", "Сделать админом группы")
		if user.Rank >= access.Admin {
			text = tr(lang, "Not the group's admin", "Снять админа")
		}
		row := []rich.Button{rich.B(text, "ra:"+itoa(tgID))}
		if user.Rank >= access.Admin {
			row = append(row, rich.B(tr(lang, "Codes' limits", "Лимиты кодов"), "al:"+itoa(tgID)))
		}
		doc.Buttons(row...)
	}
	var last []rich.Button
	if user.Student != "" {
		last = append(last, rich.B(tr(lang, "Reset the full name", "Сбросить ФИО"), "nr:"+itoa(tgID)))
	}
	doc.Buttons(append(last, rich.BS(tr(lang, "Delete", "Удалить"), "du:"+itoa(tgID), "danger"))...)
	return doc.Buttons(rich.B(tr(lang, "« Users", "« Пользователи"), "users"))
}

func (b *Bot) until(user access.User, lang string, zone bool) string {
	if user.ExpiresAt == "" {
		return "∞"
	}
	ended := ""
	if b.Access.Expired(user) {
		ended = tr(lang, " (ended)", " (закончился)")
	}
	if zone {
		return b.When(user.ExpiresAt, lang) + ended
	}
	return b.When(user.ExpiresAt, "") + ended
}

func (b *Bot) accessView(user access.User, lang string) *rich.Doc {
	return rich.New().P(tr(lang, "Access of ", "Доступ "), Who(user), ": ", rich.Bold(b.until(user, lang, true)), ". ",
		tr(lang, "Days are added to its end, or to now when it has ended.",
			"Дни добавляются к концу доступа, а если он закончился — к сегодня.")).Buttons(
		prolongButtons(user, lang)...).Buttons(rich.B(tr(lang, "Back", "Назад"), "uc:"+itoa(user.TgID)))
}

// prolong prolongs a user's access (0: for good); the message turns into the user's card, the user is told.
func (b *Bot) prolong(chat, messageID, tgID int64, days int, lang string) string {
	user, ok := b.Access.Prolong(tgID, days)
	if !ok {
		return tr(lang, "Already deleted", "Уже удалён")
	}
	b.Show(chat, messageID, b.userCard(tgID, lang))
	ulang := user.Lang
	until := rich.Bold(tr(ulang, "for good", "бессрочно"))
	if user.ExpiresAt != "" {
		until = rich.Bold(tr(ulang, "until ", "до ") + b.When(user.ExpiresAt, ulang))
	}
	b.Tell(user, rich.New().P(tr(ulang, "The owner prolonged your access: ", "Владелец продлил доступ: "), until, "."),
		false)
	if user.Student != "" { // back after it ended: read what was missed
		var ids []int64
		for _, s := range b.Sheets.Followed(tgID) {
			ids = append(ids, s.ID)
		}
		b.ReadSoon(0, ids)
	}
	return tr(lang, "Prolonged", "Продлено")
}

// groupCell is the user's group, «by code» when it came with the invite and only the owner changes it.
func (b *Bot) groupCell(user access.User, lang string) string {
	if user.GroupName == "" {
		return "—"
	}
	if groups.Fixed(user) {
		return user.GroupName + tr(lang, " (by code)", " (по коду)")
	}
	return user.GroupName
}

func (b *Bot) moveView(tgID int64, lang string) *rich.Doc {
	user, ok := b.Access.User(tgID)
	if !ok {
		return b.usersView(lang)
	}
	now, hasNow := b.Groups.Get(user.Grp)
	var from any = tr(lang, "no group", "«без группы»")
	if hasNow {
		from = rich.Bold(now.Name)
	}
	doc := rich.New().P(tr(lang, "Move ", "Перевести "), Who(user), tr(lang, " from ", " из "), from, tr(lang, " to:", " в:"))
	doc.P(rich.Italic(tr(lang, "They unfollow the old group's sheets and follow the new one's.",
		"Подписки на таблицы старой группы снимутся, на таблицы новой — добавятся.")))
	var choice []rich.Button
	for _, g := range b.Groups.All() {
		if !hasNow || g.ID != now.ID {
			choice = append(choice, rich.B(g.Name, sf("um:%d.%d", tgID, g.ID)))
		}
	}
	if hasNow {
		choice = append(choice, rich.B(tr(lang, "No group", "Без группы"), sf("um:%d.0", tgID)))
	}
	for start := 0; start < len(choice); start += 4 {
		doc.Buttons(choice[start:min(len(choice), start+4)]...)
	}
	return doc.Buttons(rich.B(tr(lang, "Cancel", "Отмена"), "uc:"+itoa(tgID)))
}

func (b *Bot) moveUser(tgID, gid int64, lang string) string {
	user, ok := b.Access.User(tgID)
	if !ok {
		return ""
	}
	before, hadBefore := b.Groups.Get(user.Grp)
	left, followed, err := b.Groups.Join(tgID, gid)
	if err != nil {
		return ""
	}
	group, hasGroup := b.Groups.Get(gid)
	ulang := user.Lang
	parts := []any{tr(ulang, "The owner took you out of your group", "Владелец вывел тебя из группы"), ""}
	if hasGroup {
		parts = []any{tr(ulang, "The owner moved you to the group ", "Владелец перевёл тебя в группу "),
			rich.Bold(group.Name)}
	}
	parts = append(parts, ". ", tr(ulang, sf("Unfollowed sheets: %d, followed: %d. The list is in /sheets.", left,
		len(followed)), sf("Отписал от таблиц: %d, подписал на: %d. Список — в /sheets.", left, len(followed))))
	b.Tell(user, rich.New().P(parts...), false)
	b.movedNews(user, before, hadBefore, group, hasGroup)
	if user.Student != "" && len(followed) > 0 {
		b.ReadSoon(tgID, followed)
	}
	return tr(lang, "Moved", "Переведён")
}

func (b *Bot) deleteView(tgID int64, lang string) *rich.Doc {
	user, ok := b.Access.User(tgID)
	if !ok {
		return b.usersView(lang)
	}
	return rich.New().P(tr(lang, "Delete ", "Удалить "), Who(user), "? ", tr(lang,
		"Everything the bot keeps about them is erased; they'll need a new code to come back.",
		"Все данные пользователя сотрутся; вернуться можно будет только с новым кодом.")).Buttons(
		rich.BS(tr(lang, "Yes, delete", "Да, удалить"), "dy:"+itoa(tgID), "danger"),
		rich.B(tr(lang, "Cancel", "Отмена"), "uc:"+itoa(tgID)))
}

func (b *Bot) deleteUser(tgID int64, lang string) string {
	user, ok := b.Access.User(tgID)
	if !ok || !b.Access.Delete(tgID) {
		return tr(lang, "Already deleted", "Уже удалён")
	}
	b.Sheets.DropOrphans()
	b.GroupNews(user.Grp, tgID, func(ulang string) []any {
		return []any{Who(user), tr(ulang, ": the owner closed their access", ": владелец закрыл доступ")}
	})
	if _, err := b.API.Send(tgID, tr(user.Lang, "The owner closed your access to the bot.",
		"Владелец закрыл тебе доступ к боту."), nil, false); err != nil {
		slog.Info("telling a deleted user failed", "error", err)
	}
	return tr(lang, "Deleted", "Удалён")
}

// answerFeedback is «Answer» under a user's /feedback: the owner's next message goes to them.
func (b *Bot) answerFeedback(chat, tgID int64, lang string) string {
	user, ok := b.Access.User(tgID)
	if !ok {
		return tr(lang, "Already deleted", "Уже удалён")
	}
	step := Step{Name: "text", Prompt: func(Data) *rich.Doc {
		return rich.New().P(tr(lang, "Your answer to ", "Ответ для "), Who(user), ":")
	}}
	b.Flow(chat, lang, []Step{step}, func(data Data) {
		ulang := user.Lang
		answer := rich.New().P(rich.Bold(tr(ulang, "The owner answers:", "Ответ владельца:"))).Quote(data["text"], false)
		if b.Tell(user, answer, false) != 0 {
			b.send(chat, rich.New().P(tr(lang, "Sent to ", "Отправлено: "), Who(user), "."))
		} else {
			b.send(chat, tr(lang, "Not delivered: the user blocked the bot.", "Не доставлено: пользователь заблокировал бота."))
		}
	}, nil)
	return ""
}

// --- requests for sheets ----------------------------------------------------------------------------------------

// approve: the owner, or by a group's admin, approves a member's sheet.
func (b *Bot) approve(chat, messageID, rid int64, lang string, by *access.User) string {
	request, sheet, found, err := b.Sheets.Approve(rid)
	if err != nil {
		if sheets.IsError(err) {
			return tr(lang, "The user has 20 sheets already", "У пользователя уже 20 таблиц")
		}
		panic(err)
	}
	if !found {
		return tr(lang, "Already handled", "Уже решено")
	}
	user, _ := b.Access.User(request.TgID)
	approved := rich.New().P(tr(lang, "Approved: ", "Одобрено: "), rich.Link(sheets.Name(sheet), request.URL),
		tr(lang, " for ", " для "), Who(user), ".")
	if by == nil {
		b.Show(chat, messageID, approved.P(noCourse(lang)).Buttons(rich.B(tr(lang, "Set a short name", "Задать имя"),
			"rn:"+itoa(sheet.ID))))
	} else {
		b.Show(chat, messageID, approved)
		owner := b.OwnerLang()
		b.News([]any{Who(*by), tr(owner, ", the group's admin, approved ", ", админ группы, одобрил(а) "),
			rich.Link(sheets.Name(sheet), request.URL), tr(owner, " for ", " для "), Who(user)},
			rich.B(tr(owner, "Set a short name", "Задать имя"), "rn:"+itoa(sheet.ID)))
	}
	ulang := user.Lang
	first := tr(ulang, "The owner approved the sheet. ", "Владелец одобрил таблицу. ")
	if by != nil {
		first = tr(ulang, "Your group's admin approved the sheet. ", "Админ группы одобрил таблицу. ")
	}
	b.Tell(user, rich.New().P(first).Extend(following(user, sheet)), false)
	if user.Student != "" {
		b.ReadSoon(user.TgID, []int64{sheet.ID})
	}
	return tr(lang, "Approved", "Одобрено")
}

// decline: the owner, or by a group's admin, declines a member's sheet.
func (b *Bot) decline(chat, messageID, rid int64, lang string, by *access.User) string {
	request, ok := b.Sheets.DropRequest(rid, 0)
	if !ok {
		return tr(lang, "Already handled", "Уже решено")
	}
	user, _ := b.Access.User(request.TgID)
	b.Show(chat, messageID, rich.New().P(tr(lang, "Declined: ", "Отклонено: "), rich.Link(request.URL, request.URL),
		tr(lang, " for ", " для "), Who(user), "."))
	if by != nil {
		owner := b.OwnerLang()
		b.News([]any{Who(*by), tr(owner, ", the group's admin, declined ", ", админ группы, отклонил(а) "),
			rich.Link(request.URL, request.URL), tr(owner, " for ", " для "), Who(user)})
	}
	ulang := user.Lang
	first := tr(ulang, "The owner declined the sheet ", "Владелец отклонил таблицу ")
	if by != nil {
		first = tr(ulang, "Your group's admin declined the sheet ", "Админ группы отклонил таблицу ")
	}
	b.Tell(user, rich.New().P(first, rich.Link(request.URL, request.URL), "."), false)
	return tr(lang, "Declined", "Отклонено")
}

func (b *Bot) resetName(chat, messageID, tgID int64, lang string) string {
	user, ok := b.Access.User(tgID)
	if !ok || user.Student == "" {
		return tr(lang, "Nothing to reset", "Сбрасывать нечего")
	}
	b.Access.SetStudent(tgID, "")
	b.Grades.Forget(tgID, 0)
	b.Show(chat, messageID, rich.New().P(tr(lang, "Full name ", "ФИО "), rich.Bold(user.Student), tr(lang, " of ", " у "),
		Who(user), tr(lang, " is reset.", " сброшено.")))
	ulang := user.Lang
	b.Tell(user, rich.New().P(tr(ulang, "The owner reset your full name. Send it again exactly as the sheets have it: ",
		"Владелец сбросил твоё ФИО. Укажи его заново, точно как в таблицах: "),
		rich.Code(tr(ulang, "/name Surname Name", "/name Фамилия Имя"))), false)
	return tr(lang, "Reset", "Сброшено")
}

// --- groups and their sheets ------------------------------------------------------------------------------------

// cmdGroups: `/groups` lists them, `/groups <name>` makes one, `/groups <name> <new name>` renames one.
func (b *Bot) cmdGroups(chat int64, arg, lang string) {
	words := strings.Fields(arg)
	switch len(words) {
	case 1:
		b.Flow(chat, lang, []Step{b.groupNameStep(lang, 0)}, func(data Data) { b.makeGroup(chat, data, lang) },
			Data{"name": words[0]})
	case 2:
		group, ok := b.Groups.Find(words[0])
		if !ok {
			b.send(chat, tr(lang, "No such group.", "Такой группы нет."))
			return
		}
		b.Flow(chat, lang, []Step{b.groupNameStep(lang, group.ID)}, func(data Data) {
			b.renameGroup(chat, group.ID, data["name"], lang)
		}, Data{"name": words[1]})
	default:
		b.send(chat, b.groupsView(lang))
	}
}

// groupNameStep is a new group's name, or a new name of the group gid.
func (b *Bot) groupNameStep(lang string, gid int64) Step {
	return Step{Name: "name",
		Prompt: func(Data) *rich.Doc {
			return rich.New().P(tr(lang, "The group's name: one word, up to 20 characters.",
				"Имя группы: одно слово, до 20 символов."))
		},
		Check: func(value string, _ Data) (string, error) {
			name, err := groups.Name(value)
			if err != nil {
				return "", err
			}
			if found, ok := b.Groups.Find(name); ok && found.ID != gid {
				return "", &groups.Error{Msg: "a group with this name exists"}
			}
			return name, nil
		},
		Error: func(lang string, err error) string {
			if strings.Contains(err.Error(), "exists") {
				return tr(lang, "There is such a group already.", "Такая группа уже есть.")
			}
			return tr(lang, "A group's name is one word, up to 20 characters.", "Имя группы — одно слово, до 20 символов.")
		}}
}

func (b *Bot) makeGroup(chat int64, data Data, lang string) {
	group, err := b.Groups.Create(data["name"])
	if err != nil {
		panic(err)
	}
	b.send(chat, rich.New().P(tr(lang, "Group ", "Группа "), rich.Bold(group.Name), tr(lang, " is made. ", " создана. "),
		tr(lang, "A code to it: ", "Код в неё: "), rich.Code("/invite "+group.Name), ".").Buttons(
		rich.B(tr(lang, "Add a sheet", "Добавить таблицу"), "gsa:"+itoa(group.ID))))
}

func (b *Bot) renameGroup(chat, gid int64, name, lang string) {
	group, err := b.Groups.Rename(gid, name)
	if err != nil {
		panic(err)
	}
	b.send(chat, rich.New().P(tr(lang, "Renamed: ", "Переименована: "), rich.Bold(group.Name), "."))
}

func (b *Bot) groupsView(lang string) *rich.Doc {
	all := b.Groups.All()
	newButton := rich.BS(tr(lang, "New group", "Новая группа"), "gn", "primary")
	if len(all) == 0 {
		return rich.New().P(tr(lang, "No groups yet.", "Групп пока нет.")).Buttons(newButton)
	}
	var rows [][]string
	for _, g := range all {
		rows = append(rows, []string{g.Name, itoa(int64(g.Users)), itoa(int64(g.Sheets))})
	}
	doc := rich.New().H(tr(lang, "Groups", "Группы")+sf(" · %d", len(all))).Table([]string{tr(lang, "Group", "Группа"),
		tr(lang, "Users", "Пользователей"), tr(lang, "Sheets", "Таблиц")}, rows, []string{"", "right", "right"})
	for _, g := range all[:min(len(all), 30)] {
		doc.Buttons(rich.B(tr(lang, "Sheets: ", "Таблицы: ")+g.Name, "gs:"+itoa(g.ID)),
			rich.B(tr(lang, "Rename ", "Переименовать ")+g.Name, "gm:"+itoa(g.ID)),
			rich.BS(tr(lang, "Delete ", "Удалить ")+g.Name, "gd:"+itoa(g.ID), "danger"))
	}
	return doc.Buttons(newButton)
}

func (b *Bot) deleteGroupView(gid int64, lang string) *rich.Doc {
	for _, g := range b.Groups.All() {
		if g.ID == gid {
			return rich.New().P(tr(lang, "Delete the group ", "Удалить группу "), rich.Bold(g.Name), "? ",
				tr(lang, sf("Its %d users will be in no group and keep their sheets.", g.Users),
					sf("Её пользователи (%d) останутся без группы, их подписки сохранятся.", g.Users))).Buttons(
				rich.BS(tr(lang, "Yes, delete", "Да, удалить"), "gx:"+itoa(gid), "danger"),
				rich.B(tr(lang, "Cancel", "Отмена"), "groups"))
		}
	}
	return b.groupsView(lang)
}

// cmdGroupsheet: `/groupsheet` lists every group's sheets, `/groupsheet <group>` one group's, both with a button to
// add one; `/groupsheet [group] <link> [short name]` gives a group a sheet, the missing ones asked for.
func (b *Bot) cmdGroupsheet(chat int64, arg, lang string) {
	words := strings.Fields(arg)
	var group groups.Group
	found := false
	if len(words) > 0 {
		group, found = b.Groups.Find(words[0])
	}
	if found {
		words = words[1:]
	}
	if len(words) == 0 {
		if found {
			b.send(chat, b.groupView(lang, group.ID, nil, true))
		} else {
			b.send(chat, b.everyGroupView(lang))
		}
		return
	}
	data := Data{"link": words[0]}
	if len(words) > 1 {
		data["name"] = strings.Join(words[1:], " ")
	}
	if found {
		data["group"] = itoa(group.ID)
	}
	b.addGroupSheet(chat, data, lang)
}

func (b *Bot) addGroupSheet(chat int64, data Data, lang string) {
	all := b.Groups.All()
	if len(all) == 0 {
		b.send(chat, b.everyGroupView(lang))
		return
	}
	if len(all) == 1 {
		if _, ok := data["group"]; !ok {
			data["group"] = itoa(all[0].ID)
		}
	}
	skip := func(data Data) string {
		if known, ok := b.Sheets.ByURL(data["link"]); ok && known.Course != "" {
			return tr(lang, "Keep ", "Оставить ") + "«" + known.Course + "»"
		}
		return tr(lang, "Title from Google", "Название из Google")
	}
	steps := []Step{b.groupStep(lang, "", false), linkStep(lang, "link"), b.shortNameStep(lang, skip)}
	b.Flow(chat, lang, steps, func(data Data) { b.groupSheetAdded(chat, data, lang) }, data)
}

func (b *Bot) groupSheetAdded(chat int64, data Data, lang string) {
	group, ok := b.Groups.Get(atoi(data["group"]))
	if !ok {
		return
	}
	var course *string
	if name := data["name"]; name != "" {
		course = &name
	}
	sheet, added, err := b.Groups.AddSheet(group.ID, data["link"], b.OwnerID, course)
	if err != nil {
		panic(err)
	}
	doc := rich.New().P(rich.Bold(sheets.Name(sheet)), tr(lang, " is a shared sheet of the group ",
		" — общая таблица группы "), rich.Bold(group.Name), ". ", tr(lang,
		sf("Everyone in the group follows it, newcomers too; added now: %d.", added),
		sf("На неё подписаны все в группе, и новички тоже; добавлено сейчас: %d.", added)))
	if sheet.Course == "" {
		doc.P(noCourse(lang))
	}
	b.send(chat, doc)
	b.ReadSoon(0, []int64{sheet.ID})
}

func (b *Bot) everyGroupView(lang string) *rich.Doc {
	all := b.Groups.All()
	if len(all) == 0 {
		return rich.New().P(tr(lang, "No groups yet: make one first in /groups.",
			"Групп пока нет: сначала создай группу в /groups.")).Buttons(
			rich.BS(tr(lang, "New group", "Новая группа"), "gn", "primary"))
	}
	doc := rich.New()
	for _, g := range all {
		b.groupView(lang, g.ID, doc, false)
	}
	return doc.Buttons(rich.BS(tr(lang, "Add a sheet", "Добавить таблицу"), "gsa", "primary"))
}

// groupView is a group's sheets with a button to take each from it, and one to add a sheet; added to doc when given.
func (b *Bot) groupView(lang string, gid int64, doc *rich.Doc, add bool) *rich.Doc {
	group, ok := b.Groups.Get(gid)
	if doc == nil {
		doc = rich.New()
	}
	if !ok {
		return b.groupsView(lang)
	}
	list := b.Groups.SheetsOf(gid)
	doc.H(tr(lang, "Group ", "Группа ") + sf("%s · %d", group.Name, len(list)))
	addButton := rich.BS(tr(lang, "Add a sheet to ", "Добавить таблицу в ")+group.Name, "gsa:"+itoa(gid), "primary")
	if len(list) == 0 {
		doc.P(tr(lang, "No shared sheets yet.", "Общих таблиц пока нет."))
		if add {
			return doc.Buttons(addButton)
		}
		return doc
	}
	var rows [][]any
	for _, s := range list {
		title := s.Title
		if title == "" {
			title = s.SheetID[max(0, len(s.SheetID)-6):]
		}
		rows = append(rows, []any{rich.Link(title, s.URL), orDash(s.Course), itoa(int64(s.Followers))})
	}
	doc.TableHTML([]any{tr(lang, "Sheet", "Таблица"), tr(lang, "Short name", "Имя"), tr(lang, "Followers", "Подписаны")},
		rows, []string{"", "", "right"})
	off := tr(lang, "Not shared: ", "Убрать из общих: ")
	for _, s := range list {
		doc.Buttons(rich.BS(off+sheets.Name(s), sf("gr:%d.%d", gid, s.ID), "danger"))
	}
	if add {
		return doc.Buttons(addButton)
	}
	return doc
}

// cmdRename is `/rename <link> [short name]`: the sheet's short name and hashtag (its course) for all its
// followers; the missing ones asked for.
func (b *Bot) cmdRename(chat int64, arg, lang string) {
	link, _, name := partition(trimPy(arg), " ")
	var data Data
	if link != "" {
		data = Data{"link": link}
		if trimPy(name) != "" {
			data["name"] = trimPy(name)
		}
	}
	steps := []Step{b.sheetStep(lang), b.shortNameStep(lang, func(Data) string {
		return tr(lang, "Title from Google", "Название из Google")
	})}
	b.Flow(chat, lang, steps, func(data Data) { b.renamed(chat, data, lang) }, data)
}

func (b *Bot) renamed(chat int64, data Data, lang string) {
	sheet, err := b.Sheets.SetCourse(data["link"], data["name"])
	if err != nil {
		panic(err)
	}
	if sheet.Course != "" {
		b.send(chat, rich.New().P(tr(lang, "Short name: ", "Имя: "), rich.Bold(sheet.Course), "."))
	} else {
		b.send(chat, rich.New().P(tr(lang, "No short name. ", "Короткого имени нет. "), noCourse(lang)))
	}
}

func noCourse(lang string) string {
	return tr(lang, "Without a short name the bot names a sheet by its title in Google, and its hashtag comes from "+
		"the first words of the title.",
		"Без короткого имени бот называет таблицу её названием в Google, а хэштег берёт из первых слов названия.")
}

// cmdRelayout is `/relayout <link>`, or the sheet asked for.
func (b *Bot) cmdRelayout(chat int64, arg, lang string) {
	var data Data
	if fields := strings.Fields(arg); len(fields) > 0 {
		data = Data{"link": fields[0]}
	}
	b.Flow(chat, lang, []Step{b.sheetStep(lang)}, func(data Data) {
		sheet, _ := b.Sheets.ByURL(data["link"])
		if b.Poller != nil {
			b.Poller.Relayout(sheet)
		}
		b.send(chat, rich.New().P(tr(lang, "The layout of ", "Разметка "), rich.Bold(sheets.Name(sheet)),
			tr(lang, " is dropped: the sheet is read and laid out again now.",
				" сброшена: таблица сейчас будет прочитана и размечена заново.")))
	}, data)
}

// --- the interval -----------------------------------------------------------------------------------------------

// cmdInterval is `/interval 15`, or the interval now with buttons, and another one typed.
func (b *Bot) cmdInterval(chat int64, arg, lang string) {
	step := Step{Name: "minutes", NoCancel: true,
		Prompt: func(Data) *rich.Doc { return b.intervalView(lang) },
		Check: func(value string, _ Data) (string, error) {
			if !isDigits(value) || atoi(value) < poller.MinMinutes || atoi(value) > poller.MaxMinutes {
				return "", argError(value)
			}
			return value, nil
		},
		Error: func(lang string, _ error) string {
			return tr(lang, sf("Minutes from %d to %d.", poller.MinMinutes, poller.MaxMinutes),
				sf("Минуты от %d до %d.", poller.MinMinutes, poller.MaxMinutes))
		}}
	var data Data
	if trimPy(arg) != "" {
		data = Data{"minutes": trimPy(arg)}
	}
	b.Flow(chat, lang, []Step{step}, func(data Data) {
		b.setInterval(int(atoi(data["minutes"])))
		b.send(chat, b.intervalView(lang))
	}, data)
}

// cmdLimits is `/limits [x50|x*] [14d]`: how many people a group admin's code lets in and how long it works, for
// every admin without limits of their own, or (tgID) one admin's own; without arguments the limits now with
// buttons, and other ones typed.
func (b *Bot) cmdLimits(chat int64, arg, lang string, tgID int64) {
	step := Step{Name: "limits", NoCancel: true,
		Prompt: func(Data) *rich.Doc { return b.limitsView(lang, tgID) },
		Check: func(value string, _ Data) (string, error) {
			_, _, err := limitArgs(value)
			return value, err
		},
		Error: func(lang string, _ error) string {
			return tr(lang, sf("Like x50 or x* for people, 14d for days, up to %d.", MaxDays),
				sf("Например x50 или x* — люди, 14д — дни, до %d.", MaxDays))
		}}
	var data Data
	if trimPy(arg) != "" {
		data = Data{"limits": trimPy(arg)}
	}
	b.Flow(chat, lang, []Step{step}, func(data Data) {
		uses, days, _ := limitArgs(data["limits"])
		b.setLimits(uses, days, tgID)
		b.send(chat, b.limitsView(lang, tgID))
	}, data)
}

// limitsText is «up to 30 people, 7 days» of the shared limits or of an admin's.
func (b *Bot) limitsText(lang string, tgID int64) string {
	uses, days := b.limits(tgID)
	return peopleLimit(uses, lang) + ", " + i18n.Days(lang, days)
}

func peopleLimit(uses int, lang string) string {
	if uses != 0 {
		return tr(lang, sf("up to %d people", uses), sf("до %d человек", uses))
	}
	return tr(lang, "any number of people", "сколько угодно человек")
}

// limitsView is the shared limits with every admin and their own ones, or (tgID) one admin's limits.
func (b *Bot) limitsView(lang string, tgID int64) *rich.Doc {
	uses, days := b.limits(tgID)
	peopleText := peopleLimit(uses, lang)
	var admin access.User
	isAdmin := false
	if tgID != 0 {
		admin, isAdmin = b.Access.User(tgID)
	}
	own := isAdmin && b.hasOwnLimits(tgID)
	var doc *rich.Doc
	if !isAdmin {
		doc = rich.New().P(tr(lang, "A group admin's code lets in ", "Код админа группы впускает "), rich.Bold(peopleText),
			tr(lang, " and works up to ", " и действует до "), rich.Bold(i18n.Days(lang, days)), ". ",
			tr(lang, "These are every admin's limits unless they have their own. ", "Это лимиты всех админов, у кого нет своих. "),
			tr(lang, "The access it gives is never longer than the admin's own.",
				"Доступ по нему — никогда не дольше, чем у самого админа."))
	} else {
		which := tr(lang, "The shared limits.", "Общие лимиты, как у всех.")
		if own {
			which = tr(lang, "Their own limits.", "Свои лимиты.")
		}
		doc = rich.New().P(tr(lang, "Codes of ", "Коды админа "), Who(admin), ": ", rich.Bold(peopleText),
			tr(lang, ", works up to ", ", действует до "), rich.Bold(i18n.Days(lang, days)), ". ", which)
	}
	example := "x50 14d"
	if lang == "ru" {
		example = "x50 14д"
	}
	doc.P(tr(lang, "Others: send them, like ", "Другие — пришли их, например "), rich.Code(example), ".")
	prefix := "al:"
	if isAdmin {
		prefix = sf("al:%d.", tgID)
	}
	var row []rich.Button
	for _, n := range []int{10, 30, 50, 0} {
		mark := ""
		if n == uses {
			mark = "✓ "
		}
		text := itoa(int64(n))
		if n == 0 {
			text = tr(lang, "Any number", "Сколько угодно")
		}
		row = append(row, rich.B(mark+text, sf("%su.%d", prefix, n)))
	}
	doc.Buttons(row...)
	row = nil
	for _, n := range []int{3, 7, 14, 30} {
		mark := ""
		if n == days {
			mark = "✓ "
		}
		row = append(row, rich.B(mark+i18n.Days(lang, n), sf("%sd.%d", prefix, n)))
	}
	doc.Buttons(row...)
	if isAdmin {
		if own {
			doc.Buttons(rich.B(tr(lang, "Shared: ", "Как у всех: ")+b.limitsText(lang, 0), sf("al:%d.r", tgID)))
		}
		return doc.Buttons(rich.B(tr(lang, "« Card", "« Карточка"), "uc:"+itoa(tgID)))
	}
	var admins []access.User
	for _, u := range b.Access.Users() {
		if u.Rank >= access.Admin && u.Grp != 0 {
			admins = append(admins, u)
		}
	}
	if len(admins) > 0 {
		var rows [][]any
		for _, u := range admins {
			limits := tr(lang, "shared", "общие")
			if b.hasOwnLimits(u.TgID) {
				limits = b.limitsText(lang, u.TgID)
			}
			rows = append(rows, []any{Who(u), u.GroupName, limits})
		}
		doc.TableHTML([]any{tr(lang, "Admin", "Админ"), tr(lang, "Group", "Группа"), tr(lang, "Limits", "Лимиты")}, rows,
			nil)
		for start := 0; start < min(len(admins), 30); start += 2 {
			var buttons []rich.Button
			for _, u := range admins[start:min(len(admins), start+2)] {
				buttons = append(buttons, rich.B(u.TgName, "al:"+itoa(u.TgID)))
			}
			doc.Buttons(buttons...)
		}
	}
	return doc
}

// setInterval: false for minutes out of range. The poller takes the new interval at once.
func (b *Bot) setInterval(minutes int) bool {
	if err := poller.SetInterval(b.Store, minutes); err != nil {
		return false
	}
	if b.Poller != nil {
		b.Poller.Rescheduled()
	}
	return true
}

func (b *Bot) intervalView(lang string) *rich.Doc {
	minutes := poller.IntervalOf(b.Store)
	doc := rich.New().P(tr(lang, "The sheets are read every ", "Таблицы читаются раз в "),
		rich.Bold(tr(lang, sf("%d minutes", minutes), sf("%d мин", minutes))), ". ",
		tr(lang, "A new grade comes within that time; each read downloads every followed sheet.",
			"Новая оценка приходит за это время; каждое чтение скачивает все таблицы с подписчиками."))
	doc.P(tr(lang, "Another: send the minutes, ", "Другой — пришли минуты, "),
		tr(lang, sf("from %d to %d.", poller.MinMinutes, poller.MaxMinutes),
			sf("от %d до %d.", poller.MinMinutes, poller.MaxMinutes)))
	var buttons []rich.Button
	for _, m := range intervals {
		if m != minutes {
			buttons = append(buttons, rich.B(tr(lang, sf("%d min", m), sf("%d мин", m)), sf("iv:%d", m)))
		}
	}
	return doc.Buttons(buttons...)
}

// --- status -----------------------------------------------------------------------------------------------------

// statusView is the sheets with their errors, users, and what Haiku cost today and this month.
func (b *Bot) statusView(lang string) *rich.Doc {
	every := b.Sheets.Every()
	minutes := poller.IntervalOf(b.Store)
	doc := rich.New().H(tr(lang, "Sheets", "Таблицы")+sf(" · %d", len(every))).P(
		tr(lang, sf("Read every %d minutes: ", minutes), sf("Читаются раз в %d мин: ", minutes)), "/interval")
	if len(every) > 0 {
		never := tr(lang, "never", "ни разу")
		var rows [][]any
		var errs []string
		for _, s := range every {
			read := never
			if s.OkAt != "" {
				read = b.When(s.OkAt, "")
			}
			state := "ok"
			if s.Error != "" {
				state = tr(lang, "failing", "ошибка")
				errs = append(errs, sf("%s: %s (%s %s)", sheets.Name(s), s.Error, tr(lang, "since", "с"),
					b.When(s.ErrorSince, lang)))
			}
			rows = append(rows, []any{rich.Link(sheets.Name(s), s.URL), itoa(int64(s.Followers)), read, state})
		}
		doc.TableHTML([]any{tr(lang, "Sheet", "Таблица"), tr(lang, "Followers", "Подписаны"),
			tr(lang, "Read", "Прочитана") + ", " + b.TZ(lang), tr(lang, "State", "Состояние")}, rows,
			[]string{"", "right", "", ""})
		if len(errs) > 0 {
			doc.Quote(strings.Join(errs, "\n"), true)
		}
	}
	users := b.Access.Users()
	badRows, _ := store.Scalar[int](b.Store, "select count(*) from subscriptions where error is not null")
	waiting, _ := store.Scalar[int](b.Store, "select count(*) from grade_events where post_id is null")
	noName, blocked, ending, ended := 0, 0, 0, 0
	for _, u := range users {
		if u.Student == "" {
			noName++
		}
		if u.Paused {
			blocked++
		}
		if b.Access.Expired(u) {
			ended++
		} else if u.ExpiresAt != "" && clock.MustParse(u.ExpiresAt).Sub(b.Now()) <= soon {
			ending++
		}
	}
	count := func(n int) string { return itoa(int64(n)) }
	doc.H(tr(lang, "Users", "Пользователи")+sf(" · %d", len(users))).Table(nil, [][]string{
		{tr(lang, "without a full name", "без ФИО"), count(noName)},
		{tr(lang, "blocked the bot", "заблокировали бота"), count(blocked)},
		{tr(lang, "subscriptions with a problem in the rows", "подписок с проблемой в строках"), count(badRows)},
		{tr(lang, "sheets waiting for approval", "таблиц ждут одобрения"), count(len(b.Sheets.Requests(0)))},
		{tr(lang, "access ends within 3 days", "доступ кончается в ближайшие 3 дня"), count(ending)},
		{tr(lang, "access ended", "доступ закончился"), count(ended)},
		{tr(lang, "grades waiting to be sent", "оценок ждут отправки"), count(waiting)},
	}, []string{"", "right"})
	doc.H("Haiku")
	if b.Budget == nil || b.Journal == nil {
		doc.P(tr(lang, "Not connected.", "Не подключён."))
	} else {
		now := b.Now()
		day := b.Journal.Totals(clock.DayStart(now, b.Zone))
		month := b.Journal.Totals(clock.MonthStart(now, b.Zone))
		state := b.Budget.Status()
		doc.Table(nil, [][]string{
			{tr(lang, "today", "сегодня"), sf("$%.3f", day.CostUSD), tr(lang, sf("calls %d, failed %d", day.Runs,
				day.Failed), sf("вызовов %d, ошибок %d", day.Runs, day.Failed))},
			{tr(lang, "this month", "за месяц"), sf("$%.3f / $%.2f", month.CostUSD, state.LimitUSD), tr(lang,
				sf("calls %d, failed %d", month.Runs, month.Failed), sf("вызовов %d, ошибок %d", month.Runs, month.Failed))},
		}, []string{"", "right", ""})
		if state.Stopped {
			doc.P(rich.Bold(tr(lang, "The monthly limit is spent: new tabs wait for the next month.",
				"Месячный лимит исчерпан: новые вкладки ждут следующего месяца.")))
		}
	}
	return doc.Buttons(rich.B(tr(lang, "Refresh", "Обновить"), "st"))
}

var _ = errors.New
