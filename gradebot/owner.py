"""The owner's commands: invite codes, users, groups and their sheets, sheets' short names and layouts.

Callbacks: `rq:<invite>` asks to revoke a code and `rv:<invite>` revokes it, `codes` and `users` redraw the lists,
`up:<page>` turns a page of users, `ua` drops the search, `uc:<user>` opens a user's card,
`du:<user>` asks to delete a user and `dy:<user>` deletes them, `rk:<user>` trusts a user or stops trusting them,
`ae:<user>` offers to prolong a user's access and `ax:<user>.<days>` prolongs it (0: for good),
`ug:<user>` offers the groups to move a user to and `um:<user>.<group>` moves them (group 0: none),
`nr:<user>` resets a user's full name, `sa:<request>` and `sd:<request>` approve and decline a user's sheet,
`groups` redraws the groups, `gs:<group>` shows a group's sheets, `gr:<group>.<sheet>` takes a sheet from a group,
`gd:<group>` asks to delete a group and `gx:<group>` deletes it, `rn:<sheet>` asks for a sheet's short name,
`rl:<sheet>` lays a sheet out again, `st` redraws
/status, `iv:<minutes>` sets how often the sheets are read, `fa:<user>` answers a user's /feedback.
"""

import datetime as dt
import logging
import re
from urllib.parse import quote

from . import clock, poller
from .access import MEMBER, TRUSTED
from .dialog import Step, asks_hint, link_step
from .groups import GroupError, group_name
from .i18n import days as n_days
from .i18n import tr
from .rich import Doc, b, code, i, link
from .sheets import SheetError, course
from .sheets import name as sheet_name
from .telegram import TelegramError

log = logging.getLogger(__name__)

USERS_VIEW = "owner.users"  # kv: {"page", "query"} of the users list, kept while the owner pages through it
USERS_PAGE = 10


def matches(user: dict, query: str) -> bool:
    """`/users <text>`: the text in the user's names, @username, full name, the note of their code or their group."""
    fold = lambda text: (text or "").lower().replace("ё", "е")  # noqa: E731
    where = " ".join(fold(user.get(key)) for key in ("tg_name", "username", "student", "note", "group_name"))
    return all(word.lstrip("@") in where for word in fold(query).split())


class OwnerCommands:
    async def owner_command(self, chat: int, cmd: str, arg: str, lang: str) -> None:
        if cmd == "/invite":
            await self.cmd_invite(chat, arg, lang)
        elif cmd == "/codes":
            await self.api.send(chat, self.codes_view(lang))
        elif cmd == "/users":
            self.store.put_json(USERS_VIEW, {"page": 0, "query": " ".join(arg.split())})
            await self.api.send(chat, self.users_view(lang))
        elif cmd == "/groups":
            await self.cmd_groups(chat, arg, lang)
        elif cmd == "/groupsheet":
            await self.cmd_groupsheet(chat, arg, lang)
        elif cmd == "/rename":
            await self.cmd_rename(chat, arg, lang)
        elif cmd == "/relayout":
            await self.cmd_relayout(chat, arg, lang)
        elif cmd == "/status":
            await self.api.send(chat, self.status_view(lang))
        elif cmd == "/interval":
            await self.cmd_interval(chat, arg, lang)
        else:
            await self.api.send(chat, self.owner_help(lang))

    async def owner_callback(self, chat: int, message_id: int | None, kind: str, arg: str,
                             lang: str) -> str | None:
        """`message_id`: the message to redraw, None to answer in a new one."""
        number = int(arg) if arg.isdigit() else None
        if kind == "rq" and number:
            await self.show(chat, message_id, self.revoke_view(number, lang))
        elif kind == "rv" and number:
            note = tr(lang, "Revoked", "Отозван") if self.access.revoke(number) else \
                tr(lang, "Already used or revoked", "Уже использован или отозван")
            await self.show(chat, message_id, self.codes_view(lang))
            return note
        elif kind == "codes":
            await self.show(chat, message_id, self.codes_view(lang))
        elif kind == "users":
            await self.show(chat, message_id, self.users_view(lang))
        elif kind == "up" and arg.isdigit():
            self.store.put_json(USERS_VIEW, {**self.store.get_json(USERS_VIEW, {}), "page": int(arg)})
            await self.show(chat, message_id, self.users_view(lang))
        elif kind == "ua":
            self.store.put_json(USERS_VIEW, {"page": 0, "query": ""})
            await self.show(chat, message_id, self.users_view(lang))
        elif kind == "fa" and number:
            return await self.answer_feedback(chat, number, lang)
        elif kind == "uc" and number:
            await self.show(chat, message_id, self.user_card(number, lang))
        elif kind == "du" and number:
            await self.show(chat, message_id, self.delete_view(number, lang))
        elif kind == "dy" and number:
            note = await self.delete_user(number, lang)
            await self.show(chat, message_id, self.users_view(lang))
            return note
        elif kind == "rk" and number:
            user = self.access.user(number)
            if user is None:
                return None
            self.access.set_rank(number, MEMBER if user["rank"] >= TRUSTED else TRUSTED)
            await self.show(chat, message_id, self.user_card(number, lang))
            return tr(lang, "Trusted", "Доверенный") if user["rank"] < TRUSTED else tr(lang, "Member", "Обычный")
        elif kind == "ae" and number:
            user = self.access.user(number)
            if user is None:
                return None
            await self.show(chat, message_id, self.access_view(user, lang))
        elif kind == "ax":
            who, _, days = arg.partition(".")
            if not (who.isdigit() and days.isdigit()):
                return None
            return await self.prolong(chat, message_id, int(who), int(days) or None, lang)
        elif kind == "ug" and number:
            await self.show(chat, message_id, self.move_view(number, lang))
        elif kind == "um":
            who, _, gid = arg.partition(".")
            if not (who.isdigit() and gid.isdigit()):
                return None
            note = await self.move_user(int(who), int(gid), lang)
            await self.show(chat, message_id, self.user_card(int(who), lang))
            return note
        elif kind == "sa" and number:
            return await self.approve(chat, message_id, number, lang)
        elif kind == "sd" and number:
            return await self.decline(chat, message_id, number, lang)
        elif kind == "nr" and number:
            return await self.reset_name(chat, message_id, number, lang)
        elif kind == "groups":
            await self.show(chat, message_id, self.groups_view(lang))
        elif kind == "gsa":
            await self.add_group_sheet(chat, {"group": arg} if number else {}, lang)
        elif kind == "gn":
            await self.flow(chat, lang, [self.group_name_step(lang)], lambda data: self.make_group(chat, data, lang))
        elif kind == "gm" and number:
            await self.flow(chat, lang, [self.group_name_step(lang, number)],
                            lambda data: self.rename_group(chat, number, data["name"], lang))
        elif kind == "gs" and number:
            await self.show(chat, message_id, self.group_view(lang, number))
        elif kind == "gr":
            gid, _, sheet = arg.partition(".")
            if not (gid.isdigit() and sheet.isdigit()):
                return None
            self.groups.remove_sheet(int(gid), int(sheet))
            await self.show(chat, message_id, self.group_view(lang, int(gid)))
        elif kind == "gd" and number:
            await self.show(chat, message_id, self.delete_group_view(number, lang))
        elif kind == "gx" and number:
            done = self.groups.delete(number)
            await self.show(chat, message_id, self.groups_view(lang))
            return tr(lang, "Deleted", "Удалена") if done else None
        elif kind == "rn" and number and (sheet := self.sheets.get(number)) is not None:
            await self.cmd_rename(chat, sheet["url"], lang)
        elif kind == "rl" and number:
            sheet = self.sheets.get(number)
            if sheet is None or self.poller is None:
                return None
            self.poller.relayout(sheet)
            return tr(lang, "Laying it out again", "Размечаю заново")
        elif kind == "st":
            await self.show(chat, message_id, self.status_view(lang))
            return tr(lang, "Updated", "Обновлено")
        elif kind == "iv" and number:
            if not self.set_interval(number):
                return None
            await self.show(chat, message_id, self.interval_view(lang))
            return tr(lang, "Saved", "Сохранено")
        return None

    def owner_help(self, lang: str) -> Doc:
        return (Doc().h(tr(lang, "Owner's commands", "Команды владельца"))
                .table([], [["/invite " + tr(lang, "[group] [x5] [3d] [+30d] [note]",
                                             "[группа] [x5] [3д] [+30д] [пометка]"),
                             tr(lang, "an invite code: for how many people, how long it works, how long the access "
                                      "lasts; /invite help explains",
                                "код приглашения: на сколько человек, сколько действует, на сколько доступ; "
                                "подробно — /invite help")],
                            ["/codes", tr(lang, "open codes, revoke", "действующие коды, отзыв")],
                            ["/users " + tr(lang, "[text]", "[текст]"),
                             tr(lang, "users, a search; a user's card: rank, group, access, delete",
                                "пользователи, поиск; карточка: ранг, группа, доступ, удаление")],
                            ["/groups " + tr(lang, "[name [new name]]", "[имя [новое имя]]"),
                             tr(lang, "groups: a new one, a new name; delete",
                                "группы: новая, переименовать; удаление")],
                            ["/groupsheet " + tr(lang, "[group] [link [short name]]", "[группа] [ссылка [имя]]"),
                             tr(lang, "a group's shared sheets: everyone in it follows them",
                                "общие таблицы группы: на них подписаны все в ней")],
                            ["/rename " + tr(lang, "<link> [short name]", "<ссылка> [имя]"),
                             tr(lang, "a sheet's short name and hashtag, up to 10 characters, for everyone who "
                                      "follows it; without one, its title in Google",
                                "короткое имя и хэштег таблицы, до 10 символов, у всех её подписчиков; без имени — "
                                "название таблицы в Google")],
                            ["/relayout " + tr(lang, "<link>", "<ссылка>"),
                             tr(lang, "lay a sheet's tabs out again", "разметить вкладки таблицы заново")],
                            ["/status", tr(lang, "sheets, errors, users, Haiku spending",
                                           "таблицы, ошибки, пользователи, расход Haiku")],
                            ["/interval " + tr(lang, "[minutes]", "[минуты]"),
                             tr(lang, "how often the sheets are read", "как часто читать таблицы")]])
                .p(asks_hint(lang)))

    # --- invites ---------------------------------------------------------------------------------

    async def cmd_invite(self, chat: int, arg: str, lang: str) -> None:
        """`/invite [group] [x5|x*] [3d|12h] [+30d|+31.12] [note]`, in any order (see `invite_args`): what is not
        typed is the default. Without arguments each is asked for, with buttons."""
        if arg.strip().lower() in ("help", "?", "помощь"):
            await self.api.send(chat, self.invite_help(lang))
        elif arg.strip():
            try:
                args = invite_args(arg, self.groups.find, self.now(), self.zone)
            except ValueError:
                await self.api.send(chat, self.invite_help(lang))
                return
            await self.api.send(chat, self.make_invite(args, lang))
        else:
            await self.flow(chat, lang, self.invite_steps(lang), lambda data: self.invite_asked(chat, data, lang))

    def invite_steps(self, lang: str) -> list[Step]:
        def token(kind: str, fix):
            def check(value: str, data: dict) -> str:
                word = fix(value.strip().lower().replace(" ", ""))
                found = invite_args(word, lambda _: None, self.now(), self.zone)
                found["access_any"] = found["access_days"] or found["access_until"]
                if found["note"] or (kind != "uses" and not found[kind]):  # not this kind of word
                    raise ValueError(value)
                return word
            return check

        def plain(text: str):
            return lambda data: Doc().p(text)

        people = tr(lang, "A number from 1 to 1000, or x* for any number.",
                    "Число от 1 до 1000 или x* — сколько угодно.")
        days = tr(lang, "Like 3d or 12h, up to 365 days.", "Например 3д или 12ч, до 365 дней.")
        access = tr(lang, "Like +30d, or a date: +31.12.", "Например +30д или дата: +31.12.")
        return [
            self.group_step(lang, tr(lang, "No group: they pick one", "Без группы: выберет сам")),
            Step("people", plain(tr(lang, "How many people may come in with the code? ",
                                    "Сколько человек может войти по коду? ") + people),
                 check=token("uses", lambda v: v if v.startswith(("x", "х")) else f"x{v}"),
                 error=lambda lang, e: people,
                 choices=lambda data: [("1", "x1"), ("5", "x5"), ("10", "x10"), ("30", "x30"),
                                       (tr(lang, "Any number", "Сколько угодно"), "x*")]),
            Step("lifetime", plain(tr(lang, "How long does the code work? ", "Сколько действует код? ") + days),
                 check=token("lifetime", lambda v: f"{v}{tr(lang, 'd', 'д')}" if v.isdigit() else v),
                 error=lambda lang, e: days,
                 choices=lambda data: [(n_days(lang, n), f"{n}d")
                                       for n in sorted({1, 3, self.access.invite_days, 30})]),
            Step("access", plain(tr(lang, "How long does the access last? ", "На сколько открыть доступ? ") + access),
                 check=token("access_any", lambda v: v if v.startswith("+") else
                             f"+{v}{tr(lang, 'd', 'д')}" if v.isdigit() else f"+{v}"),
                 error=lambda lang, e: access,
                 choices=lambda data: [(tr(lang, "30 days", "30 дней"), "+30d"),
                                       (tr(lang, "90 days", "90 дней"), "+90d")],
                 skip=lambda data: tr(lang, "For good", "Бессрочно")),
            Step("note", plain(tr(lang, "A note for yourself: who the code is for.",
                                  "Пометка для себя: для кого код.")),
                 skip=lambda data: tr(lang, "No note", "Без пометки")),
        ]

    async def invite_asked(self, chat: int, data: dict, lang: str) -> None:
        words = " ".join(data.get(name, "") for name in ("people", "lifetime"))
        args = invite_args(words, lambda _: None, self.now(), self.zone)
        if data.get("access"):
            access = invite_args(data["access"], lambda _: None, self.now(), self.zone)
            args["access_days"], args["access_until"] = access["access_days"], access["access_until"]
        args["group"] = self.groups.get(int(data["group"])) if data.get("group") else None
        args["note"] = data.get("note", "")
        await self.api.send(chat, self.make_invite(args, lang))

    def make_invite(self, args: dict, lang: str) -> Doc:
        group = args.pop("group")
        invite_id, new = self.access.invite(group=group["id"] if group else None, **args)
        invite = next(inv for inv in self.access.open_invites() if inv["id"] == invite_id)
        note = args["note"]
        doc = Doc().p(tr(lang, "Code", "Код"), f" «{note}»" if note else "",
                      *([tr(lang, " to the group ", " в группу "), b(group["name"])] if group else []), ": ",
                      self.people(invite, lang), tr(lang, ", works until ", ", действует до "),
                      self.when(invite["expires_at"]), tr(lang, ", access ", ", доступ "), self.term(invite, lang),
                      ":")
        doc.p(code(new))
        if not self.username:
            return doc.p(i(tr(lang, "Tap the code to copy it. It is shown only this once: only its hash is kept.",
                              "Нажми на код, чтобы скопировать. Он показан только сейчас: хранится лишь его хэш.")))
        url = f"https://t.me/{self.username}?start={new}"
        doc.p(code(url))   # monospace: a tap copies it
        doc.p(i(tr(lang, "Tap the link to copy it: it lets one in with a tap. The code is shown only this once: only "
                         "its hash is kept.",
                   "Нажми на ссылку, чтобы скопировать: по ней входят в одно касание. Код показан только сейчас: "
                   "хранится лишь его хэш.")))
        return doc.buttons((tr(lang, "Send to a chat", "Отправить в чат"),
                            "https://t.me/share/url?url=" + quote(url, safe="")))

    def group_step(self, lang: str, skip: str | None = None) -> Step:
        """A group: a button for each, or its name typed. Asked only when there are groups."""
        def check(value: str, data: dict) -> str:
            group = self.groups.get(int(value)) if value.isdigit() else self.groups.find(value)
            if group is None:
                raise ValueError(value)
            return str(group["id"])

        return Step("group", lambda data: Doc().p(tr(lang, "Which group?", "Какая группа?")), check=check,
                    error=lambda lang, e: tr(lang, "No such group.", "Такой группы нет."),
                    choices=lambda data: [(g["name"], str(g["id"])) for g in self.groups.all()],
                    skip=lambda data: skip, when=lambda data: bool(self.groups.all()))

    def sheet_step(self, lang: str) -> Step:
        """A sheet the bot reads: a button for each, or its link."""
        def check(value: str, data: dict) -> str:
            sheet = self.sheets.get(int(value[1:])) if value.startswith("#") and value[1:].isdigit() else \
                self.sheets.by_url(value)
            if sheet is None:
                raise ValueError(value)
            return sheet["url"]

        return Step("link", lambda data: Doc().p(tr(lang, "Which sheet? A button, or its link.",
                                                    "Какая таблица? Кнопка или ссылка.")), check=check,
                    error=lambda lang, e: tr(lang, "The bot reads no such sheet.", "Бот не читает такую таблицу."),
                    choices=lambda data: [(sheet_name(s), f"#{s['id']}") for s in self.sheets.every()[:24]])

    def short_name_step(self, lang: str, skip) -> Step:
        return Step("name", lambda data: Doc().p(
                        tr(lang, "A short name, up to 10 characters; it is the sheet's hashtag too. ",
                           "Короткое имя, до 10 символов; оно же хэштег таблицы. "), self.no_course(lang)),
                    check=lambda value, data: course(value),
                    error=lambda lang, e: tr(lang, "A short name has at most 10 characters.",
                                             "В коротком имени не больше 10 символов."),
                    skip=skip)

    def invite_help(self, lang: str) -> Doc:
        rows = [["x5", tr(lang, "up to 5 people; x* any number until it expires or is revoked; one by default",
                          "до 5 человек; x* — сколько угодно, пока код не истёк или не отозван; без этого — один")],
                [tr(lang, "3d, 12h", "3д, 12ч"),
                 tr(lang, f"how long the code works; {self.access.invite_days} days by default",
                    f"сколько действует код; по умолчанию {n_days(lang, self.access.invite_days)}")],
                [tr(lang, "+30d, +31.12", "+30д, +31.12"),
                 tr(lang, "access for 30 days from coming in, or until 31.12; for good by default",
                    "доступ на 30 дней после входа или до 31.12; по умолчанию бессрочно")],
                [tr(lang, "group", "группа"), tr(lang, "the user lands in it and only you move them",
                                                 "пользователь сразу в ней, переводишь только ты")]]
        return (Doc().p(code(tr(lang, "/invite [group] [x5] [3d] [+30d] [note]",
                                "/invite [группа] [x5] [3д] [+30д] [пометка]")), " — ",
                        tr(lang, "in any order:", "в любом порядке:"))
                .table([], rows)
                .p(code(tr(lang, "/invite A1 x* 2d +31.12 seminar", "/invite A1 x* 2д +31.12 семинар"))))

    @staticmethod
    def people(invite: dict, lang: str) -> str:
        """«one person», «3 of 5 people», «3 people, no limit»."""
        if invite["uses"] == 1:
            return tr(lang, "one person", "на одного")
        if invite["uses"] == 0:
            used = invite["used"]
            return tr(lang, f"any number of people (in: {used})", f"без ограничения (вошли: {used})")
        used, uses = invite["used"], invite["uses"]
        return tr(lang, f"{used} of {uses} people in", f"вошли {used} из {uses}")

    def term(self, invite: dict, lang: str) -> str:
        if invite["access_days"]:
            return tr(lang, "for ", "на ") + n_days(lang, invite["access_days"])
        if invite["access_until"]:
            return tr(lang, "until ", "до ") + self.when(invite["access_until"])
        return tr(lang, "for good", "бессрочный")

    def codes_view(self, lang: str) -> Doc:
        invites = self.access.open_invites()
        if not invites:
            return Doc().p(tr(lang, "No open codes. ", "Действующих кодов нет. "), "/invite")
        doc = Doc().h(tr(lang, "Open codes", "Действующие коды")).table(
            [tr(lang, "Note", "Пометка"), tr(lang, "Group", "Группа"), tr(lang, "People", "Люди"),
             tr(lang, "Access", "Доступ"), tr(lang, "Expires", "Истекает")],
            [[inv["note"] or "—", inv["group_name"] or "—",
              f"{inv['used']}/{inv['uses'] or '∞'}", self.term(inv, lang), self.when(inv["expires_at"])]
             for inv in invites], [None, None, "right", None, None])
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
        """A page of users, newest first, those that match the search of `/users <text>` when there is one; a button
        for each opens their card."""
        state = self.store.get_json(USERS_VIEW, {})
        query, everyone = state.get("query", ""), self.access.users()[::-1]
        users = [u for u in everyone if matches(u, query)] if query else everyone
        pages = max(1, -(-len(users) // USERS_PAGE))
        page = min(state.get("page", 0), pages - 1)
        shown = users[page * USERS_PAGE:(page + 1) * USERS_PAGE]
        title = tr(lang, "Users", "Пользователи") + f" · {len(users)}"
        doc = Doc().h(title + (f" · «{query}»" if query else ""))
        if not shown:
            doc.p(tr(lang, "Nobody found. ", "Никого не нашлось. ") if query else
                  tr(lang, "No users yet. ", "Пользователей пока нет. "), "/invite")
            return doc.buttons(*([(tr(lang, "Everyone", "Все"), "ua")] if query else []))
        doc.table([tr(lang, "Telegram", "Telegram"), tr(lang, "Full name", "ФИО"), tr(lang, "Group", "Группа"),
                   tr(lang, "Access until", "Доступ до")],
                  [[self.who(u), u["student"] or "—", u["group_name"] or "—", self.until(u, lang)] for u in shown])
        for start in range(0, len(shown), 2):
            doc.buttons(*[(u["tg_name"], f"uc:{u['tg_id']}") for u in shown[start:start + 2]])
        if pages > 1:
            doc.buttons(*([("◀", f"up:{page - 1}")] if page else []), (f"{page + 1} / {pages}", f"up:{page}"),
                        *([("▶", f"up:{page + 1}")] if page < pages - 1 else []))
        if query:
            doc.buttons((tr(lang, "Everyone", "Все"), "ua"))
        return doc.p(i(tr(lang, "Search: /users and a name, a full name, a @username, a code's note or a group.",
                          "Поиск: /users и имя, ФИО, @ник, пометка кода или группа.")))

    def user_card(self, tg_id: int, lang: str) -> Doc:
        """Everything about one user, and what the owner may do with them."""
        user = next((u for u in self.access.users() if u["tg_id"] == tg_id), None)
        if user is None:
            return self.users_view(lang)
        sheets = self.sheets.followed(tg_id)
        doc = Doc().h(user["tg_name"]).table([], [
            ["Telegram", self.who(user)],
            [tr(lang, "Full name", "ФИО"), user["student"] or "—"],
            [tr(lang, "Group", "Группа"), self.group_cell(user, lang)],
            [tr(lang, "Rank", "Ранг"), self.rank(user, lang)],
            [tr(lang, "Joined", "С нами с"), self.when(user["joined_at"])],
            [tr(lang, "Access until", "Доступ до"), self.until(user, lang)],
            [tr(lang, "Code", "Код"), user["note"] or "—"],
            [tr(lang, "Sheets", "Таблицы"), ", ".join(sheet_name(s) for s in sheets) or "—"],
            *([[tr(lang, "State", "Состояние"), tr(lang, "blocked the bot", "заблокировал(а) бота")]]
              if user["paused"] else [])])
        doc.p(i(tr(lang, "A member's sheet that the bot doesn't read yet waits for your approval; a trusted user's "
                         "doesn't.",
                   "Новую для бота таблицу обычного пользователя ты сначала одобряешь, доверенного — нет.")))
        trusted = user["rank"] >= TRUSTED
        doc.buttons((tr(lang, "Make a member", "Сделать обычным") if trusted else tr(lang, "Trust", "Доверять"),
                     f"rk:{tg_id}"),
                    *([(tr(lang, "Group", "Группа"), f"ug:{tg_id}")] if self.groups.all() else []),
                    (tr(lang, "Access", "Доступ"), f"ae:{tg_id}"))
        doc.buttons(*([(tr(lang, "Reset the full name", "Сбросить ФИО"), f"nr:{tg_id}")] if user["student"] else []),
                    (tr(lang, "Delete", "Удалить"), f"du:{tg_id}", "danger"))
        return doc.buttons((tr(lang, "« Users", "« Пользователи"), "users"))

    def until(self, user: dict, lang: str) -> str:
        if not user["expires_at"]:
            return "∞"
        ended = tr(lang, " (ended)", " (закончился)") if self.access.expired(user) else ""
        return self.when(user["expires_at"]) + ended

    def access_view(self, user: dict, lang: str) -> Doc:
        doc = Doc().p(tr(lang, "Access of ", "Доступ "), self.who(user), ": ", b(self.until(user, lang)), ". ",
                      tr(lang, "Days are added to its end, or to now when it has ended.",
                         "Дни добавляются к концу доступа, а если он закончился — к сегодня."))
        return doc.buttons(*self.prolong_buttons(user, lang)).buttons((tr(lang, "Back", "Назад"),
                                                                       f"uc:{user['tg_id']}"))

    async def prolong(self, chat: int, message_id: int | None, tg_id: int, days: int | None, lang: str) -> str | None:
        """Prolong a user's access (None: for good); the message turns into the users list, the user is told."""
        user = self.access.prolong(tg_id, days)
        if user is None:
            return tr(lang, "Already deleted", "Уже удалён")
        await self.show(chat, message_id, self.user_card(tg_id, lang))
        ulang = user["lang"]
        await self.tell(user, Doc().p(tr(ulang, "The owner prolonged your access: ", "Владелец продлил доступ: "),
                                      b(tr(ulang, "until ", "до ") + self.when(user["expires_at"]))
                                      if user["expires_at"] else b(tr(ulang, "for good", "бессрочно")), "."))
        if user["student"]:  # back after it ended: read what was missed
            self.read_soon(None, [s["id"] for s in self.sheets.followed(tg_id)])
        return tr(lang, "Prolonged", "Продлено")

    def group_cell(self, user: dict, lang: str) -> str:
        """The user's group, «by code» when it came with the invite and only the owner changes it."""
        if not user["group_name"]:
            return "—"
        return user["group_name"] + (tr(lang, " (by code)", " (по коду)") if self.groups.fixed(user) else "")

    def move_view(self, tg_id: int, lang: str) -> Doc:
        user = self.access.user(tg_id)
        if user is None:
            return self.users_view(lang)
        now = self.groups.get(user["grp"])
        doc = Doc().p(tr(lang, "Move ", "Перевести "), self.who(user), tr(lang, " from ", " из "),
                      b(now["name"]) if now else tr(lang, "no group", "«без группы»"), tr(lang, " to:", " в:"))
        doc.p(i(tr(lang, "They unfollow the old group's sheets and follow the new one's.",
                   "Подписки на таблицы старой группы снимутся, на таблицы новой — добавятся.")))
        choice = [(g["name"], f"um:{tg_id}.{g['id']}") for g in self.groups.all() if not now or g["id"] != now["id"]]
        if now:
            choice.append((tr(lang, "No group", "Без группы"), f"um:{tg_id}.0"))
        for start in range(0, len(choice), 4):
            doc.buttons(*choice[start:start + 4])
        return doc.buttons((tr(lang, "Cancel", "Отмена"), f"uc:{tg_id}"))

    async def move_user(self, tg_id: int, gid: int, lang: str) -> str | None:
        user = self.access.user(tg_id)
        if user is None:
            return None
        try:
            left, followed = self.groups.join(tg_id, gid)
        except GroupError:
            return None
        group = self.groups.get(gid)
        ulang = user["lang"]
        await self.tell(user, Doc().p(
            tr(ulang, "The owner moved you to the group ", "Владелец перевёл тебя в группу ") if group else
            tr(ulang, "The owner took you out of your group", "Владелец вывел тебя из группы"),
            b(group["name"]) if group else "", ". ",
            tr(ulang, f"Unfollowed sheets: {left}, followed: {len(followed)}. The list is in /sheets.",
               f"Отписал от таблиц: {left}, подписал на: {len(followed)}. Список — в /sheets.")))
        if user["student"] and followed:
            self.read_soon(tg_id, followed)
        return tr(lang, "Moved", "Переведён")

    @staticmethod
    def rank(user: dict, lang: str) -> str:
        return tr(lang, "trusted", "доверенный") if user["rank"] >= TRUSTED else tr(lang, "member", "обычный")

    def delete_view(self, tg_id: int, lang: str) -> Doc:
        user = self.access.user(tg_id)
        if user is None:
            return self.users_view(lang)
        doc = Doc().p(tr(lang, "Delete ", "Удалить "), self.who(user), "? ",
                      tr(lang, "Everything the bot keeps about them is erased; they'll need a new code to come back.",
                         "Все данные пользователя сотрутся; вернуться можно будет только с новым кодом."))
        return doc.buttons((tr(lang, "Yes, delete", "Да, удалить"), f"dy:{tg_id}", "danger"),
                           (tr(lang, "Cancel", "Отмена"), f"uc:{tg_id}"))

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

    async def answer_feedback(self, chat: int, tg_id: int, lang: str) -> str | None:
        """«Answer» under a user's /feedback: the owner's next message goes to them."""
        user = self.access.user(tg_id)
        if user is None:
            return tr(lang, "Already deleted", "Уже удалён")

        async def done(data: dict) -> None:
            ulang = user["lang"]
            answer = Doc().p(b(tr(ulang, "The owner answers:", "Ответ владельца:"))).quote(data["text"])
            if await self.tell(user, answer):
                await self.api.send(chat, Doc().p(tr(lang, "Sent to ", "Отправлено: "), self.who(user), "."))
            else:
                await self.api.send(chat, tr(lang, "Not delivered: the user blocked the bot.",
                                             "Не доставлено: пользователь заблокировал бота."))

        step = Step("text", lambda data: Doc().p(tr(lang, "Your answer to ", "Ответ для "), self.who(user), ":"))
        await self.flow(chat, lang, [step], done)
        return None

    # --- requests for sheets ----------------------------------------------------------------------

    async def approve(self, chat: int, message_id: int | None, rid: int, lang: str) -> str:
        try:
            done = self.sheets.approve(rid)
        except SheetError:
            return tr(lang, "The user has 20 sheets already", "У пользователя уже 20 таблиц")
        if done is None:
            return tr(lang, "Already handled", "Уже решено")
        request, sheet = done
        user = self.access.user(request["tg_id"])
        await self.show(chat, message_id, Doc().p(
            tr(lang, "Approved: ", "Одобрено: "), link(sheet_name(sheet), request["url"]), tr(lang, " for ", " для "),
            self.who(user), ". ", self.no_course(lang)).buttons(
            (tr(lang, "Set a short name", "Задать имя"), f"rn:{sheet['id']}")))
        ulang = user["lang"]
        await self.tell(user, Doc().p(tr(ulang, "The owner approved the sheet. ", "Владелец одобрил таблицу. "))
                        .extend(self.following(user, sheet)))
        if user["student"]:
            self.read_soon(user["tg_id"], [sheet["id"]])
        return tr(lang, "Approved", "Одобрено")

    async def decline(self, chat: int, message_id: int | None, rid: int, lang: str) -> str:
        request = self.sheets.drop_request(rid)
        if request is None:
            return tr(lang, "Already handled", "Уже решено")
        user = self.access.user(request["tg_id"])
        await self.show(chat, message_id, Doc().p(
            tr(lang, "Declined: ", "Отклонено: "), link(request["url"], request["url"]), tr(lang, " for ", " для "),
            self.who(user), "."))
        ulang = user["lang"]
        await self.tell(user, Doc().p(tr(ulang, "The owner declined the sheet ", "Владелец отклонил таблицу "),
                                      link(request["url"], request["url"]), "."))
        return tr(lang, "Declined", "Отклонено")

    async def reset_name(self, chat: int, message_id: int | None, tg_id: int, lang: str) -> str:
        user = self.access.user(tg_id)
        if user is None or not user["student"]:
            return tr(lang, "Nothing to reset", "Сбрасывать нечего")
        self.access.set_student(tg_id, None)
        self.grades.forget(tg_id)
        await self.show(chat, message_id, Doc().p(
            tr(lang, "Full name ", "ФИО "), b(user["student"]), tr(lang, " of ", " у "), self.who(user),
            tr(lang, " is reset.", " сброшено.")))
        ulang = user["lang"]
        await self.tell(user, Doc().p(
            tr(ulang, "The owner reset your full name. Send it again exactly as the sheets have it: ",
               "Владелец сбросил твоё ФИО. Укажи его заново, точно как в таблицах: "),
            code(tr(ulang, "/name Surname Name", "/name Фамилия Имя"))))
        return tr(lang, "Reset", "Сброшено")

    # --- groups and their sheets -----------------------------------------------------------------

    async def cmd_groups(self, chat: int, arg: str, lang: str) -> None:
        """`/groups` lists them, `/groups <name>` makes one, `/groups <name> <new name>` renames one."""
        words = arg.split()
        if len(words) == 1:
            await self.flow(chat, lang, [self.group_name_step(lang)], lambda data: self.make_group(chat, data, lang),
                            {"name": words[0]})
        elif len(words) == 2:
            group = self.groups.find(words[0])
            if group is None:
                await self.api.send(chat, tr(lang, "No such group.", "Такой группы нет."))
                return
            await self.flow(chat, lang, [self.group_name_step(lang, group["id"])],
                            lambda data: self.rename_group(chat, group["id"], data["name"], lang), {"name": words[1]})
        else:
            await self.api.send(chat, self.groups_view(lang))

    def group_name_step(self, lang: str, gid: int | None = None) -> Step:
        """A new group's name, or a new name of the group `gid`."""
        def check(value: str, data: dict) -> str:
            name = group_name(value)
            found = self.groups.find(name)
            if found is not None and found["id"] != gid:
                raise GroupError("a group with this name exists")
            return name

        return Step("name", lambda data: Doc().p(tr(lang, "The group's name: one word, up to 20 characters.",
                                                    "Имя группы: одно слово, до 20 символов.")), check=check,
                    error=lambda lang, e: tr(lang, "There is such a group already.", "Такая группа уже есть.")
                    if "exists" in str(e) else tr(lang, "A group's name is one word, up to 20 characters.",
                                                  "Имя группы — одно слово, до 20 символов."))

    async def make_group(self, chat: int, data: dict, lang: str) -> None:
        group = self.groups.create(data["name"])
        await self.api.send(chat, Doc().p(
            tr(lang, "Group ", "Группа "), b(group["name"]), tr(lang, " is made. ", " создана. "),
            tr(lang, "A code to it: ", "Код в неё: "), code(f"/invite {group['name']}"), ".")
            .buttons((tr(lang, "Add a sheet", "Добавить таблицу"), f"gsa:{group['id']}")))

    async def rename_group(self, chat: int, gid: int, name: str, lang: str) -> None:
        group = self.groups.rename(gid, name)
        await self.api.send(chat, Doc().p(tr(lang, "Renamed: ", "Переименована: "), b(group["name"]), "."))

    def groups_view(self, lang: str) -> Doc:
        groups = self.groups.all()
        if not groups:
            return Doc().p(tr(lang, "No groups yet.", "Групп пока нет.")).buttons(
                (tr(lang, "New group", "Новая группа"), "gn", "primary"))
        doc = Doc().h(tr(lang, "Groups", "Группы") + f" · {len(groups)}").table(
            [tr(lang, "Group", "Группа"), tr(lang, "Users", "Пользователей"), tr(lang, "Sheets", "Таблиц")],
            [[g["name"], str(g["users"]), str(g["sheets"])] for g in groups], [None, "right", "right"])
        for g in groups[:30]:
            doc.buttons((tr(lang, "Sheets: ", "Таблицы: ") + g["name"], f"gs:{g['id']}"),
                        (tr(lang, "Rename ", "Переименовать ") + g["name"], f"gm:{g['id']}"),
                        (tr(lang, "Delete ", "Удалить ") + g["name"], f"gd:{g['id']}", "danger"))
        return doc.buttons((tr(lang, "New group", "Новая группа"), "gn", "primary"))

    def delete_group_view(self, gid: int, lang: str) -> Doc:
        group = next((g for g in self.groups.all() if g["id"] == gid), None)
        if group is None:
            return self.groups_view(lang)
        return (Doc().p(tr(lang, "Delete the group ", "Удалить группу "), b(group["name"]), "? ",
                        tr(lang, f"Its {group['users']} users will be in no group and keep their sheets.",
                           f"Её пользователи ({group['users']}) останутся без группы, их подписки сохранятся."))
                .buttons((tr(lang, "Yes, delete", "Да, удалить"), f"gx:{gid}", "danger"),
                         (tr(lang, "Cancel", "Отмена"), "groups")))

    async def cmd_groupsheet(self, chat: int, arg: str, lang: str) -> None:
        """`/groupsheet` lists every group's sheets, `/groupsheet <group>` one group's, both with a button to add
        one; `/groupsheet [group] <link> [short name]` gives a group a sheet, the missing ones asked for."""
        words = arg.split()
        group = self.groups.find(words[0]) if words else None
        if group is not None:
            words = words[1:]
        if not words:
            await self.api.send(chat, self.group_view(lang, group["id"]) if group else self.every_group_view(lang))
            return
        data = {"link": words[0], **({"name": " ".join(words[1:])} if words[1:] else {})}
        await self.add_group_sheet(chat, {**data, **({"group": str(group["id"])} if group else {})}, lang)

    async def add_group_sheet(self, chat: int, data: dict, lang: str) -> None:
        groups = self.groups.all()
        if not groups:
            await self.api.send(chat, self.every_group_view(lang))
            return
        if len(groups) == 1:
            data = {"group": str(groups[0]["id"]), **data}

        def skip(data: dict) -> str:
            known = self.sheets.by_url(data["link"])
            return tr(lang, "Keep ", "Оставить ") + f"«{known['course']}»" if known and known["course"] else \
                tr(lang, "Title from Google", "Название из Google")

        steps = [self.group_step(lang), link_step(lang), self.short_name_step(lang, skip)]
        await self.flow(chat, lang, steps, lambda data: self.group_sheet_added(chat, data, lang), data)

    async def group_sheet_added(self, chat: int, data: dict, lang: str) -> None:
        group = self.groups.get(int(data["group"]))
        if group is None:
            return
        sheet, added = self.groups.add_sheet(group["id"], data["link"], self.owner_id, data.get("name") or None)
        doc = Doc().p(
            b(sheet_name(sheet)), tr(lang, " is a shared sheet of the group ", " — общая таблица группы "),
            b(group["name"]), ". ", tr(lang, f"Everyone in the group follows it, newcomers too; added now: {added}.",
                                       f"На неё подписаны все в группе, и новички тоже; добавлено сейчас: {added}."))
        await self.api.send(chat, doc if sheet["course"] else doc.p(self.no_course(lang)))
        self.read_soon(None, [sheet["id"]])

    def every_group_view(self, lang: str) -> Doc:
        groups = self.groups.all()
        if not groups:
            return Doc().p(tr(lang, "No groups yet: make one first in /groups.",
                              "Групп пока нет: сначала создай группу в /groups.")).buttons(
                (tr(lang, "New group", "Новая группа"), "gn", "primary"))
        doc = Doc()
        for g in groups:
            self.group_view(lang, g["id"], doc, add=False)
        return doc.buttons((tr(lang, "Add a sheet", "Добавить таблицу"), "gsa", "primary"))

    def group_view(self, lang: str, gid: int, doc: Doc | None = None, add: bool = True) -> Doc:
        """A group's sheets with a button to take each from it, and one to add a sheet; added to `doc` when
        given."""
        group, doc = self.groups.get(gid), doc if doc is not None else Doc()
        if group is None:
            return self.groups_view(lang)
        sheets = self.groups.sheets_of(gid)
        doc.h(tr(lang, "Group ", "Группа ") + f"{group['name']} · {len(sheets)}")
        add_button = (tr(lang, "Add a sheet to ", "Добавить таблицу в ") + group["name"], f"gsa:{gid}", "primary")
        if not sheets:
            doc.p(tr(lang, "No shared sheets yet.", "Общих таблиц пока нет."))
            return doc.buttons(add_button) if add else doc
        doc.table(
            [tr(lang, "Sheet", "Таблица"), tr(lang, "Short name", "Имя"), tr(lang, "Followers", "Подписаны")],
            [[link(s["title"] or s["sheet_id"][-6:], s["url"]), s["course"] or "—", str(s["followers"])]
             for s in sheets], [None, None, "right"])
        off = tr(lang, "Not shared: ", "Убрать из общих: ")
        for s in sheets:
            doc.buttons((off + sheet_name(s), f"gr:{gid}.{s['id']}", "danger"))
        return doc.buttons(add_button) if add else doc

    async def cmd_rename(self, chat: int, arg: str, lang: str) -> None:
        """`/rename <link> [short name]`: the sheet's short name and hashtag (its `course`) for all its followers;
        the missing ones asked for."""
        url, _, name = arg.strip().partition(" ")
        data = {"link": url, **({"name": name.strip()} if name.strip() else {})} if url else {}
        steps = [self.sheet_step(lang),
                 self.short_name_step(lang, lambda data: tr(lang, "Title from Google", "Название из Google"))]
        await self.flow(chat, lang, steps, lambda data: self.renamed(chat, data, lang), data)

    async def renamed(self, chat: int, data: dict, lang: str) -> None:
        sheet = self.sheets.set_course(data["link"], data.get("name", ""))
        await self.api.send(chat, Doc().p(tr(lang, "Short name: ", "Имя: "), b(sheet["course"]), ".")
                            if sheet["course"] else
                            Doc().p(tr(lang, "No short name. ", "Короткого имени нет. "), self.no_course(lang)))

    @staticmethod
    def no_course(lang: str) -> str:
        return tr(lang, "Without a short name the bot names a sheet by its title in Google, and its hashtag comes "
                        "from the first words of the title.",
                  "Без короткого имени бот называет таблицу её названием в Google, а хэштег берёт из первых слов "
                  "названия.")

    async def cmd_relayout(self, chat: int, arg: str, lang: str) -> None:
        """`/relayout <link>`, or the sheet asked for."""
        url = arg.split()[0] if arg.strip() else ""
        await self.flow(chat, lang, [self.sheet_step(lang)], lambda data: self.relaid(chat, data, lang),
                        {"link": url} if url else None)

    async def relaid(self, chat: int, data: dict, lang: str) -> None:
        sheet = self.sheets.by_url(data["link"])
        if self.poller is not None:
            self.poller.relayout(sheet)
        await self.api.send(chat, Doc().p(
            tr(lang, "The layout of ", "Разметка "), b(sheet_name(sheet)),
            tr(lang, " is dropped: the sheet is read and laid out again now.",
               " сброшена: таблица сейчас будет прочитана и размечена заново.")))

    # --- the interval ------------------------------------------------------------------------------

    INTERVALS = (10, 15, 30, 60)    # the buttons of /interval; any other is typed

    async def cmd_interval(self, chat: int, arg: str, lang: str) -> None:
        """`/interval 15`, or the interval now with buttons, and another one typed."""
        def check(value: str, data: dict) -> str:
            if not value.isdigit() or not poller.MIN_MINUTES <= int(value) <= poller.MAX_MINUTES:
                raise ValueError(value)
            return value

        async def done(data: dict) -> None:
            self.set_interval(int(data["minutes"]))
            await self.api.send(chat, self.interval_view(lang))

        step = Step("minutes", lambda data: self.interval_view(lang), check=check,
                    error=lambda lang, e: tr(lang, f"Minutes from {poller.MIN_MINUTES} to {poller.MAX_MINUTES}.",
                                             f"Минуты от {poller.MIN_MINUTES} до {poller.MAX_MINUTES}."),
                    cancel=False)
        await self.flow(chat, lang, [step], done, {"minutes": arg.strip()} if arg.strip() else None)

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
        doc.p(tr(lang, "Another: send the minutes, ", "Другой — пришли минуты, "),
              tr(lang, f"from {poller.MIN_MINUTES} to {poller.MAX_MINUTES}.",
                 f"от {poller.MIN_MINUTES} до {poller.MAX_MINUTES}."))
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
            [tr(lang, "subscriptions with a problem in the rows", "подписок с проблемой в строках"),
             str(rows["bad_rows"])],
            [tr(lang, "sheets waiting for approval", "таблиц ждут одобрения"), str(len(self.sheets.requests()))],
            [tr(lang, "access ends within 3 days", "доступ кончается в ближайшие 3 дня"),
             str(sum(bool(u["expires_at"]) and not self.access.expired(u)
                     and clock.parse(u["expires_at"]) - self.now() <= SOON for u in users))],
            [tr(lang, "access ended", "доступ закончился"), str(sum(self.access.expired(u) for u in users))],
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


SOON = dt.timedelta(days=3)     # /status counts the users whose access ends within this
UNITS = {"d": 24, "д": 24, "h": 1, "ч": 1}
MAX_USES = 1000
MAX_DAYS = 365


def invite_args(text: str, find_group, now: dt.datetime, zone: dt.tzinfo) -> dict:
    """`/invite` arguments in any order: a group's name (the first word that names one), `x5` or `x*` people, `3d`
    or `12h` the code works, `+30d` or `+31.12[.2027]` of access; the other words are the note. ValueError for a
    number out of range or a date gone."""
    out: dict = {"group": None, "uses": 1, "lifetime": None, "access_days": None, "access_until": None}
    note = []
    for word in text.split():
        low = word.lower()
        if out["group"] is None and (group := find_group(word)) is not None:
            out["group"] = group
        elif found := re.fullmatch(r"[xх×](\d+|\*)", low):
            out["uses"] = 0 if found[1] == "*" else int(found[1])
            if not 0 <= out["uses"] <= MAX_USES or found[1] == "0":
                raise ValueError(word)
        elif found := re.fullmatch(r"(\d+)([dдhч])", low):
            hours = int(found[1]) * UNITS[found[2]]
            if not 1 <= hours <= MAX_DAYS * 24:
                raise ValueError(word)
            out["lifetime"] = dt.timedelta(hours=hours)
        elif found := re.fullmatch(r"\+(\d+)[dд]", low):
            out["access_days"] = int(found[1])
            if not 1 <= out["access_days"] <= 10 * MAX_DAYS:
                raise ValueError(word)
        elif found := re.fullmatch(r"\+(\d{1,2})\.(\d{1,2})(?:\.(\d{2}|\d{4}))?", low):
            day, month, year = int(found[1]), int(found[2]), found[3]
            local = now.astimezone(zone)
            year = (2000 + int(year) if len(year) == 2 else int(year)) if year else local.year
            end = dt.datetime(year, month, day, 23, 59, tzinfo=zone)  # ValueError for 31.02
            if found[3] is None and end <= local:
                end = end.replace(year=year + 1)
            if end <= local:
                raise ValueError(word)
            out["access_until"] = end
        else:
            note.append(word)
    out["note"] = " ".join(note)
    return out
