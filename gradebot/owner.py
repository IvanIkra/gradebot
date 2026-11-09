"""The owner's commands: invite codes, users, group sheets, courses and layouts.

Callbacks: `rq:<invite>` asks to revoke a code and `rv:<invite>` revokes it, `codes` and `users` redraw the lists,
`du:<user>` asks to delete a user and `dy:<user>` deletes them, `rk:<user>` trusts a user or stops trusting them,
`nr:<user>` resets a user's full name, `sa:<request>` and `sd:<request>` approve and decline a user's sheet,
`gr:<sheet>` makes a group sheet an own one, `rl:<sheet>` lays a sheet out again, `st` redraws /status,
`iv:<minutes>` sets how often the sheets are read.
"""

import logging

from . import clock, poller
from .access import MEMBER, TRUSTED
from .i18n import tr
from .rich import Doc, b, code, i, link
from .sheets import SheetError
from .sheets import name as sheet_name
from .telegram import TelegramError

log = logging.getLogger(__name__)


class OwnerCommands:
    async def owner_command(self, chat: int, cmd: str, arg: str, lang: str) -> None:
        if cmd == "/invite":
            await self.api.send(chat, self.invite_view(arg, lang))
        elif cmd == "/codes":
            await self.api.send(chat, self.codes_view(lang))
        elif cmd == "/users":
            await self.api.send(chat, self.users_view(lang))
        elif cmd == "/groupsheet":
            await self.cmd_groupsheet(chat, arg, lang)
        elif cmd == "/course":
            await self.cmd_course(chat, arg, lang)
        elif cmd == "/relayout":
            await self.cmd_relayout(chat, arg, lang)
        elif cmd == "/status":
            await self.api.send(chat, self.status_view(lang))
        elif cmd == "/interval":
            await self.cmd_interval(chat, arg, lang)
        else:
            await self.api.send(chat, self.owner_help(lang))

    async def owner_callback(self, chat: int, message_id: int, kind: str, arg: str, lang: str) -> str | None:
        number = int(arg) if arg.isdigit() else None
        if kind == "rq" and number:
            await self.api.edit(chat, message_id, self.revoke_view(number, lang))
        elif kind == "rv" and number:
            note = tr(lang, "Revoked", "Отозван") if self.access.revoke(number) else \
                tr(lang, "Already used or revoked", "Уже использован или отозван")
            await self.api.edit(chat, message_id, self.codes_view(lang))
            return note
        elif kind == "codes":
            await self.api.edit(chat, message_id, self.codes_view(lang))
        elif kind == "users":
            await self.api.edit(chat, message_id, self.users_view(lang))
        elif kind == "du" and number:
            await self.api.edit(chat, message_id, self.delete_view(number, lang))
        elif kind == "dy" and number:
            note = await self.delete_user(number, lang)
            await self.api.edit(chat, message_id, self.users_view(lang))
            return note
        elif kind == "rk" and number:
            user = self.access.user(number)
            if user is None:
                return None
            self.access.set_rank(number, MEMBER if user["rank"] >= TRUSTED else TRUSTED)
            await self.api.edit(chat, message_id, self.users_view(lang))
            return tr(lang, "Trusted", "Доверенный") if user["rank"] < TRUSTED else tr(lang, "Member", "Обычный")
        elif kind == "sa" and number:
            return await self.approve(chat, message_id, number, lang)
        elif kind == "sd" and number:
            return await self.decline(chat, message_id, number, lang)
        elif kind == "nr" and number:
            return await self.reset_name(chat, message_id, number, lang)
        elif kind == "gr" and number:
            self.sheets.remove_group(number)
            await self.api.edit(chat, message_id, self.group_view(lang))
        elif kind == "rl" and number:
            sheet = self.sheets.get(number)
            if sheet is None or self.poller is None:
                return None
            self.poller.relayout(sheet)
            return tr(lang, "Laying it out again", "Размечаю заново")
        elif kind == "st":
            await self.api.edit(chat, message_id, self.status_view(lang))
            return tr(lang, "Updated", "Обновлено")
        elif kind == "iv" and number:
            if not self.set_interval(number):
                return None
            await self.api.edit(chat, message_id, self.interval_view(lang))
            return tr(lang, "Saved", "Сохранено")
        return None

    def owner_help(self, lang: str) -> Doc:
        return (Doc().h(tr(lang, "Owner's commands", "Команды владельца"))
                .table([], [["/invite " + tr(lang, "[note]", "[пометка]"),
                             tr(lang, "a one-time invite code", "одноразовый код приглашения")],
                            ["/codes", tr(lang, "unused codes, revoke", "неиспользованные коды, отзыв")],
                            ["/users", tr(lang, "users: rank, delete", "пользователи: ранг, удаление")],
                            ["/groupsheet " + tr(lang, "[link [course]]", "[ссылка [курс]]"),
                             tr(lang, "the group's sheets: everyone follows them",
                                "общие таблицы: на них подписаны все")],
                            ["/course " + tr(lang, "<link> <course>", "<ссылка> <курс>"),
                             tr(lang, "a sheet's course, up to 10 characters", "курс таблицы, до 10 символов")],
                            ["/relayout " + tr(lang, "<link>", "<ссылка>"),
                             tr(lang, "lay a sheet's tabs out again", "разметить вкладки таблицы заново")],
                            ["/status", tr(lang, "sheets, errors, users, Haiku spending",
                                           "таблицы, ошибки, пользователи, расход Haiku")],
                            ["/interval " + tr(lang, "[minutes]", "[минуты]"),
                             tr(lang, "how often the sheets are read", "как часто читать таблицы")]]))

    # --- invites ---------------------------------------------------------------------------------

    def invite_view(self, note: str, lang: str) -> Doc:
        invite_id, new = self.access.invite(note)
        expires = self.access.db.execute("select expires_at from invites where id = ?", (invite_id,)).fetchone()[0]
        doc = Doc().p(tr(lang, "Code", "Код"), f" «{note.strip()}»" if note.strip() else "",
                      tr(lang, ", works once until ", ", одноразовый, действует до "), self.when(expires), ":")
        doc.p(code(new))
        if self.username:
            url = f"https://t.me/{self.username}?start={new}"
            doc.p(link(url, url))
        return doc.p(i(tr(lang, "Only its hash is kept: this is the only time the code is shown.",
                          "Хранится только хэш: код показан один раз.")))

    def codes_view(self, lang: str) -> Doc:
        invites = self.access.open_invites()
        if not invites:
            return Doc().p(tr(lang, "No unused codes. ", "Неиспользованных кодов нет. "), "/invite")
        doc = Doc().h(tr(lang, "Unused codes", "Неиспользованные коды")).table(
            [tr(lang, "Note", "Пометка"), tr(lang, "Created", "Создан"), tr(lang, "Expires", "Истекает")],
            [[inv["note"] or "—", self.when(inv["created_at"]), self.when(inv["expires_at"])] for inv in invites])
        revoke = tr(lang, "Revoke ", "Отозвать ")
        for inv in invites[:30]:
            doc.buttons((revoke + (inv["note"] or self.when(inv["created_at"])), f"rq:{inv['id']}", "danger"))
        return doc

    def revoke_view(self, invite_id: int, lang: str) -> Doc:
        found = next((inv for inv in self.access.open_invites() if inv["id"] == invite_id), None)
        if found is None:
            return self.codes_view(lang)
        note = found["note"] or self.when(found["created_at"])
        return (Doc().p(tr(lang, "Revoke the code ", "Отозвать код "), b(note), "?")
                .buttons((tr(lang, "Yes, revoke", "Да, отозвать"), f"rv:{invite_id}", "danger"),
                         (tr(lang, "Cancel", "Отмена"), "codes")))

    # --- users -----------------------------------------------------------------------------------

    def users_view(self, lang: str) -> Doc:
        users = self.access.users()
        if not users:
            return Doc().p(tr(lang, "No users yet. ", "Пользователей пока нет. "), "/invite")
        doc = Doc().h(tr(lang, "Users", "Пользователи") + f" · {len(users)}").table(
            [tr(lang, "Telegram", "Telegram"), tr(lang, "Full name", "ФИО"), tr(lang, "Sheets", "Таблиц"),
             tr(lang, "Rank", "Ранг"), tr(lang, "Joined", "С"), tr(lang, "Code", "Код")],
            [[self.who(u), u["student"] or "—", str(len(self.sheets.followed(u["tg_id"]))), self.rank(u, lang),
              self.when(u["joined_at"]), u["note"] or "—"] for u in users], [None, None, "right", None, None, None])
        doc.p(i(tr(lang, "A member's sheet that the bot doesn't read yet waits for your approval; a trusted user's "
                         "doesn't.",
                   "Новую для бота таблицу обычного пользователя ты сначала одобряешь, доверенного — нет.")))
        for u in users[:30]:
            trusted = u["rank"] >= TRUSTED
            rank = tr(lang, "Member: ", "Обычный: ") if trusted else tr(lang, "Trust: ", "Доверять: ")
            doc.buttons((rank + u["tg_name"], f"rk:{u['tg_id']}"),
                        (tr(lang, "Delete ", "Удалить ") + u["tg_name"], f"du:{u['tg_id']}", "danger"))
        return doc

    @staticmethod
    def rank(user: dict, lang: str) -> str:
        return tr(lang, "trusted", "доверенный") if user["rank"] >= TRUSTED else tr(lang, "member", "обычный")

    def delete_view(self, tg_id: int, lang: str) -> Doc:
        user = self.access.user(tg_id)
        if user is None:
            return self.users_view(lang)
        doc = Doc().p(tr(lang, "Delete ", "Удалить "), self.who(user), "? ",
                      tr(lang, "Everything the bot keeps about them is erased; they'll need a new code to come back.",
                         "Всё, что бот о нём хранит, сотрётся; вернуться можно будет только с новым кодом."))
        return doc.buttons((tr(lang, "Yes, delete", "Да, удалить"), f"dy:{tg_id}", "danger"),
                           (tr(lang, "Cancel", "Отмена"), "users"))

    async def delete_user(self, tg_id: int, lang: str) -> str:
        user = self.access.user(tg_id)
        if user is None or not self.access.delete(tg_id):
            return tr(lang, "Already deleted", "Уже удалён")
        self.sheets.drop_orphans()
        try:
            await self.api.send(tg_id, tr(user["lang"], "The owner closed your access to the bot.",
                                          "Владелец закрыл тебе доступ к боту."))
        except TelegramError as e:
            log.info("telling a deleted user failed: %s", e)
        return tr(lang, "Deleted", "Удалён")

    # --- requests for sheets ----------------------------------------------------------------------

    async def approve(self, chat: int, message_id: int, rid: int, lang: str) -> str:
        try:
            done = self.sheets.approve(rid)
        except SheetError:
            return tr(lang, "The user has 20 sheets already", "У пользователя уже 20 таблиц")
        if done is None:
            return tr(lang, "Already handled", "Уже решено")
        request, sheet = done
        user = self.access.user(request["tg_id"])
        await self.api.edit(chat, message_id, Doc().p(
            tr(lang, "Approved: ", "Одобрено: "), link(request["url"], request["url"]), tr(lang, " for ", " для "),
            self.who(user), "."))
        ulang = user["lang"]
        await self.tell(user, Doc().p(tr(ulang, "The owner approved the sheet. ", "Владелец одобрил таблицу. "))
                        .extend(self.following(user, sheet)))
        if user["student"]:
            self.read_soon(user["tg_id"], [sheet["id"]])
        return tr(lang, "Approved", "Одобрено")

    async def decline(self, chat: int, message_id: int, rid: int, lang: str) -> str:
        request = self.sheets.drop_request(rid)
        if request is None:
            return tr(lang, "Already handled", "Уже решено")
        user = self.access.user(request["tg_id"])
        await self.api.edit(chat, message_id, Doc().p(
            tr(lang, "Declined: ", "Отклонено: "), link(request["url"], request["url"]), tr(lang, " for ", " для "),
            self.who(user), "."))
        ulang = user["lang"]
        await self.tell(user, Doc().p(tr(ulang, "The owner declined the sheet ", "Владелец отклонил таблицу "),
                                      link(request["url"], request["url"]), "."))
        return tr(lang, "Declined", "Отклонено")

    async def reset_name(self, chat: int, message_id: int, tg_id: int, lang: str) -> str:
        user = self.access.user(tg_id)
        if user is None or not user["student"]:
            return tr(lang, "Nothing to reset", "Сбрасывать нечего")
        self.access.set_student(tg_id, None)
        self.grades.forget(tg_id)
        await self.api.edit(chat, message_id, Doc().p(
            tr(lang, "Full name ", "ФИО "), b(user["student"]), tr(lang, " of ", " у "), self.who(user),
            tr(lang, " is reset.", " сброшено.")))
        ulang = user["lang"]
        await self.tell(user, Doc().p(
            tr(ulang, "The owner reset your full name. Send it again exactly as the sheets have it: ",
               "Владелец сбросил твоё ФИО. Укажи его заново, точно как в таблицах: "),
            code(tr(ulang, "/name Surname Name", "/name Фамилия Имя"))))
        return tr(lang, "Reset", "Сброшено")

    # --- sheets ----------------------------------------------------------------------------------

    async def cmd_groupsheet(self, chat: int, arg: str, lang: str) -> None:
        if not arg:
            await self.api.send(chat, self.group_view(lang))
            return
        url, _, course = arg.partition(" ")
        try:
            sheet, added = self.sheets.add_group(url, self.owner_id, course if course.strip() else None)
        except SheetError as e:
            await self.api.send(chat, self.sheet_error(e, lang))
            return
        await self.api.send(chat, Doc().p(
            tr(lang, "Group sheet ", "Общая таблица "), b(sheet_name(sheet)), ". ",
            tr(lang, f"Users who started to follow it: {added}.", f"Подписано пользователей: {added}.")))
        self.read_soon(None, [sheet["id"]])

    def group_view(self, lang: str) -> Doc:
        sheets = self.sheets.group()
        if not sheets:
            return Doc().p(tr(lang, "No group sheets yet: ", "Общих таблиц пока нет: "),
                           code(tr(lang, "/groupsheet <link> [course]", "/groupsheet <ссылка> [курс]")))
        doc = Doc().h(tr(lang, "Group sheets", "Общие таблицы") + f" · {len(sheets)}").table(
            [tr(lang, "Sheet", "Таблица"), tr(lang, "Course", "Курс"), tr(lang, "Followers", "Подписаны")],
            [[link(s["title"] or s["sheet_id"][-6:], s["url"]), s["course"] or "—", str(s["followers"])]
             for s in sheets], [None, None, "right"])
        off = tr(lang, "Not a group one: ", "Убрать из общих: ")
        for s in sheets:
            doc.buttons((off + sheet_name(s), f"gr:{s['id']}", "danger"))
        return doc

    async def cmd_course(self, chat: int, arg: str, lang: str) -> None:
        url, _, course = arg.partition(" ")
        if not url:
            await self.api.send(chat, Doc().p(code(tr(lang, "/course <link> <course>", "/course <ссылка> <курс>"))))
            return
        try:
            sheet = self.sheets.set_course(url, course)
        except SheetError as e:
            await self.api.send(chat, self.sheet_error(e, lang))
            return
        await self.api.send(chat, Doc().p(tr(lang, "Course: ", "Курс: "), b(sheet["course"] or "—"), "."))

    async def cmd_relayout(self, chat: int, arg: str, lang: str) -> None:
        try:
            sheet = self.sheets.by_url(arg.split()[0]) if arg.strip() else None
        except SheetError as e:
            await self.api.send(chat, self.sheet_error(e, lang))
            return
        if sheet is None:
            await self.api.send(chat, Doc().p(code(tr(lang, "/relayout <link>", "/relayout <ссылка>")), " — ",
                                              tr(lang, "a sheet somebody follows.", "таблица, на которую подписаны.")))
            return
        if self.poller is not None:
            self.poller.relayout(sheet)
        await self.api.send(chat, Doc().p(
            tr(lang, "The layout of ", "Разметка "), b(sheet_name(sheet)),
            tr(lang, " is dropped: the sheet is read and laid out again now.",
               " сброшена: таблица сейчас будет прочитана и размечена заново.")))

    # --- the interval ------------------------------------------------------------------------------

    INTERVALS = (10, 15, 30, 60)    # the buttons of /interval; any other is typed

    async def cmd_interval(self, chat: int, arg: str, lang: str) -> None:
        if arg.strip():
            minutes = int(arg.strip()) if arg.strip().isdigit() else 0
            if not self.set_interval(minutes):
                await self.api.send(chat, Doc().p(
                    tr(lang, "Minutes from ", "Минуты от "), str(poller.MIN_MINUTES), tr(lang, " to ", " до "),
                    str(poller.MAX_MINUTES), ": ", code("/interval 15")))
                return
        await self.api.send(chat, self.interval_view(lang))

    def set_interval(self, minutes: int) -> bool:
        """False for minutes out of range. The poller takes the new interval at once."""
        try:
            poller.set_interval(self.store, minutes)
        except ValueError:
            return False
        if self.poller is not None:
            self.poller.rescheduled()
        return True

    def interval_view(self, lang: str) -> Doc:
        minutes = poller.interval(self.store)
        doc = Doc().p(tr(lang, "The sheets are read every ", "Таблицы читаются раз в "),
                      b(tr(lang, f"{minutes} minutes", f"{minutes} мин")), ". ",
                      tr(lang, "A new grade comes within that time; each read downloads every followed sheet.",
                         "Новая оценка приходит за это время; каждое чтение скачивает все таблицы с подписчиками."))
        doc.p(tr(lang, "Another: ", "Другой: "), code("/interval 20"),
              tr(lang, f", from {poller.MIN_MINUTES} to {poller.MAX_MINUTES}.",
                 f", от {poller.MIN_MINUTES} до {poller.MAX_MINUTES}."))
        return doc.buttons(*[(tr(lang, f"{m} min", f"{m} мин"), f"iv:{m}") for m in self.INTERVALS if m != minutes])

    # --- status ----------------------------------------------------------------------------------

    def status_view(self, lang: str) -> Doc:
        """Sheets with their errors, users, and what Haiku cost today and this month."""
        sheets = self.sheets.every()
        minutes = poller.interval(self.store)
        doc = Doc().h(tr(lang, "Sheets", "Таблицы") + f" · {len(sheets)}").p(
            tr(lang, f"Read every {minutes} minutes: ", f"Читаются раз в {minutes} мин: "), "/interval")
        if sheets:
            never = tr(lang, "never", "ни разу")
            doc.table([tr(lang, "Sheet", "Таблица"), tr(lang, "Followers", "Подписаны"),
                       tr(lang, "Read", "Прочитана"), tr(lang, "State", "Состояние")],
                      [[link(sheet_name(s), s["url"]), str(s["followers"]),
                        self.when(s["ok_at"]) if s["ok_at"] else never,
                        tr(lang, "failing", "ошибка") if s["error"] else "ok"] for s in sheets],
                      [None, "right", None, None])
            errors = [f"{sheet_name(s)}: {s['error']} ({tr(lang, 'since', 'с')} {self.when(s['error_since'])})"
                      for s in sheets if s["error"]]
            if errors:
                doc.quote("\n".join(errors), expandable=True)
        users = self.access.users()
        rows = self.store.db.execute(
            "select (select count(*) from subscriptions where error is not null) bad_rows, "
            "(select count(*) from grade_events where post_id is null) waiting").fetchone()
        doc.h(tr(lang, "Users", "Пользователи") + f" · {len(users)}").table([], [
            [tr(lang, "without a full name", "без ФИО"), str(sum(not u["student"] for u in users))],
            [tr(lang, "blocked the bot", "заблокировали бота"), str(sum(bool(u["paused"]) for u in users))],
            [tr(lang, "subscriptions with no row found", "подписок без найденной строки"), str(rows["bad_rows"])],
            [tr(lang, "sheets waiting for approval", "таблиц ждут одобрения"), str(len(self.sheets.requests()))],
            [tr(lang, "grades waiting to be sent", "оценок ждут отправки"), str(rows["waiting"])]],
            [None, "right"])
        doc.h("Haiku")
        if self.budget is None:
            doc.p(tr(lang, "Not connected.", "Не подключён."))
        else:
            now = self.now()
            day = self.budget.journal.totals(clock.day_start(now, self.zone))
            month = self.budget.journal.totals(clock.month_start(now, self.zone))
            state = self.budget.status()
            doc.table([], [
                [tr(lang, "today", "сегодня"), f"${day['cost_usd']:.3f}",
                 tr(lang, f"calls {day['runs']}, failed {day['failed']}",
                    f"вызовов {day['runs']}, ошибок {day['failed']}")],
                [tr(lang, "this month", "за месяц"), f"${month['cost_usd']:.3f} / ${state['limit_usd']:.2f}",
                 tr(lang, f"calls {month['runs']}, failed {month['failed']}",
                    f"вызовов {month['runs']}, ошибок {month['failed']}")]], [None, "right", None])
            if state["stopped"]:
                doc.p(b(tr(lang, "The monthly limit is spent: new tabs wait for the next month.",
                           "Месячный лимит исчерпан: новые вкладки ждут следующего месяца.")))
        return doc.buttons((tr(lang, "Refresh", "Обновить"), "st"))

    @staticmethod
    def sheet_error(error: SheetError, lang: str) -> str:
        text = str(error)
        if "at most" in text:
            return tr(lang, "A course has at most 10 characters.", "В курсе не больше 10 символов.")
        if "nobody" in text:
            return tr(lang, "Nobody follows this sheet.", "На эту таблицу никто не подписан.")
        return tr(lang, "That's not a Google Sheets link: https://docs.google.com/spreadsheets/d/…",
                  "Это не ссылка на таблицу Google: https://docs.google.com/spreadsheets/d/…")
