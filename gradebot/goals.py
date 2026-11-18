"""/goal and the outlook of a subject's grade: a line under the subject in /grades, and under its new grades when the
user has a goal in it.

The outlook comes from the subject's formula (the user's own, else the subject's) and the user's grades in the
subject's sheets (formula.outlook): the range the final grade may still take, a forecast, and for a goal what is
needed on what is left, or that it is reached or out of reach. Without a formula there is nothing to count: a goal
then only asks for one.

Callbacks `gl:<action>.<arg>`: `s.<subject>` shows a subject's goal, `n.<sheet>` makes a subject of a lone sheet
first, `v.<subject>.<goal>` sets the goal (0 drops it).
"""

from .cards import score
from .formula import Formula, Outlook, outlook
from .i18n import tr
from .rich import Doc, b, i
from .subjects import Unit, subject_key

GOALS = range(4, 11)


def outlook_text(view: Outlook, lang: str) -> str:
    """«From 5 to 9 · forecast 8 · 8 needs 7.4 on average on what is left»."""
    goal = None if view.goal is None else int(view.goal)
    if view.settled:
        text = (tr(lang, "Final grade: ", "Итог: ") if not view.open else
                tr(lang, "What is left no longer changes the final grade: ",
                   "Итог уже не зависит от оставшегося: ")) + score(view.low)
        if goal is not None:
            text += " · " + (tr(lang, f"goal {goal} reached", f"цель {goal} набрана") if view.reached else
                             tr(lang, f"goal {goal} not reached", f"цель {goal} не набрана"))
        return text
    parts = [tr(lang, f"The final grade will be {score(view.low)} to {score(view.high)}",
                f"Итог будет от {score(view.low)} до {score(view.high)}")]
    if view.forecast is not None:
        parts.append(tr(lang, f"forecast {score(view.forecast)}", f"прогноз {score(view.forecast)}"))
    if goal is not None:
        if view.reached:
            parts.append(tr(lang, f"goal {goal} is sure even with zeros", f"цель {goal} уже гарантирована"))
        elif view.out_of_reach:
            parts.append(tr(lang, f"goal {goal} is out of reach", f"цель {goal} недостижима"))
        elif view.need is not None and len(view.open) == 1:
            parts.append(tr(lang, f"{goal} needs {score(view.need)} in «{view.open[0]}»",
                            f"до {goal} нужно {score(view.need)} за «{view.open[0]}»"))
        elif view.need is not None:
            parts.append(tr(lang, f"{goal} needs {score(view.need)} on average on what is left",
                            f"до {goal} нужно в среднем {score(view.need)} на оставшемся"))
    return " · ".join(parts)


class GoalCommands:
    """Mixed into the Bot."""

    def subject_formula_of(self, user: dict, sid: int) -> Formula | None:
        """The formula of a subject with the user's grades in it, or None without a formula."""
        found = self.subjects.formula_for(user["tg_id"], sid)
        if found is None:
            return None
        grades = {subject_key(item): item for sheet in self.subjects.sheets_of(sid)
                  for item in self.grades.items(user["tg_id"], sheet)}
        return Formula(found["tree"], self.subjects.plan(sid), grades)

    def outlook_note(self, user: dict, sid: int, lang: str, goal_only: bool = False) -> str:
        """The line under a subject: the outlook; with `goal_only`, only when the user has a goal in it."""
        goal = self.subjects.goal(user["tg_id"], sid)
        if goal_only and goal is None:
            return ""
        formula = self.subject_formula_of(user, sid)
        if formula is None:
            return tr(lang, f"Goal {goal}: to count it, the subject needs a formula: /formula",
                      f"Цель {goal}: чтобы её считать, предмету нужна формула: /formula") if goal else ""
        return outlook_text(outlook(formula, goal), lang)

    def with_notes(self, user: dict, units: list[Unit], goal_only: bool = False) -> list[Unit]:
        for unit in units:
            if unit.subject is not None:
                unit.note = self.outlook_note(user, unit.subject["id"], user["lang"], goal_only)
        return units

    async def cmd_goal(self, chat: int, user: dict, arg: str) -> None:
        """`/goal`: the user's subjects and lone sheets to pick from, or the one there is."""
        lang = user["lang"]
        units = self.subjects.units(user, self.sheets.followed(user["tg_id"]))
        if not units:
            await self.api.send(chat, Doc().p(self.no_sheets(lang)).buttons(self.add_button(lang)))
            return
        if len(units) == 1 and units[0].subject is not None:
            await self.api.send(chat, self.goal_view(user, units[0].subject["id"]))
            return
        doc = Doc().h(tr(lang, "Goal", "Цель")).p(tr(
            lang, "In which subject? A sheet in no subject becomes a subject of its own.",
            "По какому предмету? Таблица вне предметов станет своим предметом."))
        choice = [(u.name, f"gl:s.{u.subject['id']}" if u.subject else f"gl:n.{u.sheets[0]['id']}") for u in units]
        for start in range(0, len(choice), 3):
            doc.buttons(*choice[start:start + 3])
        await self.api.send(chat, doc)

    def goal_view(self, user: dict, sid: int) -> Doc:
        lang, subject = user["lang"], self.subjects.get(sid)
        goal = self.subjects.goal(user["tg_id"], sid)
        doc = Doc().h(tr(lang, "Goal · ", "Цель · ") + subject["name"])
        doc.p(tr(lang, "Your goal: ", "Твоя цель: "), b(str(goal))) if goal else \
            doc.p(tr(lang, "No goal yet: pick the grade you aim at.", "Цели пока нет: выбери оценку, к которой идёшь."))
        if note := self.outlook_note(user, sid, lang):
            doc.p(i(note))
        choice = [(str(n), f"gl:v.{sid}.{n}", "primary" if n == goal else None) for n in GOALS]
        doc.buttons(*[c if c[2] else c[:2] for c in choice[:4]])
        doc.buttons(*[c if c[2] else c[:2] for c in choice[4:]],
                    *([(tr(lang, "No goal", "Без цели"), f"gl:v.{sid}.0")] if goal else []))
        return doc.buttons((tr(lang, "Formula", "Формула"), f"fm:s.{sid}"))

    async def goal_callback(self, chat: int, message_id: int, user: dict, arg: str, lang: str) -> str | None:
        action, _, rest = arg.partition(".")
        if action == "n" and rest.isdigit():
            sheet = next((s for s in self.sheets.followed(user["tg_id"]) if s["id"] == int(rest)), None)
            if sheet is None:
                return None
            await self.api.edit(chat, message_id, self.goal_view(user, self.own_subject(user, sheet)["id"]))
            return None
        sid, _, value = rest.partition(".")
        subject = self.subjects.get(int(sid)) if sid.isdigit() else None
        if subject is None or not self.sees(user, subject):
            return tr(lang, "No such subject", "Такого предмета нет")
        if action == "s":
            await self.api.edit(chat, message_id, self.goal_view(user, subject["id"]))
        elif action == "v" and value.isdigit() and (int(value) in GOALS or value == "0"):
            self.subjects.set_goal(user["tg_id"], subject["id"], int(value) or None)
            await self.api.edit(chat, message_id, self.goal_view(user, subject["id"]))
            return tr(lang, "Saved", "Сохранено")
        return None
