"""The layout of a tab by Haiku, and the grades it gives a student's row (spec §5.2).

Haiku sees a tab's name, its top rows above a student's row and that row without the name, and says which top rows
are the header, which columns are graded works, their maximum, a bonus column and a short English label. A layout
does not depend on the student, so it is one for everyone who follows the sheet: it is asked with the row of any
follower that has numbers in the tab, and cached in kv by sheet, tab and label budget together with the text of the
header rows it named. While that text stays, the layout stands, whatever the other rows do; a changed header asks
again. With no header named, any change in the top rows asks again. An answer the model cannot give (refused, cut)
is cached as a skipped tab until the top rows change: the same input would fail again at every poll. A failure on
the way (the network, the limit) is the caller's: the sheet is read again at the next poll.
"""

import hashlib
import json
import logging
from dataclasses import dataclass

from .llm.client import BadAnswer, Haiku
from .store import Store
from .workbook import Row, Tab, text

log = logging.getLogger(__name__)

LABEL_BUDGET = 23       # characters of "<course> <work>": "Calc HW 3"
MIN_BUDGET = 8
LAYOUT_VERSION = 1      # bump when the prompt changes, to drop the cached layouts
PREFIX = "layout:"      # kv: + the sheet's Google id, ":", the tab's hash, ":", the budget
MAX_TOKENS = 4000
TOKENS_PER_WORK = 60    # {"column", "label", "max", "bonus_column"}: a tab of dates must not cut the answer

LAYOUT_SYSTEM = """You map one tab of a teacher's grade sheet to the graded works shown to a student.

You see the top rows of the tab, each with its number, and the student's own row, as `column=value` pairs. Column
letters are spreadsheet columns. The top rows hold the header (titles, dates, problem numbers, maximum points) and may
also hold other students' rows and statistics: use them only to understand the layout.

A graded work is the overall result of one homework, quiz, test, seminar, project, exam or extra sheet.
Not graded works: points for single problems (headers like 1, 2, 3a, №1, 4*), course or semester totals,
sums, averages, final or cumulative grades (ИТОГ, Итог, Средний балл, Сумма, НК, ИК), attendance, activity
points, group, comments, penalties.
When a work has per-problem columns plus a total column for that work (for example "Балл"), the work is
that total column. When columns are just numbers, the tab name says what the work is (tab "ДЗ-1А" is
homework 1A, "КР1" is test 1, "Э1" is exam 1).

Return skip=true when the tab is a summary or statistics tab (Итоги, Статистика), an archive (old),
or has no graded works.

header_rows: the numbers of the top rows that describe the columns - titles, dates, problem numbers, maximum points -
in order. Never a row of a student, never statistics that change with the grades (averages, sums or counts over
students).

For each graded work:
- column: its column letter.
- max: the maximum points. It may be written in a header row anywhere, e.g. "Max за обыкновенные" followed
  by numbers - the first number is the maximum of the main score, the next one of the bonus. If the column
  is already a 0-10 grade, max is 10. If you cannot tell, null.
- bonus_column: the column with extra points for the same work (for example "Балл*"), or null.
- label: English, telegraphic, at most {budget} characters: "HW 1A", "Quiz 2", "Sem 3", "Test 1", "Exam",
  "Project", "Extra 2", "LMS". Dates as "HW Sep 17". No course name - it is added separately.
  Russian letter suffixes go by alphabet position, not by shape: а→A, б→B, в→C, г→D, д→E
  (tab "ДЗ-1В" is "HW 1C", not "HW 1B"). Labels must differ between works.

List works in sheet order."""

LAYOUT_SCHEMA = {
    "type": "object",
    "properties": {
        "skip": {"type": "boolean"},
        "reason": {"type": "string"},
        "header_rows": {"type": "array", "items": {"type": "integer"}},
        "items": {
            "type": "array",
            "items": {
                "type": "object",
                "properties": {
                    "column": {"type": "string"},
                    "label": {"type": "string"},
                    "max": {"anyOf": [{"type": "number"}, {"type": "null"}]},
                    "bonus_column": {"anyOf": [{"type": "string"}, {"type": "null"}]},
                },
                "required": ["column", "label", "max", "bonus_column"],
                "additionalProperties": False,
            },
        },
    },
    "required": ["skip", "reason", "header_rows", "items"],
    "additionalProperties": False,
}


def budget(course: str) -> int:
    """Characters left for a work's label after the course and a space."""
    return max(MIN_BUDGET, LABEL_BUDGET - len(course) - 1)


def layout_key(sheet_id: str, tab: str, label_budget: int) -> str:
    digest = hashlib.sha256(json.dumps([tab, LAYOUT_VERSION], ensure_ascii=False).encode()).hexdigest()[:24]
    return f"{PREFIX}{sheet_id}:{digest}:{label_budget}"


def prompt(row: Row) -> str:
    def line(cells: dict) -> str:
        return " | ".join(f"{c}={v}" for c, v in cells.items()) or "(empty)"

    above = "\n".join(f"row {number}: {line(cells)}" for number, cells in row.above()) or "(no rows above the student)"
    mine = {c: text(v) for c, v in row.cells.items()}
    return (f'Tab: "{row.tab.title}"\nStudent name is in column {row.name_col}.\n{above}\n'
            f"student row (row {row.number}): {line(mine)}")


def number(value) -> float | None:
    if isinstance(value, bool) or value is None:
        return None
    if isinstance(value, int | float):
        return float(value)
    try:
        return float(str(value).strip().replace(",", "."))
    except ValueError:
        return None


def sample(rows: list[Row]) -> Row:
    """The row to ask about: the first one with a number in it, so the model sees what a grade looks like."""
    return next((row for row in rows if any(number(v) is not None for v in row.cells.values())), rows[0])


@dataclass(frozen=True)
class Item:
    key: str                # <tab>|<column>|<its header>
    tab: str
    header: str
    label: str              # without the course
    value: float            # 0-10
    raw: float
    max: float
    bonus: float | None
    position: int


def extract(row: Row, spec: dict, start: int = 0) -> list[Item]:
    """The works of a laid-out tab that have a number in the row; positions go on from `start`."""
    if spec.get("skip"):
        return []
    items, keys, header_rows = [], set(), spec.get("header_rows") or []
    for it in spec.get("items") or []:
        col = it.get("column")
        raw = number(row.cells.get(col))
        header = row.tab.column_header(col, header_rows) if col else ""
        key = f"{row.tab.title}|{col}|{header}"
        if raw is None or key in keys:
            continue
        maximum = number(it.get("max")) or 10.0
        if maximum <= 0:
            continue
        value = raw if maximum == 10 else raw / maximum * 10
        bonus = number(row.cells.get(it["bonus_column"])) if it.get("bonus_column") else None
        keys.add(key)
        items.append(Item(key, row.tab.title, header, " ".join(str(it.get("label") or "").split()),
                          round(value, 2), raw, maximum, bonus or None, start + len(items)))
    return items


class Layouts:
    def __init__(self, store: Store, llm: Haiku):
        self.store, self.llm = store, llm

    async def get(self, sheet_id: str, tab: Tab, rows: list[Row], label_budget: int) -> dict:
        """The cached layout while the rows it watches have the same text, else Haiku's, asked about one of `rows`
        (the followers' rows in the tab). LLMError or BudgetExceeded when it could not be asked."""
        key = layout_key(sheet_id, tab.title, label_budget)
        cached = self.store.get_json(key)
        if cached is not None and tab.header(cached["watch"]) == cached["header"]:
            return cached["spec"]
        row = sample(rows)
        shown = [number for number, _ in row.above()]
        columns = set(row.cells).union(*(cells for _, cells in row.above()))
        system = LAYOUT_SYSTEM.replace("{budget}", str(label_budget))
        try:
            spec = await self.llm.json("grades", system, prompt(row), LAYOUT_SCHEMA,
                                       max_tokens=max(MAX_TOKENS, TOKENS_PER_WORK * len(columns)))
            spec["header_rows"] = sorted({n for n in spec.get("header_rows") or [] if n in shown})
        except BadAnswer as e:  # the same input would fail again: skipped until the top rows change
            log.warning("layout of tab %r: %s; the tab is skipped", tab.title, e)
            spec = {"skip": True, "reason": f"no answer: {e}", "header_rows": [], "items": [], "failed": True}
        # The header the model named is what the layout stands on; with none named, the whole top.
        watch = spec["header_rows"] or [number for number, _ in tab.top]
        self.store.put_json(key, {"watch": watch, "header": tab.header(watch), "spec": spec})
        log.info("layout of tab %r: %s", tab.title,
                 "skip" if spec.get("skip") else [it.get("label") for it in spec.get("items") or []])
        return spec

    def forget(self, sheet_id: str) -> int:
        """Every cached layout of a sheet: /relayout, and the button under a complaint."""
        return self.store.delete_prefix(f"{PREFIX}{sheet_id}:")
