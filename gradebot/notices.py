"""What the bot tells unasked after the sheets are read: new grades, how a requested read went, a sheet that cannot
be read for 12 hours and its return, and the LLM limit to the owner.

New grades go to every user in one message a poll, more only when it does not fit the limits of a rich
message; each message marks its own grades sent, so a failure halfway sends nothing twice. Within the user's quiet
hours it comes without a sound. Grades not sent (Telegram failed, the user blocked the bot) wait for the next poll
while they are within the window of a week.

A user is told after a read they asked for (a new sheet, a new full name) and after the first read of a sheet for
them, which may follow a group sheet the owner added: one message for every sheet of the poll.
"""

import asyncio
import logging

from . import poller, profile
from .cards import collapse, news_messages
from .i18n import tr
from .llm.budget import Level
from .poller import SheetRead, UserRead
from .rich import Doc, b, code
from .sheets import name as sheet_name

log = logging.getLogger(__name__)


def explain(error: str, lang: str) -> str:
    """A kept error in words for a user."""
    what, _, detail = error.partition(": ")
    if what == poller.CLOSED:
        return tr(lang, "closed: open it to anyone with the link", "закрыта: открой её всем, у кого есть ссылка")
    if what == poller.DOWNLOAD:
        return tr(lang, f"cannot be downloaded ({detail}), I'll try again at the next read",
                  f"не скачивается ({detail}), попробую при следующем чтении")
    if what == poller.LAYOUT:
        return tr(lang, "its tabs could not be laid out, I'll try again at the next read",
                  "не удалось разметить вкладки, попробую при следующем чтении")
    if what == poller.BUDGET:
        return tr(lang, "the limit for laying out new tabs is spent until next month",
                  "лимит на разметку новых вкладок исчерпан до следующего месяца")
    if what == poller.NO_ROW:
        return tr(lang, "your row is on no tab: check your full name, /name",
                  "твоей строки нет ни на одной вкладке: проверь ФИО, /name")
    if what == poller.AMBIGUOUS:
        return tr(lang, f"your full name matches several rows in: {detail}. Make it longer, with the patronymic",
                  f"ФИО подходит к нескольким строкам на вкладках: {detail}. Уточни его, например с отчеством")
    return error


def outcome(read: SheetRead, user: UserRead | None, lang: str) -> str:
    if read.error is not None:
        return explain(read.error, lang)
    if user is None:
        return tr(lang, "read", "прочитана")
    if user.error is not None:
        return explain(user.error, lang)
    done = tr(lang, "connected", "подключена") if user.first else tr(lang, "read", "прочитана")
    return tr(lang, f"{done}: tabs {user.tabs}, grades {user.items}",
              f"{done}: вкладок {user.tabs}, оценок {user.items}")


class ReadNotices:
    async def after_read(self, results: list[SheetRead]) -> None:
        """Poller.on_read."""
        reports: dict[int, list[tuple[SheetRead, UserRead | None]]] = {}
        for read in results:
            if read.stale or read.back:
                await self.tell_failing(read)
            by_user = {user.tg_id: user for user in read.users}
            for tg_id in sorted(read.requested | {user.tg_id for user in read.users if user.first}):
                reports.setdefault(tg_id, []).append((read, by_user.get(tg_id)))
        for tg_id, lines in reports.items():
            if (user := self.access.user(tg_id)) is not None:
                lang = user["lang"]
                doc = Doc().h(tr(lang, "Sheets", "Таблицы")).table(
                    [], [[sheet_name(read.sheet), outcome(read, one, lang)] for read, one in lines])
                if any(one is not None and one.first and one.error is None for _, one in lines):
                    doc.p(tr(lang, "New and changed grades will come here.",
                             "Новые и изменённые оценки будут приходить сюда."))
                await self.tell(user, doc)
        await self.send_grades()

    async def send_grades(self) -> None:
        by_user: dict[int, list[dict]] = {}
        for event in self.grades.waiting():
            by_user.setdefault(event["tg_id"], []).append(event)
        for tg_id, events in by_user.items():
            user = self.access.user(tg_id)
            if user is None or user["paused"] or self.access.expired(user):
                continue  # an ended access: the grades wait for a week in case it is prolonged
            by_sheet: dict[int, list[dict]] = {}
            for event in events:
                by_sheet.setdefault(event["sheet"], []).append(event)
            sections = []
            for sheet_id, sheet_events in by_sheet.items():
                rows, dropped = collapse(sheet_events)
                self.grades.sent(dropped, 0)  # back where they were: nothing to tell
                if (sheet := self.sheets.get(sheet_id)) is not None:
                    sections.append((sheet, rows))
            silent = profile.is_quiet(user, self.now(), self.zone)
            for doc, ids in news_messages(sections, user["lang"]):
                msg_id = await self.tell(user, doc, silent=silent)
                if msg_id is None:
                    break  # the rest wait for the next poll
                self.grades.sent(ids, msg_id)

    async def tell_failing(self, read: SheetRead) -> None:
        """A sheet failing for 12 hours, or back after that: the owner and every follower, once each. A follower
        who blocked the bot or whose access ended hears nothing."""
        sheet = read.sheet
        for tg_id in self.sheets.followers(sheet["id"]):
            user = self.access.user(tg_id)
            if user is not None and not user["paused"] and not self.access.expired(user):
                await self.tell(user, self.failing_doc(sheet, read, user["lang"]))
        await self.tell_owner(self.failing_doc(sheet, read, self.owner_lang, read.error)
                              .buttons((tr(self.owner_lang, "Open the sheet", "Открыть таблицу"), sheet["url"])))

    def failing_doc(self, sheet: dict, read: SheetRead, lang: str, detail: str | None = None) -> Doc:
        """`detail`: the error as kept, for the owner; a user reads it explained."""
        title = b(sheet_name(sheet))
        if read.back:
            return Doc().p(tr(lang, "The sheet ", "Таблица "), title, tr(lang, " is read again.", " снова читается."))
        why = detail if detail is not None else explain(read.error or "", lang)
        return Doc().p(tr(lang, "The sheet ", "Таблица "), title,
                       tr(lang, " cannot be read since ", " не читается с "), self.when(read.stale), ": ", why, ". ",
                       tr(lang, "Its grades are not updated.", "Оценки из неё не обновляются."))

    def budget_notice(self, level: Level, spent: float, limit: float) -> None:
        """Budget.notify: called inside a Haiku call, so the message goes from a task of its own."""
        lang = self.owner_lang
        if level is Level.STOP:
            doc = Doc().p(tr(lang, "The monthly LLM limit is spent: ", "Месячный лимит на LLM исчерпан: "),
                          b(f"${spent:.2f} / ${limit:.2f}"), ". ",
                          tr(lang, "New tabs are not laid out until next month; raise ",
                             "Новые вкладки не размечаются до следующего месяца; поднять можно в "),
                          code("LLM_MONTHLY_USD"), tr(lang, " in .env to go on.", " в .env."))
        else:
            doc = Doc().p(tr(lang, "The LLM spending this month: ", "Расход на LLM в этом месяце: "),
                          b(f"${spent:.2f} / ${limit:.2f}"), ".")
        task = asyncio.get_running_loop().create_task(self.tell_owner(doc))
        self._tasks.add(task)
        task.add_done_callback(self._tasks.discard)

