package bot

import (
	"log/slog"
	"slices"

	"github.com/IvanIkra/gradebot/internal/clock"
	"github.com/IvanIkra/gradebot/internal/i18n"
	"github.com/IvanIkra/gradebot/internal/rich"
)

// The owner's news: what users did that needs no answer (came in, set a name, changed groups, left, blocked the
// bot) gathers in one silent message, edited as news comes, instead of a message with a sound each.
//
// The digest stays open until the owner has surely seen the chat: they write to the bot or press a button, or a
// message that does need them (a request, a complaint, a failing sheet) goes out under it. The next news starts a
// new digest below. A digest that would not fit a message, or whose message is gone, is closed too.
//
// A button pressed in a digest opens its answer in a new message, so the digest stays as it was.
//
// A group's admins get the news of their group: who came in, set a name, joined or left the group, left the bot or
// blocked it, and whose access ends. Each admin picks how (/news): a silent digest of their own like the owner's
// (the default), a message with a sound for each piece, or none.

const (
	newsKey     = "owner.news"    // kv: {"message_id", "entries": [{"text", "buttons"}]}: the open digest, or null
	digestsKey  = "owner.digests" // kv: the ids of the last digest messages, whose buttons answer in a new message
	keepDigests = 50
	Digest      = "digest" // how an admin gets the news of their group
	Each        = "each"
	Off         = "off"
)

// entry is a line of news, as kv keeps it: buttons as [text, data(, style)].
type entry struct {
	Text    string     `json:"text"`
	Buttons [][]string `json:"buttons"`
}

type digestState struct {
	MessageID int64   `json:"message_id"`
	Entries   []entry `json:"entries"`
}

func adminNews(tgID int64) string { return "news.admin." + itoa(tgID) }

func newsMode(tgID int64) string { return "news.mode." + itoa(tgID) }

func stored(buttons []rich.Button) [][]string {
	out := [][]string{}
	for _, b := range buttons {
		row := []string{b.Text, b.Data}
		if b.Style != "" {
			row = append(row, b.Style)
		}
		out = append(out, row)
	}
	return out
}

func button(row []string) rich.Button {
	b := rich.Button{}
	if len(row) > 0 {
		b.Text = row[0]
	}
	if len(row) > 1 {
		b.Data = row[1]
	}
	if len(row) > 2 {
		b.Style = row[2]
	}
	return b
}

// News is one line of news for the owner, with its buttons: into the open digest, or a new silent one.
func (b *Bot) News(parts []any, buttons ...rich.Button) {
	lang := b.OwnerLang()
	b.news(b.OwnerID, lang, newsKey, b.entry(lang, parts, buttons), i18n.Tr(lang, "News", "Новости"))
}

// GroupNews is one line of news for each admin of the group but the one it is about: parts in their language.
func (b *Bot) GroupNews(gid, about int64, parts func(lang string) []any) {
	group, ok := b.Groups.Get(gid)
	if !ok {
		return
	}
	for _, admin := range b.Access.Admins(gid) {
		mode := Digest
		b.Store.GetJSON(newsMode(admin.TgID), &mode)
		if admin.TgID == about || mode == Off {
			continue
		}
		lang := admin.Lang
		title := i18n.Tr(lang, "Group ", "Группа ") + group.Name
		e := b.entry(lang, parts(lang), []rich.Button{rich.B(i18n.Tr(lang, "Set up", "Настроить"), "nm")})
		if mode == Each {
			if _, err := b.API.Send(admin.TgID, digestDoc([]entry{e}, title), nil, false); err != nil {
				slog.Info("sending news failed", "to", admin.TgID, "error", err)
			}
		} else {
			b.news(admin.TgID, lang, adminNews(admin.TgID), e, title)
		}
	}
}

func (b *Bot) entry(lang string, parts []any, buttons []rich.Button) entry {
	moment := b.When(clock.ISO(b.Now()), lang)
	text := rich.Join(append([]any{rich.Italic(moment), " · "}, parts...)...)
	return entry{Text: string(text), Buttons: stored(buttons)}
}

func (b *Bot) news(chat int64, lang, key string, e entry, title string) {
	var state *digestState
	b.Store.GetJSON(key, &state)
	if state != nil {
		doc := digestDoc(append(slices.Clone(state.Entries), e), title)
		if doc.Fits() {
			if err := b.API.Edit(chat, state.MessageID, doc, nil); err == nil {
				state.Entries = append(state.Entries, e)
				b.Store.PutJSON(key, state)
				return
			} else { // deleted by the reader, or too old to edit
				slog.Info("a digest of news cannot be edited", "error", err)
			}
		}
	}
	id, err := b.API.Send(chat, digestDoc([]entry{e}, title), nil, true)
	if err != nil {
		slog.Warn("sending news failed", "to", chat, "error", err)
		return
	}
	b.Store.PutJSON(key, digestState{MessageID: id, Entries: []entry{e}})
	if key == newsKey {
		var ids []int64
		b.Store.GetJSON(digestsKey, &ids)
		ids = append(ids, id)
		b.Store.PutJSON(digestsKey, ids[max(0, len(ids)-keepDigests):])
	}
}

// digestDoc is the entries with their buttons; a button the same in every entry (an admin's «Set up») once, at the
// end.
func digestDoc(entries []entry, title string) *rich.Doc {
	heading := title
	if len(entries) > 1 {
		heading += " · " + itoa(int64(len(entries)))
	}
	doc := rich.New().H(heading)
	rows := make([][]rich.Button, len(entries))
	for n, e := range entries {
		for _, row := range e.Buttons {
			rows[n] = append(rows[n], button(row))
		}
	}
	var common []rich.Button
	if len(rows) > 0 {
		for _, btn := range rows[0] {
			everywhere := true
			for _, row := range rows {
				everywhere = everywhere && slices.Contains(row, btn)
			}
			if everywhere {
				common = append(common, btn)
			}
		}
	}
	for n, e := range entries {
		var own []rich.Button
		for _, btn := range rows[n] {
			if !slices.Contains(common, btn) {
				own = append(own, btn)
			}
		}
		doc.P(rich.HTML(e.Text)).Buttons(own...)
	}
	return doc.Buttons(common...)
}

// CloseNews: the reader has seen the chat, or a message that needs them goes under the digest; news starts anew.
func (b *Bot) CloseNews(key string) {
	if key == "" {
		key = newsKey
	}
	var state *digestState
	if b.Store.GetJSON(key, &state) && state != nil {
		b.Store.PutJSON(key, nil)
	}
}

func (b *Bot) isDigest(messageID int64) bool {
	var ids []int64
	b.Store.GetJSON(digestsKey, &ids)
	return slices.Contains(ids, messageID)
}
