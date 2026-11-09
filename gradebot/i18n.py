"""Languages: every user reads the bot in English or Russian; the first guess comes from their Telegram client."""

LANGUAGES = ("en", "ru")


def tr(lang: str, en: str, ru: str) -> str:
    return ru if lang == "ru" else en


def guess(language_code: str | None) -> str:
    """Russian for a Russian-speaking client (ru, uk, be, kk), English otherwise."""
    return "ru" if (language_code or "").split("-")[0] in ("ru", "uk", "be", "kk") else "en"
