package bot

import (
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/IvanIkra/gradebot/internal/access"
	"github.com/IvanIkra/gradebot/internal/clock"
	"github.com/IvanIkra/gradebot/internal/groups"
	"github.com/IvanIkra/gradebot/internal/i18n"
	"github.com/IvanIkra/gradebot/internal/layout"
	"github.com/IvanIkra/gradebot/internal/profile"
	"github.com/IvanIkra/gradebot/internal/rich"
	"github.com/IvanIkra/gradebot/internal/sheets"
	"github.com/IvanIkra/gradebot/internal/store"
)

// A user's own commands: grades, the full name, sheets, quiet hours, language and leaving.
//
// Callbacks: `grades` sends /grades, `rf` redraws it in place, `wr:<sheet>` complains about a sheet's layout,
// `pt:<s subject|h sheet>` sends a unit's points by problem, `ck:<sheet>` reads a sheet now, `us:<sheet>` unfollows
// it, `cr:<request>` cancels a request for a sheet waiting for the owner, `gp:<group>` moves the user to a group (0:
// none) from /group, `gj:<group>` from the first message, `add` asks for a sheet to follow, `qt:<hours|off>` sets
// quiet hours, `lg:<en|ru>` the language, `stop` and `stop!` cancel and confirm /stop; `sj:…` are /subjects'
// (subjects.go), `fm:…` /formula's (formulas.go), `gl:…` /goal's (goals.go).

const (
	ComplaintsEvery = 24 * time.Hour // one complaint about a sheet's layout a day from a user
	FeedbackPerDay  = 5              // /feedback messages a day from a user
	FeedbackChars   = 1000
)

// groupButtons is a button for each group, four in a row, and «No group»: callback `<kind>:<group id>`, 0 for none.
func groupButtons(doc *rich.Doc, all []groups.Group, kind, lang string) *rich.Doc {
	return groupButtonsNone(doc, all, kind, lang, true)
}

func groupButtonsNone(doc *rich.Doc, all []groups.Group, kind, lang string, none bool) *rich.Doc {
	var choice []rich.Button
	for _, g := range all {
		choice = append(choice, rich.B(g.Name, kind+":"+itoa(g.ID)))
	}
	if none {
		choice = append(choice, rich.B(tr(lang, "No group", "Без группы"), kind+":0"))
	}
	for start := 0; start < len(choice); start += 4 {
		doc.Buttons(choice[start:min(len(choice), start+4)]...)
	}
	return doc
}

func (b *Bot) userCommand(chat int64, user access.User, cmd, arg string) {
	lang := user.Lang
	if cmd == "" && sheets.GoogleSheet.MatchString(arg) { // a link pasted alone: follow it
		cmd = "/sheet"
	}
	if b.adminCommand(chat, user, cmd, arg) {
		return
	}
	switch cmd {
	case "/grades":
		b.cmdGrades(chat, user, arg)
	case "/name":
		b.cmdName(chat, user, arg)
	case "/sheet":
		b.cmdSheet(chat, user, arg)
	case "/sheets":
		b.send(chat, b.sheetsView(user))
	case "/group":
		b.send(chat, b.groupChoice(user))
	case "/quiet":
		b.cmdQuiet(chat, user, arg)
	case "/lang":
		b.cmdLang(chat, user, arg)
	case "/feedback":
		b.cmdFeedback(chat, user, arg)
	case "/stop":
		b.cmdStop(chat, user)
	case "/subjects":
		b.cmdSubjects(chat, &user, arg, lang)
	case "/formula":
		b.cmdFormula(chat, user, arg)
	case "/goal":
		b.cmdGoal(chat, user, arg)
	case "/start":
		b.send(chat, tr(lang, "You're already in. /help lists the commands.", "Ты уже в списке. Команды — в /help."))
	default:
		b.send(chat, rich.New().P(rich.Italic(tr(lang, "That's not a command. Here is what I can do:",
			"Это не команда. Вот что я умею:"))).Extend(b.userHelp(lang, &user)))
	}
}

func (b *Bot) userCallback(chat, messageID int64, user access.User, kind, arg string) string {
	lang := user.Lang
	if adminKinds[kind] {
		return b.adminCallback(chat, messageID, user, kind, arg)
	}
	switch kind {
	case "grades":
		b.cmdGrades(chat, user, "")
	case "sj":
		return b.subjectsCallback(chat, messageID, &user, arg, lang)
	case "fm":
		return b.formulaCallback(chat, messageID, &user, arg, lang)
	case "gl":
		return b.goalCallback(chat, messageID, user, arg, lang)
	case "add":
		b.cmdSheet(chat, user, "")
	case "rf":
		b.edit(chat, messageID, b.gradesView(user))
		return tr(lang, "Updated", "Обновлено")
	case "pt":
		for _, found := range b.unitGrades(user, false) {
			if unitRef(found.unit) == arg {
				for _, doc := range problemsDocs(found.unit, found.items, lang) {
					b.send(chat, doc)
				}
				return ""
			}
		}
	case "wr":
		if isDigits(arg) {
			return b.complain(user, atoi(arg))
		}
	case "ck":
		if isDigits(arg) {
			if user.Student == "" {
				return tr(lang, "Set your full name first: /name", "Сначала укажи ФИО: /name")
			}
			for _, s := range b.Sheets.Followed(user.TgID) {
				if s.ID == atoi(arg) {
					b.ReadSoon(user.TgID, []int64{s.ID})
					return tr(lang, "Reading it, I'll write what I find", "Читаю, напишу, что нашёл")
				}
			}
		}
	case "us":
		if isDigits(arg) {
			done := b.Sheets.Unfollow(user.TgID, atoi(arg))
			b.edit(chat, messageID, b.sheetsView(user))
			if done {
				return tr(lang, "Unfollowed", "Подписка снята")
			}
		}
	case "cr":
		if isDigits(arg) {
			_, found := b.Sheets.DropRequest(atoi(arg), user.TgID)
			b.edit(chat, messageID, b.sheetsView(user))
			if found {
				return tr(lang, "Request cancelled", "Заявка отменена")
			}
		}
	case "gp", "gj":
		if isDigits(arg) {
			return b.chooseGroup(chat, messageID, user, atoi(arg), kind == "gj")
		}
	case "qt":
		hours, err := profile.Quiet(arg)
		if err != nil {
			return ""
		}
		b.Access.SetQuiet(user.TgID, hours)
		now, _ := b.Access.User(user.TgID)
		b.edit(chat, messageID, b.quietView(now))
		return tr(lang, "Saved", "Сохранено")
	case "lg":
		if slices.Contains(i18n.Languages, arg) {
			b.Access.SetLang(user.TgID, arg)
			b.edit(chat, messageID, langView(arg))
		}
	case "stop!":
		b.Access.Delete(user.TgID)
		b.Sheets.DropOrphans()
		b.edit(chat, messageID, tr(lang, "Your data is erased. To come back you'll need a new invite code.",
			"Твои данные удалены. Чтобы вернуться, нужен новый код приглашения."))
		left := tr(b.OwnerLang(), "Left the bot with /stop: ", "Выход из бота через /stop: ")
		b.News([]any{left, Who(user)})
		b.GroupNews(user.Grp, user.TgID, func(lang string) []any {
			return []any{Who(user), tr(lang, " left the bot with /stop", ": вышел(а) из бота через /stop")}
		})
	case "stop":
		b.edit(chat, messageID, tr(lang, "Staying, nothing erased.", "Остаёмся, ничего не удалено."))
	}
	return ""
}

func (b *Bot) userHelp(lang string, user *access.User) *rich.Doc {
	rows := [][]string{
		{"/grades", tr(lang, "your grades, the new ones marked", "твои оценки, новые отмечены")},
		{"/name " + tr(lang, "Surname Name", "Фамилия Имя"), tr(lang, "your full name as the sheets have it",
			"ФИО, как в таблицах")},
		{"/sheet " + tr(lang, "<link>", "<ссылка>"), tr(lang, "follow a Google Sheet; a link sent alone does the same",
			"подписаться на таблицу Google; можно просто прислать ссылку")},
		{"/sheets", tr(lang, "your sheets: check, unfollow", "твои таблицы: проверить, отписаться")},
		{"/subjects", tr(lang, "subjects: several sheets of one course as one",
			"предметы: несколько таблиц одного курса как одна")},
		{"/formula", tr(lang, "a subject's grade formula, as the teacher wrote it",
			"формула оценки предмета, как у преподавателя")},
		{"/goal", tr(lang, "the grade you aim at: what is needed for it", "к какой оценке идёшь: сколько для неё нужно")},
	}
	if len(b.Groups.All()) > 0 {
		rows = append(rows, []string{"/group", tr(lang, "your group: you follow its shared sheets",
			"твоя группа и её общие таблицы")})
	}
	rows = append(rows,
		[]string{"/quiet", tr(lang, "quiet hours", "тихие часы")},
		[]string{"/lang", tr(lang, "language", "язык")},
		[]string{"/feedback " + tr(lang, "<text>", "<текст>"), tr(lang,
			"write to the bot's owner: a question, a bug, an idea", "написать владельцу бота: вопрос, ошибка, идея")},
		[]string{"/stop", tr(lang, "erase your data and leave", "удалить свои данные и уйти")})
	doc := rich.New().H(tr(lang, "Commands", "Команды")).Table(nil, rows, nil)
	if user != nil {
		if _, ok := b.adminGroup(*user); ok {
			doc.Extend(b.adminHelp(*user))
		}
	}
	return doc.P(asksHint(lang))
}

// --- the full name ----------------------------------------------------------------------------------------------

// cmdName is `/name Surname Name`, or asked for.
func (b *Bot) cmdName(chat int64, user access.User, arg string) {
	lang, now := user.Lang, user.Student
	step := Step{Name: "student",
		Prompt: func(Data) *rich.Doc {
			if now != "" {
				return rich.New().P(tr(lang, "Your full name: ", "Твоё ФИО: "), rich.Bold(now), ". ",
					tr(lang, "To change it, send the new one exactly as the sheets have it.",
						"Чтобы сменить, пришли новое — точно как в таблицах."))
			}
			return rich.New().P(rich.Bold(tr(lang, "What's your name in the sheets?", "Как ты записан в таблицах?")), " ",
				tr(lang, "Send your surname and name exactly as they are there, for example ",
					"Пришли фамилию и имя точно так же, например "),
				rich.Code(tr(lang, "Ivanov Ivan", "Иванов Иван")), ". ",
				tr(lang, "That's how I find your row.", "По ним я найду твою строку."))
		},
		Check: func(value string, _ Data) (string, error) { return profile.Student(value) },
		Error: func(lang string, _ error) string {
			return tr(lang, "That doesn't look like a full name: two words or more, letters only, as in the sheets.",
				"Это не похоже на ФИО: нужно два слова или больше, только буквы — как в таблицах.")
		}}
	var data Data
	if arg != "" {
		data = Data{"student": arg}
	}
	b.Flow(chat, lang, []Step{step}, func(data Data) { b.setName(chat, user.TgID, data["student"]) }, data)
}

func (b *Bot) setName(chat, tgID int64, student string) {
	user, ok := b.Access.User(tgID)
	if !ok {
		return
	}
	lang := user.Lang
	if student == user.Student {
		b.send(chat, tr(lang, "That's your name already.", "Это ФИО уже записано."))
		return
	}
	b.Access.SetStudent(user.TgID, student)
	b.Grades.Forget(user.TgID, 0) // other rows now: the next read is a baseline
	followed := b.Sheets.Followed(user.TgID)
	doc := rich.New().P(tr(lang, "Saved: ", "Записал: "), rich.Bold(student), ".")
	if len(followed) > 0 {
		doc.P(tr(lang, "Reading your sheets now; I'll write what I find.",
			"Сейчас прочитаю твои таблицы и напишу, что нашёл."))
	} else {
		doc.P(noSheets(lang))
	}
	b.send(chat, doc)
	owner := b.OwnerLang()
	parts := []any{Who(user), tr(owner, " set the full name ", ": ФИО "), rich.Bold(student)}
	if user.Student != "" {
		parts = append(parts, tr(owner, " (was ", " (было "), user.Student, ")")
	}
	b.News(parts, rich.BS(tr(owner, "Reset the name of ", "Сбросить ФИО: ")+user.TgName, "nr:"+itoa(user.TgID),
		"danger"))
	b.GroupNews(user.Grp, user.TgID, func(lang string) []any {
		return []any{Who(user), tr(lang, " set the full name ", ": ФИО "), rich.Bold(student)}
	})
	var ids []int64
	for _, s := range followed {
		ids = append(ids, s.ID)
	}
	b.ReadSoon(user.TgID, ids)
}

// --- sheets -----------------------------------------------------------------------------------------------------

// cmdSheet is `/sheet <link>`, or the link asked for. A sheet is named by its title in Google; its short name and
// hashtag are the owner's to set (/rename).
func (b *Bot) cmdSheet(chat int64, user access.User, arg string) {
	var data Data
	if fields := strings.Fields(arg); len(fields) > 0 {
		data = Data{"link": fields[0]}
	}
	b.Flow(chat, user.Lang, []Step{linkStep(user.Lang, "link")}, func(data Data) { b.addSheet(chat, user.TgID, data) },
		data)
}

func (b *Bot) addSheet(chat, tgID int64, data Data) {
	user, ok := b.Access.User(tgID)
	if !ok {
		return
	}
	lang, url := user.Lang, data["link"]
	known, isKnown := b.Sheets.ByURL(url)
	_ = known
	if !isKnown && user.Rank < access.Trusted {
		if err := b.askOwner(chat, user, url); err != nil {
			b.send(chat, followError(err, lang))
		}
		return
	}
	sheet, isNew, err := b.Sheets.Follow(user.TgID, url, "")
	if err != nil {
		if !sheets.IsError(err) {
			panic(err)
		}
		b.send(chat, followError(err, lang))
		return
	}
	if !isNew {
		b.send(chat, tr(lang, "You follow this sheet already.", "Эта таблица уже в твоих подписках."))
		return
	}
	b.send(chat, following(user, sheet))
	if !isKnown { // a trusted user's sheet new to the bot: its name is the owner's to set
		owner := b.OwnerLang()
		b.News([]any{Who(user), tr(owner, " follows a sheet new to the bot: ", ": новая таблица "),
			rich.Link(sheets.Name(sheet), sheet.URL)}, rich.B(tr(owner, "Set a short name", "Задать имя"),
			"rn:"+itoa(sheet.ID)))
	}
	if user.Student != "" {
		b.ReadSoon(user.TgID, []int64{sheet.ID})
	}
}

func followError(err error, lang string) string {
	if strings.Contains(err.Error(), "at most") {
		return tr(lang, "You follow 20 sheets already: unfollow one in /sheets.",
			"У тебя уже 20 таблиц: отпишись от какой-нибудь в /sheets.")
	}
	return tr(lang, "That's not a Google Sheets link: https://docs.google.com/spreadsheets/d/…",
		"Это не ссылка на таблицу Google: https://docs.google.com/spreadsheets/d/…")
}

// following is the answer to a sheet the user started to follow.
func following(user access.User, sheet sheets.Sheet) *rich.Doc {
	lang := user.Lang
	title := sheet.Course // a sheet new to the bot has neither until it is read
	if title == "" {
		title = sheet.Title
	}
	parts := []any{tr(lang, "Following the sheet", "Подписал на таблицу")}
	if title != "" {
		parts = append(parts, " ", rich.Bold(title))
	}
	parts = append(parts, ". ", tr(lang, "The sheet must be open to anyone with the link.",
		"Таблица должна быть открыта всем, у кого есть ссылка."))
	doc := rich.New().P(parts...)
	if user.Student == "" {
		return doc.P(tr(lang, "To find your row I need your full name: ", "Чтобы найти твою строку, нужно ФИО: "),
			rich.Code(tr(lang, "/name Surname Name", "/name Фамилия Имя")))
	}
	return doc.P(tr(lang, "Reading it now; I'll write what I find.", "Сейчас прочитаю её и напишу, что нашёл."))
}

// askOwner: a member's sheet the bot does not read yet; the owner approves it first. A sheets error as Follow.
func (b *Bot) askOwner(chat int64, user access.User, url string) error {
	lang := user.Lang
	request, isNew, err := b.Sheets.Ask(user.TgID, url, "")
	if err != nil {
		return err
	}
	if !isNew {
		b.send(chat, tr(lang, "This sheet is already waiting for the owner's approval.",
			"Эта таблица уже ждёт одобрения владельца."))
		return nil
	}
	admins := b.Access.Admins(user.Grp)
	if len(admins) > 0 {
		b.send(chat, rich.New().P(tr(lang,
			"The bot doesn't read this sheet yet, so the owner or your group's admin approves it first. I'll write "+
				"when they do.",
			"Бот ещё не читает эту таблицу, поэтому сначала её одобрит владелец или админ группы. Напишу, когда "+
				"ответят.")))
	} else {
		b.send(chat, rich.New().P(tr(lang,
			"The bot doesn't read this sheet yet, so the owner approves it first. I'll write when they do.",
			"Бот ещё не читает эту таблицу, поэтому сначала её одобрит владелец. Напишу, когда он ответит.")))
	}
	asking := func(lang string) *rich.Doc {
		parts := []any{Who(user), tr(lang, " asks to follow a sheet the bot doesn't read yet: ",
			": просьба подписать на таблицу, которую бот ещё не читает: "), rich.Link(request.URL, request.URL)}
		if request.Course != "" {
			parts = append(parts, tr(lang, ", short name ", ", имя "), rich.Bold(request.Course))
		}
		return rich.New().P(parts...).Buttons(
			rich.BS(tr(lang, "Approve", "Одобрить"), "sa:"+itoa(request.ID), "success"),
			rich.BS(tr(lang, "Decline", "Отклонить"), "sd:"+itoa(request.ID), "danger"))
	}
	b.TellOwner(asking(b.OwnerLang()))
	for _, admin := range admins {
		b.Tell(admin, asking(admin.Lang), false)
	}
	return nil
}

// --- the group --------------------------------------------------------------------------------------------------

func (b *Bot) groupChoice(user access.User) *rich.Doc {
	lang, all := user.Lang, b.Groups.All()
	mine, hasMine := b.Groups.Get(user.Grp)
	var name any = tr(lang, "none", "не выбрана")
	if hasMine {
		name = rich.Bold(mine.Name)
	}
	doc := rich.New().P(tr(lang, "Your group: ", "Твоя группа: "), name, ".")
	if groups.Fixed(user) {
		return doc.P(tr(lang, "It came with your invite code: only the owner moves you to another one.",
			"Она пришла с кодом приглашения: перевести тебя в другую может только владелец."))
	}
	var others []groups.Group
	for _, g := range all {
		if !hasMine || g.ID != mine.ID {
			others = append(others, g)
		}
	}
	if len(others) == 0 && !hasMine {
		return doc.P(tr(lang, "There are no groups yet.", "Групп пока нет."))
	}
	doc.P(tr(lang, "You follow the shared sheets of your group. Moving to another group swaps them: the old ones go "+
		"with their grades, the new ones come.",
		"Ты подписан на общие таблицы своей группы. В другой группе старые уйдут вместе с оценками, а новые "+
			"добавятся."))
	return groupButtonsNone(doc, others, "gp", lang, hasMine)
}

func (b *Bot) chooseGroup(chat, messageID int64, user access.User, gid int64, first bool) string {
	lang := user.Lang
	before, hadBefore := b.Groups.Get(user.Grp)
	if groups.Fixed(user) {
		return tr(lang, "Only the owner changes your group", "Группу меняет только владелец")
	}
	left, followed, err := b.Groups.Join(user.TgID, gid)
	if err != nil {
		return ""
	}
	user, _ = b.Access.User(user.TgID)
	group, hasGroup := b.Groups.Get(user.Grp)
	if first {
		b.edit(chat, messageID, b.welcome(lang, group, hasGroup, len(followed), user.ExpiresAt))
		if user.Student == "" && b.Waiting(chat) == nil {
			b.cmdName(chat, user, "") // the next step of coming in
		}
	} else {
		doc := b.groupChoice(user)
		if left > 0 || len(followed) > 0 {
			doc.P(tr(lang, sf("Unfollowed sheets: %d, followed: %d.", left, len(followed)),
				sf("Отписал от таблиц: %d, подписал на: %d.", left, len(followed))))
		}
		b.edit(chat, messageID, doc)
	}
	if before.ID != group.ID || hadBefore != hasGroup {
		owner := b.OwnerLang()
		var news []any
		if !hasGroup {
			news = []any{tr(owner, " left the group ", ": больше не в группе "), rich.Bold(before.Name)}
		} else {
			news = []any{tr(owner, " is in the group ", ": теперь в группе "), rich.Bold(group.Name)}
			if hadBefore {
				news = append(news, tr(owner, " (was ", " (была "), before.Name, ")")
			}
		}
		b.News(append([]any{Who(user)}, news...))
		b.movedNews(user, before, hadBefore, group, hasGroup)
	}
	if user.Student != "" && len(followed) > 0 {
		b.ReadSoon(user.TgID, followed)
	}
	return tr(lang, "Saved", "Сохранено")
}

// sheetProblem is what is wrong with a followed sheet now, or "".
func sheetProblem(s sheets.Sheet, lang string) string {
	if s.Error != "" {
		return Explain(s.Error, lang)
	}
	if s.UserError != "" {
		return Explain(s.UserError, lang)
	}
	return ""
}

func (b *Bot) sheetsView(user access.User) *rich.Doc {
	lang, followed := user.Lang, b.Sheets.Followed(user.TgID)
	doc := rich.New()
	if len(followed) > 0 {
		b.sheetsTable(doc, user, followed)
	} else {
		doc.P(noSheets(lang))
	}
	if requests := b.Sheets.Requests(user.TgID); len(requests) > 0 {
		doc.P(tr(lang, "Waiting for the owner's approval:", "Ждут одобрения владельца:"))
		var rows [][]any
		for _, r := range requests {
			rows = append(rows, []any{rich.Link(r.URL, r.URL)})
		}
		doc.TableHTML(nil, rows, nil)
		for n, r := range requests[:min(len(requests), 30)] {
			doc.Buttons(rich.BS(tr(lang, sf("Cancel request %d", n+1), sf("Отменить заявку %d", n+1)),
				"cr:"+itoa(r.ID), "danger"))
		}
	}
	return doc.Buttons(addButton(lang))
}

func noSheets(lang string) string {
	return tr(lang, "No sheets yet. Send me a link to a Google Sheet with your grades: just paste it here.",
		"Таблиц пока нет. Пришли ссылку на таблицу Google с оценками — просто вставь её сюда.")
}

func addButton(lang string) rich.Button {
	return rich.BS(tr(lang, "Add a sheet", "Добавить таблицу"), "add", "primary")
}

func (b *Bot) sheetsTable(doc *rich.Doc, user access.User, followed []sheets.Sheet) {
	lang := user.Lang
	never := tr(lang, "not yet", "ещё нет")
	var rows [][]any
	problems := false
	for _, s := range followed {
		read := never
		if s.ReadAt != "" {
			read = b.When(s.ReadAt, "")
		}
		problem := sheetProblem(s, lang)
		problems = problems || problem != ""
		rows = append(rows, []any{rich.Link(sheets.Name(s), s.URL), read, itoa(int64(b.Grades.Count(user.TgID, s.ID))),
			problem})
	}
	header := []any{tr(lang, "Sheet", "Таблица"), tr(lang, "Read", "Прочитана") + ", " + b.TZ(lang),
		tr(lang, "Grades", "Оценок"), tr(lang, "Problem", "Ошибка")}
	if !problems {
		header = header[:3]
		for n := range rows {
			rows[n] = rows[n][:3]
		}
	}
	doc.H(tr(lang, "Your sheets", "Твои таблицы")+sf(" · %d", len(followed))).TableHTML(header, rows,
		[]string{"", "", "right", ""})
	if user.Student == "" {
		doc.P(tr(lang, "Your rows are looked for once you set your full name: ", "Строки ищутся, когда указано ФИО: "),
			rich.Code(tr(lang, "/name Surname Name", "/name Фамилия Имя")))
	}
	for _, s := range followed {
		doc.Buttons(rich.B(tr(lang, "Check ", "Проверить: ")+sheets.Name(s), "ck:"+itoa(s.ID)),
			rich.BS(tr(lang, "Unfollow ", "Отписаться: ")+sheets.Name(s), "us:"+itoa(s.ID), "danger"))
	}
}

// --- grades -----------------------------------------------------------------------------------------------------

func (b *Bot) cmdGrades(chat int64, user access.User, arg string) {
	if user.Student == "" { // nothing to show without it: ask for it
		b.cmdName(chat, user, "")
		return
	}
	b.send(chat, b.gradesView(user))
}

// gradesView is every followed sheet as details with its grades; the ones with new grades open. When it is too long
// for one message, the tables of the sheets without new grades go first, then all of them.
func (b *Bot) gradesView(user access.User) *rich.Doc {
	lang, followed := user.Lang, b.Sheets.Followed(user.TgID)
	if user.Student == "" {
		return rich.New().P(tr(lang, "First your full name as the sheets have it: ", "Сначала ФИО, как в таблицах: "),
			rich.Code(tr(lang, "/name Surname Name", "/name Фамилия Имя")))
	}
	if len(followed) == 0 {
		return rich.New().P(noSheets(lang)).Buttons(addButton(lang))
	}
	found := b.unitGrades(user, true)
	var doc *rich.Doc
	for _, tables := range []string{"all", "new", "none"} {
		doc = rich.New().H(tr(lang, "Your grades", "Твои оценки"))
		for _, f := range found {
			fresh := false
			for _, it := range f.items {
				fresh = fresh || it.New
			}
			doc.Extend(gradesSection(f.unit, f.items, lang, f.problems, tables == "all" || (tables == "new" && fresh)))
		}
		if tables == "new" {
			doc.P(tr(lang, "The list is long: only sheets with new grades show their tables.",
				"Список длинный: таблицы показаны только там, где есть новые оценки."))
		} else if tables == "none" {
			doc.P(tr(lang, "The list is too long for tables: unfollow some sheets in /sheets.",
				"Список слишком длинный для таблиц: отпишись от части таблиц в /sheets."))
		}
		doc.Buttons(rich.B(tr(lang, "Refresh", "Обновить"), "rf"))
		if doc.Fits() {
			break
		}
	}
	return doc
}

type unitGrades struct {
	unit     *Unit
	items    []*Row
	problems []string
}

// unitGrades gives (unit, its grades, what is wrong with its sheets) for every unit of the user's, in /grades order.
func (b *Bot) unitGrades(user access.User, notes bool) []unitGrades {
	lang := user.Lang
	units := b.Subjects.Units(user, b.Sheets.Followed(user.TgID))
	if notes {
		b.withNotes(user, units, false)
	}
	var found []unitGrades
	for _, unit := range units {
		var items []*Row
		var problems []string
		for _, s := range unit.Sheets {
			for _, it := range b.Grades.Items(user.TgID, s.ID) {
				items = append(items, rowOfItem(it))
			}
			problem := sheetProblem(s, lang)
			if problem == "" && s.BaselineAt == "" {
				problem = tr(lang, "Not read yet.", "Ещё не прочитана.")
			}
			if problem != "" {
				if unit.Subject != nil {
					problem = sheets.Name(s) + ": " + problem
				}
				problems = append(problems, problem)
			}
		}
		items, _ = dropCopies(items, nil) // «ДЗ 1» of the group's sheet and of the gradebook: once
		found = append(found, unitGrades{unit, items, problems})
	}
	return found
}

// complain: «Wrong» under a sheet in /grades, the owner hears what is laid out now and may lay it out again.
func (b *Bot) complain(user access.User, sheetID int64) string {
	lang := user.Lang
	sheet, ok := b.Sheets.Get(sheetID)
	followed := false
	for _, s := range b.Sheets.Followed(user.TgID) {
		followed = followed || s.ID == sheetID
	}
	if !ok || !followed {
		return ""
	}
	since := clock.ISO(b.Now().Add(-ComplaintsEvery))
	if store.Has(b.Store, "select 1 from layout_reports where tg_id = ? and sheet = ? and created_at >= ?",
		user.TgID, sheetID, since) {
		return tr(lang, "Already sent today", "Уже отправлено сегодня")
	}
	b.Store.Tx(func() {
		b.Store.Exec("insert into layout_reports(tg_id, sheet, created_at) values(?, ?, ?)", user.TgID, sheetID,
			clock.ISO(b.Now()))
	})
	owner := b.OwnerLang()
	var order []string
	tabs := map[string][]string{}
	for _, it := range b.Grades.Items(user.TgID, sheetID) {
		if _, seen := tabs[it.Tab]; !seen {
			order = append(order, it.Tab)
		}
		header := it.Header
		if header == "" {
			header = "—"
		}
		tabs[it.Tab] = append(tabs[it.Tab], layout.Label(it.Label, it.LabelRu, owner)+" ("+header+")")
	}
	doc := rich.New().P(Who(user), tr(owner, " says the layout of ", " считает неверной разметку "),
		rich.Link(sheets.Name(sheet), sheet.URL), tr(owner, " is wrong.", "."))
	var rows [][]string
	for _, tab := range order {
		rows = append(rows, []string{tab, strings.Join(tabs[tab], "; ")})
	}
	doc.Table([]string{tr(owner, "Tab", "Вкладка"), tr(owner, "Works laid out (header)", "Задания (заголовок)")}, rows,
		nil)
	b.TellOwner(doc.Buttons(rich.B(tr(owner, "Lay out again", "Разметить заново"), "rl:"+itoa(sheetID)),
		rich.B(tr(owner, "Open the sheet", "Открыть таблицу"), sheet.URL)))
	return tr(lang, "Sent to the owner", "Отправлено владельцу")
}

// --- quiet hours and language -----------------------------------------------------------------------------------

// cmdQuiet is `/quiet 23-8`, `/quiet off`, or the hours now with buttons, and other ones typed.
func (b *Bot) cmdQuiet(chat int64, user access.User, arg string) {
	step := Step{Name: "hours", NoCancel: true,
		Prompt: func(Data) *rich.Doc {
			now, _ := b.Access.User(user.TgID)
			return b.quietView(now)
		},
		Check: func(value string, _ Data) (string, error) {
			_, err := profile.Quiet(value)
			return value, err
		},
		Error: func(lang string, _ error) string { return tr(lang, "Hours like 23-8, or off.", "Часы вида 23-8 или off.") }}
	var data Data
	if arg != "" {
		data = Data{"hours": arg}
	}
	b.Flow(chat, user.Lang, []Step{step}, func(data Data) {
		hours, _ := profile.Quiet(data["hours"])
		b.Access.SetQuiet(user.TgID, hours)
		now, _ := b.Access.User(user.TgID)
		b.send(chat, b.quietView(now))
	}, data)
}

func (b *Bot) quietView(user access.User) *rich.Doc {
	lang, hours := user.Lang, profile.Hours(user)
	var shown any = tr(lang, "off", "выключены")
	if hours != "" {
		shown = rich.Bold(hours + " " + b.TZ(lang))
	} else {
		shown = rich.Bold(shown)
	}
	doc := rich.New().P(tr(lang, "Quiet hours: ", "Тихие часы: "), shown, ". ",
		tr(lang, "Grades then come at once, but without a sound.", "В это время оценки приходят сразу, но без звука."))
	doc.P(tr(lang, "Other hours: send them, like ", "Другие часы — пришли их, например "), rich.Code("22-7"), ".")
	d := profile.QuietDefault
	var buttons []rich.Button
	if hours != sf("%d:00–%d:00", d[0], d[1]) {
		buttons = append(buttons, rich.B(sf("%d:00–%d:00", d[0], d[1]), sf("qt:%d-%d", d[0], d[1])))
	}
	if hours != "" {
		buttons = append(buttons, rich.B(tr(lang, "Turn off", "Выключить"), "qt:off"))
	}
	return doc.Buttons(buttons...)
}

func (b *Bot) cmdLang(chat int64, user access.User, arg string) {
	if lang := strings.ToLower(arg); slices.Contains(i18n.Languages, lang) {
		b.Access.SetLang(user.TgID, lang)
		b.send(chat, langView(lang))
		return
	}
	b.send(chat, langView(user.Lang))
}

func langView(lang string) *rich.Doc {
	en, ru := "English", "Русский"
	if lang == "en" {
		en += " ✓"
	}
	if lang == "ru" {
		ru += " ✓"
	}
	return rich.New().P(tr(lang, "Language: English.", "Язык: русский.")).Buttons(rich.B(en, "lg:en"), rich.B(ru, "lg:ru"))
}

// --- feedback ---------------------------------------------------------------------------------------------------

// cmdFeedback is `/feedback <text>`, or the text asked for: it goes to the owner, who may answer with a button.
func (b *Bot) cmdFeedback(chat int64, user access.User, arg string) {
	lang := user.Lang
	step := Step{Name: "text",
		Prompt: func(Data) *rich.Doc {
			return rich.New().P(tr(lang, "What would you like to tell the bot's owner? A question, a bug, an idea: one "+
				"message.", "Что передать владельцу бота? Вопрос, ошибка, идея — одним сообщением."))
		},
		Check: func(value string, _ Data) (string, error) {
			if utf8.RuneCountInString(value) > FeedbackChars {
				return "", errTooLong
			}
			return value, nil
		},
		Error: func(lang string, _ error) string {
			return tr(lang, sf("At most %d characters, please.", FeedbackChars),
				sf("Не больше %d символов, пожалуйста.", FeedbackChars))
		}}
	var data Data
	if trimPy(arg) != "" {
		data = Data{"text": trimPy(arg)}
	}
	b.Flow(chat, lang, []Step{step}, func(data Data) { b.sendFeedback(chat, user.TgID, data["text"]) }, data)
}

type plainError string

func (e plainError) Error() string { return string(e) }

const errTooLong = plainError("too long")

func (b *Bot) sendFeedback(chat, tgID int64, text string) {
	user, ok := b.Access.User(tgID)
	if !ok {
		return
	}
	lang, since := user.Lang, b.Now().Add(-24*time.Hour)
	var sent []time.Time
	for _, moment := range b.feedback[tgID] {
		if moment.After(since) {
			sent = append(sent, moment)
		}
	}
	if len(sent) >= FeedbackPerDay {
		b.send(chat, tr(lang, "That's enough for today: write again tomorrow.", "На сегодня хватит: напиши завтра."))
		return
	}
	b.feedback[tgID] = append(sent, b.Now())
	owner := b.OwnerLang()
	b.TellOwner(rich.New().P(Who(user), tr(owner, " writes:", " пишет:")).Quote(text, false).Buttons(
		rich.BS(tr(owner, "Answer", "Ответить"), "fa:"+itoa(tgID), "primary")))
	b.send(chat, tr(lang, "Sent to the owner. The answer will come here.", "Отправлено владельцу. Ответ придёт сюда."))
}

func (b *Bot) cmdStop(chat int64, user access.User) {
	lang := user.Lang
	b.send(chat, rich.New().P(tr(lang,
		"Erase everything the bot keeps about you and leave? You'll need a new code to come back.",
		"Удалить всё, что бот о тебе хранит, и уйти? Чтобы вернуться, понадобится новый код.")).Buttons(
		rich.BS(tr(lang, "Yes, erase", "Да, удалить"), "stop!", "danger"), rich.B(tr(lang, "Cancel", "Отмена"), "stop")))
}
