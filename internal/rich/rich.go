// Package rich builds messages once and sends them either way: as a rich message (Bot API 10.3+,
// `rich_message.html`) or, when the Bot API has no rich messages, as a classic HTML message with the same content.
//
// Every text that goes in is escaped here: names from Telegram, titles and headers from the sheets. Only HTML (made
// by the helpers below) passes as it is. Buttons are part of a Doc: inside the message (`<tg-button-row>`) when it
// is rich, an inline keyboard under it when it is classic.
package rich

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	MaxColumns = 20
	RowButtons = 8
)

// The limits of a rich message; tests lower them.
var (
	MaxChars  = 32768 // of a rich message (the HTML is counted, which is more than its text)
	MaxBlocks = 500   // nested blocks, table rows and details count
)

// Styles a button may have.
var Styles = []string{"primary", "success", "danger", "link"}

// HTML is markup that is safe in both renderings.
type HTML string

// Button is a callback button or an https:// link, with an optional style.
type Button struct {
	Text, Data, Style string
}

// B is a button without a style.
func B(text, data string) Button { return Button{Text: text, Data: data} }

// BS is a button with a style.
func BS(text, data, style string) Button { return Button{Text: text, Data: data, Style: style} }

var escaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
var attrEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#x27;")

// Esc escapes text: only &, < and >, since the Bot API knows few named entities and quotes need no escaping in
// text. HTML passes as it is; other values are written as text first.
func Esc(part any) HTML {
	switch v := part.(type) {
	case HTML:
		return v
	case string:
		return HTML(escaper.Replace(v))
	case nil:
		return ""
	default:
		return HTML(escaper.Replace(fmt.Sprint(v)))
	}
}

// Attr escapes an attribute value.
func Attr(value string) string { return attrEscaper.Replace(value) }

func Bold(text any) HTML      { return HTML("<b>" + string(Esc(text)) + "</b>") }
func Italic(text any) HTML    { return HTML("<i>" + string(Esc(text)) + "</i>") }
func Code(text any) HTML      { return HTML("<code>" + string(Esc(text)) + "</code>") }
func Underline(text any) HTML { return HTML("<u>" + string(Esc(text)) + "</u>") }

// Mark is something new: bold and underlined. Not <mark>: Telegram paints it pale yellow under white text on phones.
func Mark(text any) HTML { return HTML("<b><u>" + string(Esc(text)) + "</u></b>") }

// Link is a link with escaped text.
func Link(text any, url string) HTML {
	href := strings.ReplaceAll(escaper.Replace(url), `"`, "&quot;")
	return HTML(`<a href="` + href + `">` + string(Esc(text)) + "</a>")
}

// Join escapes the parts and joins them.
func Join(parts ...any) HTML {
	return JoinSep("", parts...)
}

// JoinSep escapes the parts and joins them with sep.
func JoinSep(sep any, parts ...any) HTML {
	out := make([]string, len(parts))
	for n, part := range parts {
		out[n] = string(Esc(part))
	}
	return HTML(strings.Join(out, string(Esc(sep))))
}

// CheckButton panics on a button Telegram would refuse.
func CheckButton(button Button) Button {
	if !strings.HasPrefix(button.Data, "https://") && (len(button.Data) < 1 || len(button.Data) > 64) {
		panic(fmt.Sprintf("callback_data must be 1-64 bytes: %q", button.Data))
	}
	if button.Style != "" {
		known := false
		for _, s := range Styles {
			known = known || s == button.Style
		}
		if !known {
			panic(fmt.Sprintf("unknown button style %q", button.Style))
		}
	}
	return button
}

type kind int

const (
	heading kind = iota
	paragraph
	table
	quote
	details
	buttons
)

type block struct {
	kind       kind
	text       HTML // heading, paragraph, quote; the summary of details
	header     []HTML
	rows       [][]HTML
	align      []string
	expandable bool // quote
	inner      *Doc // details
	open       bool // details
	buttons    []Button
}

// Doc is a message.
type Doc struct {
	blocks []block
}

// New makes an empty Doc.
func New() *Doc { return &Doc{} }

// Empty tells a Doc with no blocks.
func (d *Doc) Empty() bool { return len(d.blocks) == 0 }

// Len is the number of top-level blocks.
func (d *Doc) Len() int { return len(d.blocks) }

func (d *Doc) H(parts ...any) *Doc {
	d.blocks = append(d.blocks, block{kind: heading, text: Join(parts...)})
	return d
}

func (d *Doc) P(parts ...any) *Doc {
	d.blocks = append(d.blocks, block{kind: paragraph, text: Join(parts...)})
	return d
}

// Table adds a table; align is per column, "" or "right" (numbers).
func (d *Doc) Table(header []string, rows [][]string, align []string) *Doc {
	width := len(header)
	for _, row := range rows {
		width = max(width, len(row))
	}
	if width > MaxColumns {
		panic(fmt.Sprintf("a table has at most %d columns", MaxColumns))
	}
	b := block{kind: table, align: append([]string(nil), align...)}
	for _, c := range header {
		b.header = append(b.header, Esc(c))
	}
	for _, row := range rows {
		cells := make([]HTML, len(row))
		for n, c := range row {
			cells[n] = Esc(c)
		}
		b.rows = append(b.rows, cells)
	}
	d.blocks = append(d.blocks, b)
	return d
}

// TableHTML is Table with cells that may hold markup.
func (d *Doc) TableHTML(header []any, rows [][]any, align []string) *Doc {
	width := len(header)
	for _, row := range rows {
		width = max(width, len(row))
	}
	if width > MaxColumns {
		panic(fmt.Sprintf("a table has at most %d columns", MaxColumns))
	}
	b := block{kind: table, align: append([]string(nil), align...)}
	for _, c := range header {
		b.header = append(b.header, Esc(c))
	}
	for _, row := range rows {
		cells := make([]HTML, len(row))
		for n, c := range row {
			cells[n] = Esc(c)
		}
		b.rows = append(b.rows, cells)
	}
	d.blocks = append(d.blocks, b)
	return d
}

func (d *Doc) Quote(text any, expandable bool) *Doc {
	d.blocks = append(d.blocks, block{kind: quote, text: Esc(text), expandable: expandable})
	return d
}

func (d *Doc) Details(summary any, inner *Doc, open bool) *Doc {
	d.blocks = append(d.blocks, block{kind: details, text: Esc(summary), inner: inner, open: open})
	return d
}

// Buttons adds one row of buttons, at most RowButtons; an empty row adds nothing.
func (d *Doc) Buttons(row ...Button) *Doc {
	if len(row) > RowButtons {
		panic(fmt.Sprintf("at most %d buttons in a row", RowButtons))
	}
	if len(row) > 0 {
		checked := make([]Button, len(row))
		for n, button := range row {
			checked[n] = CheckButton(button)
		}
		d.blocks = append(d.blocks, block{kind: buttons, buttons: checked})
	}
	return d
}

// Extend adds the blocks of another Doc.
func (d *Doc) Extend(other *Doc) *Doc {
	d.blocks = append(d.blocks, other.blocks...)
	return d
}

// ButtonRows is every row of buttons, those inside details too, in order.
func (d *Doc) ButtonRows() [][]Button {
	var rows [][]Button
	for _, b := range d.blocks {
		switch b.kind {
		case buttons:
			rows = append(rows, b.buttons)
		case details:
			rows = append(rows, b.inner.ButtonRows()...)
		}
	}
	return rows
}

// Count is the blocks as the Bot API counts them: table rows and the blocks inside details too.
func (d *Doc) Count() int {
	total := 0
	for _, b := range d.blocks {
		total++
		switch b.kind {
		case table:
			total += len(b.rows)
			if len(b.header) > 0 {
				total++
			}
		case details:
			total += b.inner.Count()
		}
	}
	return total
}

// Fits tells whether the Doc is within the limits of one rich message.
func (d *Doc) Fits() bool {
	return d.Count() <= MaxBlocks && utf8.RuneCountInString(d.Rich()) <= MaxChars
}

// Rich is the HTML of a rich message.
func (d *Doc) Rich() string {
	var out strings.Builder
	for _, b := range d.blocks {
		switch b.kind {
		case heading:
			out.WriteString("<h3>" + string(b.text) + "</h3>")
		case paragraph:
			out.WriteString("<p>" + string(b.text) + "</p>")
		case table:
			out.WriteString("<table bordered striped compact>")
			if len(b.header) > 0 {
				out.WriteString("<tr>")
				for n, c := range b.header {
					out.WriteString(cell("th", c, b.align, n))
				}
				out.WriteString("</tr>")
			}
			for _, row := range b.rows {
				out.WriteString("<tr>")
				for n, c := range row {
					out.WriteString(cell("td", c, b.align, n))
				}
				out.WriteString("</tr>")
			}
			out.WriteString("</table>")
		case quote:
			out.WriteString(blockquote(b))
		case details:
			open := ""
			if b.open {
				open = " open"
			}
			out.WriteString("<details" + open + "><summary>" + string(b.text) + "</summary>" + b.inner.Rich() +
				"</details>")
		case buttons:
			out.WriteString("<tg-button-row>")
			for _, button := range b.buttons {
				out.WriteString(richButton(button))
			}
			out.WriteString("</tg-button-row>")
		}
	}
	return out.String()
}

func blockquote(b block) string {
	expandable := ""
	if b.expandable {
		expandable = " expandable"
	}
	return "<blockquote" + expandable + ">" + string(b.text) + "</blockquote>"
}

// Plain is classic parse_mode HTML: headings in bold, a table as a line per row; buttons go to the keyboard.
func (d *Doc) Plain() string {
	var out []string
	for _, b := range d.blocks {
		switch b.kind {
		case heading:
			out = append(out, "<b>"+string(b.text)+"</b>")
		case paragraph:
			out = append(out, string(b.text))
		case table:
			var lines []string
			if len(b.header) > 0 {
				lines = append(lines, "<i>"+joinHTML(b.header, " · ", false)+"</i>")
			}
			for _, row := range b.rows {
				lines = append(lines, joinHTML(row, " · ", true))
			}
			out = append(out, strings.Join(lines, "\n"))
		case quote:
			out = append(out, blockquote(b))
		case details:
			inner := b.inner.Plain()
			text := "<b>" + string(b.text) + "</b>"
			if inner != "" {
				text += "\n" + inner
			}
			out = append(out, text)
		}
	}
	return strings.Join(out, "\n\n")
}

func joinHTML(cells []HTML, sep string, skipEmpty bool) string {
	var parts []string
	for _, c := range cells {
		if !skipEmpty || c != "" {
			parts = append(parts, string(c))
		}
	}
	return strings.Join(parts, sep)
}

func cell(tag string, content HTML, align []string, n int) string {
	where := ""
	if n < len(align) && align[n] != "" {
		where = ` align="` + align[n] + `"`
	}
	return "<" + tag + where + ">" + string(content) + "</" + tag + ">"
}

func richButton(button Button) string {
	how := `type="callback_data" data="` + Attr(button.Data) + `"`
	if strings.HasPrefix(button.Data, "https://") {
		how = `type="url" url="` + Attr(button.Data) + `"`
	}
	if button.Style != "" {
		how += ` style="` + button.Style + `"`
	}
	return "<tg-button " + how + ">" + string(Esc(button.Text)) + "</tg-button>"
}

// Markup is an inline keyboard under a message.
type Markup struct {
	InlineKeyboard [][]map[string]string `json:"inline_keyboard"`
}

// Keyboard is inline buttons under a message: rows of buttons; nil when there are none.
func Keyboard(rows [][]Button) *Markup {
	var out [][]map[string]string
	for _, row := range rows {
		if len(row) == 0 {
			continue
		}
		line := make([]map[string]string, len(row))
		for n, button := range row {
			line[n] = InlineButton(CheckButton(button))
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		return nil
	}
	return &Markup{InlineKeyboard: out}
}

// InlineButton is a button of an inline keyboard.
func InlineButton(button Button) map[string]string {
	if strings.HasPrefix(button.Data, "https://") {
		return map[string]string{"text": button.Text, "url": button.Data}
	}
	return map[string]string{"text": button.Text, "callback_data": button.Data}
}

// Clone is a Doc with the same blocks, which grows apart from this one.
func (d *Doc) Clone() *Doc {
	return &Doc{blocks: append([]block(nil), d.blocks...)}
}
