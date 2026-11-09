"""Commands asked for step by step. A command from the menu comes without arguments, so the bot asks for each one it
still needs in a message of its own: buttons for the usual answers, `Skip` for an argument that may stay empty,
`Cancel` always; the next plain message, or a button, is the answer. A command typed with all its arguments runs at
once, and any command drops the question that was waiting.

A flow is a list of steps and what to do with the answers. Each answer is checked when it comes: a wrong one is asked
again with the reason. Questions live in memory: a restart forgets them, as does an hour without an answer.

Callbacks: `an:<value>` answers the question of the message it is under (empty: skipped), `ac` cancels it.
"""

import datetime as dt
from collections.abc import Awaitable, Callable
from dataclasses import dataclass, field

from .i18n import tr
from .rich import Doc, code
from .sheets import parse_url

ASK_FOR = dt.timedelta(hours=1)
ROW = 4         # answer buttons in a row


@dataclass
class Step:
    name: str                                           # the key of the answer in the flow's data
    prompt: Callable[[dict], Doc]                       # the question, given the answers so far
    check: Callable[[str, dict], str] = lambda value, data: value  # the answer as kept; ValueError asks again
    error: Callable[[str, Exception], str] = lambda lang, e: str(e)  # why an answer was wrong, in the language
    choices: Callable[[dict], list[tuple[str, str]]] = lambda data: []  # (button text, the answer it gives)
    skip: Callable[[dict], str | None] = lambda data: None  # the text of the button that leaves it empty
    when: Callable[[dict], bool] = lambda data: True    # asked only when this holds
    cancel: bool = True                                 # False for a view that takes a typed answer too


@dataclass
class Ask:
    chat: int
    lang: str
    steps: list[Step]
    done: Callable[[dict], Awaitable[None]]
    data: dict = field(default_factory=dict)
    step: str = ""
    message_id: int | None = None
    at: dt.datetime | None = None


class Dialogs:
    @property
    def asks(self) -> dict[int, Ask]:
        """chat -> the question the bot waits an answer to."""
        if not hasattr(self, "_asks"):
            self._asks = {}
        return self._asks

    async def flow(self, chat: int, lang: str, steps: list[Step], done: Callable[[dict], Awaitable[None]],
                   data: dict | None = None) -> None:
        """Ask for the first answer missing from `data`, or run `done` with all of them. An empty answer is a
        skipped one."""
        await self._go(Ask(chat, lang, steps, done, dict(data or {})))

    async def _go(self, ask: Ask, error: str = "") -> None:
        data = ask.data
        for step in ask.steps:
            if not step.when(data):
                continue
            if step.name in data:
                if data[step.name] == "":
                    continue
                try:
                    data[step.name] = step.check(data[step.name], data)
                    continue
                except ValueError as e:
                    error = step.error(ask.lang, e)
                    del data[step.name]
            await self._ask(ask, step, error)
            return
        self.asks.pop(ask.chat, None)
        await ask.done(data)

    async def _ask(self, ask: Ask, step: Step, error: str) -> None:
        doc = Doc()
        if error:
            doc.p(error)
        doc.extend(step.prompt(ask.data))
        choices = [(text, f"an:{value}") for text, value in step.choices(ask.data)]
        for start in range(0, len(choices), ROW):
            doc.buttons(*choices[start:start + ROW])
        skip = step.skip(ask.data)
        doc.buttons(*([(skip, "an:")] if skip else []),
                    *([(tr(ask.lang, "Cancel", "Отмена"), "ac")] if step.cancel else []))
        ask.step, ask.at = step.name, self.now()
        ask.message_id = await self.api.send(ask.chat, doc)
        self.asks[ask.chat] = ask

    def waiting(self, chat: int) -> Ask | None:
        ask = self.asks.get(chat)
        if ask is not None and self.now() - ask.at > ASK_FOR:
            del self.asks[chat]
            return None
        return ask

    async def answer(self, chat: int, value: str) -> bool:
        """A plain message to a waiting question: False when nothing waits."""
        ask = self.waiting(chat)
        if ask is None:
            return False
        ask.data[ask.step] = value.strip()
        await self._go(ask)
        return True

    async def dialog_callback(self, chat: int, message_id: int, kind: str, value: str, lang: str) -> str | None:
        """`an` and `ac` under a question; a button of an older question is told so."""
        ask = self.waiting(chat)
        if ask is None or ask.message_id != message_id:
            return tr(lang, "This question is closed", "Этот вопрос уже закрыт")
        if kind == "ac":
            del self.asks[chat]
            await self.api.edit(chat, message_id, tr(lang, "Cancelled.", "Отменено."))
            return None
        ask.data[ask.step] = value
        await self._go(ask)
        return None


def asks_hint(lang: str) -> str:
    """The line under the list of commands."""
    return tr(lang, "A command sent without its arguments asks for them one by one.",
              "Команда без аргументов спросит их по одному.")


def link_step(lang: str, name: str = "link") -> Step:
    """A Google Sheets link, kept as the link up to the sheet's id."""
    return Step(name, lambda data: Doc().p(tr(lang, "Send the sheet's link: ", "Пришли ссылку на таблицу: "),
                                           code("https://docs.google.com/spreadsheets/d/…")),
                check=lambda value, data: parse_url(value)[1],
                error=lambda lang, e: tr(lang, "That's not a Google Sheets link.", "Это не ссылка на таблицу Google."))
