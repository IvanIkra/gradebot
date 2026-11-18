"""Subjects: several sheets of one course read as one. A group's subject is run by its admin and the owner, a user's
own one by that user, for the sheets they follow. In a scope (a group, a user) a sheet is in one subject at most.

For a user, a followed sheet belongs to their own subject that has it, else to their group's, else to none: then it
is a unit of its own, as before subjects. /grades and the messages of new grades show a section per unit.

Callbacks `sj:<action>.<arg>`: `h` the root of /subjects, `l.<scope>` the subjects of a scope (`g<group>`,
`u<user>`), `n.<scope>` a new one, `o.<subject>` its card, `p.<subject>.<sheet>` puts a sheet in it and
`t.<subject>.<sheet>` takes one out, `r.<subject>` renames it, `d.<subject>` asks to delete it and `x.<subject>`
deletes it.
"""

import datetime as dt
import json
from collections.abc import Callable
from dataclasses import dataclass, field

from . import clock
from .dialog import Step
from .i18n import tr
from .rich import Doc, b, link
from .sheets import SheetError, course, course_of
from .sheets import name as sheet_name
from .store import Store


class SubjectError(ValueError):
    pass


def subject_name(text: str) -> str:
    """A subject's name: its hashtag too, so as short as a sheet's (sheets.course)."""
    try:
        value = course(text)
    except SheetError as e:
        raise SubjectError(str(e)) from None
    if not value:
        raise SubjectError("a subject needs a name")
    return value


@dataclass
class Unit:
    """What /grades and the messages of new grades show as one section: a subject, or a sheet in none."""
    name: str                   # the subject's name, or the sheet's title
    tag: str                    # the course its hashtag is made of
    sheets: list[dict] = field(default_factory=list)
    subject: dict | None = None

    @classmethod
    def of(cls, sheet: dict) -> "Unit":
        return cls(sheet["title"] or course_of(sheet), course_of(sheet), [sheet])


class Subjects:
    def __init__(self, store: Store, now: Callable[[], dt.datetime] = clock.now_utc):
        self.db, self.now = store.db, now

    def create(self, name: str, grp: int | None = None, tg_id: int | None = None) -> dict:
        """SubjectError for a bad name, or one the scope already has."""
        name = subject_name(name)
        if self._taken(name, grp, tg_id):
            raise SubjectError(f"there is a subject «{name}» already")
        with self.db:
            sid = self.db.execute("insert into subjects(grp, tg_id, name, created_at) values(?, ?, ?, ?)",
                                  (grp, tg_id, name, clock.utc_iso(self.now()))).lastrowid
        return self.get(sid)

    def _taken(self, name: str, grp: int | None, tg_id: int | None, but: int | None = None) -> bool:
        """Compared here, not by SQLite: its nocase folds only ASCII letters."""
        return any(s["name"].casefold() == name.casefold() and s["id"] != but for s in self.scope(grp, tg_id))

    def get(self, sid: int) -> dict | None:
        row = self.db.execute("select * from subjects where id = ?", (sid,)).fetchone()
        return dict(row) if row else None

    def rename(self, sid: int, name: str) -> dict:
        subject, name = self.get(sid), subject_name(name)
        if subject is None:
            raise SubjectError("no such subject")
        if self._taken(name, subject["grp"], subject["tg_id"], sid):
            raise SubjectError(f"there is a subject «{name}» already")
        with self.db:
            self.db.execute("update subjects set name = ? where id = ?", (name, sid))
        return self.get(sid)

    def delete(self, sid: int) -> bool:
        with self.db:
            return self.db.execute("delete from subjects where id = ?", (sid,)).rowcount == 1

    def scope(self, grp: int | None = None, tg_id: int | None = None) -> list[dict]:
        """A group's subjects or a user's, by name."""
        rows = self.db.execute("select * from subjects where grp is ? and tg_id is ?", (grp, tg_id))
        return sorted((dict(row) for row in rows), key=lambda s: s["name"].casefold())

    def sheets_of(self, sid: int) -> list[int]:
        return [row["sheet"] for row in self.db.execute(
            "select sheet from subject_sheets where subject = ? order by rowid", (sid,))]

    def put(self, sid: int, sheet: int) -> None:
        """The sheet into the subject, out of any other subject of the same scope."""
        subject = self.get(sid)
        with self.db:
            self.db.execute("delete from subject_sheets where sheet = ? and subject in (select id from subjects "
                            "where grp is ? and tg_id is ?)", (sheet, subject["grp"], subject["tg_id"]))
            self.db.execute("insert into subject_sheets(subject, sheet) values(?, ?)", (sid, sheet))

    def take(self, sid: int, sheet: int) -> bool:
        with self.db:
            return self.db.execute("delete from subject_sheets where subject = ? and sheet = ?",
                                   (sid, sheet)).rowcount == 1

    # --- formulas ---------------------------------------------------------------------------------

    def set_formula(self, sid: int, text: str | None, tree: dict | None, tg_id: int | None = None) -> None:
        """The subject's formula, or with `tg_id` that user's own one; None drops it."""
        with self.db:
            if tg_id is None:
                self.db.execute("update subjects set formula = ?, tree = ? where id = ?",
                                (text, json.dumps(tree, ensure_ascii=False) if tree else None, sid))
            elif tree is None:
                self.db.execute("delete from subject_formulas where tg_id = ? and subject = ?", (tg_id, sid))
            else:
                self.db.execute("insert into subject_formulas(tg_id, subject, formula, tree) values(?, ?, ?, ?) "
                                "on conflict(tg_id, subject) do update set formula = excluded.formula, "
                                "tree = excluded.tree", (tg_id, sid, text, json.dumps(tree, ensure_ascii=False)))

    def own_formula(self, tg_id: int, sid: int) -> dict | None:
        """{text, tree, own: True} of a user's own formula of a subject."""
        row = self.db.execute("select formula, tree from subject_formulas where tg_id = ? and subject = ?",
                              (tg_id, sid)).fetchone()
        return None if row is None else {"text": row["formula"], "tree": json.loads(row["tree"]), "own": True}

    def subject_formula(self, sid: int) -> dict | None:
        """{text, tree, own: False} of the subject's formula."""
        row = self.db.execute("select formula, tree from subjects where id = ? and tree is not null",
                              (sid,)).fetchone()
        return None if row is None else {"text": row["formula"], "tree": json.loads(row["tree"]), "own": False}

    def formula_for(self, tg_id: int, sid: int) -> dict | None:
        """The formula a user's grades in a subject are counted by: their own, else the subject's."""
        return self.own_formula(tg_id, sid) or self.subject_formula(sid)

    def plan(self, sid: int) -> list[dict]:
        """Every work of the subject's sheets, graded or not yet, in the subject's order, with the sheet's name."""
        rows = self.db.execute(
            "select w.*, coalesce(nullif(s.course, ''), s.title, '') sheet_name from subject_sheets j "
            "join sheet_works w on w.sheet = j.sheet join sheets s on s.id = j.sheet where j.subject = ? "
            "order by j.rowid, w.position", (sid,))
        return [dict(row) for row in rows]

    def units(self, user: dict, sheets: list[dict]) -> list[Unit]:
        """The user's followed sheets (in their order) as sections: their own subjects first, then their group's."""
        subjects = self.scope(tg_id=user["tg_id"])
        if user["grp"]:
            subjects = self.scope(grp=user["grp"]) + subjects
        mine: dict[int, dict] = {}
        for subject in subjects:
            for sheet in self.sheets_of(subject["id"]):
                mine[sheet] = subject   # the user's own come last and win
        units: list[Unit] = []
        by_subject: dict[int, Unit] = {}
        for sheet in sheets:
            subject = mine.get(sheet["id"])
            if subject is None:
                units.append(Unit.of(sheet))
            elif subject["id"] in by_subject:
                by_subject[subject["id"]].sheets.append(sheet)
            else:
                by_subject[subject["id"]] = Unit(subject["name"], subject["name"], [sheet], subject)
                units.append(by_subject[subject["id"]])
        return units


# --- /subjects -------------------------------------------------------------------------------------


def scope_of(subject: dict) -> str:
    return f"g{subject['grp']}" if subject["grp"] else f"u{subject['tg_id']}"


class SubjectCommands:
    """Mixed into the Bot. `who` is the user who acts, None for the owner."""

    def can_edit(self, who: dict | None, scope: str) -> bool:
        kind, number = scope[:1], scope[1:]
        if not number.isdigit():
            return False
        if who is None:
            return kind == "g" and self.groups.get(int(number)) is not None
        if kind == "u":
            return int(number) == who["tg_id"]
        group = self.admin_group(who)
        return kind == "g" and group is not None and group["id"] == int(number)

    def can_see(self, who: dict | None, scope: str) -> bool:
        return self.can_edit(who, scope) or (who is not None and scope == f"g{who['grp']}" and who["grp"] is not None)

    def candidates(self, scope: str) -> list[dict]:
        """The sheets a subject of the scope may take: a group's shared ones, or the ones a user follows."""
        number = int(scope[1:])
        return self.groups.sheets_of(number) if scope[0] == "g" else self.sheets.followed(number)

    async def cmd_subjects(self, chat: int, who: dict | None, arg: str, lang: str) -> None:
        """`/subjects`: the user's own subjects and their group's; the owner picks a group, `/subjects <group>`
        opens one."""
        if who is None and arg.strip() and (group := self.groups.find(arg.strip())) is not None:
            await self.api.send(chat, self.subjects_view(who, f"g{group['id']}", lang))
            return
        await self.api.send(chat, self.subjects_root(who, lang))

    def subjects_root(self, who: dict | None, lang: str) -> Doc:
        doc = Doc().h(tr(lang, "Subjects", "Предметы")).p(tr(
            lang, "A subject joins the sheets of one course: /grades and the messages of new grades show them as one "
                  "section.",
            "Предмет объединяет таблицы одного курса: в /grades и в сообщениях о новых оценках они идут одной "
            "секцией."))
        if who is None:
            groups = self.groups.all()
            if not groups:
                return doc.p(tr(lang, "No groups yet: /groups.", "Групп пока нет: /groups."))
            for start in range(0, len(groups), 4):
                doc.buttons(*[(g["name"], f"sj:l.g{g['id']}") for g in groups[start:start + 4]])
            return doc
        if who["grp"] and (group := self.groups.get(who["grp"])) is not None:
            self.scope_part(doc, who, f"g{group['id']}", tr(lang, "Group ", "Группа ") + group["name"], lang)
        return self.scope_part(doc, who, f"u{who['tg_id']}", tr(lang, "Mine", "Мои"), lang)

    def subjects_view(self, who: dict | None, scope: str, lang: str) -> Doc:
        if not self.can_see(who, scope):
            return self.subjects_root(who, lang)
        title = tr(lang, "Mine", "Мои") if scope[0] == "u" else \
            tr(lang, "Group ", "Группа ") + self.groups.get(int(scope[1:]))["name"]
        doc = self.scope_part(Doc(), who, scope, title, lang)
        return doc.buttons((tr(lang, "« Subjects", "« Предметы"), "sj:h"))

    def scope_part(self, doc: Doc, who: dict | None, scope: str, title: str, lang: str) -> Doc:
        """A scope's subjects with their sheets; buttons to open them and to add one when `who` may."""
        number = int(scope[1:])
        subjects = self.subjects.scope(grp=number) if scope[0] == "g" else self.subjects.scope(tg_id=number)
        doc.h(title)
        names = {s["id"]: sheet_name(s) for s in self.candidates(scope)}
        if subjects:
            doc.table([], [[b(s["name"]), ", ".join(names.get(sheet) or self.sheet_label(sheet)
                                                    for sheet in self.subjects.sheets_of(s["id"])) or "—"]
                           for s in subjects])
        else:
            doc.p(tr(lang, "No subjects yet.", "Предметов пока нет."))
        if self.can_edit(who, scope):
            opens = [(s["name"], f"sj:o.{s['id']}") for s in subjects]
            for start in range(0, len(opens), 4):
                doc.buttons(*opens[start:start + 4])
            doc.buttons((tr(lang, "New group subject", "Новый предмет группы") if scope[0] == "g" else
                         tr(lang, "New subject of mine", "Новый свой предмет"), f"sj:n.{scope}", "primary"))
        return doc

    def sheet_label(self, sheet: int) -> str:
        found = self.sheets.get(sheet)
        return sheet_name(found) if found else "?"

    def subject_card(self, who: dict | None, sid: int, lang: str) -> Doc:
        subject = self.subjects.get(sid)
        if subject is None or not self.can_edit(who, scope_of(subject)):
            return self.subjects_root(who, lang)
        scope = scope_of(subject)
        inside = self.subjects.sheets_of(sid)
        candidates = self.candidates(scope)
        doc = Doc().h(tr(lang, "Subject ", "Предмет ") + subject["name"])
        if inside:
            doc.table([], [[link(s["title"] or sheet_name(s), s["url"])] for sheet in inside
                           if (s := self.sheets.get(sheet)) is not None])
        else:
            doc.p(tr(lang, "No sheets in it yet: add them with the buttons below.",
                     "Таблиц в нём пока нет: добавь их кнопками ниже."))
        for sheet in inside:
            doc.buttons(("− " + self.sheet_label(sheet), f"sj:t.{sid}.{sheet}"))
        for s in candidates:
            if s["id"] not in inside:
                doc.buttons(("+ " + sheet_name(s), f"sj:p.{sid}.{s['id']}"))
        if not candidates:
            doc.p(tr(lang, "There are no sheets to add: the group has no shared ones.",
                     "Добавить нечего: у группы нет общих таблиц.") if scope[0] == "g" else
                  tr(lang, "There are no sheets to add: follow some with /sheet.",
                     "Добавить нечего: подпишись на таблицы через /sheet."))
        doc.buttons((tr(lang, "Formula", "Формула"), f"fm:s.{sid}"))
        return doc.buttons((tr(lang, "Rename", "Переименовать"), f"sj:r.{sid}"),
                           (tr(lang, "Delete", "Удалить"), f"sj:d.{sid}", "danger"),
                           (tr(lang, "« Back", "« Назад"), f"sj:l.{scope}"))

    def name_step(self, lang: str) -> Step:
        return Step("name", lambda data: Doc().p(tr(lang, "The subject's name, up to 10 characters: it is its "
                                                          "hashtag too.",
                                                    "Имя предмета, до 10 символов: оно же его хэштег.")),
                    check=lambda value, data: subject_name(value),
                    error=lambda lang, e: tr(lang, "The name must be 1 to 10 characters.",
                                             "Имя — от 1 до 10 символов."))

    async def subjects_callback(self, chat: int, message_id: int | None, who: dict | None, arg: str,
                                lang: str) -> str | None:
        action, _, rest = arg.partition(".")
        if action == "h":
            await self.show(chat, message_id, self.subjects_root(who, lang))
            return None
        if action == "l":
            await self.show(chat, message_id, self.subjects_view(who, rest, lang))
            return None
        if action == "n":
            if not self.can_edit(who, rest):
                return None
            number = int(rest[1:])
            scope = {"grp": number} if rest[0] == "g" else {"tg_id": number}
            await self.flow(chat, lang, [self.name_step(lang)],
                            lambda data: self.subject_made(chat, who, data["name"], scope, lang))
            return None
        sid, _, sheet = rest.partition(".")
        subject = self.subjects.get(int(sid)) if sid.isdigit() else None
        if subject is None or not self.can_edit(who, scope_of(subject)):
            return tr(lang, "No such subject", "Такого предмета нет")
        if action == "o":
            await self.show(chat, message_id, self.subject_card(who, subject["id"], lang))
        elif action in ("p", "t") and sheet.isdigit():
            if action == "p":
                if int(sheet) not in {s["id"] for s in self.candidates(scope_of(subject))}:
                    return None
                self.subjects.put(subject["id"], int(sheet))
            else:
                self.subjects.take(subject["id"], int(sheet))
            await self.show(chat, message_id, self.subject_card(who, subject["id"], lang))
        elif action == "r":
            await self.flow(chat, lang, [self.name_step(lang)],
                            lambda data: self.subject_renamed(chat, who, subject["id"], data["name"], lang))
        elif action == "d":
            await self.show(chat, message_id, Doc().p(
                tr(lang, "Delete the subject ", "Удалить предмет "), b(subject["name"]),
                tr(lang, "? Its sheets stay, each as a section of its own.",
                   "? Таблицы останутся, каждая своей секцией.")).buttons(
                (tr(lang, "Yes, delete", "Да, удалить"), f"sj:x.{subject['id']}", "danger"),
                (tr(lang, "Cancel", "Отмена"), f"sj:o.{subject['id']}")))
        elif action == "x":
            self.subjects.delete(subject["id"])
            await self.show(chat, message_id, self.subjects_view(who, scope_of(subject), lang))
            return tr(lang, "Deleted", "Удалён")
        return None

    async def subject_made(self, chat: int, who: dict | None, name: str, scope: dict, lang: str) -> None:
        try:
            subject = self.subjects.create(name, **scope)
        except SubjectError:
            await self.api.send(chat, tr(lang, f"There is a subject «{name}» already.",
                                         f"Предмет «{name}» уже есть."))
            return
        await self.api.send(chat, self.subject_card(who, subject["id"], lang))

    async def subject_renamed(self, chat: int, who: dict | None, sid: int, name: str, lang: str) -> None:
        try:
            self.subjects.rename(sid, name)
        except SubjectError:
            await self.api.send(chat, tr(lang, f"There is a subject «{name}» already.",
                                         f"Предмет «{name}» уже есть."))
            return
        await self.api.send(chat, self.subject_card(who, sid, lang))
