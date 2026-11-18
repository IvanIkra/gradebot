"""The command menus Telegram shows: everyone's, the owner's, and a group admin's (theirs and the user's)."""

USER_COMMANDS = {
    "en": [("grades", "your grades"), ("sheets", "your sheets"), ("sheet", "follow a Google Sheet"),
           ("group", "your group"), ("name", "your full name"),
           ("quiet", "quiet hours"), ("lang", "language"), ("feedback", "write to the owner"), ("help", "commands"),
           ("stop", "erase your data and leave")],
    "ru": [("grades", "твои оценки"), ("sheets", "твои таблицы"), ("sheet", "подписаться на таблицу"),
           ("group", "твоя группа"), ("name", "ФИО"),
           ("quiet", "тихие часы"), ("lang", "язык"), ("feedback", "написать владельцу"), ("help", "команды"),
           ("stop", "удалить свои данные и уйти")],
}
OWNER_COMMANDS = {
    "en": [("invite", "an invite code"), ("codes", "open codes"), ("users", "users"),
           ("groups", "groups"), ("groupsheet", "the groups' shared sheets"), ("rename", "a sheet's short name"),
           ("relayout", "lay a sheet out again"), ("status", "sheets, users, Haiku"),
           ("interval", "how often the sheets are read"), ("help", "commands")],
    "ru": [("invite", "код приглашения"), ("codes", "действующие коды"), ("users", "пользователи"),
           ("groups", "группы"), ("groupsheet", "общие таблицы групп"), ("rename", "короткое имя таблицы"),
           ("relayout", "разметить таблицу заново"),
           ("status", "таблицы, пользователи, Haiku"), ("interval", "как часто читать таблицы"),
           ("help", "команды")],
}
ADMIN_COMMANDS = {
    "en": [("invite", "a code to your group"), ("codes", "your group's codes"), ("users", "your group"),
           ("groupsheet", "your group's shared sheets"), ("news", "how the group's news come")],
    "ru": [("invite", "код в твою группу"), ("codes", "коды твоей группы"), ("users", "участники группы"),
           ("groupsheet", "общие таблицы группы"), ("news", "как присылать новости группы")],
}
