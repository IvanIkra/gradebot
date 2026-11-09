"""The service: the database, the Bot API client, the long-polling loop and the reading of the sheets."""

import asyncio
import logging
from pathlib import Path

import aiohttp

from .access import Access
from .bot import Bot
from .config import Env
from .layout import Layouts
from .llm.budget import Budget
from .llm.client import Haiku
from .llm.journal import Journal
from .poller import Poller
from .sheets import Sheets
from .store import Store
from .telegram import BotApi, TelegramError

log = logging.getLogger(__name__)

OFFSET = "updates.offset"   # kv: the next update to ask for, so a restart neither loses nor repeats updates

USER_COMMANDS = {
    "en": [("grades", "your grades"), ("sheets", "your sheets"), ("sheet", "follow a Google Sheet"),
           ("name", "your full name"),
           ("quiet", "quiet hours"), ("lang", "language"), ("help", "commands"), ("stop", "erase your data and leave")],
    "ru": [("grades", "твои оценки"), ("sheets", "твои таблицы"), ("sheet", "подписаться на таблицу"),
           ("name", "ФИО"),
           ("quiet", "тихие часы"), ("lang", "язык"), ("help", "команды"), ("stop", "удалить свои данные и уйти")],
}
OWNER_COMMANDS = {
    "en": [("invite", "a one-time invite code"), ("codes", "unused codes"), ("users", "users"),
           ("groupsheet", "the group's sheets"), ("course", "a sheet's course"),
           ("relayout", "lay a sheet out again"), ("status", "sheets, users, Haiku"),
           ("interval", "how often the sheets are read"), ("help", "commands")],
    "ru": [("invite", "одноразовый код приглашения"), ("codes", "неиспользованные коды"), ("users", "пользователи"),
           ("groupsheet", "общие таблицы"), ("course", "курс таблицы"), ("relayout", "разметить таблицу заново"),
           ("status", "таблицы, пользователи, Haiku"), ("interval", "как часто читать таблицы"),
           ("help", "команды")],
}


class Service:
    def __init__(self, env: Env, data: Path):
        data.mkdir(parents=True, exist_ok=True)
        self.env = env
        self.store = Store(data / "gradebot.db")
        self.access = Access(self.store, env.invite_days)
        self.sheets = Sheets(self.store)
        journal = Journal(self.store)
        self.budget = Budget(journal, self.store, env.llm_monthly_usd, env.zone)
        self.layouts = Layouts(self.store, Haiku(self.budget, journal))

    async def run(self) -> None:
        async with BotApi(self.env.bot_token, self.env.api_url) as api:
            me = await api.call("getMe")
            log.info("running as @%s", me.get("username"))
            bot = Bot(api, self.store, self.access, self.sheets, self.env.owner_id, self.env.zone,
                      me.get("username") or "")
            poller = Poller(self.store, self.sheets, bot.grades, self.layouts)
            bot.poller, bot.budget = poller, self.budget
            poller.on_read, self.budget.notify = bot.after_read, bot.budget_notice
            await self.set_commands(api)
            await asyncio.gather(poll(api, bot, self.store), poller.run())

    async def set_commands(self, api: BotApi) -> None:
        for lang in ("en", "ru"):
            code = None if lang == "en" else lang
            await api.set_commands(USER_COMMANDS[lang], lang=code)
            try:
                await api.set_commands(OWNER_COMMANDS[lang], chat_id=self.env.owner_id, lang=code)
            except TelegramError as e:  # the owner has not opened the bot yet
                log.warning("owner's commands not set: %s", e)


async def poll(api: BotApi, bot: Bot, store: Store, once: bool = False) -> None:
    """getUpdates forever; an update that fails is logged and skipped, not retried for ever."""
    offset = store.get_json(OFFSET)
    while True:
        try:
            updates = await api.get_updates(offset)
        except TelegramError as e:
            wait = 30 if e.code in (401, 409) else 5  # a wrong token, or a webhook or another copy of the bot
            log.error("getUpdates: %s; again in %ss", e, wait)
            await asyncio.sleep(wait)
            continue
        except (aiohttp.ClientError, TimeoutError) as e:
            log.warning("getUpdates: %r; again in 5s", e)
            await asyncio.sleep(5)
            continue
        for update in updates:
            try:
                await bot.handle(update)
            except Exception:
                log.exception("update %s failed", update.get("update_id"))
            offset = update["update_id"] + 1
            store.put_json(OFFSET, offset)
        if once:
            return
