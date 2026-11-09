"""Every LLM call, a failed one too, with its tokens, cost and duration."""

import datetime as dt

from .. import clock
from ..store import Store

FIELDS = ("at", "task", "model", "ok", "error", "input_tokens", "output_tokens", "cache_write", "cache_read",
          "cost_usd", "duration_s")


class Journal:
    def __init__(self, store: Store):
        self.db = store.db

    def add(self, run: dict) -> None:
        with self.db:
            self.db.execute(f"insert into llm_runs({', '.join(FIELDS)}) values({', '.join('?' * len(FIELDS))})",
                            [run.get(f) for f in FIELDS])

    def runs(self, limit: int = 100) -> list[dict]:
        """Newest first."""
        rows = self.db.execute("select * from llm_runs order by id desc limit ?", (limit,))
        return [{**{f: row[f] for f in FIELDS}, "ok": bool(row["ok"])} for row in rows]

    def cost_since(self, since: dt.datetime) -> float:
        row = self.db.execute("select coalesce(sum(cost_usd), 0) cost from llm_runs where at >= ?",
                              (clock.utc_iso(since),)).fetchone()
        return row["cost"]

    def totals(self, since: dt.datetime) -> dict:
        """Calls, failed calls and cost since a moment: for the owner's /status."""
        row = self.db.execute("select count(*) runs, coalesce(sum(ok = 0), 0) failed, "
                              "coalesce(sum(cost_usd), 0) cost from llm_runs where at >= ?",
                              (clock.utc_iso(since),)).fetchone()
        return {"runs": row["runs"], "failed": row["failed"], "cost_usd": round(row["cost"], 6)}
