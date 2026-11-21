"""The layout of a tab by Haiku, and the grades it gives a student's row.

Haiku sees a tab's name, its top rows above a student's row and that row without the name, and says which top rows
are the header, which columns are graded works, their maximum, a bonus column and a short label in English and in
Russian: a user sees the one of their language.

A layout does not depend on the student, so it is one for everyone who follows the sheet: it is asked with the row of
any follower that has numbers in the tab, and cached in kv by sheet, tab and label budget together with the text of
the header rows it named and the names of the other tabs. While those stay, the layout stands, whatever the other
rows do; a changed header, or a tab added or renamed, asks again. The other tabs are shown so that a summary tab
that copies works with tabs of their own (a group's gradebook with a column «Коллок» next to a tab «Коллоквиум 1»)
leaves those columns out: each work is counted once, on its own tab. With no header named, any change in the top
rows asks again. An answer the model cannot give (refused, cut) is cached as a skipped tab until the top rows
change: the same input would fail again at every poll. A failure on the way (the network, the limit) is the
caller's: the sheet is read again at the next poll.
"""

import hashlib
import json
import logging
import re
from dataclasses import dataclass

from .llm.client import BadAnswer, Haiku
from .store import Store
from .workbook import Row, Tab, index, text

log = logging.getLogger(__name__)

LABEL_BUDGET = 23       # characters of "<course> <work>": "Calc HW 3"
MIN_BUDGET = 8
LAYOUT_VERSION = 3      # bump when the prompt changes, to drop the cached layouts (2: label_ru, 3: other tabs)
PREFIX = "layout:"      # kv: + the sheet's Google id, ":", the tab's hash, ":", the budget
MAX_TOKENS = 4000
TOKENS_PER_WORK = 80    # {"column", "label", "label_ru", "max", "bonus_column"}: a tab of dates must not cut it

LAYOUT_SYSTEM = """You map one tab of a teacher's grade sheet to the graded works shown to a student.

You see the top rows of the tab, each with its number, and the student's own row, as `column=value` pairs. Column
letters are spreadsheet columns. The top rows hold the header (titles, dates, problem numbers, maximum points) and may
also hold other students' rows and statistics: use them only to understand the layout.

A graded work is the overall result of one homework, quiz, test, seminar, project, exam or extra sheet.
Not graded works: points for single problems (headers like 1, 2, 3a, №1, 4*), course or semester totals,
sums, averages, final or cumulative grades (ИТОГ, Итог, Средний балл, Сумма, НК, ИК), attendance, activity
points, group, comments, penalties.
When a work has per-problem columns plus a total column for that work (for example "Балл"), the work is
that total column. When the work also has its own grade column next to the points (for example "Сумма" then
"Оценка"), the work is the grade column with max 10: that is the grade the student gets, and it may be scaled
or capped differently from the points. When columns are just numbers, the tab name says what the work is (tab "ДЗ-1А" is
homework 1A, "КР1" is test 1, "Э1" is exam 1).

Return skip=true when the tab is a summary or statistics tab (Итоги, Статистика), an archive (old),
or has no graded works.

You also see the names of the sheet's other tabs. A tab that gathers the works of a group or of the course (a
gradebook) may copy works that have tabs of their own. A column whose work has its own tab is not a graded work
here: it is counted on its own tab. For example, with a tab "Коллоквиум 1", a column "Коллок" or "Коллоквиум 1" is
left out; with tabs "Доп. листок 1" and "Доп. листок 2", the columns "Доп 1" and "Доп 2" are left out, while "Доп 3"
with no tab of its own stays. Columns of such a tab whose works have no tab of their own are graded works as usual.

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
- label_ru: the same label in Russian, telegraphic, at most {budget} characters: "ДЗ 1А", "Квиз 2", "Сем 3",
  "КР 1", "Экзамен", "Проект", "Доп 2", "LMS". Dates as "ДЗ 17 сен". Russian letter suffixes stay as in the
  sheet (tab "ДЗ-1В" is "ДЗ 1В"). Labels must differ between works.

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
                    "label_ru": {"type": "string"},
                    "max": {"anyOf": [{"type": "number"}, {"type": "null"}]},
                    "bonus_column": {"anyOf": [{"type": "string"}, {"type": "null"}]},
                },
                "required": ["column", "label", "label_ru", "max", "bonus_column"],
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


def prompt(row: Row, others: list[str] | tuple[str, ...] = ()) -> str:
    def line(cells: dict) -> str:
        return " | ".join(f"{c}={v}" for c, v in cells.items()) or "(empty)"

    above = "\n".join(f"row {number}: {line(cells)}" for number, cells in row.above()) or "(no rows above the student)"
    mine = {c: text(v) for c, v in row.cells.items()}
    tabs = ", ".join(f"«{title}»" for title in others) or "(none)"
    return (f'Tab: "{row.tab.title}"\nOther tabs of the sheet: {tabs}\nStudent name is in column {row.name_col}.\n'
            f"{above}\nstudent row (row {row.number}): {line(mine)}")


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
    label_ru: str = ""      # the label in Russian; "" when the layout has none
    graded: float | None = None     # the share of the other students with points in the column; None: no others
    parts: tuple[tuple[str, str], ...] = ()  # the work's problems in the own row: (its number, its points)


@dataclass(frozen=True)
class Planned:
    """A work a laid-out tab has a column for, graded for someone or not yet: what a formula is made of."""
    key: str
    tab: str
    label: str
    label_ru: str
    max: float


def work_key(tab: Tab, col: str, header_rows: list[int]) -> tuple[str, str]:
    """(key, header) of a work's column: the key is its place, not its label."""
    header = tab.column_header(col, header_rows) if col else ""
    return f"{tab.title}|{col}|{header}", header


def plan(tab: Tab, spec: dict) -> list[Planned]:
    """Every work of a laid-out tab, in sheet order, whoever is graded in it."""
    if spec.get("skip"):
        return []
    works, keys = [], set()
    for it in spec.get("items") or []:
        key, _ = work_key(tab, it.get("column"), spec.get("header_rows") or [])
        maximum = number(it.get("max")) or 10.0
        if key in keys or maximum <= 0:
            continue
        keys.add(key)
        works.append(Planned(key, tab.title, " ".join(str(it.get("label") or "").split()),
                             " ".join(str(it.get("label_ru") or "").split()), maximum))
    return works


def label(item: dict, lang: str) -> str:
    """A work's label in the user's language: the Russian one when there is one."""
    return (item.get("label_ru") or item["label"]) if lang == "ru" else item["label"]


# A column of a work's problem, by its header: «1», «3a», «№ 5 b)», «4*», «2 вопрос», «Задача», a penalty.
PROBLEM = re.compile(r"№?\s*\d{1,2}\s*[a-zа-яё]?\)?\s*\*{0,2}")
ANSWER = re.compile(r"\d{1,2}\s*вопрос|вопрос\s*\d{1,2}|задача(\s*\d{1,2})?|опрос|.{0,20}штраф")
NAME = re.compile(r"[^\W\d_][\w.\-]*(?: [^\W\d_][\w.\-]*){1,3}")  # two to four words of letters: a person


def problem(header: str) -> str | None:
    """The short name of a problem's column from its header rows («ДЗ 1 / №3 a)» -> «3a», «7 / 1а» -> «1а»), None
    for a column that is no problem (a sum, a grade, a copy of another work). A part with № or a letter wins over
    a bare number: a row of maximums is numbers too."""
    found = [part for part in (p.strip().casefold() for p in header.split(" / ")) if PROBLEM.fullmatch(part)]
    if found:
        best = next((part for part in found if "№" in part or re.search(r"[a-zа-яё]", part)), found[-1])
        return re.sub(r"[№\s)]", "", best)
    words = [p.strip() for p in header.split(" / ") if ANSWER.fullmatch(p.strip().casefold())]
    return words[-1] if words else None


def graded_shares(row: Row, header_rows: list[int]) -> dict[str, float]:
    """For each column, the share of the other students of the tab with points above zero in it: a zero is the
    student's own when the work is graded for the group, a placeholder when nobody has points yet. Others are the
    rows with a person's name in the name column and a number somewhere (a blank copy of the list is none); only
    the shares are kept."""
    name = index(row.name_col)
    others = []
    for n, cells in enumerate(row.tab.cells, 1):
        if n == row.number or n in header_rows or name >= len(cells):
            continue
        who = cells[name]
        if not isinstance(who, str) or not NAME.fullmatch(" ".join(who.split())):
            continue
        if any(number(c) is not None for j, c in enumerate(cells) if j != name):
            others.append(cells)
    if not others:
        return {}
    shares = {}
    for col in row.cells:
        j = index(col)
        shares[col] = sum((number(r[j]) or 0) > 0 for r in others if j < len(r)) / len(others)
    return shares


def parts_of(row: Row, spec: dict) -> dict[str, tuple[tuple[str, str], ...]]:
    """Each work's problems: the own row's cells between the work before it (or the name) and its column whose
    header names a problem, the works' and bonus columns aside."""
    items = [it for it in spec.get("items") or [] if it.get("column")]
    own = {it["column"] for it in items} | {it["bonus_column"] for it in items if it.get("bonus_column")}
    header_rows = spec.get("header_rows") or []
    out, before = {}, index(row.name_col)
    for it in sorted(items, key=lambda it: index(it["column"])):
        at = index(it["column"])
        found = []
        for col, value in sorted(row.cells.items(), key=lambda kv: index(kv[0])):
            if not before < index(col) < at or col in own:
                continue
            name = problem(row.tab.column_header(col, header_rows))
            shown = text(value)
            if name and shown:
                raw = number(value)
                found.append((name, f"{round(raw, 2):g}" if raw is not None else shown[:8]))
        out[it["column"]] = tuple(found)
        before = max(before, at)
    return out


def extract(row: Row, spec: dict, start: int = 0) -> list[Item]:
    """The works of a laid-out tab that have a number in the row; positions go on from `start`."""
    if spec.get("skip"):
        return []
    items, keys, header_rows = [], set(), spec.get("header_rows") or []
    shares, parts = graded_shares(row, header_rows), parts_of(row, spec)
    for it in spec.get("items") or []:
        col = it.get("column")
        raw = number(row.cells.get(col))
        key, header = work_key(row.tab, col, header_rows)
        if raw is None or key in keys:
            continue
        maximum = number(it.get("max")) or 10.0
        if maximum <= 0:
            continue
        value = raw if maximum == 10 else raw / maximum * 10
        bonus = number(row.cells.get(it["bonus_column"])) if it.get("bonus_column") else None
        keys.add(key)
        items.append(Item(key, row.tab.title, header, " ".join(str(it.get("label") or "").split()),
                          round(value, 2), raw, maximum, bonus or None, start + len(items),
                          " ".join(str(it.get("label_ru") or "").split()), shares.get(col), parts.get(col, ())))
    return items


class Layouts:
    def __init__(self, store: Store, llm: Haiku):
        self.store, self.llm = store, llm

    async def get(self, sheet_id: str, tab: Tab, rows: list[Row], label_budget: int,
                  others: list[str] | tuple[str, ...] = ()) -> dict:
        """The cached layout while the rows it watches have the same text and the other tabs (`others`, their
        titles) are the same, else Haiku's, asked about one of `rows` (the followers' rows in the tab). LLMError or
        BudgetExceeded when it could not be asked."""
        key = layout_key(sheet_id, tab.title, label_budget)
        cached = self.store.get_json(key)
        if cached is not None and tab.header(cached["watch"]) == cached["header"] \
                and cached.get("others", []) == sorted(others):
            return cached["spec"]
        row = sample(rows)
        shown = [number for number, _ in row.above()]
        columns = set(row.cells).union(*(cells for _, cells in row.above()))
        system = LAYOUT_SYSTEM.replace("{budget}", str(label_budget))
        try:
            spec = await self.llm.json("grades", system, prompt(row, others), LAYOUT_SCHEMA,
                                       max_tokens=max(MAX_TOKENS, TOKENS_PER_WORK * len(columns)))
            spec["header_rows"] = sorted({n for n in spec.get("header_rows") or [] if n in shown})
        except BadAnswer as e:  # the same input would fail again: skipped until the top rows change
            log.warning("layout of tab %r: %s; the tab is skipped", tab.title, e)
            spec = {"skip": True, "reason": f"no answer: {e}", "header_rows": [], "items": [], "failed": True}
        # The header the model named is what the layout stands on; with none named, the whole top.
        watch = spec["header_rows"] or [number for number, _ in tab.top]
        self.store.put_json(key, {"watch": watch, "header": tab.header(watch), "others": sorted(others),
                                  "spec": spec})
        log.info("layout of tab %r: %s", tab.title,
                 "skip" if spec.get("skip") else [it.get("label") for it in spec.get("items") or []])
        return spec

    def forget(self, sheet_id: str) -> int:
        """Every cached layout of a sheet: /relayout, and the button under a complaint."""
        return self.store.delete_prefix(f"{PREFIX}{sheet_id}:")
