from types import SimpleNamespace

import pytest

from gradebot.llm.budget import Budget, BudgetExceeded, Level
from gradebot.llm.client import BadAnswer, Haiku, LLMError
from gradebot.llm.journal import Journal

from .conftest import ZONE

SCHEMA = {"type": "object", "properties": {"x": {"type": "integer"}}, "required": ["x"]}


def answer(text: str, stop_reason: str = "end_turn", output_tokens: int = 100):
    usage = SimpleNamespace(input_tokens=1000, output_tokens=output_tokens, cache_creation_input_tokens=0,
                            cache_read_input_tokens=0)
    return SimpleNamespace(usage=usage, stop_reason=stop_reason, stop_details=None,
                           content=[SimpleNamespace(type="text", text=text)])


class FakeMessages:
    def __init__(self, response):
        self.response, self.calls = response, []

    async def create(self, **kwargs):
        self.calls.append(kwargs)
        return self.response


@pytest.fixture
def journal(store):
    return Journal(store)


@pytest.fixture
def budget(journal, store, clock):
    return Budget(journal, store, 1.0, ZONE, now=clock)


@pytest.fixture
def haiku(budget, journal, monkeypatch):
    monkeypatch.setenv("ANTHROPIC_API_KEY", "test")

    def make(response):
        llm = Haiku(budget, journal)
        llm.messages = FakeMessages(response)
        llm._client = SimpleNamespace(messages=llm.messages)
        return llm

    return make


async def test_a_call_is_logged_and_its_reserve_released(haiku, budget, journal):
    llm = haiku(answer('{"x": 1}'))
    assert await llm.json("grades", "sys", "data", SCHEMA) == {"x": 1}
    call = llm.messages.calls[0]
    assert call["model"] == "claude-haiku-5-5"
    assert call["output_config"]["format"] == {"type": "json_schema", "schema": SCHEMA}
    run = journal.runs()[0]
    assert run["ok"] and run["task"] == "grades"
    assert run["cost_usd"] == pytest.approx((1000 * 0.10 + 100 * 0.50) / 1e6)
    assert budget.spent() == pytest.approx(run["cost_usd"])


async def test_a_cut_answer_is_a_bad_answer_and_is_logged(haiku, journal):
    with pytest.raises(BadAnswer, match="max_tokens"):
        await haiku(answer('{"x": ', stop_reason="max_tokens", output_tokens=4000)).json("grades", "s", "d", SCHEMA)
    assert not journal.runs()[0]["ok"]


async def test_no_key_and_unknown_task(haiku, monkeypatch):
    with pytest.raises(ValueError):
        await haiku(answer("{}")).json("news", "s", "d", SCHEMA)
    monkeypatch.delenv("ANTHROPIC_API_KEY")
    monkeypatch.delenv("ANTHROPIC_AUTH_TOKEN", raising=False)
    with pytest.raises(LLMError, match="not set"):
        await haiku(answer("{}")).json("grades", "s", "d", SCHEMA)


def run(cost: float, at: str) -> dict:
    return {"at": at, "task": "grades", "model": "m", "ok": True, "input_tokens": 0, "output_tokens": 0,
            "cache_write": 0, "cache_read": 0, "cost_usd": cost, "duration_s": 1}


def test_the_limit_is_monthly_and_told_once(budget, journal, clock):
    told = []
    budget.notify = lambda level, spent, limit: told.append(level)
    journal.add(run(0.5, "2026-10-31T22:00:00+00:00"))  # 1 November in Moscow: this month
    journal.add(run(5.0, "2026-10-31T20:00:00+00:00"))  # October in Moscow
    assert budget.spent() == pytest.approx(0.5)
    budget.release(budget.reserve(0.1))
    assert told == []
    journal.add(run(0.35, "2026-11-09T10:00:00+00:00"))  # 0.85 of 1 now
    budget.release(budget.reserve(0.01))
    budget.release(budget.reserve(0.01))
    assert told == [Level.WARN]
    with pytest.raises(BudgetExceeded):
        budget.reserve(0.2)
    with pytest.raises(BudgetExceeded):
        budget.reserve(0.2)
    assert told == [Level.WARN, Level.STOP] and budget.status()["stopped"]

    clock.advance(days=25)  # December: a new month
    assert budget.spent() == 0 and not budget.status()["stopped"]
    budget.release(budget.reserve(0.3))


async def test_a_refused_call_is_not_made(haiku, budget, journal):
    budget.limit = 0.000001
    llm = haiku(answer('{"x": 1}'))
    with pytest.raises(BudgetExceeded):
        await llm.json("grades", "sys", "data", SCHEMA)
    assert llm.messages.calls == [] and journal.runs() == []
