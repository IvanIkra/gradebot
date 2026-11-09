"""A user's own commands: the full name, sheets, quiet hours, language and leaving.

Callbacks: `us:<sheet>` unfollows a sheet, `qt:<hours|off>` sets quiet hours, `lg:<en|ru>` the language,
`stop` and `stop!` cancel and confirm /stop.
"""

from . import profile
from .i18n import LANGUAGES, tr
from .profile import ProfileError
from .rich import Doc, b, code, keyboard
from .sheets import SheetError
from .sheets import name as sheet_name


class UserCommands:
    async def user_command(self, chat: int, user: dict, cmd: str, arg: str) -> None:
        lang = user["lang"]
        handlers = {"/name": self.cmd_name, "/sheet": self.cmd_sheet, "/sheets": self.cmd_sheets,
                    "/quiet": self.cmd_quiet, "/lang": self.cmd_lang, "/stop": self.cmd_stop}
        if cmd in handlers:
            await handlers[cmd](chat, user, arg)
        elif cmd == "/start" and not arg:
            await self.api.send(chat, tr(lang, "You're already in. /help lists the commands.",
                                         "Ты уже в списке. Команды — в /help."))
        else:
            await self.api.send(chat, self.user_help(lang))

    async def user_callback(self, chat: int, message_id: int, user: dict, kind: str, arg: str) -> str | None:
        lang = user["lang"]
        if kind == "us" and arg.isdigit():
            done = self.sheets.unfollow(user["tg_id"], int(arg))
            await self.api.edit(chat, message_id, *self.sheets_view(user))
            return tr(lang, "Unfollowed", "Отписан") if done else None
        if kind == "qt":
            try:
                self.access.set_quiet(user["tg_id"], profile.quiet(arg))
            except ProfileError:
                return None
            await self.api.edit(chat, message_id, *self.quiet_view(self.access.user(user["tg_id"])))
            return tr(lang, "Saved", "Сохранено")
        if kind == "lg" and arg in LANGUAGES:
            self.access.set_lang(user["tg_id"], arg)
            await self.api.edit(chat, message_id, *self.lang_view(arg))
            return None
        if kind == "stop!":
            self.access.delete(user["tg_id"])
            self.sheets.drop_orphans()
            await self.api.edit(chat, message_id, tr(
                lang, "Your data is erased. To come back you'll need a new invite code.",
                "Твои данные удалены. Чтобы вернуться, нужен новый код приглашения."))
            left = tr(self.owner_lang, "Left the bot with /stop: ", "Ушёл из бота через /stop: ")
            await self.tell_owner(Doc().p(left, self.who(user)))
        elif kind == "stop":
            await self.api.edit(chat, message_id, tr(lang, "Staying, nothing erased.", "Остаёмся, ничего не удалено."))
        return None

    def user_help(self, lang: str) -> Doc:
        rows = [["/name " + tr(lang, "Surname Name", "Фамилия Имя"),
                 tr(lang, "your full name as the sheets have it", "ФИО, как в таблицах")],
                ["/sheet " + tr(lang, "<link>", "<ссылка>"),
                 tr(lang, "follow a Google Sheet", "подписаться на таблицу")],
                ["/sheets", tr(lang, "your sheets, unfollow", "твои таблицы, отписка")],
                ["/quiet", tr(lang, "quiet hours", "тихие часы")],
                ["/lang", tr(lang, "language", "язык")],
                ["/stop", tr(lang, "erase your data and leave", "удалить свои данные и уйти")]]
        return Doc().h(tr(lang, "Commands", "Команды")).table([], rows)

    # --- the full name ---------------------------------------------------------------------------

    async def cmd_name(self, chat: int, user: dict, arg: str) -> None:
        lang = user["lang"]
        if not arg:
            now = user["student"]
            await self.api.send(chat, Doc().p(
                tr(lang, "Your full name: ", "Твоё ФИО: ") if now else
                tr(lang, "No full name yet. ", "ФИО не указано. "),
                b(now) if now else "",
                tr(lang, ". To change it: ", ". Сменить: ") if now else tr(lang, "Send it: ", "Укажи его: "),
                code(tr(lang, "/name Surname Name", "/name Фамилия Имя"))))
            return
        try:
            student = profile.student(arg)
        except ProfileError:
            await self.api.send(chat, tr(
                lang, "That doesn't look like a full name: two words or more, letters only, as in the sheets.",
                "Это не похоже на ФИО: нужно два слова или больше, только буквы — как в таблицах."))
            return
        if student == user["student"]:
            await self.api.send(chat, tr(lang, "That's your name already.", "Это ФИО уже записано."))
            return
        self.access.set_student(user["tg_id"], student)
        doc = Doc().p(tr(lang, "Saved: ", "Записал: "), b(student), ".")
        if not self.sheets.followed(user["tg_id"]):
            doc.p(tr(lang, "Now a sheet to follow: ", "Теперь таблица: "),
                  code(tr(lang, "/sheet <link>", "/sheet <ссылка>")))
        await self.api.send(chat, doc)
        owner = self.owner_lang
        before = [tr(owner, " (was ", " (было "), user["student"], ")"] if user["student"] else []
        await self.tell_owner(
            Doc().p(self.who(user), tr(owner, " set the full name ", " указал ФИО "), b(student), *before),
            keyboard([[(tr(owner, "Reset", "Сбросить"), f"nr:{user['tg_id']}")]]))

    # --- sheets ----------------------------------------------------------------------------------

    async def cmd_sheet(self, chat: int, user: dict, arg: str) -> None:
        lang = user["lang"]
        if not arg:
            await self.api.send(chat, Doc().p(tr(lang, "Send the sheet's link: ", "Пришли ссылку на таблицу: "),
                                              code("/sheet https://docs.google.com/spreadsheets/d/…")))
            return
        try:
            sheet, new = self.sheets.follow(user["tg_id"], arg.split()[0])
        except SheetError as e:
            text = tr(lang, "You follow 20 sheets already: unfollow one in /sheets.",
                      "У тебя уже 20 таблиц: отпишись от какой-нибудь в /sheets.") if "at most" in str(e) else \
                tr(lang, "That's not a Google Sheets link: https://docs.google.com/spreadsheets/d/…",
                   "Это не ссылка на таблицу Google: https://docs.google.com/spreadsheets/d/…")
            await self.api.send(chat, text)
            return
        if not new:
            await self.api.send(chat, tr(lang, "You follow this sheet already.", "Ты уже подписан на эту таблицу."))
            return
        doc = Doc().p(tr(lang, "Following ", "Подписал на "), b(sheet_name(sheet)), ". ",
                      tr(lang, "The sheet must be open to anyone with the link.",
                         "Таблица должна быть открыта всем, у кого есть ссылка."))
        if not user["student"]:
            doc.p(tr(lang, "To find your row I need your full name: ", "Чтобы найти твою строку, нужно ФИО: "),
                  code(tr(lang, "/name Surname Name", "/name Фамилия Имя")))
        await self.api.send(chat, doc)

    async def cmd_sheets(self, chat: int, user: dict, arg: str) -> None:
        await self.api.send(chat, *self.sheets_view(user))

    def sheets_view(self, user: dict) -> tuple[Doc, dict | None]:
        lang, sheets = user["lang"], self.sheets.followed(user["tg_id"])
        if not sheets:
            return Doc().p(tr(lang, "No sheets yet: ", "Таблиц пока нет: "),
                           code(tr(lang, "/sheet <link>", "/sheet <ссылка>"))), None
        group, own = tr(lang, "group", "общая"), tr(lang, "own", "своя")
        doc = Doc().h(tr(lang, "Your sheets", "Твои таблицы") + f" · {len(sheets)}").table(
            [tr(lang, "Sheet", "Таблица"), tr(lang, "Kind", "Тип")],
            [[sheet_name(s), group if s["group_sheet"] else own] for s in sheets])
        unfollow = tr(lang, "Unfollow ", "Отписаться: ")
        return doc, keyboard([[(unfollow + sheet_name(s), f"us:{s['id']}")] for s in sheets])

    # --- quiet hours and language ----------------------------------------------------------------

    async def cmd_quiet(self, chat: int, user: dict, arg: str) -> None:
        lang = user["lang"]
        if arg:
            try:
                self.access.set_quiet(user["tg_id"], profile.quiet(arg))
            except ProfileError:
                await self.api.send(chat, Doc().p(tr(lang, "Hours like ", "Часы вида "), code("/quiet 23-8"),
                                                  tr(lang, ", or ", " или "), code("/quiet off")))
                return
            user = self.access.user(user["tg_id"])
        await self.api.send(chat, *self.quiet_view(user))

    def quiet_view(self, user: dict) -> tuple[Doc, dict | None]:
        lang, hours = user["lang"], profile.hours(user)
        doc = Doc().p(tr(lang, "Quiet hours: ", "Тихие часы: "), b(hours or tr(lang, "off", "выключены")), ". ",
                      tr(lang, "Grades then come at once, but without a sound.",
                         "В это время оценки приходят сразу, но без звука."))
        doc.p(tr(lang, "Other hours: ", "Другие часы: "), code("/quiet 22-7"))
        default = "{}-{}".format(*profile.QUIET_DEFAULT)
        buttons = [(f"{profile.QUIET_DEFAULT[0]}:00–{profile.QUIET_DEFAULT[1]}:00", f"qt:{default}")] \
            if hours != "{}:00–{}:00".format(*profile.QUIET_DEFAULT) else []
        if hours:
            buttons.append((tr(lang, "Turn off", "Выключить"), "qt:off"))
        return doc, keyboard([buttons])

    async def cmd_lang(self, chat: int, user: dict, arg: str) -> None:
        if arg.lower() in LANGUAGES:
            self.access.set_lang(user["tg_id"], arg.lower())
            await self.api.send(chat, *self.lang_view(arg.lower()))
            return
        await self.api.send(chat, *self.lang_view(user["lang"]))

    def lang_view(self, lang: str) -> tuple[Doc, dict | None]:
        doc = Doc().p(tr(lang, "Language: English.", "Язык: русский."))
        return doc, keyboard([[("English", "lg:en"), ("Русский", "lg:ru")]])

    async def cmd_stop(self, chat: int, user: dict, arg: str) -> None:
        lang = user["lang"]
        await self.api.send(chat, Doc().p(tr(
            lang, "Erase everything the bot keeps about you and leave? You'll need a new code to come back.",
            "Удалить всё, что бот о тебе хранит, и уйти? Чтобы вернуться, понадобится новый код.")),
            keyboard([[(tr(lang, "Yes, erase", "Да, удалить"), "stop!"), (tr(lang, "Cancel", "Отмена"), "stop")]]))
