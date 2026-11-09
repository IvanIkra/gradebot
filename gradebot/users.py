"""A user's own commands: grades, the full name, sheets, quiet hours, language and leaving.

Callbacks: `grades` sends /grades, `rf` redraws it in place, `wr:<sheet>` complains about a sheet's layout,
`ck:<sheet>` reads a sheet now, `us:<sheet>` unfollows it, `cr:<request>` cancels a request for a sheet waiting for
the owner, `qt:<hours|off>` sets quiet hours, `lg:<en|ru>` the language, `stop` and `stop!` cancel and confirm /stop.
"""

import datetime as dt

from . import clock, profile
from .access import TRUSTED
from .cards import grades_section
from .i18n import LANGUAGES, tr
from .notices import explain
from .profile import ProfileError
from .rich import Doc, b, code, link
from .sheets import SheetError
from .sheets import name as sheet_name

COMPLAINTS_EVERY = dt.timedelta(days=1)     # one complaint about a sheet's layout a day from a user


class UserCommands:
    async def user_command(self, chat: int, user: dict, cmd: str, arg: str) -> None:
        lang = user["lang"]
        handlers = {"/grades": self.cmd_grades, "/name": self.cmd_name, "/sheet": self.cmd_sheet,
                    "/sheets": self.cmd_sheets, "/quiet": self.cmd_quiet, "/lang": self.cmd_lang,
                    "/stop": self.cmd_stop}
        if cmd in handlers:
            await handlers[cmd](chat, user, arg)
        elif cmd == "/start" and not arg:
            await self.api.send(chat, tr(lang, "You're already in. /help lists the commands.",
                                         "Ты уже в списке. Команды — в /help."))
        else:
            await self.api.send(chat, self.user_help(lang))

    async def user_callback(self, chat: int, message_id: int, user: dict, kind: str, arg: str) -> str | None:
        lang = user["lang"]
        if kind == "grades":
            await self.api.send(chat, self.grades_view(user))
            return None
        if kind == "rf":
            await self.api.edit(chat, message_id, self.grades_view(user))
            return tr(lang, "Updated", "Обновлено")
        if kind == "wr" and arg.isdigit():
            return await self.complain(user, int(arg))
        if kind == "ck" and arg.isdigit():
            if not user["student"]:
                return tr(lang, "Set your full name first: /name", "Сначала укажи ФИО: /name")
            if int(arg) in {s["id"] for s in self.sheets.followed(user["tg_id"])}:
                self.read_soon(user["tg_id"], [int(arg)])
                return tr(lang, "Reading it, I'll write what I find", "Читаю, напишу, что нашёл")
            return None
        if kind == "us" and arg.isdigit():
            done = self.sheets.unfollow(user["tg_id"], int(arg))
            await self.api.edit(chat, message_id, self.sheets_view(user))
            return tr(lang, "Unfollowed", "Отписан") if done else None
        if kind == "cr" and arg.isdigit():
            found = self.sheets.drop_request(int(arg), user["tg_id"])
            await self.api.edit(chat, message_id, self.sheets_view(user))
            return tr(lang, "Request cancelled", "Заявка отменена") if found else None
        if kind == "qt":
            try:
                self.access.set_quiet(user["tg_id"], profile.quiet(arg))
            except ProfileError:
                return None
            await self.api.edit(chat, message_id, self.quiet_view(self.access.user(user["tg_id"])))
            return tr(lang, "Saved", "Сохранено")
        if kind == "lg" and arg in LANGUAGES:
            self.access.set_lang(user["tg_id"], arg)
            await self.api.edit(chat, message_id, self.lang_view(arg))
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
        rows = [["/grades", tr(lang, "your grades, the new ones marked", "твои оценки, новые отмечены")],
                ["/name " + tr(lang, "Surname Name", "Фамилия Имя"),
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
        self.grades.forget(user["tg_id"])  # other rows now: the next read is a baseline
        followed = self.sheets.followed(user["tg_id"])
        doc = Doc().p(tr(lang, "Saved: ", "Записал: "), b(student), ".")
        if followed:
            doc.p(tr(lang, "Reading your sheets now; I'll write what I find.",
                     "Сейчас прочитаю твои таблицы и напишу, что нашёл."))
        else:
            doc.p(tr(lang, "Now a sheet to follow: ", "Теперь таблица: "),
                  code(tr(lang, "/sheet <link>", "/sheet <ссылка>")))
        await self.api.send(chat, doc)
        owner = self.owner_lang
        before = [tr(owner, " (was ", " (было "), user["student"], ")"] if user["student"] else []
        await self.tell_owner(
            Doc().p(self.who(user), tr(owner, " set the full name ", " указал ФИО "), b(student), *before)
            .buttons((tr(owner, "Reset", "Сбросить"), f"nr:{user['tg_id']}", "danger")))
        self.read_soon(user["tg_id"], [sheet["id"] for sheet in followed])

    # --- sheets ----------------------------------------------------------------------------------

    async def cmd_sheet(self, chat: int, user: dict, arg: str) -> None:
        lang = user["lang"]
        if not arg:
            await self.api.send(chat, Doc().p(tr(lang, "Send the sheet's link: ", "Пришли ссылку на таблицу: "),
                                              code("/sheet https://docs.google.com/spreadsheets/d/…")))
            return
        url = arg.split()[0]
        try:
            if self.sheets.by_url(url) is None and user["rank"] < TRUSTED:
                await self.ask_owner(chat, user, url)
                return
            sheet, new = self.sheets.follow(user["tg_id"], url)
        except SheetError as e:
            await self.api.send(chat, self.follow_error(e, lang))
            return
        if not new:
            await self.api.send(chat, tr(lang, "You follow this sheet already.", "Ты уже подписан на эту таблицу."))
            return
        await self.api.send(chat, self.following(user, sheet))
        if user["student"]:
            self.read_soon(user["tg_id"], [sheet["id"]])

    @staticmethod
    def follow_error(error: SheetError, lang: str) -> str:
        if "at most" in str(error):
            return tr(lang, "You follow 20 sheets already: unfollow one in /sheets.",
                      "У тебя уже 20 таблиц: отпишись от какой-нибудь в /sheets.")
        return tr(lang, "That's not a Google Sheets link: https://docs.google.com/spreadsheets/d/…",
                  "Это не ссылка на таблицу Google: https://docs.google.com/spreadsheets/d/…")

    @staticmethod
    def following(user: dict, sheet: dict) -> Doc:
        """The answer to a sheet the user started to follow."""
        lang = user["lang"]
        doc = Doc().p(tr(lang, "Following ", "Подписал на "), b(sheet_name(sheet)), ". ",
                      tr(lang, "The sheet must be open to anyone with the link.",
                         "Таблица должна быть открыта всем, у кого есть ссылка."))
        if not user["student"]:
            return doc.p(tr(lang, "To find your row I need your full name: ", "Чтобы найти твою строку, нужно ФИО: "),
                         code(tr(lang, "/name Surname Name", "/name Фамилия Имя")))
        return doc.p(tr(lang, "Reading it now; I'll write what I find.", "Сейчас прочитаю её и напишу, что нашёл."))

    async def ask_owner(self, chat: int, user: dict, url: str) -> None:
        """A member's sheet the bot does not read yet: the owner approves it first. SheetError as follow."""
        lang = user["lang"]
        request, new = self.sheets.ask(user["tg_id"], url)
        if not new:
            await self.api.send(chat, tr(lang, "This sheet is already waiting for the owner's approval.",
                                         "Эта таблица уже ждёт одобрения владельца."))
            return
        await self.api.send(chat, Doc().p(
            tr(lang, "The bot doesn't read this sheet yet, so the owner approves it first. I'll write when they do.",
               "Бот ещё не читает эту таблицу, поэтому сначала её одобрит владелец. Напишу, когда он ответит.")))
        owner = self.owner_lang
        await self.tell_owner(
            Doc().p(self.who(user), tr(owner, " asks to follow a sheet the bot doesn't read yet: ",
                                       " просит подписать его на таблицу, которую бот ещё не читает: "),
                    link(request["url"], request["url"]))
            .buttons((tr(owner, "Approve", "Одобрить"), f"sa:{request['id']}", "success"),
                     (tr(owner, "Decline", "Отклонить"), f"sd:{request['id']}", "danger")))

    async def cmd_sheets(self, chat: int, user: dict, arg: str) -> None:
        await self.api.send(chat, self.sheets_view(user))

    def sheet_problem(self, sheet: dict, lang: str) -> str:
        """Why a followed sheet gives no grades now, or ''."""
        if sheet["error"]:
            return explain(sheet["error"], lang)
        if sheet["user_error"]:
            return explain(sheet["user_error"], lang)
        return ""

    def sheets_view(self, user: dict) -> Doc:
        lang, sheets = user["lang"], self.sheets.followed(user["tg_id"])
        doc = Doc() if sheets else Doc().p(tr(lang, "No sheets yet: ", "Таблиц пока нет: "),
                                           code(tr(lang, "/sheet <link>", "/sheet <ссылка>")))
        if sheets:
            self.sheets_table(doc, user, sheets)
        if requests := self.sheets.requests(user["tg_id"]):
            doc.p(tr(lang, "Waiting for the owner's approval:", "Ждут одобрения владельца:"))
            doc.table([], [[link(r["url"], r["url"])] for r in requests])
            for n, r in enumerate(requests[:30], 1):
                doc.buttons((tr(lang, f"Cancel request {n}", f"Отменить заявку {n}"), f"cr:{r['id']}", "danger"))
        return doc

    def sheets_table(self, doc: Doc, user: dict, sheets: list[dict]) -> None:
        lang = user["lang"]
        never = tr(lang, "not yet", "ещё нет")
        rows = [[link(sheet_name(s), s["url"]), self.when(s["read_at"]) if s["read_at"] else never,
                 str(self.grades.count(user["tg_id"], s["id"])), self.sheet_problem(s, lang)] for s in sheets]
        header = [tr(lang, "Sheet", "Таблица"), tr(lang, "Read", "Прочитана"), tr(lang, "Grades", "Оценок"),
                  tr(lang, "Problem", "Ошибка")]
        if not any(row[3] for row in rows):
            header, rows = header[:3], [row[:3] for row in rows]
        doc.h(tr(lang, "Your sheets", "Твои таблицы") + f" · {len(sheets)}").table(
            header, rows, [None, None, "right", None])
        if not user["student"]:
            doc.p(tr(lang, "Your rows are looked for once you set your full name: ",
                     "Строки ищутся, когда указано ФИО: "), code(tr(lang, "/name Surname Name", "/name Фамилия Имя")))
        for s in sheets:
            doc.buttons((tr(lang, "Check ", "Проверить: ") + sheet_name(s), f"ck:{s['id']}"),
                        (tr(lang, "Unfollow ", "Отписаться: ") + sheet_name(s), f"us:{s['id']}", "danger"))

    # --- grades ----------------------------------------------------------------------------------

    async def cmd_grades(self, chat: int, user: dict, arg: str) -> None:
        await self.api.send(chat, self.grades_view(user))

    def grades_view(self, user: dict) -> Doc:
        """Every followed sheet as details with its grades; the ones with new grades open. When it is too long for
        one message, the tables of the sheets without new grades go first, then all of them."""
        lang, sheets = user["lang"], self.sheets.followed(user["tg_id"])
        if not user["student"]:
            return Doc().p(tr(lang, "First your full name as the sheets have it: ", "Сначала ФИО, как в таблицах: "),
                           code(tr(lang, "/name Surname Name", "/name Фамилия Имя")))
        if not sheets:
            return Doc().p(tr(lang, "No sheets yet: ", "Таблиц пока нет: "),
                           code(tr(lang, "/sheet <link>", "/sheet <ссылка>")))
        found = []
        for s in sheets:
            problem = self.sheet_problem(s, lang) or \
                (tr(lang, "Not read yet.", "Ещё не прочитана.") if s["baseline_at"] is None else "")
            found.append((s, self.grades.items(user["tg_id"], s["id"]), problem))
        for tables in ("all", "new", "none"):
            doc = Doc().h(tr(lang, "Your grades", "Твои оценки"))
            for s, items, problem in found:
                table = tables == "all" or (tables == "new" and any(item["new"] for item in items))
                doc.extend(grades_section(s, items, lang, problem, table))
            if tables != "all":
                doc.p(tr(lang, "The list is long: only sheets with new grades show their tables.",
                         "Список длинный: таблицы показаны только там, где есть новые оценки.")
                      if tables == "new" else
                      tr(lang, "The list is too long for tables: unfollow some sheets in /sheets.",
                         "Список слишком длинный для таблиц: отпишись от части таблиц в /sheets."))
            doc.buttons((tr(lang, "Refresh", "Обновить"), "rf"))
            if doc.fits():
                break
        return doc

    async def complain(self, user: dict, sheet_id: int) -> str | None:
        """«Wrong» under a sheet in /grades: the owner hears what is laid out now and may lay it out again."""
        lang, sheet = user["lang"], self.sheets.get(sheet_id)
        if sheet is None or sheet_id not in {s["id"] for s in self.sheets.followed(user["tg_id"])}:
            return None
        since = clock.utc_iso(self.now() - COMPLAINTS_EVERY)
        if self.store.db.execute("select 1 from layout_reports where tg_id = ? and sheet = ? and created_at >= ?",
                                 (user["tg_id"], sheet_id, since)).fetchone():
            return tr(lang, "Already sent today", "Уже отправлено сегодня")
        with self.store.db:
            self.store.db.execute("insert into layout_reports(tg_id, sheet, created_at) values(?, ?, ?)",
                                  (user["tg_id"], sheet_id, clock.utc_iso(self.now())))
        owner = self.owner_lang
        tabs: dict[str, list[str]] = {}
        for item in self.grades.items(user["tg_id"], sheet_id):
            tabs.setdefault(item["tab"], []).append(f"{item['label']} ({item['header'] or '—'})")
        doc = Doc().p(self.who(user), tr(owner, " says the layout of ", " считает неверной разметку "),
                      link(sheet_name(sheet), sheet["url"]), tr(owner, " is wrong.", "."))
        doc.table([tr(owner, "Tab", "Вкладка"), tr(owner, "Works laid out (header)", "Задания (заголовок)")],
                  [[tab, "; ".join(works)] for tab, works in tabs.items()])
        await self.tell_owner(doc.buttons((tr(owner, "Lay out again", "Разметить заново"), f"rl:{sheet_id}"),
                                          (tr(owner, "Open the sheet", "Открыть таблицу"), sheet["url"])))
        return tr(lang, "Sent to the owner", "Отправлено владельцу")

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
        await self.api.send(chat, self.quiet_view(user))

    def quiet_view(self, user: dict) -> Doc:
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
        return doc.buttons(*buttons)

    async def cmd_lang(self, chat: int, user: dict, arg: str) -> None:
        if arg.lower() in LANGUAGES:
            self.access.set_lang(user["tg_id"], arg.lower())
            await self.api.send(chat, self.lang_view(arg.lower()))
            return
        await self.api.send(chat, self.lang_view(user["lang"]))

    def lang_view(self, lang: str) -> Doc:
        doc = Doc().p(tr(lang, "Language: English.", "Язык: русский."))
        return doc.buttons(("English", "lg:en"), ("Русский", "lg:ru"))

    async def cmd_stop(self, chat: int, user: dict, arg: str) -> None:
        lang = user["lang"]
        await self.api.send(chat, Doc().p(tr(
            lang, "Erase everything the bot keeps about you and leave? You'll need a new code to come back.",
            "Удалить всё, что бот о тебе хранит, и уйти? Чтобы вернуться, понадобится новый код."))
            .buttons((tr(lang, "Yes, erase", "Да, удалить"), "stop!", "danger"),
                     (tr(lang, "Cancel", "Отмена"), "stop")))
