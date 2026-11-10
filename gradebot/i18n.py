"""Languages: every user reads the bot in English or Russian; the first guess comes from their Telegram client."""

LANGUAGES = ("en", "ru")


def tr(lang: str, en: str, ru: str) -> str:
    return ru if lang == "ru" else en


def guess(language_code: str | None) -> str:
    """Russian for a Russian-speaking client (ru, uk, be, kk), English otherwise."""
    return "ru" if (language_code or "").split("-")[0] in ("ru", "uk", "be", "kk") else "en"


def days(lang: str, n: int) -> str:
    """'1 day', '3 days'; '1 день', '3 дня', '7 дней'."""
    if lang != "ru":
        return f"{n} day" if n == 1 else f"{n} days"
    if n % 10 == 1 and n % 100 != 11:
        word = "день"
    elif n % 10 in (2, 3, 4) and n % 100 not in (12, 13, 14):
        word = "дня"
    else:
        word = "дней"
    return f"{n} {word}"
