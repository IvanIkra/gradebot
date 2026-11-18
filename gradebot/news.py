"""The owner's news: what users did that needs no answer (came in, set a name, changed groups, left, blocked the bot)
gathers in one silent message, edited as news comes, instead of a message with a sound each.

The digest stays open until the owner has surely seen the chat: they write to the bot or press a button, or a message
that does need them (a request, a complaint, a failing sheet) goes out under it. The next news starts a new digest
below. A digest that would not fit a message, or whose message is gone, is closed too.

A button pressed in a digest opens its answer in a new message, so the digest stays as it was.

A group's admins get the news of their group: who came in, set a name, joined or left the group, left the bot or
blocked it, and whose access ends. Each admin picks how (`/news`): a silent digest of their own like the owner's (the
default), a message with a sound for each piece, or none.
"""

import asyncio
import logging
from collections.abc import Callable

from . import clock
from .i18n import tr
from .rich import Button, Doc, Html, i, join
from .telegram import TelegramError

log = logging.getLogger(__name__)

NEWS = "owner.news"         # kv: {"message_id", "entries": [{"text", "buttons"}]}: the open digest, or null
DIGESTS = "owner.digests"   # kv: the ids of the last digest messages, whose buttons answer in a new message
KEEP_DIGESTS = 50
DIGEST, EACH, OFF = "digest", "each", "off"  # how an admin gets the news of their group


class OwnerNews:
    @property
    def _news_lock(self) -> asyncio.Lock:
        if not hasattr(self, "_news_lock_"):
            self._news_lock_ = asyncio.Lock()
        return self._news_lock_

    async def news(self, *parts: str, buttons: tuple[Button, ...] = ()) -> None:
        """One line of news for the owner, with its buttons: into the open digest, or a new silent one."""
        lang = self.owner_lang
        await self._news(self.owner_id, lang, NEWS, self._entry(lang, parts, buttons), tr(lang, "News", "Новости"))

    async def group_news(self, gid: int | None, about: int | None, parts: Callable[[str], list]) -> None:
        """One line of news for each admin of the group but the one it is `about`: `parts` in their language."""
        group = self.groups.get(gid)
        if group is None:
            return
        for admin in self.access.admins(gid):
            mode = self.store.get_json(news_mode(admin["tg_id"]), DIGEST)
            if admin["tg_id"] == about or mode == OFF:
                continue
            lang = admin["lang"]
            title = tr(lang, "Group ", "Группа ") + group["name"]
            entry = self._entry(lang, parts(lang), ((tr(lang, "Set up", "Настроить"), "nm"),))
            if mode == EACH:
                try:
                    await self.api.send(admin["tg_id"], self.digest([entry], lang, title))
                except TelegramError as e:
                    log.info("sending news to %s failed: %s", admin["tg_id"], e)
            else:
                await self._news(admin["tg_id"], lang, admin_news(admin["tg_id"]), entry, title)

    def _entry(self, lang: str, parts, buttons: tuple[Button, ...] = ()) -> dict:
        moment = self.when(clock.utc_iso(self.now()), lang)
        return {"text": join(i(moment), " · ", *parts), "buttons": list(buttons)}

    async def _news(self, chat: int, lang: str, key: str, entry: dict, title: str) -> None:
        async with self._news_lock:
            state = self.store.get_json(key)
            if state is not None:
                doc = self.digest([*state["entries"], entry], lang, title)
                if doc.fits():
                    try:
                        await self.api.edit(chat, state["message_id"], doc)
                        state["entries"].append(entry)
                        self.store.put_json(key, state)
                        return
                    except TelegramError as e:  # deleted by the reader, or too old to edit
                        log.info("a digest of news cannot be edited: %s", e)
            try:
                message_id = await self.api.send(chat, self.digest([entry], lang, title), silent=True)
            except TelegramError as e:
                log.warning("sending news to %s failed: %s", chat, e)
                return
            self.store.put_json(key, {"message_id": message_id, "entries": [entry]})
            if key == NEWS:
                self.store.put_json(DIGESTS, [*self.store.get_json(DIGESTS, []), message_id][-KEEP_DIGESTS:])

    @staticmethod
    def digest(entries: list[dict], lang: str, title: str) -> Doc:
        """The entries with their buttons; a button the same in every entry (an admin's «Set up») once, at the end."""
        doc = Doc().h(title + (f" · {len(entries)}" if len(entries) > 1 else ""))
        rows = [[tuple(b) for b in entry["buttons"]] for entry in entries]  # stored ones come back as lists
        common = [b for b in rows[0] if all(b in row for row in rows)] if rows else []
        for entry, row in zip(entries, rows, strict=True):
            doc.p(Html(entry["text"])).buttons(*(b for b in row if b not in common))
        return doc.buttons(*common)

    def close_news(self, key: str = NEWS) -> None:
        """The reader has seen the chat, or a message that needs them goes under the digest: news starts anew."""
        if self.store.get_json(key) is not None:
            self.store.put_json(key, None)

    def is_digest(self, message_id: int) -> bool:
        return message_id in self.store.get_json(DIGESTS, [])


def admin_news(tg_id: int) -> str:
    """kv: the open digest of a group admin."""
    return f"news.admin.{tg_id}"


def news_mode(tg_id: int) -> str:
    """kv: how a group admin gets the news of their group: DIGEST, EACH or OFF."""
    return f"news.mode.{tg_id}"
