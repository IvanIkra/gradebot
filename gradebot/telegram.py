"""A thin Bot API client on aiohttp: long polling and the few methods the bot needs. No bot library: rich messages
(Bot API 10.3+: buttons inside them came then) are new, and the bot needs only a handful of calls."""

import asyncio
import logging

import aiohttp

from .rich import Doc, inline_button

log = logging.getLogger(__name__)

POLL_TIMEOUT = 50   # seconds getUpdates waits for an update
RETRIES = 3         # 429 and 5xx answers are retried this many times


class TelegramError(Exception):
    def __init__(self, method: str, code: int, description: str):
        super().__init__(f"{method}: {code} {description}")
        self.code, self.description = code, description

    @property
    def blocked(self) -> bool:
        """The user blocked the bot or deleted their account."""
        return self.code == 403


class BotApi:
    def __init__(self, token: str, api_url: str = "https://api.telegram.org",
                 session: aiohttp.ClientSession | None = None):
        self.base = f"{api_url}/bot{token}"
        self._session, self._own = session, session is None
        self.rich = True    # turned off for good when the Bot API answers that it has no rich messages

    async def __aenter__(self) -> "BotApi":
        return self

    async def __aexit__(self, *exc) -> None:
        await self.close()

    async def close(self) -> None:
        if self._own and self._session is not None:
            await self._session.close()

    @property
    def session(self) -> aiohttp.ClientSession:
        if self._session is None:
            self._session = aiohttp.ClientSession(timeout=aiohttp.ClientTimeout(total=POLL_TIMEOUT + 20))
        return self._session

    async def call(self, method: str, **params):
        params = {k: v for k, v in params.items() if v is not None}
        for attempt in range(RETRIES + 1):
            async with self.session.post(f"{self.base}/{method}", json=params) as resp:
                try:
                    data = await resp.json(content_type=None)
                except ValueError:
                    data = {"ok": False, "error_code": resp.status, "description": (await resp.text())[:200]}
            if data.get("ok"):
                return data.get("result")
            code = data.get("error_code", resp.status)
            if attempt < RETRIES and (code == 429 or code >= 500):
                wait = (data.get("parameters") or {}).get("retry_after", 2 ** attempt)
                log.warning("%s: %s, retrying in %ss", method, code, wait)
                await asyncio.sleep(wait)
                continue
            raise TelegramError(method, code, data.get("description", ""))
        raise AssertionError("unreachable")

    # --- the methods --------------------------------------------------------------------------

    async def get_updates(self, offset: int | None, timeout: int = POLL_TIMEOUT) -> list[dict]:
        return await self.call("getUpdates", offset=offset, timeout=timeout,
                               allowed_updates=["message", "callback_query"])

    async def send(self, chat_id: int, doc: Doc | str, keyboard: dict | None = None, silent: bool = False) -> int:
        """A Doc as a rich message (classic HTML without rich messages); a str as a classic HTML message."""
        if isinstance(doc, Doc) and self.rich:
            try:
                msg = await self.call("sendRichMessage", chat_id=chat_id, rich_message={"html": doc.rich()},
                                      reply_markup=keyboard, disable_notification=silent or None)
                return msg["message_id"]
            except TelegramError as e:
                if not no_rich(e):
                    raise
                log.warning("the Bot API has no rich messages (%s): classic HTML from now on", e)
                self.rich = False
        text, markup = classic(doc, keyboard)
        msg = await self.call("sendMessage", chat_id=chat_id, text=text, parse_mode="HTML",
                              link_preview_options={"is_disabled": True}, reply_markup=markup,
                              disable_notification=silent or None)
        return msg["message_id"]

    async def edit(self, chat_id: int, message_id: int, doc: Doc | str, keyboard: dict | None = None) -> None:
        """Edit what `send` sent. An unchanged message is not an error."""
        if isinstance(doc, Doc) and self.rich:
            params = {"rich_message": {"html": doc.rich()}, "reply_markup": keyboard}
        else:
            text, markup = classic(doc, keyboard)
            params = {"text": text, "parse_mode": "HTML", "link_preview_options": {"is_disabled": True},
                      "reply_markup": markup}
        try:
            await self.call("editMessageText", chat_id=chat_id, message_id=message_id, **params)
        except TelegramError as e:
            if "message is not modified" not in e.description:
                raise

    async def answer(self, callback_id: str, text: str | None = None) -> None:
        await self.call("answerCallbackQuery", callback_query_id=callback_id, text=text)

    async def set_commands(self, commands: list[tuple[str, str]], chat_id: int | None = None,
                           lang: str | None = None) -> None:
        scope = {"type": "chat", "chat_id": chat_id} if chat_id else {"type": "default"}
        await self.call("setMyCommands", commands=[{"command": c, "description": d} for c, d in commands],
                        scope=scope, language_code=lang)


def classic(doc: Doc | str, keyboard: dict | None) -> tuple[str, dict | None]:
    """A classic message: the Doc's text, and its buttons under the keyboard given."""
    if not isinstance(doc, Doc):
        return doc, keyboard
    rows = [[inline_button(button) for button in row] for row in doc.button_rows()]
    if rows:
        keyboard = {"inline_keyboard": [*(keyboard or {}).get("inline_keyboard", []), *rows]}
    return doc.plain(), keyboard


def no_rich(error: TelegramError) -> bool:
    """The answer of a Bot API server that does not know sendRichMessage."""
    return error.code == 404 or "method not found" in error.description.lower()
