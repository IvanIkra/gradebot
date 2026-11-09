import datetime as dt
import re
from zoneinfo import ZoneInfo

import pytest

from gradebot.access import Access
from gradebot.bot import Bot
from gradebot.sheets import Sheets
from gradebot.store import Store
from gradebot.telegram import BotApi, TelegramError

OWNER = 1000
ZONE = ZoneInfo("Europe/Moscow")


class Clock:
    def __init__(self):
        self.moment = dt.datetime(2026, 11, 9, 12, 0, tzinfo=dt.UTC)

    def __call__(self) -> dt.datetime:
        return self.moment

    def advance(self, **kwargs) -> None:
        self.moment += dt.timedelta(**kwargs)


class FakeApi(BotApi):
    """The real BotApi with `call` recording requests instead of going to Telegram."""

    def __init__(self, rich: bool = True):
        super().__init__("TOKEN", "http://bot.invalid")
        self.calls: list[tuple[str, dict]] = []
        self.fail: dict[str, TelegramError] = {}   # method -> the error it raises
        self.server_rich = rich
        self._next_id = 100

    async def call(self, method: str, **params):
        params = {k: v for k, v in params.items() if v is not None}
        self.calls.append((method, params))
        if method == "sendRichMessage" and not self.server_rich:
            raise TelegramError(method, 404, "Not Found")
        if method in self.fail:
            raise self.fail[method]
        if method.startswith("send"):
            self._next_id += 1
            return {"message_id": self._next_id}
        return True

    def sent(self, chat_id: int | None = None) -> list[dict]:
        """Sent and edited messages, oldest first: {chat_id, text, keyboard, method}."""
        out = []
        for method, params in self.calls:
            if method in ("sendRichMessage", "sendMessage", "editMessageText"):
                text = params["rich_message"]["html"] if "rich_message" in params else params.get("text", "")
                if chat_id is None or params["chat_id"] == chat_id:
                    out.append({"chat_id": params["chat_id"], "text": text, "method": method,
                                "keyboard": params.get("reply_markup")})
        return out

    def last(self, chat_id: int) -> dict:
        return self.sent(chat_id)[-1]


def buttons(message: dict) -> dict[str, str]:
    """text -> callback_data of a message's keyboard."""
    rows = (message["keyboard"] or {}).get("inline_keyboard", [])
    return {button["text"]: button["callback_data"] for row in rows for button in row}


def invite_code(text: str) -> str:
    return re.search(r"<code>([A-Z0-9]{4}-[A-Z0-9]{4})</code>", text)[1]


def message(sender: int, text: str, lang: str = "ru", first: str = "Пётр", username: str | None = "petr_example",
            chat_type: str = "private") -> dict:
    who = {"id": sender, "first_name": first, "language_code": lang, **({"username": username} if username else {})}
    return {"update_id": 1, "message": {"message_id": 1, "from": who, "chat": {"id": sender, "type": chat_type},
                                        "text": text}}


def callback(sender: int, data: str, message_id: int = 555) -> dict:
    return {"update_id": 1, "callback_query": {"id": "cb1", "from": {"id": sender, "first_name": "X"}, "data": data,
                                               "message": {"message_id": message_id, "chat": {"id": sender}}}}


@pytest.fixture
def clock() -> Clock:
    return Clock()


@pytest.fixture
def store():
    s = Store(":memory:")
    yield s
    s.close()


@pytest.fixture
def access(store, clock) -> Access:
    return Access(store, invite_days=7, now=clock)


@pytest.fixture
def api() -> FakeApi:
    return FakeApi()


@pytest.fixture
def sheets(store, clock) -> Sheets:
    return Sheets(store, now=clock)


@pytest.fixture
def bot(api, store, access, sheets, clock) -> Bot:
    return Bot(api, store, access, sheets, OWNER, ZONE, username="gradebot_test_bot", now=clock)
