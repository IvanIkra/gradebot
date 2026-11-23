package bot

import (
	"sort"
	"strings"

	"github.com/IvanIkra/gradebot/internal/access"
	"github.com/IvanIkra/gradebot/internal/grades"
	"github.com/IvanIkra/gradebot/internal/llm"
	"github.com/IvanIkra/gradebot/internal/poller"
	"github.com/IvanIkra/gradebot/internal/profile"
	"github.com/IvanIkra/gradebot/internal/rich"
	"github.com/IvanIkra/gradebot/internal/sheets"
)

// What the bot tells unasked after the sheets are read: new grades, how a requested read went, a sheet that cannot
// be read for 12 hours and its return, and the LLM limit to the owner.
//
// New grades go to every user in one message a poll, a section per subject or sheet (Unit), more only when it does
// not fit the limits of a rich message; each message marks its own grades sent, so a failure halfway sends nothing
// twice. Within the user's quiet hours it comes without a sound. Grades not sent (Telegram failed, the user blocked
// the bot) wait for the next poll while they are within the window of a week.
//
// A user is told after a read they asked for (a new sheet, a new full name) and after the first read of a sheet
// for them, which may follow a group sheet the owner added: one message for every sheet of the poll.

// Explain is a kept error in words for a user.
func Explain(errText, lang string) string {
	what, _, detail := partition(errText, ": ")
	switch {
	case what == poller.Closed:
		return tr(lang, "closed: open it to anyone with the link", "закрыта: открой её всем, у кого есть ссылка")
	case what == poller.Download && strings.Contains(" "+detail+" ", " 404 "):
		return tr(lang, "not found: it may have been deleted, or the link is wrong",
			"не найдена: возможно, её удалили или ссылка неверна")
	case what == poller.Download:
		return tr(lang, "cannot be downloaded ("+detail+"), I'll try again at the next read",
			"не скачивается ("+detail+"), попробую при следующем чтении")
	case what == poller.Broken:
		return tr(lang, "its file cannot be read, I'll try again at the next read",
			"её файл не читается, попробую при следующем чтении")
	case what == poller.Layout:
		return tr(lang, "its tabs could not be laid out, I'll try again at the next read",
			"не удалось разметить вкладки, попробую при следующем чтении")
	case what == poller.Budget:
		return tr(lang, "the limit for laying out new tabs is spent until next month",
			"лимит на разметку новых вкладок исчерпан до следующего месяца")
	case what == poller.NoRow:
		return tr(lang, "your row is on no tab: check your full name, /name",
			"твоей строки нет ни на одной вкладке: проверь ФИО, /name")
	case what == poller.Ambiguous:
		return tr(lang, "your full name matches several rows in: "+detail+", so these tabs are skipped. Make it "+
			"longer, with the patronymic",
			"ФИО подходит к нескольким строкам на вкладках: "+detail+", их я пропускаю. Уточни ФИО, например "+
				"с отчеством")
	}
	return errText
}

func outcome(read poller.SheetRead, user *poller.UserRead, lang string) string {
	if read.Error != "" {
		return Explain(read.Error, lang)
	}
	if user == nil {
		return tr(lang, "read", "прочитана")
	}
	if user.Tabs == 0 {
		e := user.Error
		if e == "" {
			e = poller.NoRow
		}
		return Explain(e, lang)
	}
	done := tr(lang, "read", "прочитана")
	if user.First {
		done = tr(lang, "connected", "подключена")
	}
	text := tr(lang, sf("%s: tabs %d, grades %d", done, user.Tabs, user.Items),
		sf("%s: вкладок %d, оценок %d", done, user.Tabs, user.Items))
	if user.Error != "" {
		return text + "; " + Explain(user.Error, lang)
	}
	return text
}

type report struct {
	read poller.SheetRead
	user *poller.UserRead
}

// AfterRead is the poller's OnRead.
func (b *Bot) AfterRead(results []poller.SheetRead) {
	reports := map[int64][]report{}
	var order []int64
	for _, read := range results {
		if read.Stale != "" || read.Back {
			b.tellFailing(read)
		}
		byUser := map[int64]*poller.UserRead{}
		ids := map[int64]bool{}
		for n := range read.Users {
			byUser[read.Users[n].TgID] = &read.Users[n]
			if read.Users[n].First {
				ids[read.Users[n].TgID] = true
			}
		}
		for id := range read.Requested {
			ids[id] = true
		}
		for _, id := range sortedIDs(ids) {
			if _, ok := reports[id]; !ok {
				order = append(order, id)
			}
			reports[id] = append(reports[id], report{read, byUser[id]})
		}
	}
	for _, id := range order {
		user, ok := b.Access.User(id)
		if !ok {
			continue
		}
		lang := user.Lang
		var rows [][]string
		connected := false
		for _, r := range reports[id] {
			rows = append(rows, []string{sheets.Name(r.read.Sheet), outcome(r.read, r.user, lang)})
			connected = connected || (r.user != nil && r.user.First && r.user.Tabs > 0)
		}
		doc := rich.New().H(tr(lang, "Sheets", "Таблицы")).Table(nil, rows, nil)
		if connected {
			doc.P(tr(lang, "New and changed grades will come here.", "Новые и изменённые оценки будут приходить сюда.")).
				Buttons(allGradesButton(lang))
		}
		b.Tell(user, doc, false)
	}
	b.SendGrades()
}

// SendGrades sends the new grades waiting, a message a user.
func (b *Bot) SendGrades() {
	byUser := map[int64][]grades.Event{}
	var users []int64
	for _, e := range b.Grades.Waiting(0) {
		if _, ok := byUser[e.TgID]; !ok {
			users = append(users, e.TgID)
		}
		byUser[e.TgID] = append(byUser[e.TgID], e)
	}
	for _, tgID := range users {
		user, ok := b.Access.User(tgID)
		if !ok || user.Paused || b.Access.Expired(user) {
			continue // an ended access: the grades wait for a week in case it is prolonged
		}
		bySheet := map[int64][]grades.Event{}
		for _, e := range byUser[tgID] {
			bySheet[e.Sheet] = append(bySheet[e.Sheet], e)
		}
		var sections []Section
		for _, unit := range b.Subjects.Units(user, b.Sheets.Followed(tgID)) {
			var rows []*Row
			for _, s := range unit.Sheets {
				if events, ok := bySheet[s.ID]; ok {
					sheetRows, dropped := collapse(events)
					b.Grades.Sent(dropped, 0) // back where they were: nothing to tell
					rows = append(rows, sheetRows...)
				}
			}
			if len(rows) > 0 {
				// A copy of a grade on another tab or sheet of the unit (the gradebook repeating the group's sheet)
				// is told once: the first in this message, or none when the grade is known already.
				batch := map[[2]any]bool{}
				for _, r := range rows {
					batch[[2]any{r.Sheet, r.Key}] = true
				}
				var known []*Row
				for _, s := range unit.Sheets {
					for _, it := range b.Grades.Items(tgID, s.ID) {
						if !batch[[2]any{it.Sheet, it.Key}] {
							known = append(known, rowOfItem(it))
						}
					}
				}
				var copies []*Row
				rows, copies = dropCopies(rows, known)
				var ids []int64
				for _, c := range copies {
					ids = append(ids, c.IDs...)
				}
				b.Grades.Sent(ids, 0)
			}
			if len(rows) > 0 {
				sections = append(sections, Section{unit, rows})
			}
		}
		var units []*Unit
		for _, s := range sections {
			units = append(units, s.Unit)
		}
		b.withNotes(user, units, true)
		silent := profile.IsQuiet(user, b.Now(), b.Zone)
		for _, msg := range newsMessages(sections, user.Lang) {
			id := b.Tell(user, msg.Doc, silent)
			if id == 0 {
				break // the rest wait for the next poll
			}
			b.Grades.Sent(msg.IDs, id)
		}
	}
}

// tellFailing: a sheet failing for 12 hours, or back after that; the owner and every follower, once each. A
// follower who blocked the bot or whose access ended hears nothing.
func (b *Bot) tellFailing(read poller.SheetRead) {
	s := read.Sheet
	followers := b.Sheets.Followers(s.ID)
	sort.Slice(followers, func(i, j int) bool { return false })
	for _, tgID := range followers {
		if user, ok := b.Access.User(tgID); ok && !user.Paused && !b.Access.Expired(user) {
			b.Tell(user, b.failingDoc(s, read, user.Lang, nil), false)
		}
	}
	owner := b.OwnerLang()
	detail := read.Error
	b.TellOwner(b.failingDoc(s, read, owner, &detail).Buttons(rich.B(tr(owner, "Open the sheet", "Открыть таблицу"),
		s.URL)))
}

// failingDoc: detail is the error as kept, for the owner; a user reads it explained.
func (b *Bot) failingDoc(s sheets.Sheet, read poller.SheetRead, lang string, detail *string) *rich.Doc {
	title := rich.Bold(sheets.Name(s))
	if read.Back {
		return rich.New().P(tr(lang, "The sheet ", "Таблица "), title, tr(lang, " is read again.", " снова читается."))
	}
	why := Explain(read.Error, lang)
	if detail != nil {
		why = *detail
	}
	return rich.New().P(tr(lang, "The sheet ", "Таблица "), title, tr(lang, " cannot be read since ", " не читается с "),
		b.When(read.Stale, lang), ": ", why, ". ", tr(lang, "Its grades are not updated.", "Оценки из неё не обновляются."))
}

// BudgetNotice is the budget's Notify: the owner hears of the spending.
func (b *Bot) BudgetNotice(level llm.Level, spent, limit float64) {
	lang := b.OwnerLang()
	money := rich.Bold(sf("$%.2f / $%.2f", spent, limit))
	var doc *rich.Doc
	if level == llm.Stop {
		doc = rich.New().P(tr(lang, "The monthly LLM limit is spent: ", "Месячный лимит на LLM исчерпан: "), money,
			". ", tr(lang, "New tabs are not laid out until next month; raise ",
				"Новые вкладки не размечаются до следующего месяца; поднять можно в "),
			rich.Code("LLM_MONTHLY_USD"), tr(lang, " in .env to go on.", " в .env."))
	} else {
		doc = rich.New().P(tr(lang, "The LLM spending this month: ", "Расход на LLM в этом месяце: "), money, ".")
	}
	b.TellOwner(doc)
}

var _ access.User
