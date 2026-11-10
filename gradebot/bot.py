"""What the bot answers. Only private chats count; the bot stays silent in groups.

Three kinds of senders: the owner (`owner.py`), users who came in with a code (`users.py`), and strangers, whom only
a code lets in (here). Callback data is short and typed, `kind:arg`; each module lists its own kinds.
"""

import asyncio
import datetime as dt
import logging
from collections.abc import Callable

from . import clock
from .access import ALPHABET, CODE_LENGTH, Access, NewUser, normalise
from .dialog import Dialogs
from .grades import Grades
from .groups import Groups
from .i18n import days, guess, tr
from .llm.budget import Budget
from .news import OwnerNews
from .notices import ReadNotices
from .owner import OwnerCommands
from .poller import Poller
from .rich import Doc, Html, b, code, join
from .sheets import GOOGLE_SHEET, Sheets
from .store import Store
from .telegram import BotApi, TelegramError
from .users import UserCommands, group_buttons

log = logging.getLogger(__name__)

ACCESS_CHECK = 600                      # seconds between looks at whose access ends
ACCESS_WARN = dt.timedelta(days=1)      # a user hears this long before their access ends
PROLONG = (7, 30, 90)                   # days the owner's buttons add
NAME_REMINDER = dt.timedelta(days=1)    # a user who came in and set no full name hears of it once after this
REMINDED = "remind.name"    # kv: the users told that their full name is missing
OWNER_LANG = "owner.lang"   # kv: the language of the owner's last message, for what the bot tells them unasked


def command(text: str) -> tuple[str, str]:
    """'/start@gradebot  K7QF-29XM' -> ('/start', 'K7QF-29XM'); plain text -> ('', text)."""
    text = text.strip()
    if not text.startswith("/"):
        return "", text
    head, _, rest = text.partition(" ")
    return head.split("@")[0].lower(), rest.strip()


def full_name(user: dict) -> str:
    return " ".join(filter(None, (user.get("first_name"), user.get("last_name")))) or str(user["id"])


class Bot(OwnerCommands, UserCommands, ReadNotices, OwnerNews, Dialogs):
    def __init__(self, api: BotApi, store: Store, access: Access, sheets: Sheets, owner_id: int, zone: dt.tzinfo,
                 username: str = "", now: Callable[[], dt.datetime] = clock.now_utc):
        self.api, self.store, self.access, self.sheets = api, store, access, sheets
        self.owner_id, self.zone, self.username, self.now = owner_id, zone, username, now
        self.grades = Grades(store, now)
        self.groups = Groups(store, sheets, now)
        self.poller: Poller | None = None   # set by the service; without it nothing is read
        self.budget: Budget | None = None   # set by the service: the owner's /status shows the spending
        self._tasks: set[asyncio.Task] = set()

    @property
    def owner_lang(self) -> str:
        return self.store.get_json(OWNER_LANG, "en")

    def when(self, iso: str) -> str:
        return clock.parse(iso).astimezone(self.zone).strftime("%d.%m %H:%M")

    @staticmethod
    def who(user: dict) -> Html:
        """«Name (@username)» of a user row."""
        return join(user["tg_name"], f" (@{user['username']})" if user.get("username") else "")

    async def tell_owner(self, doc: Doc, buttons: dict | None = None) -> None:
        """Something the owner should act on or know at once: a message with a sound, under the digest of news."""
        self.close_news()
        try:
            await self.api.send(self.owner_id, doc, buttons)
        except TelegramError as e:
            log.warning("telling the owner failed: %s", e)

    async def show(self, chat: int, message_id: int | None, doc: Doc | str) -> None:
        """Redraw a message in place, or send it as a new one when there is none to redraw."""
        if message_id is None:
            await self.api.send(chat, doc)
        else:
            await self.api.edit(chat, message_id, doc)

    def read_soon(self, tg_id: int | None, sheets: list[int]) -> None:
        """Read these sheets now; `tg_id` is told how it went."""
        if self.poller is not None:
            for sheet in sheets:
                self.poller.request(sheet, tg_id)

    async def tell(self, user: dict, text: str | Doc, silent: bool = False) -> int | None:
        """A user, unasked: the message id, or None when it failed. One who blocked the bot is paused."""
        try:
            return await self.api.send(user["tg_id"], text, silent=silent)
        except TelegramError as e:
            log.info("telling user %s failed: %s", user["tg_id"], e)
            if e.blocked:
                await self.blocked(user)
            return None

    async def blocked(self, user: dict) -> None:
        """The user blocked the bot: no reads, no messages until they write again; the owner hears of it once."""
        if user["paused"]:
            return
        self.access.set_paused(user["tg_id"], True)
        lang = self.owner_lang
        await self.news(self.who(user), tr(lang, " blocked the bot: their sheets are paused until they write to it "
                                                 "again.",
                                           ": бот заблокирован, таблицы на паузе до нового сообщения боту."),
                        buttons=(self.delete_button(user, lang),))

    def delete_button(self, user: dict, lang: str) -> tuple:
        return (tr(lang, "Delete ", "Удалить ") + user["tg_name"], f"du:{user['tg_id']}", "danger")

    # --- access for a while --------------------------------------------------------------------

    @staticmethod
    def ended(lang: str) -> Doc:
        return Doc().p(tr(lang, "Your access to the bot has ended: grades don't come any more. Your data is kept, "
                                "so once the owner prolongs it, everything goes on as before.",
                          "Доступ к боту закончился: оценки больше не приходят. Данные сохранены — если владелец "
                          "продлит доступ, всё продолжится как было.")).buttons(
            (tr(lang, "Ask to prolong", "Попросить продлить"), "pr", "primary"))

    async def watch_access(self) -> None:
        """Every few minutes: tell the users whose access ends within a day, or has ended, and remind the ones who
        never set their full name. The owner hears only when a user asks to prolong it; /status and /users show the
        rest."""
        while True:
            try:
                await self.check_access()
                await self.remind_names()
            except Exception:
                log.exception("checking access failed")
            await asyncio.sleep(ACCESS_CHECK)

    async def remind_names(self) -> None:
        """Once, a day after coming in: without a full name no row is looked for, and nothing ever comes."""
        told = set(self.store.get_json(REMINDED, []))
        since = clock.utc_iso(self.now() - NAME_REMINDER)
        for user in self.access.users():
            if user["student"] or user["tg_id"] in told or user["joined_at"] > since or user["paused"] \
                    or self.access.expired(user):
                continue
            told.add(user["tg_id"])
            self.store.put_json(REMINDED, sorted(told))
            lang = user["lang"]
            await self.tell(user, Doc().p(
                tr(lang, "I can't find your grades yet: I need your full name exactly as the sheets have it. ",
                   "Я пока не могу найти твои оценки: нужно ФИО — точно так, как в таблицах. "),
                code(tr(lang, "/name Surname Name", "/name Фамилия Имя"))))

    async def check_access(self) -> None:
        near, ended = self.access.ending(ACCESS_WARN)
        for user in near:
            self.access.told(user["tg_id"], 1)
            lang = user["lang"]
            await self.tell(user, Doc().p(tr(lang, "Your access to the bot ends ", "Доступ к боту закончится "),
                                          b(self.when(user["expires_at"])), ". ",
                                          tr(lang, "The owner may prolong it.", "Владелец может его продлить."))
                            .buttons((tr(lang, "Ask to prolong", "Попросить продлить"), "pr")))
        for user in ended:
            self.access.told(user["tg_id"], 2)
            await self.tell(user, self.ended(user["lang"]))

    async def ask_prolong(self, user: dict) -> str:
        """«Ask to prolong»: the owner hears it once until the access is prolonged."""
        lang = user["lang"]
        if not user["expires_at"]:
            return tr(lang, "Your access is for good", "Доступ и так бессрочный")
        if user["prolong_asked"]:
            return tr(lang, "Already asked", "Просьба уже отправлена")
        self.access.asked_prolong(user["tg_id"])
        owner = self.owner_lang
        what = tr(owner, " asks to prolong the access, which ends ", ": просьба продлить доступ, он заканчивается ") \
            if not self.access.expired(user) else \
            tr(owner, " asks to prolong the access, which ended ", ": просьба продлить доступ, он закончился ")
        await self.tell_owner(self.prolong_doc(user, owner, what)
                              .buttons((tr(owner, "Delete", "Удалить"), f"du:{user['tg_id']}", "danger")))
        return tr(lang, "Sent to the owner", "Отправлено владельцу")

    def prolong_doc(self, user: dict, lang: str, what: str) -> Doc:
        """«Name has access until …» with the buttons that prolong it."""
        doc = Doc().p(self.who(user), what, self.when(user["expires_at"]), ".")
        return doc.buttons(*self.prolong_buttons(user, lang))

    @staticmethod
    def prolong_buttons(user: dict, lang: str) -> list[tuple]:
        return [*[("+" + days(lang, n), f"ax:{user['tg_id']}.{n}") for n in PROLONG],
                (tr(lang, "For good", "Бессрочно"), f"ax:{user['tg_id']}.0", "success")]

    # --- updates -------------------------------------------------------------------------------

    async def handle(self, update: dict) -> None:
        if msg := update.get("message"):
            if msg.get("chat", {}).get("type") == "private" and "from" in msg:
                await self.on_message(msg)
        elif cb := update.get("callback_query"):
            await self.on_callback(cb)

    async def on_message(self, msg: dict) -> None:
        sender, chat = msg["from"], msg["chat"]["id"]
        cmd, arg = command(msg.get("text") or "")
        if sender["id"] == self.owner_id:
            self.close_news()
            lang = guess(sender.get("language_code"))
            if lang != self.owner_lang:
                self.store.put_json(OWNER_LANG, lang)
            if cmd:
                self.asks.pop(chat, None)
            elif await self.answer(chat, arg):
                return
            await self.owner_command(chat, cmd, arg, lang)
            return
        user = self.access.user(sender["id"])
        if user is not None:
            if (full_name(sender), sender.get("username")) != (user["tg_name"], user["username"]) or user["paused"]:
                self.access.update_profile(sender["id"], full_name(sender), sender.get("username"))
            if self.access.expired(user) and cmd != "/stop":  # leaving works after the end too
                await self.api.send(chat, self.ended(user["lang"]))
                return
            if not cmd and GOOGLE_SHEET.match(arg) and getattr(self.waiting(chat), "step", "link") != "link":
                cmd = "/sheet"  # a link pasted while another question waits: follow it, the question goes
            if cmd:
                self.asks.pop(chat, None)
            elif await self.answer(chat, arg):
                return
            await self.user_command(chat, user, cmd, arg)
            return
        await self.stranger(chat, sender, cmd, arg)

    async def on_callback(self, cb: dict) -> None:
        sender, msg = cb["from"], cb.get("message")
        note = None
        if msg is not None:
            chat, message_id = msg["chat"]["id"], msg["message_id"]
            kind, _, arg = (cb.get("data") or "").partition(":")
            if kind in ("an", "ac") and (sender["id"] == self.owner_id or (
                    (user := self.access.user(sender["id"])) is not None and not self.access.expired(user))):
                lang = self.owner_lang if sender["id"] == self.owner_id else user["lang"]
                note = await self.dialog_callback(chat, message_id, kind, arg, lang)
            elif sender["id"] == self.owner_id:
                self.close_news()
                where = None if self.is_digest(message_id) else message_id  # a digest stays as it is
                note = await self.owner_callback(chat, where, kind, arg, self.owner_lang)
            elif (user := self.access.user(sender["id"])) is not None:
                if kind == "pr":
                    note = await self.ask_prolong(user)
                elif self.access.expired(user) and kind not in ("stop", "stop!"):
                    note = tr(user["lang"], "Your access has ended", "Доступ закончился")
                else:
                    note = await self.user_callback(chat, message_id, user, kind, arg)
        await self.api.answer(cb["id"], note)

    # --- strangers -----------------------------------------------------------------------------

    async def stranger(self, chat: int, sender: dict, cmd: str, arg: str) -> None:
        """Only a code lets one in: `/start CODE`, the t.me link (the same), or the code pasted alone."""
        tg_id = sender["id"]
        if self.access.locked(tg_id):
            return
        lang = guess(sender.get("language_code"))
        raw = normalise(arg)
        pasted = not cmd and len(raw) == CODE_LENGTH and set(raw) <= set(ALPHABET)  # «привет» is no code
        candidate = arg if cmd == "/start" or pasted else ""
        if candidate:
            new = NewUser(tg_id, full_name(sender), sender.get("username"), lang)
            if (invite := self.access.redeem(candidate, new)) is not None:
                group = self.groups.get(invite["grp"])
                _, followed = self.groups.join(tg_id, group["id"]) if group else (0, [])
                if group:
                    self.groups.fix(tg_id)
                user = self.access.user(tg_id)
                await self.api.send(chat, self.welcome(lang, group, len(followed), user["expires_at"]))
                await self.cmd_name(chat, user, "")  # the full name next: without it no row is found
                note = f" «{invite['note']}»" if invite["note"] else ""
                owner = self.owner_lang
                count = f" ({invite['used']}/{invite['uses'] or '∞'})" if invite["uses"] != 1 else ""
                await self.news(self.who(user), tr(owner, " came in with the code", ": вход по коду"), note, count,
                                *([tr(owner, ", group ", ", группа "), group["name"]] if group else []),
                                *([tr(owner, ", access until ", ", доступ до "), self.when(user["expires_at"])]
                                  if user["expires_at"] else []),
                                buttons=(self.delete_button(user, owner),))
                return
            if self.access.locked(tg_id):
                return
            await self.api.send(chat, tr(
                lang, "This code doesn't work: it is wrong, used up, expired or revoked. Check it, or ask for a new "
                      "one.",
                "Этот код не подходит: он неверный, уже использован, истёк или отозван. Проверь его или попроси "
                "новый."))
            return
        await self.api.send(chat, Doc().p(tr(
            lang, "I send new grades from the Google Sheets of your courses as soon as they appear.",
            "Я присылаю новые оценки из таблиц Google твоих курсов, как только они появляются.")).p(tr(
            lang, "Access is by invite code: send me the code you got, or open the invite link.",
            "Вход по коду приглашения: пришли код, который тебе дали, или открой ссылку-приглашение.")))

    def welcome(self, lang: str, group: dict | None, followed: int, expires: str | None = None) -> Doc:
        """The first message: the user's group, or a choice of one when the code carried none, and the full name."""
        doc = (Doc().h(tr(lang, "You're in", "Ты в деле"))
               .p(tr(lang, "I'll send you new grades from the Google Sheets of your courses as soon as they appear.",
                     "Я буду присылать новые оценки из таблиц Google твоих курсов, как только они появятся.")))
        if expires:
            doc.p(tr(lang, "Your access lasts until ", "Доступ открыт до "), b(self.when(expires)), ".")
        if group:
            doc.p(tr(lang, "Your group: ", "Твоя группа: "), b(group["name"]), ". ",
                  tr(lang, f"You follow its shared sheets: {followed}. The list is in /sheets.",
                     f"Её общие таблицы ({followed}) уже в твоих подписках. Список — в /sheets.") if followed else "")
        elif groups := self.groups.all():
            doc.p(tr(lang, "Which group are you in? I'll follow its shared sheets for you. You can change it later "
                           "with /group.",
                     "В какой ты группе? Я подпишу тебя на её общие таблицы. Сменить группу можно потом: /group."))
            group_buttons(doc, groups, "gj", lang)
        return doc

