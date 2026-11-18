"""A group's admins: users of rank ADMIN who run the group they are in, within limits the owner does not need to
watch.

An admin makes codes to their group (members only, within the limits the owner sets with /limits: so many people,
working so many days; and no longer access than their own), sees their group's codes and revokes them, sees who is in
the group, gives the group shared sheets or takes them away, and approves or declines the sheets its members ask for.
The owner hears of what an admin does in their news. An admin gets the news of their group in the way they pick
with /news (`news.py`).

The owner makes a user an admin with a button in their card, or with a code that lets one in as the admin of its
group. An admin moved to another group, or whose group is deleted, is only trusted.

Callbacks: `ml` lists the group's users and `mc:<user>` opens one's card, `ak` lists the group's codes, `aq:<invite>`
asks to revoke one and `av:<invite>` revokes it, `gr:<group>.<sheet>` and `gsa:<group>` take a shared sheet away and
add one, `sa:<request>` and `sd:<request>` approve and decline a member's sheet, `nm` shows the news settings and
`nw:<mode>` sets them.
"""

import datetime as dt
import logging

from . import clock
from .access import ADMIN, MEMBER, TRUSTED
from .dialog import link_step
from .i18n import days as n_days
from .i18n import tr
from .menus import ADMIN_COMMANDS, USER_COMMANDS
from .news import DIGEST, EACH, OFF, admin_news, news_mode
from .owner import invite_args
from .rich import Doc, b, i, link
from .sheets import name as sheet_name
from .telegram import TelegramError

log = logging.getLogger(__name__)

LIMITS = "admin.limits"     # kv: {"uses", "days"}: people an admin's code lets in at most (0: any number), and days
DEFAULT_LIMITS = {"uses": 30, "days": 7}    # it works at most; shared by the admins without limits of their own


def own_limits(tg_id: int) -> str:
    """kv: an admin's own limits, {"uses", "days"}, over the shared ones."""
    return f"{LIMITS}.{tg_id}"
ADMIN_KINDS = {"ml", "mc", "ak", "aq", "av", "gr", "gsa", "sa", "sd", "nm", "nw"}
ADMIN_COMMAND_NAMES = {"/invite", "/codes", "/users", "/groupsheet", "/news"}
MODES = (DIGEST, EACH, OFF)


class GroupAdmins:
    def limits(self, tg_id: int | None = None) -> tuple[int, int]:
        """(people an admin's code lets in at most, 0 for any number; days it works at most): the admin's own when
        they have them, else the shared ones (tg_id None: the shared ones)."""
        found = {**DEFAULT_LIMITS, **(self.store.get_json(LIMITS) or {}),
                 **((self.store.get_json(own_limits(tg_id)) or {}) if tg_id else {})}
        return found["uses"], found["days"]

    def has_own_limits(self, tg_id: int) -> bool:
        return bool(self.store.get_json(own_limits(tg_id)))

    def set_limits(self, uses: int | None = None, days: int | None = None, tg_id: int | None = None) -> None:
        """The shared limits, or (with `tg_id`) an admin's own ones, starting from what they have now."""
        now_uses, now_days = self.limits(tg_id)
        self.store.put_json(own_limits(tg_id) if tg_id else LIMITS,
                            {"uses": now_uses if uses is None else uses, "days": now_days if days is None else days})

    def drop_own_limits(self, tg_id: int) -> None:
        """The admin goes by the shared limits again."""
        self.store.put_json(own_limits(tg_id), None)

    def admin_group(self, user: dict | None) -> dict | None:
        """The group the user runs: an admin's own group, or None."""
        if user is None or user["rank"] < ADMIN or not user["grp"]:
            return None
        return self.groups.get(user["grp"])

    @staticmethod
    def rank(user: dict, lang: str) -> str:
        if user["rank"] >= ADMIN and user["grp"]:
            return tr(lang, "group admin", "админ группы")
        return tr(lang, "trusted", "доверенный") if user["rank"] >= TRUSTED else tr(lang, "member", "обычный")

    # --- menus -----------------------------------------------------------------------------------

    async def admin_menu(self, user: dict) -> None:
        """The command menu of one user: with the admin's commands while they run a group."""
        admin = self.admin_group(user) is not None
        for lang in ("en", "ru"):
            commands = USER_COMMANDS[lang] + (ADMIN_COMMANDS[lang] if admin else [])
            try:
                await self.api.set_commands(commands, chat_id=user["tg_id"], lang=None if lang == "en" else lang)
            except TelegramError as e:
                log.info("the menu of %s not set: %s", user["tg_id"], e)

    async def admin_menus(self) -> None:
        """At the start: every admin's menu."""
        for user in self.access.users():
            if self.admin_group(user) is not None:
                await self.admin_menu(user)

    async def set_admin(self, tg_id: int, admin: bool) -> dict | None:
        """The owner makes a user their group's admin, or only trusted again; the user is told and gets the menu."""
        user = self.access.user(tg_id)
        if user is None or (admin and not user["grp"]):
            return None
        self.access.set_rank(tg_id, ADMIN if admin else TRUSTED)
        user = self.access.user(tg_id)
        lang, group = user["lang"], self.groups.get(user["grp"])
        await self.tell(user, Doc().p(
            tr(lang, "The owner made you the admin of the group ", "Владелец сделал тебя админом группы "),
            b(group["name"]), ". ", tr(lang, "What you can do is in /help.", "Что ты можешь — в /help."))
            if admin else Doc().p(tr(lang, "You are no longer the admin of your group.",
                                     "Ты больше не админ группы.")))
        await self.admin_menu(user)
        return user

    # --- commands ------------------------------------------------------------------------------

    async def admin_command(self, chat: int, user: dict, cmd: str, arg: str) -> bool:
        """An admin's command: True when it was one."""
        group = self.admin_group(user)
        if group is None or cmd not in ADMIN_COMMAND_NAMES:
            return False
        lang = user["lang"]
        if cmd == "/invite":
            await self.admin_invite(chat, user, group, arg)
        elif cmd == "/codes":
            await self.api.send(chat, self.admin_codes_view(user, group))
        elif cmd == "/users":
            await self.api.send(chat, self.members_view(user, group))
        elif cmd == "/groupsheet":
            if arg.strip():
                await self.flow(chat, lang, [link_step(lang)],
                                lambda data: self.admin_sheet_added(chat, user, group, data), {"link": arg.split()[0]})
            else:
                await self.api.send(chat, self.group_view(lang, group["id"]))
        elif cmd == "/news":
            await self.api.send(chat, self.news_view(user))
        return True

    async def admin_callback(self, chat: int, message_id: int, user: dict, kind: str, arg: str) -> str | None:
        lang, group = user["lang"], self.admin_group(user)
        if group is None:
            return tr(lang, "Only your group's admin can do this", "Это может только админ группы")
        number = int(arg) if arg.isdigit() else None
        if kind == "ml":
            await self.api.edit(chat, message_id, self.members_view(user, group))
        elif kind == "mc" and number:
            await self.api.edit(chat, message_id, self.member_card(user, group, number))
        elif kind == "ak":
            await self.api.edit(chat, message_id, self.admin_codes_view(user, group))
        elif kind == "aq" and number:
            invite = self.group_invite(group, number)
            if invite is None:
                return tr(lang, "Already used or revoked", "Уже использован или отозван")
            await self.api.edit(chat, message_id, Doc().p(
                tr(lang, "Revoke the code ", "Отозвать код "), b(invite["note"] or self.when(invite["created_at"],
                                                                                         lang)), "?")
                .buttons((tr(lang, "Yes, revoke", "Да, отозвать"), f"av:{number}", "danger"),
                         (tr(lang, "Cancel", "Отмена"), "ak")))
        elif kind == "av" and number:
            done = self.group_invite(group, number) is not None and self.access.revoke(number)
            await self.api.edit(chat, message_id, self.admin_codes_view(user, group))
            return tr(lang, "Revoked", "Отозван") if done else tr(lang, "Already used or revoked",
                                                                 "Уже использован или отозван")
        elif kind == "gr":
            gid, _, sheet = arg.partition(".")
            if gid != str(group["id"]) or not sheet.isdigit():
                return None
            found = next((s for s in self.groups.sheets_of(group["id"]) if s["id"] == int(sheet)), None)
            if found is not None and self.groups.remove_sheet(group["id"], int(sheet)):
                owner = self.owner_lang
                await self.news(self.who(user), tr(owner, ", admin of ", ", админ "), b(group["name"]),
                                tr(owner, ", took away the shared sheet ", ", убрал(а) общую таблицу "),
                                link(sheet_name(found), found["url"]))
            await self.api.edit(chat, message_id, self.group_view(lang, group["id"]))
        elif kind == "gsa":
            await self.flow(chat, lang, [link_step(lang)], lambda data: self.admin_sheet_added(chat, user, group, data))
        elif kind in ("sa", "sd") and number:
            request = self.sheets.request(number)
            asker = self.access.user(request["tg_id"]) if request else None
            if asker is None:
                return tr(lang, "Already handled", "Уже решено")
            if asker["grp"] != group["id"]:
                return None
            if kind == "sa":
                return await self.approve(chat, message_id, number, lang, by=user)
            return await self.decline(chat, message_id, number, lang, by=user)
        elif kind == "nm":
            await self.api.send(chat, self.news_view(user))
        elif kind == "nw" and arg in MODES:
            self.store.put_json(news_mode(user["tg_id"]), arg)
            self.close_news(admin_news(user["tg_id"]))
            await self.api.edit(chat, message_id, self.news_view(user))
            return tr(lang, "Saved", "Сохранено")
        return None

    def admin_help(self, user: dict) -> Doc:
        lang, group = user["lang"], self.admin_group(user)
        uses, days = self.limits(user["tg_id"])
        people = tr(lang, f"up to {uses} people", f"до {uses} человек") if uses else \
            tr(lang, "any number of people", "сколько угодно человек")
        return Doc().h(tr(lang, "Admin of ", "Админ группы ") + group["name"]).table([], [
            ["/invite " + tr(lang, "[x5] [3d] [+30d] [note]", "[x5] [3д] [+30д] [пометка]"),
             tr(lang, f"a code to your group: {people}, works up to {n_days('en', days)}, access no longer "
                      "than yours",
                f"код в твою группу: {people}, действует до {n_days('ru', days)}, доступ — не дольше твоего")],
            ["/codes", tr(lang, "your group's open codes, revoke", "действующие коды группы, отзыв")],
            ["/users", tr(lang, "who is in your group", "кто в твоей группе")],
            ["/groupsheet " + tr(lang, "[link]", "[ссылка]"),
             tr(lang, "the group's shared sheets: everyone in it follows them",
                "общие таблицы группы: на них подписаны все в ней")],
            ["/news", tr(lang, "how the group's news come: a quiet digest, a message each, or none",
                         "как присылать новости группы: тихой сводкой, каждую отдельно или не присылать")]]).p(
            i(tr(lang, "Sheets your group's members ask for come to you to approve, as to the owner.",
                 "Заявки участников группы на новые таблицы приходят тебе на одобрение, как и владельцу.")))

    # --- codes ---------------------------------------------------------------------------------

    async def admin_invite(self, chat: int, user: dict, group: dict, arg: str) -> None:
        """`/invite [x5] [3d] [+30d] [note]` to the admin's group, or asked step by step."""
        lang = user["lang"]
        if arg.strip().lower() in ("help", "?", "помощь"):
            await self.api.send(chat, self.admin_help(user))
        elif arg.strip():
            try:
                args = invite_args(arg, lambda _: None, self.now(), self.zone)
            except ValueError:
                await self.api.send(chat, self.admin_help(user))
                return
            await self.admin_make_invite(chat, user, group, args)
        else:
            steps = [step for step in self.invite_steps(lang, admin=user) if step.name not in ("group", "new_group",
                                                                                                  "rank")]
            await self.flow(chat, lang, steps, lambda data: self.admin_invite_asked(chat, user, group, data))

    async def admin_invite_asked(self, chat: int, user: dict, group: dict, data: dict) -> None:
        words = " ".join(data.get(name, "") for name in ("people", "lifetime", "access"))
        args = invite_args(words, lambda _: None, self.now(), self.zone)
        args["note"] = data.get("note", "")
        await self.admin_make_invite(chat, user, group, args)

    async def admin_make_invite(self, chat: int, user: dict, group: dict, args: dict) -> None:
        lang = user["lang"]
        error = self.admin_limits(user, args)
        if error:
            await self.api.send(chat, Doc().p(error).extend(self.admin_help(user)))
            return
        args["group"], args["rank"] = group, MEMBER
        await self.api.send(chat, self.make_invite(args, lang))
        owner = self.owner_lang
        await self.news(self.who(user), tr(owner, ", admin of ", ", админ "), b(group["name"]),
                        tr(owner, ", made a code", ", создал(а) код"),
                        f" «{args['note']}»" if args["note"] else "",
                        tr(owner, f" for {args['uses']} people", f" на {args['uses']} чел."))

    def admin_limits(self, user: dict, args: dict) -> str | None:
        """Hold an admin's code within their limits: why it can't be, or None. Access past the admin's own is cut to
        it."""
        lang, (uses, days) = user["lang"], self.limits(user["tg_id"])
        if uses and (args["uses"] == 0 or args["uses"] > uses):
            return tr(lang, f"A code of yours lets in at most {uses} people.",
                      f"Твой код может впустить не больше {uses} человек.")
        lifetime, longest = args["lifetime"] or dt.timedelta(days=self.access.invite_days), dt.timedelta(days=days)
        if lifetime > longest:
            if args["lifetime"]:
                return tr(lang, f"A code of yours works at most {n_days('en', days)}.",
                          f"Твой код действует не больше {n_days('ru', days)}.")
            lifetime = longest
        args["lifetime"] = lifetime
        if user["expires_at"]:
            end = clock.parse(user["expires_at"])
            wanted = args["access_until"] or (self.now() + dt.timedelta(days=args["access_days"])
                                              if args["access_days"] else None)
            if wanted is None or wanted > end:
                args["access_until"], args["access_days"] = end, None
        return None

    def group_invite(self, group: dict, invite_id: int) -> dict | None:
        """An open code to the group that an admin may revoke: one for members."""
        return next((inv for inv in self.access.open_invites()
                     if inv["id"] == invite_id and inv["grp"] == group["id"] and inv["rank"] < ADMIN), None)

    def admin_codes_view(self, user: dict, group: dict) -> Doc:
        lang = user["lang"]
        invites = [inv for inv in self.access.open_invites() if inv["grp"] == group["id"] and inv["rank"] < ADMIN]
        if not invites:
            return Doc().p(tr(lang, "Your group has no open codes. ", "Действующих кодов в группу нет. "), "/invite")
        doc = Doc().h(tr(lang, "Codes to ", "Коды в ") + group["name"]).table(
            [tr(lang, "Note", "Пометка"), tr(lang, "People", "Люди"), tr(lang, "Access", "Доступ"),
             tr(lang, "Expires", "Истекает") + ", " + self.tz(lang)],
            [[inv["note"] or "—", f"{inv['used']}/{inv['uses'] or '∞'}", self.term(inv, lang),
              self.when(inv["expires_at"])] for inv in invites], [None, "right", None, None])
        for inv in invites[:30]:
            doc.buttons((tr(lang, "Revoke ", "Отозвать ") + (inv["note"] or self.when(inv["created_at"], lang)),
                         f"aq:{inv['id']}", "danger"))
        return doc

    # --- users ---------------------------------------------------------------------------------

    def members(self, group: dict) -> list[dict]:
        return [u for u in self.access.users() if u["grp"] == group["id"]]

    def members_view(self, user: dict, group: dict) -> Doc:
        lang, members = user["lang"], self.members(group)
        doc = Doc().h(tr(lang, "Group ", "Группа ") + f"{group['name']} · {len(members)}")
        doc.table([tr(lang, "Telegram", "Telegram"), tr(lang, "Full name", "ФИО"),
                   tr(lang, "Access until", "Доступ до") + ", " + self.tz(lang)],
                  [[self.who(u), u["student"] or "—", self.until(u, lang, zone=False)] for u in members])
        shown = [u for u in members if u["tg_id"] != user["tg_id"]][:50]
        for start in range(0, len(shown), 2):
            doc.buttons(*[(u["tg_name"], f"mc:{u['tg_id']}") for u in shown[start:start + 2]])
        return doc

    def member_card(self, user: dict, group: dict, tg_id: int) -> Doc:
        lang = user["lang"]
        member = next((u for u in self.members(group) if u["tg_id"] == tg_id), None)
        if member is None:
            return self.members_view(user, group)
        sheets = self.sheets.followed(tg_id)
        doc = Doc().h(member["tg_name"]).table([], [
            ["Telegram", self.who(member)],
            [tr(lang, "Full name", "ФИО"), member["student"] or "—"],
            [tr(lang, "Rank", "Ранг"), self.rank(member, lang)],
            [tr(lang, "Joined", "С нами с"), self.when(member["joined_at"], lang)],
            [tr(lang, "Access until", "Доступ до"), self.until(member, lang)],
            [tr(lang, "Code", "Код"), member["note"] or "—"],
            [tr(lang, "Sheets", "Таблицы"), ", ".join(sheet_name(s) for s in sheets) or "—"],
            *([[tr(lang, "State", "Состояние"), tr(lang, "blocked the bot", "заблокировал(а) бота")]]
              if member["paused"] else [])])
        return doc.buttons((tr(lang, "« Group", "« Группа"), "ml"))

    # --- shared sheets -------------------------------------------------------------------------

    async def admin_sheet_added(self, chat: int, user: dict, group: dict, data: dict) -> None:
        lang = user["lang"]
        if self.admin_group(self.access.user(user["tg_id"])) is None:
            return
        sheet, added = self.groups.add_sheet(group["id"], data["link"], user["tg_id"])
        await self.api.send(chat, Doc().p(
            b(sheet_name(sheet)), tr(lang, " is a shared sheet of the group ", " — общая таблица группы "),
            b(group["name"]), ". ", tr(lang, f"Everyone in the group follows it, newcomers too; added now: {added}.",
                                       f"На неё подписаны все в группе, и новички тоже; добавлено сейчас: {added}.")))
        owner = self.owner_lang
        await self.news(self.who(user), tr(owner, ", admin of ", ", админ "), b(group["name"]),
                        tr(owner, ", added the shared sheet ", ", добавил(а) общую таблицу "),
                        link(sheet_name(sheet), sheet["url"]),
                        buttons=((tr(owner, "Set a short name", "Задать имя"), f"rn:{sheet['id']}"),))
        self.read_soon(None, [sheet["id"]])

    # --- news ----------------------------------------------------------------------------------

    async def moved_news(self, user: dict, before: dict | None, after: dict | None) -> None:
        """A user left a group or joined one: the admins of each hear of it."""
        if (before or {}).get("id") == (after or {}).get("id"):
            return
        if before:
            await self.group_news(before["id"], user["tg_id"], lambda lang: [
                self.who(user), tr(lang, " left the group", ": больше не в группе")])
            if (fresh := self.access.user(user["tg_id"])) is not None:
                await self.admin_menu(fresh)  # an admin who left their group runs it no more
        if after:
            await self.group_news(after["id"], user["tg_id"], lambda lang: [
                self.who(user), tr(lang, " joined the group", ": теперь в группе")])

    def news_view(self, user: dict) -> Doc:
        lang = user["lang"]
        mode = self.store.get_json(news_mode(user["tg_id"]), DIGEST)
        names = {DIGEST: tr(lang, "a quiet digest", "тихой сводкой"),
                 EACH: tr(lang, "a message each", "каждую отдельно"), OFF: tr(lang, "none", "не присылать")}
        doc = Doc().p(tr(lang, "News of your group: who came in, set a name, joined or left, and whose access ends. "
                               "Now: ",
                         "Новости группы: кто вошёл, указал ФИО, пришёл или ушёл и у кого кончается доступ. Сейчас: "),
                      b(names[mode]), ".")
        doc.p(i(tr(lang, "A digest is one silent message, edited as news comes, until you write to the bot; a message "
                         "each comes with a sound.",
                   "Сводка — одно сообщение без звука, оно дополняется, пока ты не напишешь боту; каждая отдельно — "
                   "со звуком.")))
        labels = {DIGEST: tr(lang, "Quiet digest", "Тихой сводкой"), EACH: tr(lang, "Each one", "Каждую отдельно"),
                  OFF: tr(lang, "None", "Не присылать")}
        return doc.buttons(*[(("✓ " if m == mode else "") + labels[m], f"nw:{m}") for m in MODES])
