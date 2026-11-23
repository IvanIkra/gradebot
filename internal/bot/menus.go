package bot

import "github.com/IvanIkra/gradebot/internal/telegram"

// The command menus Telegram shows: everyone's, the owner's, and a group admin's (theirs and the user's).

type cmd struct{ name, description string }

type menu map[string][]cmd

// Commands is a menu in a language as the Bot API takes it.
func (m menu) Commands(lang string) []telegram.Command {
	out := make([]telegram.Command, len(m[lang]))
	for n, c := range m[lang] {
		out[n] = telegram.Command{Command: c.name, Description: c.description}
	}
	return out
}

var UserCommands = menu{
	"en": {{"grades", "your grades"}, {"sheets", "your sheets"}, {"sheet", "follow a Google Sheet"},
		{"subjects", "subjects: sheets joined"}, {"formula", "a subject's grade formula"},
		{"goal", "the grade you aim at"},
		{"group", "your group"}, {"name", "your full name"},
		{"quiet", "quiet hours"}, {"lang", "language"}, {"feedback", "write to the owner"}, {"help", "commands"},
		{"stop", "erase your data and leave"}},
	"ru": {{"grades", "твои оценки"}, {"sheets", "твои таблицы"}, {"sheet", "подписаться на таблицу"},
		{"subjects", "предметы: таблицы вместе"}, {"formula", "формула оценки предмета"},
		{"goal", "к какой оценке идёшь"},
		{"group", "твоя группа"}, {"name", "ФИО"},
		{"quiet", "тихие часы"}, {"lang", "язык"}, {"feedback", "написать владельцу"}, {"help", "команды"},
		{"stop", "удалить свои данные и уйти"}},
}

var OwnerCommands = menu{
	"en": {{"invite", "an invite code"}, {"codes", "open codes"}, {"users", "users"},
		{"groups", "groups"}, {"groupsheet", "the groups' shared sheets"}, {"subjects", "the groups' subjects"},
		{"rename", "a sheet's short name"},
		{"relayout", "lay a sheet out again"}, {"status", "sheets, users, Haiku"},
		{"interval", "how often the sheets are read"}, {"limits", "the limits of the groups' admins"},
		{"help", "commands"}},
	"ru": {{"invite", "код приглашения"}, {"codes", "действующие коды"}, {"users", "пользователи"},
		{"groups", "группы"}, {"groupsheet", "общие таблицы групп"}, {"subjects", "предметы групп"},
		{"rename", "короткое имя таблицы"},
		{"relayout", "разметить таблицу заново"},
		{"status", "таблицы, пользователи, Haiku"}, {"interval", "как часто читать таблицы"},
		{"limits", "лимиты админов групп"}, {"help", "команды"}},
}

var AdminCommands = menu{
	"en": {{"invite", "a code to your group"}, {"codes", "your group's codes"}, {"users", "your group"},
		{"groupsheet", "your group's shared sheets"}, {"news", "how the group's news come"}},
	"ru": {{"invite", "код в твою группу"}, {"codes", "коды твоей группы"}, {"users", "участники группы"},
		{"groupsheet", "общие таблицы группы"}, {"news", "как присылать новости группы"}},
}
