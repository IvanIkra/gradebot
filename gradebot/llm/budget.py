"""One limit in dollars a month for every LLM call. Months are local months (TZ).

Before a call the worst case is reserved: the input estimated from its length plus max_tokens of output. A call
whose reserve does not fit what is left is not made: new tabs wait for the next month, layouts already cached keep
working. The owner is told once a month when the spending crosses WARN_SHARE of the limit, and once when a call is
refused.
"""

import datetime as dt
import enum
import itertools
import math
from collections.abc import Callable

from .. import clock
from ..store import Store
from .journal import Journal

LONG_PROMPT = 100_000           # tokens; longer prompts are billed at the long rates
CACHE_WRITE, CACHE_READ = 1.25, 0.1  # times the input price
CHARS_PER_TOKEN = 2             # pessimistic for Russian text: the reserve must not come out below the real cost
WARN_SHARE = 0.8
NOTIFIED = "llm.notified"       # kv: {"month": "2026-11", "levels": [...]}, what the owner was told this month
# $ per million tokens.
PRICES = {"claude-haiku-5-5": {"input": 0.10, "output": 0.50, "long_input": 0.50, "long_output": 2.50}}


class Level(enum.StrEnum):
    WARN = "warn"               # WARN_SHARE of the limit is spent
    STOP = "stop"               # a call was refused: no new layouts until the next month


class BudgetExceeded(RuntimeError):
    """The call was not made; the caller tries again later."""


def cost_usd(price: dict, input_tokens: int, output_tokens: int, cache_write: int = 0, cache_read: int = 0) -> float:
    long = input_tokens + cache_write + cache_read > LONG_PROMPT
    price_in, price_out = (price["long_input"], price["long_output"]) if long else (price["input"], price["output"])
    total = (input_tokens + cache_write * CACHE_WRITE + cache_read * CACHE_READ) * price_in + output_tokens * price_out
    return total / 1_000_000


class Budget:
    def __init__(self, journal: Journal, store: Store, monthly_usd: float, zone: dt.tzinfo,
                 now: Callable[[], dt.datetime] = clock.now_utc):
        self.journal, self.store, self.limit, self.zone, self.now = journal, store, monthly_usd, zone, now
        self.notify: Callable[[Level, float, float], None] | None = None  # (level, spent, limit), once a month each
        self._reserved: dict[int, float] = {}
        self._tickets = itertools.count(1)

    @staticmethod
    def price(model: str) -> dict | None:
        return PRICES.get(model)

    @staticmethod
    def estimate(price: dict, input_chars: int, max_tokens: int) -> float:
        return cost_usd(price, math.ceil(input_chars / CHARS_PER_TOKEN), max_tokens)

    def spent(self) -> float:
        """This month's real cost plus what calls in flight have reserved."""
        return self.journal.cost_since(clock.month_start(self.now(), self.zone)) + sum(self._reserved.values())

    def reserve(self, cost: float) -> int:
        """A ticket for release(), or BudgetExceeded."""
        if self.spent() + cost > self.limit:
            self._announce(Level.STOP)
            raise BudgetExceeded("the monthly LLM limit is spent, new layouts wait for the next month")
        ticket = next(self._tickets)
        self._reserved[ticket] = cost
        return ticket

    def release(self, ticket: int) -> None:
        """After the call is in the journal: the real cost replaces the reserve."""
        self._reserved.pop(ticket, None)
        if self.spent() >= WARN_SHARE * self.limit:
            self._announce(Level.WARN)

    def status(self) -> dict:
        return {"spent_usd": round(self.spent(), 4), "limit_usd": self.limit,
                "stopped": Level.STOP in self._told()}

    def _told(self) -> list[str]:
        saved = self.store.get_json(NOTIFIED)
        return saved["levels"] if saved and saved["month"] == clock.month(self.now(), self.zone) else []

    def _announce(self, level: Level) -> None:
        told = self._told()
        if level in told:
            return
        self.store.put_json(NOTIFIED, {"month": clock.month(self.now(), self.zone), "levels": [*told, level.value]})
        if self.notify is not None:
            self.notify(level, self.spent(), self.limit)
