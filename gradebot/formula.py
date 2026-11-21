"""A subject's grade formula as a tree, evaluated here: Haiku only turns the teacher's text into the tree.

The tree is JSON: `{"nodes": {name: node}, "final": node}`. Named nodes («Накоп») are shown by name and used with
`ref`. A node is one of:

- `{"type": "part", "name", "agg", "k", "count", "works": [{"key", "weight"}], "match": [prefix]}`: works of a
  kind, 0-10; a work of the plan in no part's `works` joins the part whose `match` its label starts with (a
  homework added after the formula was laid out): the longest prefix wins, so «КР 2» goes to «КР2», not «КР»;
  case, spaces, hyphens and dots aside. `agg`:
  `mean`; `best` (the mean of the `k` best); `weighted` (by each work's weight); `points` (by each work's maximum,
  as the sum of points over the sum of maximums). `count`: how many works the part will have when the formula says
  so; the plan may know fewer yet.
- `{"type": "sum", "terms": [{"weight", "node", "bonus"}]}`: a weighted sum; a bonus term is on top of the weights.
- `{"type": "ref", "name"}`, `{"type": "const", "value"}`.
- `{"type": "round", "mode": "half_up" | "floor" | "ceil", "digits", "node"}`.
- `{"type": "min" | "max", "nodes": [...]}`.
- `{"type": "if", "cond": {"node", "op", "value"}, "then", "else"}`: `op` is <, <=, > or >=.

A work is graded (its grade with the bonus, at most 10) or not yet; one not yet takes the value the caller fills in
for its part. A part with no works yet (the exam is not in the sheet) is one work not graded yet.

The outlook of a goal: the lowest and the highest grade with everything left at one grade from 0 to 10, a forecast
(what is left at the user's own mean of the part, else of the subject), and the least grade on everything left from
which on the goal is reached. All by trying every grade by 0.1: rounding makes the formula a step function, and a
blocking condition («the exam below 4 is the final grade») can even make it go down where the exam crosses 4.
"""

import math
import re
from collections.abc import Callable
from dataclasses import dataclass, field

from .i18n import tr

AGGS = ("mean", "best", "weighted", "points")
ROUNDS = ("half_up", "floor", "ceil")
EPS = 1e-9
OPS = {"<": lambda a, b: a < b - EPS, "<=": lambda a, b: a <= b + EPS,   # 3.9999999 is 4
       ">": lambda a, b: a > b + EPS, ">=": lambda a, b: a >= b - EPS}
MAX_NODES = 200
STEP = 0.1              # the grades the search for what is needed tries
WEIGHTS_OFF = 0.01      # weights of a sum that are this far from 1 are warned of


class FormulaError(ValueError):
    pass


# --- checking --------------------------------------------------------------------------------------


def check(tree: dict) -> dict:
    """The tree as it is, or FormulaError: unknown nodes, missing fields, references to no node or in a circle."""
    if not isinstance(tree, dict) or not isinstance(tree.get("nodes", {}), dict) or "final" not in tree:
        raise FormulaError("a formula is {nodes, final}")
    named, seen = tree.get("nodes") or {}, [0]

    def walk(node, path: tuple[str, ...]) -> None:
        seen[0] += 1
        if seen[0] > MAX_NODES:
            raise FormulaError("the formula is too big")
        if not isinstance(node, dict):
            raise FormulaError("a node is an object")
        kind = node.get("type")
        if kind == "part":
            if not node.get("name") or node.get("agg", "mean") not in AGGS:
                raise FormulaError("a part needs a name and a known agg")
            if node.get("agg") == "best" and not (isinstance(node.get("k"), int) and node["k"] > 0):
                raise FormulaError("best of a part needs k")
            for work in node.get("works") or []:
                if not isinstance(work, dict) or not work.get("key"):
                    raise FormulaError("a work of a part needs its key")
        elif kind == "sum":
            if not node.get("terms"):
                raise FormulaError("a sum needs terms")
            for term in node["terms"]:
                if not isinstance(term.get("weight"), int | float):
                    raise FormulaError("a term needs a weight")
                walk(term.get("node"), path)
        elif kind == "ref":
            name = node.get("name")
            if name not in named:
                raise FormulaError(f"no node «{name}»")
            if name in path:
                raise FormulaError(f"«{name}» refers to itself")
            walk(named[name], (*path, name))
        elif kind == "const":
            if not isinstance(node.get("value"), int | float):
                raise FormulaError("a const needs a value")
        elif kind == "round":
            if node.get("mode", "half_up") not in ROUNDS or not isinstance(node.get("digits", 0), int):
                raise FormulaError("a round needs a known mode")
            walk(node.get("node"), path)
        elif kind in ("min", "max"):
            if not node.get("nodes"):
                raise FormulaError(f"{kind} needs nodes")
            for inner in node["nodes"]:
                walk(inner, path)
        elif kind == "if":
            cond = node.get("cond") or {}
            if cond.get("op") not in OPS or not isinstance(cond.get("value"), int | float):
                raise FormulaError("a condition is a node, an op and a number")
            for inner in (cond.get("node"), node.get("then"), node.get("else")):
                walk(inner, path)
        else:
            raise FormulaError(f"unknown node type {kind!r}")

    walk(tree["final"], ())
    for name, node in named.items():
        walk(node, (name,))
    return tree


def nodes(tree: dict) -> list[dict]:
    """Every node of the tree once, the named ones and the final one with what is inside them."""
    out: list[dict] = []

    def walk(node: dict) -> None:
        out.append(node)
        kind = node["type"]
        inner = [t["node"] for t in node["terms"]] if kind == "sum" else node["nodes"] if kind in ("min", "max") \
            else [node["node"]] if kind == "round" else \
            [node["cond"]["node"], node["then"], node["else"]] if kind == "if" else []
        for child in inner:
            walk(child)

    for node in [*(tree.get("nodes") or {}).values(), tree["final"]]:
        walk(node)
    return out


def parts(tree: dict) -> list[dict]:
    """The parts, each once by name, in the order the tree has them."""
    found: dict[str, dict] = {}
    for node in nodes(tree):
        if node["type"] == "part":
            found.setdefault(node["name"], node)
    return list(found.values())


# --- evaluating ------------------------------------------------------------------------------------


@dataclass
class Work:
    key: str
    value: float | None     # 0-10 with the bonus, at most 10; None: not graded yet
    weight: float = 1.0
    max: float = 10.0


def bare(label: str) -> str:
    """A label or a prefix as matched: «КР-1», «кр 1» and «КР.1» are one."""
    return re.sub(r"[\s\-.]", "", label.casefold())


def matching(found: list[dict], labels: list[str]) -> dict | None:
    """The part whose longest `match` prefix one of the labels starts with; the first such part on a tie."""
    best, length = None, 0
    labels = [bare(label) for label in labels]
    for part in found:
        for prefix in map(bare, part.get("match") or []):
            if len(prefix) > length and any(label.startswith(prefix) for label in labels):
                best, length = part, len(prefix)
    return best


@dataclass
class Formula:
    """A checked tree with a user's grades: `plan` the subject's works (sheet_works rows), `grades` the user's
    (grade_items rows) by key."""
    tree: dict
    plan: list[dict]
    grades: dict[str, dict]
    _works: dict[str, list[Work]] = field(default_factory=dict, init=False)

    def __post_init__(self) -> None:
        planned = {work["key"]: work for work in self.plan}
        found = parts(self.tree)
        listed = {work["key"] for part in found for work in part.get("works") or []}
        joined: dict[str, list[dict]] = {}
        for work in self.plan:
            if work["key"] in listed:
                continue
            part = matching(found, [work.get(name) or "" for name in ("label_ru", "label")])
            if part is not None:
                joined.setdefault(part["name"], []).append({"key": work["key"]})
        for part in found:
            works = []
            for work in [*(part.get("works") or []), *joined.get(part["name"], [])]:
                key = work["key"]
                if key not in planned and key not in self.grades:
                    continue  # a work the sheet no longer has
                grade = self.grades.get(key)
                value = None if grade is None else min(10.0, grade["value"] + (grade["bonus"] or 0))
                maximum = (planned.get(key) or grade)["max"]
                works.append(Work(key, value, float(work.get("weight") or 1), maximum))
            for _ in range(max(1 - len(works), (part.get("count") or 0) - len(works))):
                works.append(Work("", None))  # works the sheet has no column for yet
            self._works[part["name"]] = works

    def works(self, part: str) -> list[Work]:
        return self._works.get(part, [])

    def open_parts(self) -> list[str]:
        """Parts with works not graded yet."""
        return [name for name, works in self._works.items() if any(w.value is None for w in works)]

    def mean(self, part: str | None = None) -> float | None:
        """The mean of the graded works of a part, or of all of them."""
        values = [w.value for name, works in self._works.items() if part in (None, name)
                  for w in works if w.value is not None]
        return sum(values) / len(values) if values else None

    def value(self, fill: Callable[[str], float] | float) -> float:
        """The final grade with the works not graded yet at `fill` (a number, or one per part)."""
        at = fill if callable(fill) else lambda part: fill
        named: dict[str, float] = {}

        def ev(node: dict) -> float:
            kind = node["type"]
            if kind == "part":
                return self._part(node, at(node["name"]))
            if kind == "sum":
                return sum(t["weight"] * ev(t["node"]) for t in node["terms"])
            if kind == "ref":
                if node["name"] not in named:
                    named[node["name"]] = ev(self.tree["nodes"][node["name"]])
                return named[node["name"]]
            if kind == "const":
                return float(node["value"])
            if kind == "round":
                return rounded(ev(node["node"]), node.get("mode", "half_up"), node.get("digits", 0))
            if kind == "min":
                return min(ev(inner) for inner in node["nodes"])
            if kind == "max":
                return max(ev(inner) for inner in node["nodes"])
            cond = node["cond"]
            return ev(node["then"] if OPS[cond["op"]](ev(cond["node"]), cond["value"]) else node["else"])

        return ev(self.tree["final"])

    def _part(self, node: dict, fill: float) -> float:
        works = [(w.value if w.value is not None else fill, w) for w in self.works(node["name"])]
        agg = node.get("agg", "mean")
        if agg == "best":
            best = sorted((v for v, _ in works), reverse=True)[:node["k"]]
            return sum(best) / len(best)
        if agg == "weighted":
            total = sum(w.weight for _, w in works)
            return sum(v * w.weight for v, w in works) / total if total else 0.0
        if agg == "points":
            total = sum(w.max for _, w in works)
            return sum(v * w.max for v, w in works) / total if total else 0.0
        return sum(v for v, _ in works) / len(works)


def rounded(value: float, mode: str, digits: int = 0) -> float:
    """Half up as schools round (7.5 -> 8, not banker's 8 -> 8 and 6.5 -> 6), or down, or up; 7.9999999 is 8."""
    scale = 10 ** digits
    if mode == "floor":
        return math.floor(value * scale + EPS) / scale
    if mode == "ceil":
        return math.ceil(value * scale - EPS) / scale
    return math.floor(value * scale + 0.5 + EPS) / scale


# --- the goal --------------------------------------------------------------------------------------


@dataclass
class Outlook:
    low: float                      # the lowest grade with everything left at one grade
    high: float                     # the highest
    forecast: float | None          # what is left at the user's own mean; None with nothing graded
    open: list[str]                 # parts with works left
    goal: float | None = None
    need: float | None = None       # the least grade on everything left from which on the goal is reached

    @property
    def settled(self) -> bool:
        """Nothing left can change the grade: all is graded, or a condition (an exemption) made the rest moot."""
        return self.high - self.low < EPS

    @property
    def reached(self) -> bool:
        return self.goal is not None and self.low >= self.goal - EPS

    @property
    def out_of_reach(self) -> bool:
        return self.goal is not None and self.high < self.goal - EPS


def outlook(formula: Formula, goal: float | None = None) -> Outlook:
    fills = [round(n * STEP, 1) for n in range(round(10 / STEP) + 1)]
    values = [formula.value(x) for x in fills]
    overall = formula.mean()
    forecast = None if overall is None else \
        formula.value(lambda part: m if (m := formula.mean(part)) is not None else overall)
    out = Outlook(min(values), max(values), forecast, formula.open_parts(), goal)
    if goal is not None and not out.reached and not out.out_of_reach:
        for x, value in reversed(list(zip(fills, values, strict=True))):
            if value < goal - EPS:
                break
            out.need = x
    return out


# --- in words --------------------------------------------------------------------------------------


def number(value: float) -> str:
    return f"{value:g}"


def describe(tree: dict, lang: str) -> list[str]:
    """The formula as lines a person checks: each named node, then the final grade."""
    def text(node: dict, inner: bool = False) -> str:
        kind = node["type"]
        if kind == "part":
            agg = node.get("agg", "mean")
            note = tr(lang, f" (best {node['k']})", f" (лучшие {node['k']})") if agg == "best" else \
                tr(lang, " (by points)", " (по баллам)") if agg == "points" else \
                tr(lang, " (weighted)", " (с весами)") if agg == "weighted" else ""
            return node["name"] + note
        if kind == "sum":
            terms = [("+ " if t.get("bonus") else "") + f"{number(t['weight'])}·{text(t['node'], True)}"
                     for t in node["terms"]]
            joined = " + ".join(terms).replace("+ + ", "+ ")
            return f"({joined})" if inner and len(terms) > 1 else joined
        if kind == "ref":
            return node["name"]
        if kind == "const":
            return number(node["value"])
        if kind == "round":
            how = {"half_up": tr(lang, "round", "округл"), "floor": tr(lang, "floor", "вниз"),
                   "ceil": tr(lang, "ceil", "вверх")}[node.get("mode", "half_up")]
            digits = node.get("digits", 0)
            return f"{how}({text(node['node'])}{f', {digits}' if digits else ''})"
        if kind in ("min", "max"):
            return f"{kind}({', '.join(text(inner) for inner in node['nodes'])})"
        cond = node["cond"]
        return tr(lang, "if ", "если ") + f"{text(cond['node'])} {cond['op']} {number(cond['value'])}" + \
            tr(lang, ": ", ", то ") + text(node["then"]) + tr(lang, ", else ", ", иначе ") + text(node["else"])

    lines = [f"{name} = {text(node)}" for name, node in (tree.get("nodes") or {}).items()]
    return [*lines, tr(lang, "Final = ", "Итог = ") + text(tree["final"])]


def warnings(tree: dict, plan: list[dict]) -> list[tuple[str, str, float | None]]:
    """What a person should look at before saving: (kind, name, number). `weights`: a sum (its name, "" for an
    unnamed one) whose weights, bonuses aside, add up to the number, not 1; `empty`: a part with no works yet;
    `outside`: a work of the subject (its label) in no part."""
    out: list[tuple[str, str, float | None]] = []
    names = {id(node): name for name, node in (tree.get("nodes") or {}).items()}
    for node in nodes(tree):
        if node["type"] == "sum":
            total = sum(t["weight"] for t in node["terms"] if not t.get("bonus"))
            if abs(total - 1) > WEIGHTS_OFF:
                out.append(("weights", names.get(id(node), ""), round(total, 4)))
    planned = {work["key"] for work in plan}
    used = set()
    laid = Formula(tree, plan, {})
    for part in parts(tree):
        keys = {work.key for work in laid.works(part["name"])} & planned
        used |= keys
        if not keys:
            out.append(("empty", part["name"], None))
    for work in plan:
        if work["key"] not in used:
            out.append(("outside", work.get("label_ru") or work["label"], None))
    return out
