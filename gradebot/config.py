"""Secrets and limits from the environment. Everything users set lives in the database, not here."""

import os
from collections.abc import Mapping
from dataclasses import dataclass, field
from pathlib import Path
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError


class ConfigError(ValueError):
    pass


def load_env(path: Path) -> None:
    """KEY=VALUE lines from .env; real environment variables win."""
    if not path.exists():
        return
    for line in path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if line and not line.startswith("#") and "=" in line:
            key, value = line.split("=", 1)
            os.environ.setdefault(key.strip(), value.strip().strip("'\""))


@dataclass(frozen=True)
class Env:
    bot_token: str = field(repr=False)
    owner_id: int
    invite_days: int = 7            # how long an invite code works
    tz: str = "Europe/Moscow"       # quiet hours and the dates the bot shows
    api_url: str = "https://api.telegram.org"  # a local Bot API server may stand in
    llm_monthly_usd: float = 1.0    # Haiku lays out the tabs within this much a month

    @property
    def zone(self) -> ZoneInfo:
        return ZoneInfo(self.tz)

    @classmethod
    def from_environ(cls, environ: Mapping[str, str] = os.environ) -> "Env":
        def get(var: str) -> str:
            return environ.get(var, "").strip()

        missing = [var for var in ("BOT_TOKEN", "OWNER_ID") if not get(var)]
        if missing:
            raise ConfigError(f"not set in the environment or .env: {', '.join(missing)}")
        values: dict = {"bot_token": get("BOT_TOKEN")}
        for name, var in (("owner_id", "OWNER_ID"), ("invite_days", "INVITE_DAYS")):
            if raw := get(var):
                try:
                    values[name] = int(raw)
                except ValueError:
                    raise ConfigError(f"{var} must be an integer, got {raw!r}") from None
        if not 1 <= values.get("invite_days", 7) <= 90:
            raise ConfigError("INVITE_DAYS must be from 1 to 90")
        if tz := get("TZ"):
            try:
                ZoneInfo(tz)
            except (ZoneInfoNotFoundError, ValueError):
                raise ConfigError(f"TZ is not a time zone: {tz!r}") from None
            values["tz"] = tz
        if raw := get("LLM_MONTHLY_USD"):
            try:
                values["llm_monthly_usd"] = float(raw)
            except ValueError:
                raise ConfigError(f"LLM_MONTHLY_USD must be a number, got {raw!r}") from None
            if not 0 <= values["llm_monthly_usd"] <= 100:
                raise ConfigError("LLM_MONTHLY_USD must be from 0 to 100")
        if api_url := get("BOT_API_URL"):
            if not api_url.startswith(("https://", "http://")):
                raise ConfigError("BOT_API_URL must be an http(s):// address")
            values["api_url"] = api_url.rstrip("/")
        return cls(**values)
