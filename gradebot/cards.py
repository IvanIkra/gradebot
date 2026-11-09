"""Grades as messages: new grades after a poll, and /grades.

A sheet is a section headed by its title, linked to the sheet, and its course's hashtag; a work is a table row: its
label in the user's language (without the course, the heading has it), the grade on 0-10 with the bonus, the sheet's
own points when its scale is not 0-10, and the grade before for a changed one. Columns empty in the whole section are
left out.
"""

import re

from .i18n import tr
from .layout import label
from .rich import Doc, b, join, link, mark
from .sheets import course_of

RIGHT = "right"


def hashtag(course: str) -> str:
    """'Calc' -> '#Calc'; a tag holds letters, digits and _ only."""
    tag = re.sub(r"\W+", "_", course or "").strip("_")
    return f"#{tag}" if tag and not tag.isdigit() else ""


def score(value: float) -> str:
    """8.43 -> '8.4', 9.0 -> '9', 9.96 -> '10'."""
    rounded = round(value, 1)
    return str(int(rounded)) if rounded.is_integer() else f"{rounded:.1f}"


def points(value: float) -> str:
    """The sheet's own numbers: 5.9, 7, 2.5."""
    return f"{round(value, 2):g}"


def heading(sheet: dict) -> list[str]:
    """The parts of a section's heading: the title linked to the sheet, and the course's hashtag."""
    tag = hashtag(course_of(sheet))
    return [link(sheet["title"] or course_of(sheet), sheet["url"]), *([" ", tag] if tag else [])]


def grade_cell(row: dict, new: bool = False) -> str:
    value = b(score(row["value"]))
    text = join(mark(value) if new else value, f" {row['bonus']:+g}" if row["bonus"] else "")
    return text


def works_table(doc: Doc, rows: list[dict], lang: str, with_previous: bool) -> Doc:
    """Rows: {label, label_ru?, value, raw, max, bonus, previous?, new?}."""
    of = tr(lang, "of", "из")
    columns = [(tr(lang, "Work", "Задание"), None,
                lambda r: mark(label(r, lang)) if r.get("new") else label(r, lang)),
               (tr(lang, "Grade", "Оценка"), RIGHT, lambda r: grade_cell(r, r.get("new", False))),
               (tr(lang, "Points", "Баллы"), RIGHT,
                lambda r: f"{points(r['raw'])} {of} {points(r['max'])}" if r["max"] != 10 else "")]
    if with_previous:
        columns.append((tr(lang, "Was", "Было"), RIGHT,
                        lambda r: score(r["previous"]) if r.get("previous") is not None else ""))
    cells = [[get(r) for _, _, get in columns] for r in rows]
    keep = [n for n in range(len(columns)) if n < 2 or any(row[n] for row in cells)]
    return doc.table([columns[n][0] for n in keep], [[row[n] for n in keep] for row in cells],
                     [columns[n][1] for n in keep])


# --- new grades ----------------------------------------------------------------------------------


def collapse(events: list[dict]) -> tuple[list[dict], list[int]]:
    """The events of one sheet waiting to be sent -> one row per work: the latest grade with the grade before the
    first of them, and `ids`, the events it stands for. A work that went back to where it was is no news: its ids
    come second, to be marked sent all the same."""
    by_key: dict[str, list[dict]] = {}
    for event in events:
        by_key.setdefault(event["key"], []).append(event)
    rows, dropped = [], []
    for group in by_key.values():
        last, first = group[-1], group[0]
        ids = [event["id"] for event in group]
        if len(group) > 1 and first["previous"] == last["value"] and (first["bonus"] or 0) == (last["bonus"] or 0):
            dropped += ids
        else:
            rows.append({**last, "previous": first["previous"], "ids": ids})
    return rows, dropped


def news_section(sheet: dict, rows: list[dict], lang: str) -> Doc:
    return works_table(Doc().h(*heading(sheet)), rows, lang, with_previous=True)


def all_grades_button(lang: str) -> tuple:
    return (tr(lang, "All grades", "Все оценки"), "grades")


def news_messages(sections: list[tuple[dict, list[dict]]], lang: str) -> list[tuple[Doc, list[int]]]:
    """(sheet, rows) -> as few messages as fit the limits, a section of a sheet whole when it fits alone, each with
    the event ids it sends. Each ends with the «All grades» button."""
    def done(doc: Doc) -> Doc:
        return Doc(list(doc.blocks)).buttons(all_grades_button(lang))

    def pieces(sheet: dict, rows: list[dict]) -> list[tuple[Doc, list[int]]]:
        doc = news_section(sheet, rows, lang)
        if len(rows) == 1 or done(doc).fits():
            return [(doc, [i for row in rows for i in row["ids"]])]
        half = len(rows) // 2
        return pieces(sheet, rows[:half]) + pieces(sheet, rows[half:])

    messages: list[tuple[Doc, list[int]]] = []
    for sheet, rows in sections:
        for doc, ids in pieces(sheet, rows) if rows else []:
            if messages and done(Doc(list(messages[-1][0].blocks)).extend(doc)).fits():
                messages[-1][0].extend(doc)
                messages[-1][1].extend(ids)
            else:
                messages.append((Doc(list(doc.blocks)), list(ids)))
    return [(done(doc), ids) for doc, ids in messages]


# --- /grades -------------------------------------------------------------------------------------


def grades_section(sheet: dict, items: list[dict], lang: str, problem: str | None, table: bool = True) -> Doc:
    """A sheet in /grades: details headed «Calc · 12 grades · 2 new», open when some are new; the table of its
    grades and the «Wrong» button for the layout inside."""
    new = sum(item["new"] for item in items)
    summary = " · ".join([sheet["title"] or course_of(sheet),
                          tr(lang, f"{len(items)} grades", f"оценок: {len(items)}"),
                          *([tr(lang, f"{new} new", f"новых: {new}")] if new else [])])
    inner = Doc()
    if problem:
        inner.p(problem)
    if items and table:
        works_table(inner, items, lang, with_previous=False)
    elif not items and not problem:
        inner.p(tr(lang, "No grades yet.", "Оценок пока нет."))
    if items:
        inner.buttons((tr(lang, "Wrong", "Неверно"), f"wr:{sheet['id']}", "link"))
    return Doc().details(summary, inner, open_=bool(new))
