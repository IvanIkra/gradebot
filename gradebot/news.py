"""The owner's news: what users did that needs no answer (came in, set a name, changed groups, left, blocked the bot)
gathers in one silent message, edited as news comes, instead of a message with a sound each.

The digest stays open until the owner has surely seen the chat: they write to the bot or press a button, or a message
that does need them (a request, a complaint, a failing sheet) goes out under it. The next news starts a new digest
below. A digest that would not fit a message, or whose message is gone, is closed too.

A button pressed in a digest opens its answer in a new message, so the digest stays as it was.
"""

import asyncio
import logging

from . import clock
from .i18n import tr
from .rich import Button, Doc, Html, i, join
from .telegram import TelegramError

log = logging.getLogger(__name__)

NEWS = "owner.news"         # kv: {"message_id", "entries": [{"text", "buttons"}]}: the open digest, or null
DIGESTS = "owner.digests"   # kv: the ids of the last digest messages, whose buttons answer in a new message
KEEP_DIGESTS = 50


class OwnerNews:
    @property
    def _news_lock(self) -> asyncio.Lock:
        if not hasattr(self, "_news_lock_"):
            self._news_lock_ = asyncio.Lock()
        return self._news_lock_

    async def news(self, *parts: str, buttons: tuple[Button, ...] = ()) -> None:
        """One line of news for the owner, with its buttons: into the open digest, or a new silent one."""
        entry = {"text": join(i(self.when(clock.utc_iso(self.now()))), " · ", *parts), "buttons": list(buttons)}
        async with self._news_lock:
            state = self.store.get_json(NEWS)
            if state is not None:
                doc = self.digest([*state["entries"], entry])
                if doc.fits():
                    try:
                        await self.api.edit(self.owner_id, state["message_id"], doc)
                        state["entries"].append(entry)
                        self.store.put_json(NEWS, state)
                        return
                    except TelegramError as e:  # deleted by the owner, or too old to edit
                        log.info("the owner's digest cannot be edited: %s", e)
            try:
                message_id = await self.api.send(self.owner_id, self.digest([entry]), silent=True)
            except TelegramError as e:
                log.warning("telling the owner failed: %s", e)
                return
            self.store.put_json(NEWS, {"message_id": message_id, "entries": [entry]})
            self.store.put_json(DIGESTS, [*self.store.get_json(DIGESTS, []), message_id][-KEEP_DIGESTS:])

    def digest(self, entries: list[dict]) -> Doc:
        lang = self.owner_lang
        doc = Doc().h(tr(lang, "News", "Новости") + (f" · {len(entries)}" if len(entries) > 1 else ""))
        for entry in entries:
            doc.p(Html(entry["text"])).buttons(*(tuple(button) for button in entry["buttons"]))
        return doc

    def close_news(self) -> None:
        """The owner has seen the chat, or a message that needs them goes under the digest: news starts anew."""
        if self.store.get_json(NEWS) is not None:
            self.store.put_json(NEWS, None)

    def is_digest(self, message_id: int) -> bool:
        return message_id in self.store.get_json(DIGESTS, [])
