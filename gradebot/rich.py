"""Messages built once and sent either way: as a rich message (Bot API 10.1+, `rich_message.html`) or, when the Bot
API has no rich messages, as a classic HTML message with the same content.

Every text that goes in is escaped here: names from Telegram and, later, titles and headers from the sheets. Only
`Html` (made by the helpers below) passes as it is.
"""

import html
from dataclasses import dataclass, field


class Html(str):
    """Markup that is safe in both renderings."""


def esc(text: str) -> Html:
    """Only &, < and >: the Bot API knows few named entities, and quotes need no escaping in text."""
    return text if isinstance(text, Html) else Html(html.escape(str(text), quote=False))


def b(text: str) -> Html:
    return Html(f"<b>{esc(text)}</b>")


def i(text: str) -> Html:
    return Html(f"<i>{esc(text)}</i>")


def code(text: str) -> Html:
    return Html(f"<code>{esc(text)}</code>")


def link(text: str, url: str) -> Html:
    href = html.escape(url, quote=False).replace('"', "&quot;")
    return Html(f'<a href="{href}">{esc(text)}</a>')


def join(*parts: str, sep: str = "") -> Html:
    return Html(esc(sep).join(esc(part) for part in parts))


@dataclass
class Doc:
    blocks: list[tuple] = field(default_factory=list)

    def h(self, text: str) -> "Doc":
        self.blocks.append(("h", esc(text)))
        return self

    def p(self, *parts: str) -> "Doc":
        self.blocks.append(("p", join(*parts)))
        return self

    def table(self, header: list[str], rows: list[list[str]]) -> "Doc":
        self.blocks.append(("table", [esc(c) for c in header], [[esc(c) for c in row] for row in rows]))
        return self

    def quote(self, text: str, expandable: bool = False) -> "Doc":
        self.blocks.append(("quote", esc(text), expandable))
        return self

    def rich(self) -> str:
        out = []
        for kind, *rest in self.blocks:
            if kind == "h":
                out.append(f"<h3>{rest[0]}</h3>")
            elif kind == "p":
                out.append(f"<p>{rest[0]}</p>")
            elif kind == "table":
                header, rows = rest
                cells = ["<tr>" + "".join(f"<th>{c}</th>" for c in header) + "</tr>"] if header else []
                cells += ["<tr>" + "".join(f"<td>{c}</td>" for c in row) + "</tr>" for row in rows]
                out.append("<table bordered striped compact>" + "".join(cells) + "</table>")
            elif kind == "quote":
                out.append(f"<blockquote{' expandable' if rest[1] else ''}>{rest[0]}</blockquote>")
        return "".join(out)

    def plain(self) -> str:
        """Classic parse_mode HTML: headings in bold, a table as a line per row."""
        out = []
        for kind, *rest in self.blocks:
            if kind == "h":
                out.append(f"<b>{rest[0]}</b>")
            elif kind == "p":
                out.append(rest[0])
            elif kind == "table":
                header, rows = rest
                lines = [f"<i>{' · '.join(header)}</i>"] if header else []
                out.append("\n".join(lines + [" · ".join(c for c in row if c) for row in rows]))
            elif kind == "quote":
                out.append(f"<blockquote{' expandable' if rest[1] else ''}>{rest[0]}</blockquote>")
        return "\n\n".join(out)


def keyboard(rows: list[list[tuple[str, str]]]) -> dict | None:
    """Inline buttons: rows of (text, callback_data); callback_data is at most 64 bytes."""
    rows = [row for row in rows if row]
    if not rows:
        return None
    for row in rows:
        for _, data in row:
            if len(data.encode()) > 64:
                raise ValueError(f"callback_data longer than 64 bytes: {data!r}")
    return {"inline_keyboard": [[{"text": text, "callback_data": data} for text, data in row] for row in rows]}
