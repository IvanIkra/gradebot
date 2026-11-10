"""A user's own commands: grades, the full name, sheets, quiet hours, language and leaving.

Callbacks: `grades` sends /grades, `rf` redraws it in place, `wr:<sheet>` complains about a sheet's layout,
`ck:<sheet>` reads a sheet now, `us:<sheet>` unfollows it, `cr:<request>` cancels a request for a sheet waiting for
the owner, `gp:<group>` moves the user to a group (0: none) from /group, `gj:<group>` from the first message,
`add` asks for a sheet to follow, `qt:<hours|off>` sets quiet hours, `lg:<en|ru>` the language, `stop` and `stop!`
cancel and confirm /stop.
"""

import datetime as dt

from . import clock, profile
from .access import TRUSTED
from .cards import grades_section
from .dialog import Step, asks_hint, link_step
from .groups import GroupError
from .i18n import LANGUAGES, tr
from .layout import label
from .notices import explain
from .profile import ProfileError
from .rich import Doc, Html, b, code, i, join, link
from .sheets import GOOGLE_SHEET, SheetError, course
from .sheets import name as sheet_name

COMPLAINTS_EVERY = dt.timedelta(days=1)     # one complaint about a sheet's layout a day from a user
FEEDBACK_PER_DAY = 5                        # /feedback messages a day from a user
FEEDBACK_CHARS = 1000


def group_buttons(doc: Doc, groups: list[dict], kind: str, lang: str, none: bool = True) -> Doc:
    """A button for each group, four in a row, and «No group»: callback `<kind>:<group id>`, 0 for none."""
    choice = [(g["name"], f"{kind}:{g['id']}") for g in groups]
    if none:
        choice.append((tr(lang, "No group", "Без группы"), f"{kind}:0"))
    for start in range(0, len(choice), 4):
        doc.buttons(*choice[start:start + 4])
    return doc


class UserCommands:
    async def user_command(self, chat: int, user: dict, cmd: str, arg: str) -> None:
        lang = user["lang"]
        if not cmd and GOOGLE_SHEET.match(arg):  # a link pasted alone: follow it
            cmd = "/sheet"
        handlers = {"/grades": self.cmd_grades, "/name": self.cmd_name, "/sheet": self.cmd_sheet,
                    "/sheets": self.cmd_sheets, "/group": self.cmd_group, "/quiet": self.cmd_quiet,
                    "/lang": self.cmd_lang, "/feedback": self.cmd_feedback, "/stop": self.cmd_stop}
        if cmd in handlers:
            await handlers[cmd](chat, user, arg)
        elif cmd == "/start":
            await self.api.send(chat, tr(lang, "You're already in. /help lists the commands.",
                                         "Ты уже в списке. Команды — в /help."))
        else:
            await self.api.send(chat, Doc().p(i(tr(lang, "That's not a command. Here is what I can do:",
                                                   "Это не команда. Вот что я умею:"))).extend(self.user_help(lang)))

    async def user_callback(self, chat: int, message_id: int, user: dict, kind: str, arg: str) -> str | None:
        lang = user["lang"]
        if kind == "grades":
            await self.cmd_grades(chat, user, "")
            return None
        if kind == "add":
            await self.cmd_sheet(chat, user, "")
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
            return tr(lang, "Unfollowed", "Подписка снята") if done else None
        if kind == "cr" and arg.isdigit():
            found = self.sheets.drop_request(int(arg), user["tg_id"])
            await self.api.edit(chat, message_id, self.sheets_view(user))
            return tr(lang, "Request cancelled", "Заявка отменена") if found else None
        if kind in ("gp", "gj") and arg.isdigit():
            return await self.choose_group(chat, message_id, user, int(arg), kind == "gj")
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
            left = tr(self.owner_lang, "Left the bot with /stop: ", "Выход из бота через /stop: ")
            await self.news(left, self.who(user))
        elif kind == "stop":
            await self.api.edit(chat, message_id, tr(lang, "Staying, nothing erased.", "Остаёмся, ничего не удалено."))
        return None

    def user_help(self, lang: str) -> Doc:
        rows = [["/grades", tr(lang, "your grades, the new ones marked", "твои оценки, новые отмечены")],
                ["/name " + tr(lang, "Surname Name", "Фамилия Имя"),
                 tr(lang, "your full name as the sheets have it", "ФИО, как в таблицах")],
                ["/sheet " + tr(lang, "<link> [short name]", "<ссылка> [имя]"),
                 tr(lang, "follow a Google Sheet; a link sent alone does the same",
                    "подписаться на таблицу Google; можно просто прислать ссылку")],
                ["/sheets", tr(lang, "your sheets: check, unfollow", "твои таблицы: проверить, отписаться")],
                *([["/group", tr(lang, "your group: you follow its shared sheets", "твоя группа и её общие таблицы")]]
                  if self.groups.all() else []),
                ["/quiet", tr(lang, "quiet hours", "тихие часы")],
                ["/lang", tr(lang, "language", "язык")],
                ["/feedback " + tr(lang, "<text>", "<текст>"),
                 tr(lang, "write to the bot's owner: a question, a bug, an idea", "написать владельцу бота: вопрос, "
                                                                                    "ошибка, идея")],
                ["/stop", tr(lang, "erase your data and leave", "удалить свои данные и уйти")]]
        return Doc().h(tr(lang, "Commands", "Команды")).table([], rows).p(asks_hint(lang))

    # --- the full name ---------------------------------------------------------------------------

    async def cmd_name(self, chat: int, user: dict, arg: str) -> None:
        """`/name Surname Name`, or asked for."""
        lang, now = user["lang"], user["student"]

        def prompt(data: dict) -> Doc:
            if now:
                return Doc().p(tr(lang, "Your full name: ", "Твоё ФИО: "), b(now), ". ",
                               tr(lang, "To change it, send the new one exactly as the sheets have it.",
                                  "Чтобы сменить, пришли новое — точно как в таблицах."))
            return Doc().p(b(tr(lang, "What's your name in the sheets?", "Как ты записан в таблицах?")), " ",
                           tr(lang, "Send your surname and name exactly as they are there, for example ",
                              "Пришли фамилию и имя точно так же, например "),
                           code(tr(lang, "Ivanov Ivan", "Иванов Иван")), ". ",
                           tr(lang, "That's how I find your row.", "По ним я найду твою строку."))

        step = Step("student", prompt, check=lambda value, data: profile.student(value),
                    error=lambda lang, e: tr(lang, "That doesn't look like a full name: two words or more, letters "
                                                   "only, as in the sheets.",
                                             "Это не похоже на ФИО: нужно два слова или больше, только буквы — как "
                                             "в таблицах."))
        await self.flow(chat, lang, [step], lambda data: self.set_name(chat, user["tg_id"], data["student"]),
                        {"student": arg} if arg else None)

    async def set_name(self, chat: int, tg_id: int, student: str) -> None:
        user = self.access.user(tg_id)
        if user is None:
            return
        lang = user["lang"]
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
            doc.p(self.no_sheets(lang))
        await self.api.send(chat, doc)
        owner = self.owner_lang
        before = [tr(owner, " (was ", " (было "), user["student"], ")"] if user["student"] else []
        await self.news(self.who(user), tr(owner, " set the full name ", ": ФИО "), b(student), *before,
                        buttons=((tr(owner, "Reset the name of ", "Сбросить ФИО: ") + user["tg_name"],
                                  f"nr:{user['tg_id']}", "danger"),))
        self.read_soon(user["tg_id"], [sheet["id"] for sheet in followed])

    # --- sheets ----------------------------------------------------------------------------------

    async def cmd_sheet(self, chat: int, user: dict, arg: str) -> None:
        """`/sheet <link> [short name]`, the missing ones asked for. The short name (the sheet's `course`) is also
        its hashtag, and counts for a sheet new to the bot only: without one the bot names the sheet by its title in
        Google, and later only the owner changes it."""
        lang = user["lang"]
        url, _, name = arg.strip().partition(" ")
        data = {"link": url, **({"name": name.strip()} if name.strip() else {})} if url else {}
        steps = [link_step(lang),
                 Step("name", lambda data: Doc().p(self.course_hint(lang)), check=lambda value, data: course(value),
                      error=lambda lang, e: self.follow_error(e, lang),
                      skip=lambda data: tr(lang, "As in Google", "Как в Google"),
                      when=lambda data: self.sheets.by_url(data["link"]) is None and not any(
                          r["url"] == data["link"] for r in self.sheets.requests(user["tg_id"])))]
        await self.flow(chat, lang, steps, lambda data: self.add_sheet(chat, user["tg_id"], data), data)

    async def add_sheet(self, chat: int, tg_id: int, data: dict) -> None:
        user = self.access.user(tg_id)
        if user is None:
            return
        lang, url, course_text = user["lang"], data["link"], data.get("name") or None
        try:
            known = self.sheets.by_url(url)
            if known is None and user["rank"] < TRUSTED:
                await self.ask_owner(chat, user, url, course_text)
                return
            sheet, new = self.sheets.follow(user["tg_id"], url, course_text if known is None else None)
        except SheetError as e:
            await self.api.send(chat, self.follow_error(e, lang))
            return
        if not new:
            await self.api.send(chat, tr(lang, "You follow this sheet already.", "Эта таблица уже в твоих подписках."))
            return
        doc = self.following(user, sheet)
        if known is not None and course_text:
            doc.p(tr(lang, "The bot reads this sheet already, so its name stays: ",
                     "Бот уже читает эту таблицу, поэтому её имя остаётся прежним: "), b(sheet_name(sheet)), ". ",
                  tr(lang, "Only the owner changes it.", "Поменять его может только владелец."))
        await self.api.send(chat, doc)
        if user["student"]:
            self.read_soon(user["tg_id"], [sheet["id"]])

    @staticmethod
    def course_hint(lang: str) -> Html:
        return join(b(tr(lang, "A short name for the sheet?", "Как коротко назвать таблицу?")), " ",
                    tr(lang, "Up to 10 characters, like ", "До 10 символов, например "),
                    code(tr(lang, "Calc", "Матан")), tr(lang, ": it becomes the hashtag in messages with grades.",
                                                        ": это будет хэштег в сообщениях с оценками."))

    @staticmethod
    def follow_error(error: SheetError, lang: str) -> str:
        if "course" in str(error):
            return tr(lang, "A short name has at most 10 characters.", "В коротком имени не больше 10 символов.")
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

    async def ask_owner(self, chat: int, user: dict, url: str, course_text: str | None = None) -> None:
        """A member's sheet the bot does not read yet: the owner approves it first. SheetError as follow."""
        lang = user["lang"]
        request, new = self.sheets.ask(user["tg_id"], url, course_text)
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
                                       ": просьба подписать на таблицу, которую бот ещё не читает: "),
                    link(request["url"], request["url"]),
                    *([tr(owner, ", short name ", ", имя "), b(request["course"])] if request["course"] else []))
            .buttons((tr(owner, "Approve", "Одобрить"), f"sa:{request['id']}", "success"),
                     (tr(owner, "Decline", "Отклонить"), f"sd:{request['id']}", "danger")))

    # --- the group -------------------------------------------------------------------------------

    async def cmd_group(self, chat: int, user: dict, arg: str) -> None:
        await self.api.send(chat, self.group_choice(user))

    def group_choice(self, user: dict) -> Doc:
        lang, groups = user["lang"], self.groups.all()
        mine = self.groups.get(user["grp"])
        doc = Doc().p(tr(lang, "Your group: ", "Твоя группа: "), b(mine["name"]) if mine else
                      tr(lang, "none", "не выбрана"), ".")
        if self.groups.fixed(user):
            return doc.p(tr(lang, "It came with your invite code: only the owner moves you to another one.",
                            "Она пришла с кодом приглашения: перевести тебя в другую может только владелец."))
        others = [g for g in groups if not mine or g["id"] != mine["id"]]
        if not others and not mine:
            return doc.p(tr(lang, "There are no groups yet.", "Групп пока нет."))
        doc.p(tr(lang, "You follow the shared sheets of your group. Moving to another group swaps them: the old "
                       "ones go with their grades, the new ones come.",
                 "Ты подписан на общие таблицы своей группы. В другой группе старые уйдут вместе с оценками, "
                 "а новые добавятся."))
        return group_buttons(doc, others, "gp", lang, none=mine is not None)

    async def choose_group(self, chat: int, message_id: int, user: dict, gid: int, first: bool) -> str | None:
        lang, before = user["lang"], self.groups.get(user["grp"])
        if self.groups.fixed(user):
            return tr(lang, "Only the owner changes your group", "Группу меняет только владелец")
        try:
            left, followed = self.groups.join(user["tg_id"], gid)
        except GroupError:
            return None
        user = self.access.user(user["tg_id"])
        group = self.groups.get(user["grp"])
        if first:
            await self.api.edit(chat, message_id, self.welcome(lang, group, len(followed), user["expires_at"]))
        else:
            doc = self.group_choice(user)
            if left or followed:
                doc.p(tr(lang, f"Unfollowed sheets: {left}, followed: {len(followed)}.",
                         f"Отписал от таблиц: {left}, подписал на: {len(followed)}."))
            await self.api.edit(chat, message_id, doc)
        if (before or {}).get("id") != (group or {}).get("id"):
            owner = self.owner_lang
            if group is None:
                news = [tr(owner, " left the group ", ": больше не в группе "), b(before["name"])]
            else:
                news = [tr(owner, " is in the group ", ": теперь в группе "), b(group["name"]),
                        *([tr(owner, " (was ", " (была "), before["name"], ")"] if before else [])]
            await self.news(self.who(user), *news)
        if user["student"] and followed:
            self.read_soon(user["tg_id"], followed)
        return tr(lang, "Saved", "Сохранено")

    async def cmd_sheets(self, chat: int, user: dict, arg: str) -> None:
        await self.api.send(chat, self.sheets_view(user))

    def sheet_problem(self, sheet: dict, lang: str) -> str:
        """What is wrong with a followed sheet now, or ''."""
        if sheet["error"]:
            return explain(sheet["error"], lang)
        if sheet["user_error"]:
            return explain(sheet["user_error"], lang)
        return ""

    def sheets_view(self, user: dict) -> Doc:
        lang, sheets = user["lang"], self.sheets.followed(user["tg_id"])
        doc = Doc() if sheets else Doc().p(self.no_sheets(lang))
        if sheets:
            self.sheets_table(doc, user, sheets)
        if requests := self.sheets.requests(user["tg_id"]):
            doc.p(tr(lang, "Waiting for the owner's approval:", "Ждут одобрения владельца:"))
            doc.table([], [[link(r["url"], r["url"])] for r in requests])
            for n, r in enumerate(requests[:30], 1):
                doc.buttons((tr(lang, f"Cancel request {n}", f"Отменить заявку {n}"), f"cr:{r['id']}", "danger"))
        return doc.buttons(self.add_button(lang))

    @staticmethod
    def no_sheets(lang: str) -> str:
        return tr(lang, "No sheets yet. Send me a link to a Google Sheet with your grades: just paste it here.",
                  "Таблиц пока нет. Пришли ссылку на таблицу Google с оценками — просто вставь её сюда.")

    @staticmethod
    def add_button(lang: str) -> tuple:
        return (tr(lang, "Add a sheet", "Добавить таблицу"), "add", "primary")

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
        if not user["student"]:  # nothing to show without it: ask for it
            await self.cmd_name(chat, user, "")
            return
        await self.api.send(chat, self.grades_view(user))

    def grades_view(self, user: dict) -> Doc:
        """Every followed sheet as details with its grades; the ones with new grades open. When it is too long for
        one message, the tables of the sheets without new grades go first, then all of them."""
        lang, sheets = user["lang"], self.sheets.followed(user["tg_id"])
        if not user["student"]:
            return Doc().p(tr(lang, "First your full name as the sheets have it: ", "Сначала ФИО, как в таблицах: "),
                           code(tr(lang, "/name Surname Name", "/name Фамилия Имя")))
        if not sheets:
            return Doc().p(self.no_sheets(lang)).buttons(self.add_button(lang))
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
            tabs.setdefault(item["tab"], []).append(f"{label(item, owner)} ({item['header'] or '—'})")
        doc = Doc().p(self.who(user), tr(owner, " says the layout of ", " считает неверной разметку "),
                      link(sheet_name(sheet), sheet["url"]), tr(owner, " is wrong.", "."))
        doc.table([tr(owner, "Tab", "Вкладка"), tr(owner, "Works laid out (header)", "Задания (заголовок)")],
                  [[tab, "; ".join(works)] for tab, works in tabs.items()])
        await self.tell_owner(doc.buttons((tr(owner, "Lay out again", "Разметить заново"), f"rl:{sheet_id}"),
                                          (tr(owner, "Open the sheet", "Открыть таблицу"), sheet["url"])))
        return tr(lang, "Sent to the owner", "Отправлено владельцу")

    # --- quiet hours and language ----------------------------------------------------------------

    async def cmd_quiet(self, chat: int, user: dict, arg: str) -> None:
        """`/quiet 23-8`, `/quiet off`, or the hours now with buttons, and other ones typed."""
        lang = user["lang"]

        def check(value: str, data: dict) -> str:
            profile.quiet(value)
            return value

        async def done(data: dict) -> None:
            self.access.set_quiet(user["tg_id"], profile.quiet(data["hours"]))
            await self.api.send(chat, self.quiet_view(self.access.user(user["tg_id"])))

        step = Step("hours", lambda data: self.quiet_view(self.access.user(user["tg_id"])), check=check,
                    error=lambda lang, e: tr(lang, "Hours like 23-8, or off.", "Часы вида 23-8 или off."),
                    cancel=False)
        await self.flow(chat, lang, [step], done, {"hours": arg} if arg else None)

    def quiet_view(self, user: dict) -> Doc:
        lang, hours = user["lang"], profile.hours(user)
        doc = Doc().p(tr(lang, "Quiet hours: ", "Тихие часы: "), b(hours or tr(lang, "off", "выключены")), ". ",
                      tr(lang, "Grades then come at once, but without a sound.",
                         "В это время оценки приходят сразу, но без звука."))
        doc.p(tr(lang, "Other hours: send them, like ", "Другие часы — пришли их, например "), code("22-7"), ".")
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
        return doc.buttons(("English" + (" ✓" if lang == "en" else ""), "lg:en"),
                           ("Русский" + (" ✓" if lang == "ru" else ""), "lg:ru"))

    # --- feedback ---------------------------------------------------------------------------------

    async def cmd_feedback(self, chat: int, user: dict, arg: str) -> None:
        """`/feedback <text>`, or the text asked for: it goes to the owner, who may answer with a button."""
        lang = user["lang"]

        def check(value: str, data: dict) -> str:
            if len(value) > FEEDBACK_CHARS:
                raise ValueError("too long")
            return value

        step = Step("text", lambda data: Doc().p(tr(
                        lang, "What would you like to tell the bot's owner? A question, a bug, an idea: one message.",
                        "Что передать владельцу бота? Вопрос, ошибка, идея — одним сообщением.")),
                    check=check, error=lambda lang, e: tr(lang, f"At most {FEEDBACK_CHARS} characters, please.",
                                                          f"Не больше {FEEDBACK_CHARS} символов, пожалуйста."))
        await self.flow(chat, lang, [step], lambda data: self.send_feedback(chat, user["tg_id"], data["text"]),
                        {"text": arg.strip()} if arg.strip() else None)

    async def send_feedback(self, chat: int, tg_id: int, text: str) -> None:
        user = self.access.user(tg_id)
        if user is None:
            return
        lang, since = user["lang"], self.now() - dt.timedelta(days=1)
        sent = [moment for moment in self.feedback_sent.get(tg_id, []) if moment > since]
        if len(sent) >= FEEDBACK_PER_DAY:
            await self.api.send(chat, tr(lang, "That's enough for today: write again tomorrow.",
                                         "На сегодня хватит: напиши завтра."))
            return
        self.feedback_sent[tg_id] = [*sent, self.now()]
        owner = self.owner_lang
        await self.tell_owner(Doc().p(self.who(user), tr(owner, " writes:", " пишет:")).quote(text)
                              .buttons((tr(owner, "Answer", "Ответить"), f"fa:{tg_id}", "primary")))
        await self.api.send(chat, tr(lang, "Sent to the owner. The answer will come here.",
                                     "Отправлено владельцу. Ответ придёт сюда."))

    @property
    def feedback_sent(self) -> dict[int, list]:
        """tg_id -> when they wrote with /feedback lately: in memory, a restart forgets it."""
        if not hasattr(self, "_feedback_sent"):
            self._feedback_sent = {}
        return self._feedback_sent

    async def cmd_stop(self, chat: int, user: dict, arg: str) -> None:
        lang = user["lang"]
        await self.api.send(chat, Doc().p(tr(
            lang, "Erase everything the bot keeps about you and leave? You'll need a new code to come back.",
            "Удалить всё, что бот о тебе хранит, и уйти? Чтобы вернуться, понадобится новый код."))
            .buttons((tr(lang, "Yes, erase", "Да, удалить"), "stop!", "danger"),
                     (tr(lang, "Cancel", "Отмена"), "stop")))

