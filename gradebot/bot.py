"""What the bot answers. Only private chats count; the bot stays silent in groups.

Three kinds of senders: the owner (`owner.py`), users who came in with a code (`users.py`), and strangers, whom only
a code lets in (here). Callback data is short and typed, `kind:arg`; each module lists its own kinds.
"""

import asyncio
import datetime as dt
import logging
from collections.abc import Callable

from . import clock
from .access import Access, NewUser, normalise
from .grades import Grades
from .i18n import guess, tr
from .notices import ReadNotices
from .owner import OwnerCommands
from .poller import Poller
from .rich import Doc, Html, code, join, keyboard
from .sheets import Sheets
from .store import Store
from .telegram import BotApi, TelegramError
from .users import UserCommands

log = logging.getLogger(__name__)

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


class Bot(OwnerCommands, UserCommands, ReadNotices):
    def __init__(self, api: BotApi, store: Store, access: Access, sheets: Sheets, owner_id: int, zone: dt.tzinfo,
                 username: str = "", now: Callable[[], dt.datetime] = clock.now_utc):
        self.api, self.store, self.access, self.sheets = api, store, access, sheets
        self.owner_id, self.zone, self.username, self.now = owner_id, zone, username, now
        self.grades = Grades(store, now)
        self.poller: Poller | None = None   # set by the service; without it nothing is read
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
        try:
            await self.api.send(self.owner_id, doc, buttons)
        except TelegramError as e:
            log.warning("telling the owner failed: %s", e)

    def read_soon(self, tg_id: int | None, sheets: list[int]) -> None:
        """Read these sheets now; `tg_id` is told how it went."""
        if self.poller is not None:
            for sheet in sheets:
                self.poller.request(sheet, tg_id)

    async def tell(self, user: dict, text: str | Doc) -> None:
        """A user, unasked; one who blocked the bot is paused."""
        try:
            await self.api.send(user["tg_id"], text)
        except TelegramError as e:
            if e.blocked:
                self.access.set_paused(user["tg_id"], True)
            log.info("telling user %s failed: %s", user["tg_id"], e)

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
            lang = guess(sender.get("language_code"))
            if lang != self.owner_lang:
                self.store.put_json(OWNER_LANG, lang)
            await self.owner_command(chat, cmd, arg, lang)
            return
        user = self.access.user(sender["id"])
        if user is not None:
            if (full_name(sender), sender.get("username")) != (user["tg_name"], user["username"]) or user["paused"]:
                self.access.update_profile(sender["id"], full_name(sender), sender.get("username"))
            await self.user_command(chat, user, cmd, arg)
            return
        await self.stranger(chat, sender, cmd, arg)

    async def on_callback(self, cb: dict) -> None:
        sender, msg = cb["from"], cb.get("message")
        note = None
        if msg is not None:
            chat, message_id = msg["chat"]["id"], msg["message_id"]
            kind, _, arg = (cb.get("data") or "").partition(":")
            if sender["id"] == self.owner_id:
                note = await self.owner_callback(chat, message_id, kind, arg, self.owner_lang)
            elif (user := self.access.user(sender["id"])) is not None:
                note = await self.user_callback(chat, message_id, user, kind, arg)
        await self.api.answer(cb["id"], note)

    # --- strangers -----------------------------------------------------------------------------

    async def stranger(self, chat: int, sender: dict, cmd: str, arg: str) -> None:
        """Only a code lets one in: `/start CODE`, the t.me link (the same), or the code pasted alone."""
        tg_id = sender["id"]
        if self.access.locked(tg_id):
            return
        lang = guess(sender.get("language_code"))
        candidate = arg if cmd == "/start" else (arg if not cmd and len(normalise(arg)) == 8 else "")
        if candidate:
            new = NewUser(tg_id, full_name(sender), sender.get("username"), lang)
            if (invite := self.access.redeem(candidate, new)) is not None:
                group = self.sheets.follow_group(tg_id)
                await self.api.send(chat, self.welcome(lang, group))
                user = self.access.user(tg_id)
                note = f" «{invite['note']}»" if invite["note"] else ""
                await self.tell_owner(
                    Doc().p(self.who(user), tr(self.owner_lang, " came in with the code", " вошёл по коду"), note),
                    keyboard([[(tr(self.owner_lang, "Delete", "Удалить"), f"du:{tg_id}")]]))
                return
            if self.access.locked(tg_id):
                return
        await self.api.send(chat, tr(lang, "You need an invite code to use this bot.",
                                     "Чтобы пользоваться ботом, нужен код приглашения."))

    def welcome(self, lang: str, group: int) -> Doc:
        doc = (Doc().h(tr(lang, "You're in", "Ты в деле"))
               .p(tr(lang, "I'll send you new grades from the Google Sheets of your courses as soon as they appear.",
                     "Я буду присылать новые оценки из таблиц Google твоих курсов, как только они появятся.")))
        if group:
            doc.p(tr(lang, f"You already follow the group's sheets: {group}. The list is in /sheets.",
                     f"Ты уже подписан на общие таблицы группы: {group}. Список — в /sheets."))
        return doc.p(tr(lang, "First, your full name exactly as the sheets have it: ",
                        "Сначала ФИО — точно так, как в таблицах: "),
                     code(tr(lang, "/name Surname Name", "/name Фамилия Имя")))
