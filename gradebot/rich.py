"""Messages built once and sent either way: as a rich message (Bot API 10.3+, `rich_message.html`) or, when the Bot
API has no rich messages, as a classic HTML message with the same content.

Every text that goes in is escaped here: names from Telegram, titles and headers from the sheets. Only `Html` (made
by the helpers below) passes as it is. Buttons are part of a Doc: inside the message (`<tg-button-row>`) when it is
rich, an inline keyboard under it when it is classic.
"""

import html
from dataclasses import dataclass, field

MAX_CHARS = 32768       # of a rich message (the HTML is counted, which is more than its text)
MAX_BLOCKS = 500        # nested blocks, table rows and details count
MAX_COLUMNS = 20
ROW_BUTTONS = 8
STYLES = ("primary", "success", "danger", "link")

Button = tuple  # (text, callback data or an https:// link[, style])


class Html(str):
    """Markup that is safe in both renderings."""


def esc(text: str) -> Html:
    """Only &, < and >: the Bot API knows few named entities, and quotes need no escaping in text."""
    return text if isinstance(text, Html) else Html(html.escape(str(text), quote=False))


def attr(value: str) -> str:
    return html.escape(str(value), quote=True)


def b(text: str) -> Html:
    return Html(f"<b>{esc(text)}</b>")


def i(text: str) -> Html:
    return Html(f"<i>{esc(text)}</i>")


def code(text: str) -> Html:
    return Html(f"<code>{esc(text)}</code>")


def mark(text: str) -> Html:
    return Html(f"<mark>{esc(text)}</mark>")


def link(text: str, url: str) -> Html:
    href = html.escape(url, quote=False).replace('"', "&quot;")
    return Html(f'<a href="{href}">{esc(text)}</a>')


def join(*parts: str, sep: str = "") -> Html:
    return Html(esc(sep).join(esc(part) for part in parts))


def check_button(button: Button) -> Button:
    text, data, *style = button
    if not data.startswith("https://") and not 1 <= len(data.encode()) <= 64:
        raise ValueError(f"callback_data must be 1-64 bytes: {data!r}")
    if style and style[0] not in (*STYLES, None):
        raise ValueError(f"unknown button style {style[0]!r}")
    return button


@dataclass
class Doc:
    blocks: list[tuple] = field(default_factory=list)

    def h(self, *parts: str) -> "Doc":
        self.blocks.append(("h", join(*parts)))
        return self

    def p(self, *parts: str) -> "Doc":
        self.blocks.append(("p", join(*parts)))
        return self

    def table(self, header: list[str], rows: list[list[str]], align: list[str | None] | None = None) -> "Doc":
        """`align`: per column, None or "right" (numbers)."""
        width = max([len(header), *(len(row) for row in rows)], default=0)
        if width > MAX_COLUMNS:
            raise ValueError(f"a table has at most {MAX_COLUMNS} columns")
        self.blocks.append(("table", [esc(c) for c in header], [[esc(c) for c in row] for row in rows],
                            list(align or [])))
        return self

    def quote(self, text: str, expandable: bool = False) -> "Doc":
        self.blocks.append(("quote", esc(text), expandable))
        return self

    def details(self, summary: str, inner: "Doc", open_: bool = False) -> "Doc":
        self.blocks.append(("details", esc(summary), inner, open_))
        return self

    def buttons(self, *row: Button) -> "Doc":
        """One row of buttons, at most ROW_BUTTONS; an empty row adds nothing."""
        if len(row) > ROW_BUTTONS:
            raise ValueError(f"at most {ROW_BUTTONS} buttons in a row")
        if row:
            self.blocks.append(("buttons", [check_button(button) for button in row]))
        return self

    def extend(self, other: "Doc") -> "Doc":
        self.blocks.extend(other.blocks)
        return self

    def button_rows(self) -> list[list[Button]]:
        """Every row of buttons, those inside details too, in order."""
        rows = []
        for kind, *rest in self.blocks:
            if kind == "buttons":
                rows.append(rest[0])
            elif kind == "details":
                rows += rest[1].button_rows()
        return rows

    def count(self) -> int:
        """Blocks as the Bot API counts them: table rows and the blocks inside details too."""
        total = 0
        for kind, *rest in self.blocks:
            total += 1
            if kind == "table":
                total += len(rest[1]) + bool(rest[0])
            elif kind == "details":
                total += rest[1].count()
        return total

    def fits(self) -> bool:
        return self.count() <= MAX_BLOCKS and len(self.rich()) <= MAX_CHARS

    def rich(self) -> str:
        out = []
        for kind, *rest in self.blocks:
            if kind == "h":
                out.append(f"<h3>{rest[0]}</h3>")
            elif kind == "p":
                out.append(f"<p>{rest[0]}</p>")
            elif kind == "table":
                header, rows, align = rest
                cells = ["<tr>" + "".join(cell("th", c, align, n) for n, c in enumerate(header)) + "</tr>"] \
                    if header else []
                cells += ["<tr>" + "".join(cell("td", c, align, n) for n, c in enumerate(row)) + "</tr>"
                          for row in rows]
                out.append("<table bordered striped compact>" + "".join(cells) + "</table>")
            elif kind == "quote":
                out.append(f"<blockquote{' expandable' if rest[1] else ''}>{rest[0]}</blockquote>")
            elif kind == "details":
                summary, inner, open_ = rest
                out.append(f"<details{' open' if open_ else ''}><summary>{summary}</summary>{inner.rich()}</details>")
            elif kind == "buttons":
                out.append("<tg-button-row>" + "".join(rich_button(button) for button in rest[0])
                           + "</tg-button-row>")
        return "".join(out)

    def plain(self) -> str:
        """Classic parse_mode HTML: headings in bold, a table as a line per row; buttons go to the keyboard."""
        out = []
        for kind, *rest in self.blocks:
            if kind == "h":
                out.append(f"<b>{rest[0]}</b>")
            elif kind == "p":
                out.append(rest[0])
            elif kind == "table":
                header, rows, _ = rest
                lines = [f"<i>{' · '.join(header)}</i>"] if header else []
                out.append("\n".join(lines + [" · ".join(c for c in row if c) for row in rows]))
            elif kind == "quote":
                out.append(f"<blockquote{' expandable' if rest[1] else ''}>{rest[0]}</blockquote>")
            elif kind == "details":
                inner = rest[1].plain()
                out.append(f"<b>{rest[0]}</b>" + (f"\n{inner}" if inner else ""))
        return "\n\n".join(part.replace("<mark>", "<u>").replace("</mark>", "</u>") for part in out)


def cell(tag: str, content: str, align: list[str | None], n: int) -> str:
    where = f' align="{align[n]}"' if n < len(align) and align[n] else ""
    return f"<{tag}{where}>{content}</{tag}>"


def rich_button(button: Button) -> str:
    text, data, *style = button
    how = f'type="url" url="{attr(data)}"' if data.startswith("https://") else \
        f'type="callback_data" data="{attr(data)}"'
    if style and style[0]:
        how += f' style="{style[0]}"'
    return f"<tg-button {how}>{esc(text)}</tg-button>"


def keyboard(rows: list[list[Button]]) -> dict | None:
    """Inline buttons under a message: rows of (text, callback_data or https:// link[, style])."""
    rows = [row for row in rows if row]
    if not rows:
        return None
    return {"inline_keyboard": [[inline_button(check_button(button)) for button in row] for row in rows]}


def inline_button(button: Button) -> dict:
    text, data, *_ = button
    return {"text": text, "url": data} if data.startswith("https://") else {"text": text, "callback_data": data}
