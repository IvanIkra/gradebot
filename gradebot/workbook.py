"""A downloaded grade sheet: its visible tabs, and in them the rows of the students who follow it (spec §5.1).

The workbook is parsed once per read, then searched for every follower's full name. A name is compared without case,
"ё" and punctuation, and matches the start of a cell: "Иванов Иван" finds "ИВАНОВ Иван Сергеевич". A name that
matches two rows of one tab is an error, not the first row: two namesakes in a group are told apart by a longer
name.

Of every tab the top rows are kept (TOP_ROWS, whoever reads it): the header is somewhere among them, and the layout
is cached by the text of the header rows (layout.py), so it must not depend on whose row is asked about.
"""

import datetime as dt
import io
import re
from dataclasses import dataclass

TOP_ROWS = 15               # the first rows of a tab the model sees: the header is somewhere among them


class NotWorkbook(ValueError):
    """A sheet closed to the link gives a login page instead of an xlsx."""


class Ambiguous(ValueError):
    """The name matches more than one row of a tab."""

    def __init__(self, tabs: list[str]):
        super().__init__(f"the name matches several rows in: {', '.join(tabs)}")
        self.tabs = tabs


def norm(text: str) -> str:
    """Lower case, ё as е, punctuation but "-" as spaces: "ИВАНОВ  Иван," -> "иванов иван"."""
    text = text.lower().replace("ё", "е")
    return " ".join(re.sub(r"[^\w\s-]", " ", text).split())


def matches(cell, student: list[str]) -> bool:
    """The cell starts with the words of the name."""
    if not isinstance(cell, str):
        return False
    words = norm(cell).split()
    return len(words) >= len(student) and words[:len(student)] == student


def text(value) -> str:
    if value is None:
        return ""
    if isinstance(value, dt.datetime):
        return value.date().isoformat() if value.time() == dt.time() else value.isoformat(" ", "minutes")
    if isinstance(value, float) and value.is_integer():
        return str(int(value))
    return str(value).strip()


def column(index: int) -> str:
    """0 -> A, 26 -> AA."""
    letters = ""
    index += 1
    while index:
        index, rest = divmod(index - 1, 26)
        letters = chr(65 + rest) + letters
    return letters


@dataclass(frozen=True, eq=False)
class Tab:
    title: str
    cells: list[list]                       # every row, raw values
    top: list[tuple[int, dict[str, str]]]   # the first TOP_ROWS rows that are not empty: (number, letter -> text)

    def header(self, numbers: list[int]) -> list[dict[str, str]]:
        """The rows named the header, in tab order."""
        return [cells for number, cells in self.top if number in numbers]

    def column_header(self, col: str, numbers: list[int]) -> str:
        return " / ".join(row[col] for row in self.header(numbers) if row.get(col))


@dataclass(frozen=True, eq=False)
class Row:
    """One student's row in a tab, without their name."""
    tab: Tab
    number: int                 # in the tab, from 1
    name_col: str
    cells: dict[str, object]    # column letter -> raw value

    def above(self) -> list[tuple[int, dict[str, str]]]:
        """The top rows above this one: what the model sees of the tab next to it."""
        return [(number, cells) for number, cells in self.tab.top if number < self.number]


def load(data: bytes) -> list[Tab]:
    """The visible tabs of a workbook. NotWorkbook when the data is none."""
    from openpyxl import load_workbook

    try:
        wb = load_workbook(io.BytesIO(data), read_only=True, data_only=True)
    except Exception as e:  # a login page or a broken file: zipfile, openpyxl and XML errors alike
        raise NotWorkbook(f"not a workbook ({type(e).__name__})") from None
    tabs = []
    try:
        for ws in wb.worksheets:
            if ws.sheet_state != "visible":
                continue
            cells = [list(r) for r in ws.iter_rows(values_only=True)]
            top = [(i + 1, row) for i, raw in enumerate(cells[:TOP_ROWS])
                   if (row := {column(j): text(c) for j, c in enumerate(raw) if text(c)})]
            tabs.append(Tab(ws.title, cells, top))
    finally:
        wb.close()
    return tabs


def find(tabs: list[Tab], student: str) -> list[Row]:
    """The student's row in every tab that has it. Ambiguous when a tab has the name in more than one row."""
    target = norm(student).split()
    rows, twice = [], []
    for tab in tabs:
        found = [(i, j) for i, r in enumerate(tab.cells)
                 if (j := next((j for j, c in enumerate(r) if matches(c, target)), None)) is not None]
        if len(found) > 1:
            twice.append(tab.title)
        elif found:
            i, j = found[0]
            rows.append(Row(tab, i + 1, column(j), {column(k): c for k, c in enumerate(tab.cells[i])
                                                    if k != j and text(c)}))
    if twice:
        raise Ambiguous(twice)
    return rows
