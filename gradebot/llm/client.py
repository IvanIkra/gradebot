"""Claude Haiku with JSON-schema structured output, behind the monthly limit and the journal.

The SDK is imported on first use, so tests and a bot with nothing to lay out do not pay for it.
"""

import json
import logging
import os
import time

from .. import clock
from .budget import Budget, cost_usd
from .journal import Journal

log = logging.getLogger(__name__)

MODEL = "claude-haiku-5-5"
TASKS = frozenset({"grades"})


class LLMError(RuntimeError):
    pass


class BadAnswer(LLMError):
    """The model answered, but not usably: a refusal, a cut answer, no text. The same input would fail again."""


class Haiku:
    def __init__(self, budget: Budget, journal: Journal, model: str = MODEL):
        self.budget, self.journal, self.model = budget, journal, model
        self._client = None

    @property
    def available(self) -> bool:
        return bool(os.environ.get("ANTHROPIC_API_KEY") or os.environ.get("ANTHROPIC_AUTH_TOKEN"))

    async def json(self, task: str, system: str, prompt: str, schema: dict, *, max_tokens: int = 4000) -> dict:
        """One request whose answer must match `schema`.

        BudgetExceeded when the limit does not let the call happen (nothing is spent or logged then), LLMError when
        the call failed (logged with whatever tokens it spent), BadAnswer when the failure is in the answer itself.
        """
        if task not in TASKS:
            raise ValueError(f"unknown LLM task {task!r}")
        if not self.available:
            raise LLMError("ANTHROPIC_API_KEY is not set")
        price = self.budget.price(self.model)
        if price is None:
            raise LLMError(f"no price for {self.model}")
        input_chars = len(system) + len(prompt) + len(json.dumps(schema))
        ticket = self.budget.reserve(self.budget.estimate(price, input_chars, max_tokens))
        started = time.monotonic()
        response = None
        try:
            response, text = await self._ask(system, prompt, schema, max_tokens)
            try:
                answer = json.loads(text)
            except ValueError as e:
                raise BadAnswer(f"the answer is not JSON: {e}") from e
        except Exception as e:
            response = getattr(e, "response", response)  # tokens are spent on a refusal or a cut answer too
            self._record(task, price, started, response, str(e) or type(e).__name__)
            raise
        else:
            self._record(task, price, started, response, None)
            return answer
        finally:
            self.budget.release(ticket)

    def _record(self, task: str, price: dict, started: float, response, error: str | None) -> None:
        usage = getattr(response, "usage", None)
        tokens = {name: getattr(usage, name, None) or 0 for name in (
            "input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens")}
        run = {"at": clock.utc_iso(clock.now_utc()), "task": task, "model": self.model, "ok": error is None,
               "error": error, "input_tokens": tokens["input_tokens"], "output_tokens": tokens["output_tokens"],
               "cache_write": tokens["cache_creation_input_tokens"], "cache_read": tokens["cache_read_input_tokens"],
               "cost_usd": cost_usd(price, tokens["input_tokens"], tokens["output_tokens"],
                                    tokens["cache_creation_input_tokens"], tokens["cache_read_input_tokens"]),
               "duration_s": round(time.monotonic() - started, 2)}
        try:
            self.journal.add(run)
        except Exception:
            log.exception("cannot record an LLM run")

    def _get_client(self):
        if self._client is None:
            import anthropic

            self._client = anthropic.AsyncAnthropic(timeout=120.0, max_retries=3)
        return self._client

    async def _ask(self, system: str, prompt: str, schema: dict, max_tokens: int):
        import anthropic

        try:
            response = await self._get_client().messages.create(
                model=self.model,
                max_tokens=max_tokens,
                system=system,
                messages=[{"role": "user", "content": prompt}],
                output_config={"effort": "low", "format": {"type": "json_schema", "schema": schema}},
            )
        except anthropic.AuthenticationError as e:
            raise LLMError("Anthropic API key was rejected") from e
        except anthropic.RateLimitError as e:
            raise LLMError("Anthropic rate limit") from e
        except anthropic.APIStatusError as e:
            raise LLMError(f"Anthropic API error {e.status_code}: {e.message}") from e
        except anthropic.APIConnectionError as e:
            raise LLMError(f"cannot reach the Anthropic API: {e}") from e

        log.info("haiku: %d in / %d out tokens", response.usage.input_tokens, response.usage.output_tokens)
        problem = None
        if response.stop_reason == "refusal":
            problem = f"request declined ({getattr(response.stop_details, 'category', None)})"
        elif response.stop_reason == "max_tokens":
            problem = "answer was cut at max_tokens"
        text = next((b.text for b in response.content if b.type == "text"), None)
        if problem is None and text is None:
            problem = "no text in the answer"
        if problem:
            error = BadAnswer(problem)
            error.response = response
            raise error
        return response, text

