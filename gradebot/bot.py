"""What the bot answers: the owner's commands (invites, codes, users), the way in with a code, and the users' own
commands. Only private chats count; the bot stays silent in groups.

Callback data is short and typed: `rv:<invite>` revokes a code, `du:<user>` asks to delete a user and `dy:<user>`
deletes them, `users` and `codes` redraw the lists, `stop` and `stop!` are a user's own /stop and its confirmation.
"""

import datetime as dt
import logging
from collections.abc import Callable

from . import clock
from .access import Access, NewUser, normalise
from .i18n import guess, tr
from .rich import Doc, Html, code, i, join, keyboard, link
from .store import Store
from .telegram import BotApi, TelegramError

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


class Bot:
    def __init__(self, api: BotApi, store: Store, access: Access, owner_id: int, zone: dt.tzinfo,
                 username: str = "", now: Callable[[], dt.datetime] = clock.now_utc):
        self.api, self.store, self.access, self.owner_id, self.zone = api, store, access, owner_id, zone
        self.username, self.now = username, now

    @property
    def owner_lang(self) -> str:
        return self.store.get_json(OWNER_LANG, "en")

    def when(self, iso: str) -> str:
        return clock.parse(iso).astimezone(self.zone).strftime("%d.%m %H:%M")

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
        sender, data, msg = cb["from"], cb.get("data") or "", cb.get("message")
        if msg is None:
            await self.api.answer(cb["id"])
            return
        chat, message_id = msg["chat"]["id"], msg["message_id"]
        kind, _, arg = data.partition(":")
        note = None
        if sender["id"] == self.owner_id:
            lang = self.owner_lang
            if kind == "rv" and arg.isdigit():
                note = tr(lang, "Revoked", "Отозван") if self.access.revoke(int(arg)) else \
                    tr(lang, "Already used or revoked", "Уже использован или отозван")
                await self.api.edit(chat, message_id, *self.codes_view(lang))
            elif kind == "codes":
                await self.api.edit(chat, message_id, *self.codes_view(lang))
            elif kind == "du" and arg.isdigit():
                await self.api.edit(chat, message_id, *self.delete_view(int(arg), lang))
            elif kind == "dy" and arg.isdigit():
                note = await self.delete_user(int(arg), lang)
                await self.api.edit(chat, message_id, *self.users_view(lang))
            elif kind == "users":
                await self.api.edit(chat, message_id, *self.users_view(lang))
        elif (user := self.access.user(sender["id"])) is not None:
            lang = user["lang"]
            if data == "stop!":
                self.access.delete(user["tg_id"])
                await self.api.edit(chat, message_id, tr(
                    lang, "Your data is erased. To come back you'll need a new invite code.",
                    "Твои данные удалены. Чтобы вернуться, нужен новый код приглашения."))
                left = tr(self.owner_lang, "Left the bot with /stop: ", "Ушёл из бота через /stop: ")
                await self.tell_owner(Doc().p(left, self.who(user)))
            elif data == "stop":
                await self.api.edit(chat, message_id,
                                    tr(lang, "Staying, nothing erased.", "Остаёмся, ничего не удалено."))
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
                await self.api.send(chat, self.welcome(lang))
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

    def welcome(self, lang: str) -> Doc:
        return (Doc().h(tr(lang, "You're in", "Ты в деле"))
                .p(tr(lang, "I'll send you new grades from the Google Sheets of your courses as soon as they appear.",
                      "Я буду присылать новые оценки из таблиц Google твоих курсов, как только они появятся."))
                .p(tr(lang, "First, your full name exactly as the sheets have it: ",
                      "Сначала ФИО — точно так, как в таблицах: "),
                   code(tr(lang, "/name Surname Name", "/name Фамилия Имя"))))

    # --- users ---------------------------------------------------------------------------------

    async def user_command(self, chat: int, user: dict, cmd: str, arg: str) -> None:
        lang = user["lang"]
        if cmd == "/stop":
            await self.api.send(chat, Doc().p(tr(
                lang, "Erase everything the bot keeps about you and leave? You'll need a new code to come back.",
                "Удалить всё, что бот о тебе хранит, и уйти? Чтобы вернуться, понадобится новый код.")),
                keyboard([[(tr(lang, "Yes, erase", "Да, удалить"), "stop!"), (tr(lang, "Cancel", "Отмена"), "stop")]]))
        elif cmd == "/start" and not arg:
            await self.api.send(chat, tr(lang, "You're already in. /help lists the commands.",
                                         "Ты уже в списке. Команды — в /help."))
        else:
            await self.api.send(chat, self.user_help(lang))

    def user_help(self, lang: str) -> Doc:
        return (Doc().h(tr(lang, "Commands", "Команды"))
                .table([], [["/help", tr(lang, "this list", "этот список")],
                            ["/stop", tr(lang, "erase your data and leave", "удалить свои данные и уйти")]]))

    # --- the owner -----------------------------------------------------------------------------

    async def owner_command(self, chat: int, cmd: str, arg: str, lang: str) -> None:
        if cmd == "/invite":
            await self.api.send(chat, self.invite_view(arg, lang))
        elif cmd == "/codes":
            await self.api.send(chat, *self.codes_view(lang))
        elif cmd == "/users":
            await self.api.send(chat, *self.users_view(lang))
        else:
            await self.api.send(chat, self.owner_help(lang))

    def owner_help(self, lang: str) -> Doc:
        return (Doc().h(tr(lang, "Owner's commands", "Команды владельца"))
                .table([], [["/invite " + tr(lang, "[note]", "[пометка]"),
                             tr(lang, "a one-time invite code", "одноразовый код приглашения")],
                            ["/codes", tr(lang, "unused codes, revoke", "неиспользованные коды, отзыв")],
                            ["/users", tr(lang, "users, delete", "пользователи, удаление")]]))

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

    def codes_view(self, lang: str) -> tuple[Doc, dict | None]:
        invites = self.access.open_invites()
        if not invites:
            return Doc().p(tr(lang, "No unused codes. ", "Неиспользованных кодов нет. "), "/invite"), None
        doc = Doc().h(tr(lang, "Unused codes", "Неиспользованные коды")).table(
            [tr(lang, "Note", "Пометка"), tr(lang, "Created", "Создан"), tr(lang, "Expires", "Истекает")],
            [[inv["note"] or "—", self.when(inv["created_at"]), self.when(inv["expires_at"])] for inv in invites])
        revoke = tr(lang, "Revoke ", "Отозвать ")
        buttons = [[(revoke + (inv["note"] or self.when(inv["created_at"])), f"rv:{inv['id']}")]
                   for inv in invites[:20]]
        return doc, keyboard(buttons)

    def users_view(self, lang: str) -> tuple[Doc, dict | None]:
        users = self.access.users()
        if not users:
            return Doc().p(tr(lang, "No users yet. ", "Пользователей пока нет. "), "/invite"), None
        doc = Doc().h(tr(lang, "Users", "Пользователи") + f" · {len(users)}").table(
            [tr(lang, "Telegram", "Telegram"), tr(lang, "Full name", "ФИО"), tr(lang, "Joined", "С"),
             tr(lang, "Code", "Код")],
            [[self.who(u), u["student"] or "—", self.when(u["joined_at"]), u["note"] or "—"] for u in users])
        buttons = [[(tr(lang, "Delete ", "Удалить ") + u["tg_name"], f"du:{u['tg_id']}")] for u in users[:30]]
        return doc, keyboard(buttons)

    def delete_view(self, tg_id: int, lang: str) -> tuple[Doc, dict | None]:
        user = self.access.user(tg_id)
        if user is None:
            return self.users_view(lang)
        doc = Doc().p(tr(lang, "Delete ", "Удалить "), self.who(user), "? ",
                      tr(lang, "Everything the bot keeps about them is erased; they'll need a new code to come back.",
                         "Всё, что бот о нём хранит, сотрётся; вернуться можно будет только с новым кодом."))
        return doc, keyboard([[(tr(lang, "Yes, delete", "Да, удалить"), f"dy:{tg_id}"),
                               (tr(lang, "Cancel", "Отмена"), "users")]])

    async def delete_user(self, tg_id: int, lang: str) -> str:
        user = self.access.user(tg_id)
        if user is None or not self.access.delete(tg_id):
            return tr(lang, "Already deleted", "Уже удалён")
        try:
            await self.api.send(tg_id, tr(user["lang"], "The owner closed your access to the bot.",
                                          "Владелец закрыл тебе доступ к боту."))
        except TelegramError as e:
            log.info("telling a deleted user failed: %s", e)
        return tr(lang, "Deleted", "Удалён")

    @staticmethod
    def who(user: dict) -> Html:
        """«Name (@username)» of a user row."""
        return join(user["tg_name"], f" (@{user['username']})" if user.get("username") else "")

    async def tell_owner(self, doc: Doc, buttons: dict | None = None) -> None:
        try:
            await self.api.send(self.owner_id, doc, buttons)
        except TelegramError as e:
            log.warning("telling the owner failed: %s", e)

