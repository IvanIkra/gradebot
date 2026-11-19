"""/formula: a subject's grade formula from the teacher's text, by Haiku, checked by a person before it is kept.

Haiku turns the text and the subject's works into a flat list of nodes (structured output has no recursion), which
becomes formula.py's tree here. It also lays the works out by part and gives each part label prefixes, so a work
added later joins its part without asking again. The person sees the formula in words, the works of each part and
what looks wrong, and keeps it with «Correct» or sends the text again.

A subject's formula is set by whoever runs the subject (subjects.py); anyone who sees a subject may keep their own,
which wins over it. A sheet in no subject gets a subject of its own when a formula is set for it.

Callbacks `fm:<action>.<arg>`: `s.<subject>` shows a subject's formula, `n.<sheet>` makes a subject of a lone
sheet and shows it, `e.<subject>.<s|u>` asks for the text (s: the subject's, u: one's own), `ok` keeps the formula
just read, `x.<subject>` drops one's own.
"""

import datetime as dt
import logging
from dataclasses import dataclass

from .dialog import Step
from .formula import Formula, FormulaError, check, describe, parts, warnings
from .i18n import tr
from .llm.budget import BudgetExceeded
from .llm.client import LLMError
from .rich import Doc, b, code, i
from .sheets import course_of
from .subjects import SubjectError, merge_copies, scope_of, subject_name

log = logging.getLogger(__name__)

TEXT_CHARS = 3000
DRAFT_FOR = dt.timedelta(hours=1)
MAX_TOKENS = 4000

FORMULA_SYSTEM = """You turn the grade formula of a university course into a tree of nodes, and lay the course's
graded works out by the parts of the formula.

You get the text the teacher or a student wrote (it may be a whole paragraph of a syllabus: find the formula in it)
and the list of graded works the bot knows, each with an id like w3, its label, its tab and its maximum. Grades
are on a 0-10 scale.

Nodes (each has an id; children are referred to by id):
- part: works of one kind (homeworks, tests, the exam). agg: mean (the default), best (the mean of the k best:
  "4 best of 5", "the worst test is dropped" = best with k = count - 1), weighted (each work its own weight, given
  in works[].weight), points (sum of points over the sum of maximums). works: the ids of the works of this part.
  match: short label prefixes that will catch works of this part added later (for homeworks "ДЗ", "HW"). count:
  how many works the part will have, only when the text says it. A part with no works yet (an exam not in the
  sheet) has works = [] and still its match.
- sum: weighted terms; percentages become fractions (30% -> 0.3). A bonus on top of the weights (activity points
  "plus up to 1 point") is a term with bonus = true.
- round: args = [the node rounded]; mode half_up (ordinary rounding, the default when the text says "rounded"),
  floor or ceil; digits after the point. Only when the text says it is rounded.
- min / max: args = the nodes; "at most 10" is min with a const 10.
- const: value.
- if: cond = {node, op, value}, then, else. A blocking exam ("an exam below 4 means the final grade is the exam")
  is if(exam < 4, exam, rest). An exemption ("with an accumulated 8 or more the exam may be skipped and the final is
  the accumulated grade") is if(accumulated >= 8, accumulated, rest).

name: the name of the node as the text calls it, short, in the text's language ("Накоп", "ДЗ", "Экзамен"); a part
always has a name; another node has one only when the text names it (an accumulated grade), else "". final: the id
of the node that is the final grade.

Use only what the text says; do not invent weights, rounding or conditions. Every work goes into at most one part;
works the formula does not use stay out. When the text has no grade formula, found = false and reason says why.
Fields a node type does not use: empty strings, empty lists, null, 0 or false."""

ID = {"type": "string"}
NUMBER_OR_NULL = {"anyOf": [{"type": "number"}, {"type": "null"}]}
INT_OR_NULL = {"anyOf": [{"type": "integer"}, {"type": "null"}]}


def strict(properties: dict) -> dict:
    return {"type": "object", "properties": properties, "required": list(properties), "additionalProperties": False}


FORMULA_SCHEMA = strict({
    "found": {"type": "boolean"},
    "reason": {"type": "string"},
    "final": ID,
    "nodes": {"type": "array", "items": strict({
        "id": ID,
        "type": {"type": "string", "enum": ["part", "sum", "const", "round", "min", "max", "if"]},
        "name": {"type": "string"},
        "agg": {"type": "string", "enum": ["mean", "best", "weighted", "points"]},
        "k": INT_OR_NULL,
        "count": INT_OR_NULL,
        "works": {"type": "array", "items": strict({"work": ID, "weight": NUMBER_OR_NULL})},
        "match": {"type": "array", "items": {"type": "string"}},
        "terms": {"type": "array", "items": strict({"weight": {"type": "number"}, "node": ID,
                                                     "bonus": {"type": "boolean"}})},
        "mode": {"type": "string", "enum": ["half_up", "floor", "ceil"]},
        "digits": {"type": "integer"},
        "args": {"type": "array", "items": ID},
        "value": NUMBER_OR_NULL,
        "cond": {"anyOf": [strict({"node": ID, "op": {"type": "string", "enum": ["<", "<=", ">", ">="]},
                                   "value": {"type": "number"}}), {"type": "null"}]},
        "then": ID,
        "else": ID,
    })},
})


def prompt(text: str, plan: list[dict]) -> str:
    works = "\n".join(f"w{n}: «{w.get('label_ru') or w['label']}» ({w['label']}), tab «{w['tab']}», "
                      f"sheet «{w.get('sheet_name') or ''}», max {w['max']:g}" for n, w in enumerate(plan, 1))
    return f"The formula:\n{text}\n\nThe works:\n{works or '(none known yet)'}"


def to_tree(answer: dict, plan: list[dict]) -> dict:
    """The flat answer as formula.py's tree; FormulaError when it is no formula or does not hold together."""
    if not answer.get("found"):
        raise FormulaError(answer.get("reason") or "no formula in the text")
    keys = {f"w{n}": w["key"] for n, w in enumerate(plan, 1)}
    by_id = {node["id"]: node for node in answer.get("nodes") or []}
    named: dict[str, dict] = {}
    names: dict[str, str] = {}      # a named node's name -> its id: two nodes of one name get two names

    def build(nid: str, path: tuple[str, ...], top: bool = False) -> dict:
        if nid in path:
            raise FormulaError(f"node {nid} refers to itself")
        node = by_id.get(nid)
        if node is None:
            raise FormulaError(f"no node {nid}")
        name = (node.get("name") or "").strip()
        if node["type"] != "part" and name and not top:
            while names.setdefault(name, nid) != nid:
                name += "'"
            if name not in named:
                named[name] = {}   # taken before it is built: a circle meets the path check above
                named[name] = inner(node, (*path, nid))
            return {"type": "ref", "name": name}
        return inner(node, (*path, nid))

    def inner(node: dict, path: tuple[str, ...]) -> dict:
        kind = node["type"]
        args = node.get("args") or []
        if kind == "part":
            return {"type": "part", "name": (node.get("name") or node["id"]).strip(), "agg": node.get("agg") or "mean",
                    "k": node.get("k"), "count": node.get("count"), "match": node.get("match") or [],
                    "works": [{"key": keys[w["work"]], "weight": w.get("weight") or 1}
                              for w in node.get("works") or [] if w.get("work") in keys]}
        if kind == "sum":
            return {"type": "sum", "terms": [{"weight": t["weight"], "node": build(t["node"], path),
                                              "bonus": bool(t.get("bonus"))} for t in node.get("terms") or []]}
        if kind == "const":
            return {"type": "const", "value": node.get("value") or 0}
        if kind == "round":
            if not args:
                raise FormulaError("a round of nothing")
            return {"type": "round", "mode": node.get("mode") or "half_up", "digits": node.get("digits") or 0,
                    "node": build(args[0], path)}
        if kind in ("min", "max"):
            return {"type": kind, "nodes": [build(a, path) for a in args]}
        cond = node.get("cond") or {}
        if not cond:
            raise FormulaError("an if without a condition")
        return {"type": "if", "cond": {"node": build(cond["node"], path), "op": cond["op"], "value": cond["value"]},
                "then": build(node["then"], path), "else": build(node["else"], path)}

    tree = {"nodes": {}, "final": build(answer.get("final") or "", (), top=True)}
    tree["nodes"] = named
    return check(tree)


def formula_text(value: str) -> str:
    if not 0 < len(value) <= TEXT_CHARS:
        raise ValueError(f"1 to {TEXT_CHARS} characters")
    return value


@dataclass
class Draft:
    """A formula read and shown, waiting for «Correct»."""
    subject: int
    target: str     # "s": the subject's, "u": the user's own
    text: str
    tree: dict
    at: dt.datetime


class FormulaCommands:
    """Mixed into the Bot. `who` is the user who acts, None for the owner."""

    @property
    def drafts(self) -> dict[int, Draft]:
        if not hasattr(self, "_drafts"):
            self._drafts = {}
        return self._drafts

    def sees(self, who: dict | None, subject: dict) -> bool:
        """A user's own subject, or one of their group; the owner sees every group's."""
        if who is None:
            return subject["grp"] is not None
        return subject["tg_id"] == who["tg_id"] or (subject["grp"] is not None and subject["grp"] == who["grp"])

    async def cmd_formula(self, chat: int, user: dict, arg: str) -> None:
        """`/formula`: the user's subjects and lone sheets to pick from, or the one there is."""
        lang = user["lang"]
        units = self.subjects.units(user, self.sheets.followed(user["tg_id"]))
        if not units:
            await self.api.send(chat, Doc().p(self.no_sheets(lang)).buttons(self.add_button(lang)))
            return
        if len(units) == 1:
            unit = units[0]
            doc = self.formula_view(user, unit.subject["id"], lang) if unit.subject else None
            await self.api.send(chat, doc or self.lone_sheet_view(lang, unit.sheets[0]))
            return
        doc = Doc().h(tr(lang, "Formula", "Формула")).p(tr(
            lang, "Which subject's? A sheet in no subject becomes a subject of its own.",
            "Для какого предмета? Таблица вне предметов станет своим предметом."))
        choice = [(u.name, f"fm:s.{u.subject['id']}" if u.subject else f"fm:n.{u.sheets[0]['id']}") for u in units]
        for start in range(0, len(choice), 3):
            doc.buttons(*choice[start:start + 3])
        await self.api.send(chat, doc)

    def lone_sheet_view(self, lang: str, sheet: dict) -> Doc:
        return Doc().p(tr(lang, "A formula belongs to a subject: the sheet becomes a subject of its own.",
                          "Формула задаётся предмету: таблица станет своим предметом.")).buttons(
            (tr(lang, "Go on", "Дальше"), f"fm:n.{sheet['id']}", "primary"))

    def own_subject(self, user: dict, sheet: dict) -> dict:
        """A subject of the user's own made of a lone sheet: named by its course, numbered when the name is taken."""
        base = subject_name(course_of(sheet)[:10])
        for n in range(1, 100):
            name = base if n == 1 else subject_name(f"{base[:10 - len(str(n))]}{n}")
            try:
                subject = self.subjects.create(name, tg_id=user["tg_id"])
            except SubjectError:
                continue
            self.subjects.put(subject["id"], sheet["id"])
            return subject
        raise SubjectError("no free name")

    def formula_view(self, who: dict | None, sid: int, lang: str) -> Doc | None:
        """A subject's formula in words, and what one may do with it."""
        subject = self.subjects.get(sid)
        if subject is None or not self.sees(who, subject):
            return None
        doc = Doc().h(tr(lang, "Formula · ", "Формула · ") + subject["name"])
        own = self.subjects.own_formula(who["tg_id"], sid) if who is not None else None
        shared = self.subjects.subject_formula(sid)
        current = own or shared
        if current is None:
            doc.p(tr(lang, "No formula yet. Send it as the teacher wrote it: I'll lay it out and show you before "
                           "keeping it.",
                     "Формулы пока нет. Пришли её, как написал преподаватель: я разберу её и покажу, прежде чем "
                     "сохранить."))
        else:
            if current["own"]:
                doc.p(i(tr(lang, "Your own formula: it counts for you instead of the subject's.",
                           "Твоя формула: для тебя она вместо формулы предмета.")))
            elif subject["grp"] is not None:
                doc.p(i(tr(lang, "The formula of the group's subject.", "Формула предмета группы.")))
            doc.quote("\n".join(describe(current["tree"], lang)))
        may_set = self.can_edit(who, scope_of(subject))
        buttons = []
        if may_set:
            verb = tr(lang, "Set", "Задать") if shared is None else tr(lang, "Change", "Изменить")
            whose = tr(lang, " for the group", " для группы") if subject["grp"] else ""
            buttons.append((verb + whose, f"fm:e.{sid}.s"))
        if who is not None and subject["tg_id"] is None:
            buttons.append((tr(lang, "Own formula", "Своя формула"), f"fm:e.{sid}.u"))
        if own is not None:
            buttons.append((tr(lang, "Drop own", "Сбросить свою"), f"fm:x.{sid}", "danger"))
        return doc.buttons(*buttons) if buttons else doc

    def text_step(self, lang: str) -> Step:
        return Step("text", lambda data: Doc().p(tr(
            lang, "Send the formula as the teacher wrote it, or the paragraph of the syllabus with it. For example:",
            "Пришли формулу, как её написал преподаватель, или абзац программы курса с ней. Например:")).p(code(tr(
                lang, "Final = round(0.4·Accumulated + 0.6·Exam); Accumulated = 0.5·HW (4 best of 5) + 0.5·Tests; "
                      "an exam below 4 is the final grade",
                "Итог = округл(0.4·Накоп + 0.6·Экз); Накоп = 0.5·ДЗ (4 лучших из 5) + 0.5·КР; экзамен ниже 4 — "
                "итог равен экзамену"))),
            check=lambda value, data: formula_text(value),
            error=lambda lang, e: tr(lang, f"The text must be 1 to {TEXT_CHARS} characters.",
                                     f"Текст — от 1 до {TEXT_CHARS} символов."))

    async def formula_callback(self, chat: int, message_id: int | None, who: dict | None, arg: str,
                               lang: str) -> str | None:
        action, _, rest = arg.partition(".")
        if action == "ok":
            return await self.keep_formula(chat, message_id, who, lang)
        if action == "n" and who is not None and rest.isdigit():
            sheet = next((s for s in self.sheets.followed(who["tg_id"]) if s["id"] == int(rest)), None)
            if sheet is None:
                return None
            subject = self.own_subject(who, sheet)
            await self.show(chat, message_id, self.formula_view(who, subject["id"], lang))
            return None
        sid, _, target = rest.partition(".")
        subject = self.subjects.get(int(sid)) if sid.isdigit() else None
        if subject is None or not self.sees(who, subject):
            return tr(lang, "No such subject", "Такого предмета нет")
        if action == "s":
            await self.show(chat, message_id, self.formula_view(who, subject["id"], lang))
        elif action == "e" and target in ("s", "u"):
            if target == "s" and not self.can_edit(who, scope_of(subject)) or target == "u" and who is None:
                return None
            await self.flow(chat, lang, [self.text_step(lang)],
                            lambda data: self.read_formula(chat, who, subject, target, data["text"], lang))
        elif action == "x" and who is not None:
            self.subjects.set_formula(subject["id"], None, None, who["tg_id"])
            await self.show(chat, message_id, self.formula_view(who, subject["id"], lang))
            return tr(lang, "Dropped", "Сброшена")
        return None

    async def read_formula(self, chat: int, who: dict | None, subject: dict, target: str, text: str,
                           lang: str) -> None:
        llm = getattr(self, "llm", None)
        if llm is None or not llm.available:
            await self.api.send(chat, tr(lang, "Reading formulas is off: the bot has no Haiku key.",
                                         "Разбор формул выключен: у бота нет ключа Haiku."))
            return
        plan, _ = merge_copies(self.subjects.plan(subject["id"]))   # a work the sheets repeat, once
        try:
            answer = await llm.json("formula", FORMULA_SYSTEM, prompt(text, plan), FORMULA_SCHEMA,
                                    max_tokens=MAX_TOKENS)
            tree = to_tree(answer, plan)
        except BudgetExceeded:
            await self.api.send(chat, tr(lang, "The monthly limit for Haiku is spent: try next month.",
                                         "Месячный лимит на Haiku исчерпан: попробуй в следующем месяце."))
            return
        except LLMError as e:
            log.warning("formula of subject %s: %s", subject["id"], e)
            await self.api.send(chat, tr(lang, "Haiku did not answer: try again in a while.",
                                         "Haiku не ответил: попробуй чуть позже."))
            return
        except FormulaError as e:
            await self.api.send(chat, Doc().p(tr(lang, "I could not make a formula of it: ",
                                                 "Не получилось разобрать формулу: "), str(e), ".").buttons(
                (tr(lang, "Send again", "Прислать заново"), f"fm:e.{subject['id']}.{target}")))
            return
        self.drafts[chat] = Draft(subject["id"], target, text, tree, self.now())
        await self.api.send(chat, self.draft_view(subject, target, tree, plan, lang))

    def draft_view(self, subject: dict, target: str, tree: dict, plan: list[dict], lang: str) -> Doc:
        doc = Doc().h(tr(lang, "Check the formula · ", "Проверь формулу · ") + subject["name"])
        doc.quote("\n".join(describe(tree, lang)))
        laid = Formula(tree, plan, {})
        labels = {w["key"]: w.get("label_ru") or w["label"] for w in plan} if lang == "ru" else \
            {w["key"]: w["label"] for w in plan}
        doc.table([tr(lang, "Part", "Часть"), tr(lang, "Works", "Работы")],
                  [[b(p["name"]), ", ".join(labels[w.key] for w in laid.works(p["name"]) if w.key in labels) or
                    tr(lang, "none yet", "пока нет")] for p in parts(tree)])
        for kind, name, total in warnings(tree, plan):
            if kind == "weights":
                where = f"«{name}»" if name else tr(lang, "the formula", "формуле")
                doc.p("⚠️ " + tr(lang, f"The weights of {where} add up to {total:g}, not 1.",
                                 f"Веса в {where} дают {total:g}, а не 1."))
            elif kind == "empty":
                doc.p("⚠️ " + tr(lang, f"«{name}»: no works in the sheets yet, counted as one to come.",
                                 f"«{name}»: в таблицах пока нет работ, считаю одной будущей."))
            else:
                doc.p("⚠️ " + tr(lang, f"«{name}» is in no part.", f"«{name}» не входит ни в одну часть."))
        whose = tr(lang, "for the whole group", "для всей группы") if target == "s" and subject["grp"] else \
            tr(lang, "for you only", "только для тебя") if target == "u" else \
            tr(lang, "for the subject", "для предмета")
        doc.p(i(tr(lang, "It will be kept ", "Сохраню ") + whose + "."))
        return doc.buttons((tr(lang, "Correct", "Верно"), "fm:ok", "success"),
                           (tr(lang, "Send again", "Прислать заново"), f"fm:e.{subject['id']}.{target}"))

    async def keep_formula(self, chat: int, message_id: int | None, who: dict | None, lang: str) -> str | None:
        draft = self.drafts.pop(chat, None)
        if draft is None or self.now() - draft.at > DRAFT_FOR:
            return tr(lang, "This formula is no longer waiting: send it again", "Эта формула уже не ждёт: пришли "
                                                                                 "заново")
        subject = self.subjects.get(draft.subject)
        if subject is None or not self.sees(who, subject):
            return None
        if draft.target == "s":
            if not self.can_edit(who, scope_of(subject)):
                return None
            self.subjects.set_formula(subject["id"], draft.text, draft.tree)
        else:
            self.subjects.set_formula(subject["id"], draft.text, draft.tree, who["tg_id"])
        await self.show(chat, message_id, self.formula_view(who, subject["id"], lang))
        return tr(lang, "Kept", "Сохранено")
