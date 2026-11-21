"""Grades as messages: new grades after a poll, and /grades.

A unit (subjects.Unit) is a section: a sheet in no subject headed by its title, linked to the sheet, and its course's
hashtag; a subject headed by its hashtag, its sheets linked under it. «By problem» sends a unit's works with the
points of their problems as the sheet has them in the student's row. A work is a table row: its
label in the user's language (without the course, the heading has it), the grade on 0-10 with the bonus, the sheet's
own points when its scale is not 0-10, and the grade before for a changed one. Columns empty in the whole section are
left out.
"""

import re

from .i18n import tr
from .layout import label
from .rich import Doc, b, i, join, link, mark
from .sheets import course_of
from .sheets import name as sheet_name
from .subjects import Unit

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


def heading(unit: Unit) -> list[str]:
    """The parts of a section's heading: a sheet's title linked to it and the course's hashtag; a subject's
    hashtag."""
    tag = hashtag(unit.tag)
    if unit.subject is not None:
        return [tag or unit.name]
    sheet = unit.sheets[0]
    return [link(sheet["title"] or course_of(sheet), sheet["url"]), *([" ", tag] if tag else [])]


def sheet_links(doc: Doc, unit: Unit) -> Doc:
    """Under a subject's heading: its sheets, linked."""
    if unit.subject is not None:
        doc.p(join(*[link(s["title"] or sheet_name(s), s["url"]) for s in unit.sheets], sep=" · "))
    return doc


def grade_cell(row: dict, new: bool = False) -> str:
    value = mark(score(row["value"])) if new else b(score(row["value"]))
    text = join(value, f" {row['bonus']:+g}" if row["bonus"] else "")
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


def news_section(unit: Unit, rows: list[dict], lang: str) -> Doc:
    doc = works_table(sheet_links(Doc().h(*heading(unit)), unit), rows, lang, with_previous=True)
    return doc.p(i(unit.note)) if unit.note else doc


def all_grades_button(lang: str) -> tuple:
    return (tr(lang, "All grades", "Все оценки"), "grades")


def news_messages(sections: list[tuple[Unit, list[dict]]], lang: str) -> list[tuple[Doc, list[int]]]:
    """(unit, rows) -> as few messages as fit the limits, a section of a unit whole when it fits alone, each with
    the event ids it sends. Each ends with the «All grades» button."""
    def done(doc: Doc) -> Doc:
        return Doc(list(doc.blocks)).buttons(all_grades_button(lang))

    def pieces(unit: Unit, rows: list[dict]) -> list[tuple[Doc, list[int]]]:
        doc = news_section(unit, rows, lang)
        if len(rows) == 1 or done(doc).fits():
            return [(doc, [i for row in rows for i in row["ids"]])]
        half = len(rows) // 2
        return pieces(unit, rows[:half]) + pieces(unit, rows[half:])

    messages: list[tuple[Doc, list[int]]] = []
    for unit, rows in sections:
        for doc, ids in pieces(unit, rows) if rows else []:
            if messages and done(Doc(list(messages[-1][0].blocks)).extend(doc)).fits():
                messages[-1][0].extend(doc)
                messages[-1][1].extend(ids)
            else:
                messages.append((Doc(list(doc.blocks)), list(ids)))
    return [(done(doc), ids) for doc, ids in messages]


# --- /grades -------------------------------------------------------------------------------------


def grades_section(unit: Unit, items: list[dict], lang: str, problems: list[str], table: bool = True) -> Doc:
    """A unit in /grades: details headed «Calc · 12 grades · 2 new», open when some are new; a subject's sheets
    linked, its outlook, the table of its grades, a «Report a mistake» button for each sheet's layout and a
    subject's «Goal» and «Formula» inside."""
    new = sum(item["new"] for item in items)
    summary = " · ".join([unit.name, tr(lang, f"{len(items)} grades", f"оценок: {len(items)}"),
                          *([tr(lang, f"{new} new", f"новых: {new}")] if new else [])])
    inner = sheet_links(Doc(), unit)
    if unit.note:
        inner.p(i(unit.note))
    for problem in problems:
        inner.p(problem)
    if items and table:
        works_table(inner, items, lang, with_previous=False)
    elif not items and not problems:
        inner.p(tr(lang, "No grades yet.", "Оценок пока нет."))
    if items:
        report = tr(lang, "Report a mistake", "Сообщить об ошибке")
        graded = {item["sheet"] for item in items}
        for sheet in unit.sheets:
            if sheet["id"] in graded:
                inner.buttons((report if len(graded) == 1 else f"{report}: {sheet_name(sheet)}",
                               f"wr:{sheet['id']}", "link"))
    if any(item.get("parts") for item in items):
        inner.buttons((tr(lang, "By problem", "По задачам"), f"pt:{unit_ref(unit)}"))
    if unit.subject is not None:
        sid = unit.subject["id"]
        inner.buttons((tr(lang, "Goal", "Цель"), f"gl:s.{sid}"), (tr(lang, "Formula", "Формула"), f"fm:s.{sid}"))
    return Doc().details(summary, inner, open_=bool(new))


def unit_ref(unit: Unit) -> str:
    """A unit in a callback: «s<subject>», or «h<sheet>» for a sheet in no subject."""
    return f"s{unit.subject['id']}" if unit.subject is not None else f"h{unit.sheets[0]['id']}"


PROBLEMS_PER_ROW = 8     # problem columns in one table: wider ones scroll sideways on a phone


def problems_work(item: dict, lang: str) -> Doc:
    """A work «By problem»: «HW 1 · grade 7 · 3.5 of 5», then its problems' numbers over their points, in tables
    of at most PROBLEMS_PER_ROW columns."""
    facts = [join(tr(lang, "grade ", "оценка "), grade_cell(item))]
    if item["max"] != 10:
        facts.append(tr(lang, f"{points(item['raw'])} of {points(item['max'])}",
                        f"{points(item['raw'])} из {points(item['max'])}"))
    doc = Doc().p(join(b(label(item, lang)), " · ", join(*facts, sep=" · ")))
    parts = item["parts"]
    for at in range(0, len(parts), PROBLEMS_PER_ROW):
        chunk = parts[at:at + PROBLEMS_PER_ROW]
        doc.table([name for name, _ in chunk], [[value for _, value in chunk]], [RIGHT] * len(chunk))
    return doc


def problems_docs(unit: Unit, items: list[dict], lang: str) -> list[Doc]:
    """«By problem»: each work with a table of its problems' points, as few messages as fit."""
    title = join(unit.name, tr(lang, " · by problem", " · по задачам"))
    works = [problems_work(item, lang) for item in items if item.get("parts")]
    if not works:
        return [Doc().h(title).p(tr(lang, "The sheets show no problems of these works.",
                                    "В таблицах нет баллов по задачам этих работ."))]
    docs = [Doc().h(title)]
    for work in works:
        if len(docs[-1].blocks) > 1 and not Doc(list(docs[-1].blocks)).extend(work).fits():
            docs.append(Doc().h(title))
        docs[-1].extend(work)
    return docs
