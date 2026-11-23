package bot

import (
	"log/slog"
	"strings"
	"time"

	"github.com/IvanIkra/gradebot/internal/access"
	"github.com/IvanIkra/gradebot/internal/clock"
	"github.com/IvanIkra/gradebot/internal/groups"
	"github.com/IvanIkra/gradebot/internal/i18n"
	"github.com/IvanIkra/gradebot/internal/rich"
	"github.com/IvanIkra/gradebot/internal/sheets"
)

// A group's admins: users of rank Admin who run the group they are in, within limits the owner does not need to
// watch.
//
// An admin makes codes to their group (members only, within the limits the owner sets with /limits: so many people,
// working so many days; and no longer access than their own), sees their group's codes and revokes them, sees who is
// in the group, gives the group shared sheets or takes them away, and approves or declines the sheets its members
// ask for. The owner hears of what an admin does in their news. An admin gets the news of their group in the way
// they pick with /news (news.go).
//
// The owner makes a user an admin with a button in their card, or with a code that lets one in as the admin of its
// group. An admin moved to another group, or whose group is deleted, is only trusted.
//
// Callbacks: `ml` lists the group's users and `mc:<user>` opens one's card, `ak` lists the group's codes,
// `aq:<invite>` asks to revoke one and `av:<invite>` revokes it, `gr:<group>.<sheet>` and `gsa:<group>` take a shared
// sheet away and add one, `sa:<request>` and `sd:<request>` approve and decline a member's sheet, `nm` shows the news
// settings and `nw:<mode>` sets them.

const limitsKey = "admin.limits" // kv: {"uses", "days"}: people an admin's code lets in at most (0: any), days it works

var defaultLimits = limitsValue{Uses: 30, Days: 7} // shared by the admins without limits of their own

type limitsValue struct {
	Uses int `json:"uses"`
	Days int `json:"days"`
}

// ownLimits is the kv key of an admin's own limits over the shared ones.
func ownLimits(tgID int64) string { return limitsKey + "." + itoa(tgID) }

var adminKinds = map[string]bool{"ml": true, "mc": true, "ak": true, "aq": true, "av": true, "gr": true, "gsa": true,
	"sa": true, "sd": true, "nm": true, "nw": true}
var adminCommandNames = map[string]bool{"/invite": true, "/codes": true, "/users": true, "/groupsheet": true,
	"/news": true}
var modes = []string{Digest, Each, Off}

// partialLimits is a stored limits value that may lack a field.
type partialLimits struct {
	Uses *int `json:"uses"`
	Days *int `json:"days"`
}

func (p *partialLimits) over(base limitsValue) limitsValue {
	if p == nil {
		return base
	}
	if p.Uses != nil {
		base.Uses = *p.Uses
	}
	if p.Days != nil {
		base.Days = *p.Days
	}
	return base
}

// limits are (people an admin's code lets in at most, 0 for any number; days it works at most): the admin's own when
// they have them, else the shared ones (tgID 0: the shared ones).
func (b *Bot) limits(tgID int64) (int, int) {
	var shared, own *partialLimits
	b.Store.GetJSON(limitsKey, &shared)
	found := shared.over(defaultLimits)
	if tgID != 0 {
		b.Store.GetJSON(ownLimits(tgID), &own)
		found = own.over(found)
	}
	return found.Uses, found.Days
}

func (b *Bot) hasOwnLimits(tgID int64) bool {
	var own *partialLimits
	return b.Store.GetJSON(ownLimits(tgID), &own) && own != nil
}

// setLimits sets the shared limits, or (tgID) an admin's own ones, starting from what they have now.
func (b *Bot) setLimits(uses, days *int, tgID int64) {
	nowUses, nowDays := b.limits(tgID)
	if uses != nil {
		nowUses = *uses
	}
	if days != nil {
		nowDays = *days
	}
	key := limitsKey
	if tgID != 0 {
		key = ownLimits(tgID)
	}
	b.Store.PutJSON(key, limitsValue{Uses: nowUses, Days: nowDays})
}

// dropOwnLimits: the admin goes by the shared limits again.
func (b *Bot) dropOwnLimits(tgID int64) { b.Store.PutJSON(ownLimits(tgID), nil) }

// adminGroup is the group the user runs: an admin's own group.
func (b *Bot) adminGroup(user access.User) (groups.Group, bool) {
	if user.Rank < access.Admin || user.Grp == 0 {
		return groups.Group{}, false
	}
	return b.Groups.Get(user.Grp)
}

func rankText(user access.User, lang string) string {
	switch {
	case user.Rank >= access.Admin && user.Grp != 0:
		return tr(lang, "group admin", "админ группы")
	case user.Rank >= access.Trusted:
		return tr(lang, "trusted", "доверенный")
	}
	return tr(lang, "member", "обычный")
}

// --- menus ------------------------------------------------------------------------------------------------------

// adminMenu is the command menu of one user: with the admin's commands while they run a group.
func (b *Bot) adminMenu(user access.User) {
	_, admin := b.adminGroup(user)
	for _, lang := range []string{"en", "ru"} {
		commands := UserCommands.Commands(lang)
		if admin {
			commands = append(commands, AdminCommands.Commands(lang)...)
		}
		code := lang
		if lang == "en" {
			code = ""
		}
		if err := b.API.SetCommands(commands, user.TgID, code); err != nil {
			slog.Info("a menu not set", "user", user.TgID, "error", err)
		}
	}
}

// AdminMenus sets every admin's menu, at the start.
func (b *Bot) AdminMenus() {
	for _, user := range b.Access.Users() {
		if _, ok := b.adminGroup(user); ok {
			b.adminMenu(user)
		}
	}
}

// setAdmin: the owner makes a user their group's admin, or only trusted again; the user is told and gets the menu.
func (b *Bot) setAdmin(tgID int64, admin bool) (access.User, bool) {
	user, ok := b.Access.User(tgID)
	if !ok || (admin && user.Grp == 0) {
		return access.User{}, false
	}
	rank := access.Trusted
	if admin {
		rank = access.Admin
	}
	b.Access.SetRank(tgID, rank)
	user, _ = b.Access.User(tgID)
	lang := user.Lang
	if admin {
		group, _ := b.Groups.Get(user.Grp)
		b.Tell(user, rich.New().P(tr(lang, "The owner made you the admin of the group ",
			"Владелец сделал тебя админом группы "), rich.Bold(group.Name), ". ",
			tr(lang, "What you can do is in /help.", "Что ты можешь — в /help.")), false)
	} else {
		b.Tell(user, rich.New().P(tr(lang, "You are no longer the admin of your group.", "Ты больше не админ группы.")),
			false)
	}
	b.adminMenu(user)
	return user, true
}

// --- commands ---------------------------------------------------------------------------------------------------

// adminCommand: an admin's command, true when it was one.
func (b *Bot) adminCommand(chat int64, user access.User, cmd, arg string) bool {
	group, ok := b.adminGroup(user)
	if !ok || !adminCommandNames[cmd] {
		return false
	}
	lang := user.Lang
	switch cmd {
	case "/invite":
		b.adminInvite(chat, user, group, arg)
	case "/codes":
		b.send(chat, b.adminCodesView(user, group))
	case "/users":
		b.send(chat, b.membersView(user, group))
	case "/groupsheet":
		if trimPy(arg) != "" {
			b.Flow(chat, lang, []Step{linkStep(lang, "link")}, func(data Data) {
				b.adminSheetAdded(chat, user, group, data)
			}, Data{"link": strings.Fields(arg)[0]})
		} else {
			b.send(chat, b.groupView(lang, group.ID, nil, true))
		}
	case "/news":
		b.send(chat, b.newsView(user))
	}
	return true
}

func (b *Bot) adminCallback(chat, messageID int64, user access.User, kind, arg string) string {
	lang := user.Lang
	group, ok := b.adminGroup(user)
	if !ok {
		return tr(lang, "Only your group's admin can do this", "Это может только админ группы")
	}
	var number int64
	if isDigits(arg) {
		number = atoi(arg)
	}
	switch kind {
	case "ml":
		b.edit(chat, messageID, b.membersView(user, group))
	case "mc":
		if number != 0 {
			b.edit(chat, messageID, b.memberCard(user, group, number))
		}
	case "ak":
		b.edit(chat, messageID, b.adminCodesView(user, group))
	case "aq":
		if number != 0 {
			invite, ok := b.groupInvite(group, number)
			if !ok {
				return tr(lang, "Already used or revoked", "Уже использован или отозван")
			}
			note := invite.Note
			if note == "" {
				note = b.When(invite.CreatedAt, lang)
			}
			b.edit(chat, messageID, rich.New().P(tr(lang, "Revoke the code ", "Отозвать код "), rich.Bold(note), "?").
				Buttons(rich.BS(tr(lang, "Yes, revoke", "Да, отозвать"), "av:"+arg, "danger"),
					rich.B(tr(lang, "Cancel", "Отмена"), "ak")))
		}
	case "av":
		if number != 0 {
			_, open := b.groupInvite(group, number)
			done := open && b.Access.Revoke(number)
			b.edit(chat, messageID, b.adminCodesView(user, group))
			if done {
				return tr(lang, "Revoked", "Отозван")
			}
			return tr(lang, "Already used or revoked", "Уже использован или отозван")
		}
	case "gr":
		gid, _, sheet := partition(arg, ".")
		if gid != itoa(group.ID) || !isDigits(sheet) {
			return ""
		}
		var found *sheets.Sheet
		for _, s := range b.Groups.SheetsOf(group.ID) {
			if s.ID == atoi(sheet) {
				s := s
				found = &s
			}
		}
		if found != nil && b.Groups.RemoveSheet(group.ID, atoi(sheet)) {
			owner := b.OwnerLang()
			b.News([]any{Who(user), tr(owner, ", admin of ", ", админ "), rich.Bold(group.Name),
				tr(owner, ", took away the shared sheet ", ", убрал(а) общую таблицу "),
				rich.Link(sheets.Name(*found), found.URL)})
		}
		b.edit(chat, messageID, b.groupView(lang, group.ID, nil, true))
	case "gsa":
		b.Flow(chat, lang, []Step{linkStep(lang, "link")}, func(data Data) { b.adminSheetAdded(chat, user, group, data) },
			nil)
	case "sa", "sd":
		if number != 0 {
			request, found := b.Sheets.Request(number)
			var asker access.User
			if found {
				asker, found = b.Access.User(request.TgID)
			}
			if !found {
				return tr(lang, "Already handled", "Уже решено")
			}
			if asker.Grp != group.ID {
				return ""
			}
			if kind == "sa" {
				return b.approve(chat, messageID, number, lang, &user)
			}
			return b.decline(chat, messageID, number, lang, &user)
		}
	case "nm":
		b.send(chat, b.newsView(user))
	case "nw":
		for _, m := range modes {
			if arg == m {
				b.Store.PutJSON(newsMode(user.TgID), arg)
				b.CloseNews(adminNews(user.TgID))
				b.edit(chat, messageID, b.newsView(user))
				return tr(lang, "Saved", "Сохранено")
			}
		}
	}
	return ""
}

func (b *Bot) adminHelp(user access.User) *rich.Doc {
	lang := user.Lang
	group, _ := b.adminGroup(user)
	uses, days := b.limits(user.TgID)
	peopleText := tr(lang, "any number of people", "сколько угодно человек")
	if uses != 0 {
		peopleText = tr(lang, sf("up to %d people", uses), sf("до %d человек", uses))
	}
	return rich.New().H(tr(lang, "Admin of ", "Админ группы ")+group.Name).Table(nil, [][]string{
		{"/invite " + tr(lang, "[x5] [3d] [+30d] [note]", "[x5] [3д] [+30д] [пометка]"),
			tr(lang, sf("a code to your group: %s, works up to %s, access no longer than yours", peopleText,
				i18n.Days("en", days)), sf("код в твою группу: %s, действует до %s, доступ — не дольше твоего",
				peopleText, i18n.Days("ru", days)))},
		{"/codes", tr(lang, "your group's open codes, revoke", "действующие коды группы, отзыв")},
		{"/users", tr(lang, "who is in your group", "кто в твоей группе")},
		{"/groupsheet " + tr(lang, "[link]", "[ссылка]"), tr(lang, "the group's shared sheets: everyone in it follows them",
			"общие таблицы группы: на них подписаны все в ней")},
		{"/subjects", tr(lang, "the group's subjects: several shared sheets of one course as one",
			"предметы группы: несколько общих таблиц одного курса как одна")},
		{"/news", tr(lang, "how the group's news come: a quiet digest, a message each, or none",
			"как присылать новости группы: тихой сводкой, каждую отдельно или не присылать")},
	}, nil).P(rich.Italic(tr(lang, "Sheets your group's members ask for come to you to approve, as to the owner.",
		"Заявки участников группы на новые таблицы приходят тебе на одобрение, как и владельцу.")))
}

// --- codes ------------------------------------------------------------------------------------------------------

// adminInvite is `/invite [x5] [3d] [+30d] [note]` to the admin's group, or asked step by step.
func (b *Bot) adminInvite(chat int64, user access.User, group groups.Group, arg string) {
	lang := user.Lang
	switch strings.ToLower(trimPy(arg)) {
	case "help", "?", "помощь":
		b.send(chat, b.adminHelp(user))
		return
	case "":
		var steps []Step
		for _, step := range b.inviteSteps(lang, &user) {
			if step.Name != "group" && step.Name != "new_group" && step.Name != "rank" {
				steps = append(steps, step)
			}
		}
		b.Flow(chat, lang, steps, func(data Data) {
			args, _ := InviteArgsOf(data["people"]+" "+data["lifetime"]+" "+data["access"], noGroup, b.Now(), b.Zone,
				false)
			args.Note = data["note"]
			b.adminMakeInvite(chat, user, group, args)
		}, nil)
		return
	}
	args, err := InviteArgsOf(arg, noGroup, b.Now(), b.Zone, false)
	if err != nil {
		b.send(chat, b.adminHelp(user))
		return
	}
	b.adminMakeInvite(chat, user, group, args)
}

func (b *Bot) adminMakeInvite(chat int64, user access.User, group groups.Group, args InviteArgs) {
	lang := user.Lang
	if problem := b.adminLimits(user, &args); problem != "" {
		b.send(chat, rich.New().P(problem).Extend(b.adminHelp(user)))
		return
	}
	args.Group, args.Rank = &group, access.Member
	b.send(chat, b.makeInvite(args, lang))
	owner := b.OwnerLang()
	note := ""
	if args.Note != "" {
		note = " «" + args.Note + "»"
	}
	b.News([]any{Who(user), tr(owner, ", admin of ", ", админ "), rich.Bold(group.Name),
		tr(owner, ", made a code", ", создал(а) код"), note,
		tr(owner, sf(" for %d people", args.Uses), sf(" на %d чел.", args.Uses))})
}

// adminLimits holds an admin's code within their limits: why it can't be, or "". Access past the admin's own is
// cut to it.
func (b *Bot) adminLimits(user access.User, args *InviteArgs) string {
	lang := user.Lang
	uses, days := b.limits(user.TgID)
	if uses != 0 && (args.Uses == 0 || args.Uses > uses) {
		return tr(lang, sf("A code of yours lets in at most %d people.", uses),
			sf("Твой код может впустить не больше %d человек.", uses))
	}
	lifetime := args.Lifetime
	if lifetime == 0 {
		lifetime = time.Duration(b.Access.InviteDays) * 24 * time.Hour
	}
	longest := time.Duration(days) * 24 * time.Hour
	if lifetime > longest {
		if args.Lifetime != 0 {
			return tr(lang, "A code of yours works at most "+i18n.Days("en", days)+".",
				"Твой код действует не больше "+i18n.Days("ru", days)+".")
		}
		lifetime = longest
	}
	args.Lifetime = lifetime
	if user.ExpiresAt != "" {
		end := clock.MustParse(user.ExpiresAt)
		wanted := args.AccessUntil
		if wanted.IsZero() && args.AccessDays != 0 {
			wanted = b.Now().Add(time.Duration(args.AccessDays) * 24 * time.Hour)
		}
		if wanted.IsZero() || wanted.After(end) {
			args.AccessUntil, args.AccessDays = end, 0
		}
	}
	return ""
}

// groupInvite is an open code to the group that an admin may revoke: one for members.
func (b *Bot) groupInvite(group groups.Group, inviteID int64) (access.Invite, bool) {
	for _, inv := range b.Access.OpenInvites() {
		if inv.ID == inviteID && inv.Grp == group.ID && inv.Rank < access.Admin {
			return inv, true
		}
	}
	return access.Invite{}, false
}

func (b *Bot) adminCodesView(user access.User, group groups.Group) *rich.Doc {
	lang := user.Lang
	var invites []access.Invite
	for _, inv := range b.Access.OpenInvites() {
		if inv.Grp == group.ID && inv.Rank < access.Admin {
			invites = append(invites, inv)
		}
	}
	if len(invites) == 0 {
		return rich.New().P(tr(lang, "Your group has no open codes. ", "Действующих кодов в группу нет. "), "/invite")
	}
	var rows [][]string
	for _, inv := range invites {
		rows = append(rows, []string{orDash(inv.Note), usesText(inv), b.term(inv, lang), b.When(inv.ExpiresAt, "")})
	}
	doc := rich.New().H(tr(lang, "Codes to ", "Коды в ")+group.Name).Table([]string{tr(lang, "Note", "Пометка"),
		tr(lang, "People", "Люди"), tr(lang, "Access", "Доступ"), tr(lang, "Expires", "Истекает") + ", " + b.TZ(lang)},
		rows, []string{"", "right", "", ""})
	for _, inv := range invites[:min(len(invites), 30)] {
		label := inv.Note
		if label == "" {
			label = b.When(inv.CreatedAt, lang)
		}
		doc.Buttons(rich.BS(tr(lang, "Revoke ", "Отозвать ")+label, "aq:"+itoa(inv.ID), "danger"))
	}
	return doc
}

// --- users ------------------------------------------------------------------------------------------------------

func (b *Bot) members(group groups.Group) []access.User {
	var out []access.User
	for _, u := range b.Access.Users() {
		if u.Grp == group.ID {
			out = append(out, u)
		}
	}
	return out
}

func (b *Bot) membersView(user access.User, group groups.Group) *rich.Doc {
	lang, members := user.Lang, b.members(group)
	doc := rich.New().H(tr(lang, "Group ", "Группа ") + sf("%s · %d", group.Name, len(members)))
	var rows [][]any
	for _, u := range members {
		rows = append(rows, []any{Who(u), orDash(u.Student), b.until(u, lang, false)})
	}
	doc.TableHTML([]any{tr(lang, "Telegram", "Telegram"), tr(lang, "Full name", "ФИО"),
		tr(lang, "Access until", "Доступ до") + ", " + b.TZ(lang)}, rows, nil)
	var shown []access.User
	for _, u := range members {
		if u.TgID != user.TgID && len(shown) < 50 {
			shown = append(shown, u)
		}
	}
	for start := 0; start < len(shown); start += 2 {
		var row []rich.Button
		for _, u := range shown[start:min(len(shown), start+2)] {
			row = append(row, rich.B(u.TgName, "mc:"+itoa(u.TgID)))
		}
		doc.Buttons(row...)
	}
	return doc
}

func (b *Bot) memberCard(user access.User, group groups.Group, tgID int64) *rich.Doc {
	lang := user.Lang
	for _, member := range b.members(group) {
		if member.TgID != tgID {
			continue
		}
		rows := [][]any{
			{"Telegram", Who(member)},
			{tr(lang, "Full name", "ФИО"), orDash(member.Student)},
			{tr(lang, "Rank", "Ранг"), rankText(member, lang)},
			{tr(lang, "Joined", "С нами с"), b.When(member.JoinedAt, lang)},
			{tr(lang, "Access until", "Доступ до"), b.until(member, lang, true)},
			{tr(lang, "Code", "Код"), orDash(member.Note)},
			{tr(lang, "Subjects", "Предметы"), b.subjectNames(member)},
		}
		if member.Paused {
			rows = append(rows, []any{tr(lang, "State", "Состояние"), tr(lang, "blocked the bot", "заблокировал(а) бота")})
		}
		return rich.New().H(member.TgName).TableHTML(nil, rows, nil).Buttons(rich.B(tr(lang, "« Group", "« Группа"), "ml"))
	}
	return b.membersView(user, group)
}

// --- shared sheets ----------------------------------------------------------------------------------------------

func (b *Bot) adminSheetAdded(chat int64, user access.User, group groups.Group, data Data) {
	lang := user.Lang
	if now, ok := b.Access.User(user.TgID); !ok {
		return
	} else if _, admin := b.adminGroup(now); !admin {
		return
	}
	sheet, added, err := b.Groups.AddSheet(group.ID, data["link"], user.TgID, nil)
	if err != nil {
		panic(err)
	}
	b.send(chat, rich.New().P(rich.Bold(sheets.Name(sheet)), tr(lang, " is a shared sheet of the group ",
		" — общая таблица группы "), rich.Bold(group.Name), ". ", tr(lang,
		sf("Everyone in the group follows it, newcomers too; added now: %d.", added),
		sf("На неё подписаны все в группе, и новички тоже; добавлено сейчас: %d.", added))))
	owner := b.OwnerLang()
	b.News([]any{Who(user), tr(owner, ", admin of ", ", админ "), rich.Bold(group.Name),
		tr(owner, ", added the shared sheet ", ", добавил(а) общую таблицу "), rich.Link(sheets.Name(sheet), sheet.URL)},
		rich.B(tr(owner, "Set a short name", "Задать имя"), "rn:"+itoa(sheet.ID)))
	b.ReadSoon(0, []int64{sheet.ID})
}

// --- news -------------------------------------------------------------------------------------------------------

// movedNews: a user left a group or joined one, the admins of each hear of it.
func (b *Bot) movedNews(user access.User, before groups.Group, hadBefore bool, after groups.Group, hasAfter bool) {
	if hadBefore == hasAfter && before.ID == after.ID {
		return
	}
	if hadBefore {
		b.GroupNews(before.ID, user.TgID, func(lang string) []any {
			return []any{Who(user), tr(lang, " left the group", ": больше не в группе")}
		})
		if fresh, ok := b.Access.User(user.TgID); ok {
			b.adminMenu(fresh) // an admin who left their group runs it no more
		}
	}
	if hasAfter {
		b.GroupNews(after.ID, user.TgID, func(lang string) []any {
			return []any{Who(user), tr(lang, " joined the group", ": теперь в группе")}
		})
	}
}

func (b *Bot) newsView(user access.User) *rich.Doc {
	lang := user.Lang
	mode := Digest
	b.Store.GetJSON(newsMode(user.TgID), &mode)
	names := map[string]string{Digest: tr(lang, "a quiet digest", "тихой сводкой"),
		Each: tr(lang, "a message each", "каждую отдельно"), Off: tr(lang, "none", "не присылать")}
	doc := rich.New().P(tr(lang, "News of your group: who came in, set a name, joined or left, and whose access ends. "+
		"Now: ", "Новости группы: кто вошёл, указал ФИО, пришёл или ушёл и у кого кончается доступ. Сейчас: "),
		rich.Bold(names[mode]), ".")
	doc.P(rich.Italic(tr(lang, "A digest is one silent message, edited as news comes, until you write to the bot; a "+
		"message each comes with a sound.",
		"Сводка — одно сообщение без звука, оно дополняется, пока ты не напишешь боту; каждая отдельно — со звуком.")))
	labels := map[string]string{Digest: tr(lang, "Quiet digest", "Тихой сводкой"), Each: tr(lang, "Each one",
		"Каждую отдельно"), Off: tr(lang, "None", "Не присылать")}
	var buttons []rich.Button
	for _, m := range modes {
		mark := ""
		if m == mode {
			mark = "✓ "
		}
		buttons = append(buttons, rich.B(mark+labels[m], "nw:"+m))
	}
	return doc.Buttons(buttons...)
}
