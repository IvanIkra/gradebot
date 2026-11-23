package bot

import (
	"slices"
	"strings"
	"testing"

	"github.com/IvanIkra/gradebot/internal/access"
	"github.com/IvanIkra/gradebot/internal/groups"
)

const ada, bob, eva = int64(3001), int64(3002), int64(3003) // Ada runs A1, Bob is in A1, Eva is in B2

var names = map[int64]string{ada: "Ада", bob: "Боб", eva: "Ева"}

func (f *fixture) as(tgID int64, text string) {
	f.t.Helper()
	f.say(tgID, text, first(names[tgID]), noUsername())
}

func (f *fixture) comeIn(tgID int64, args, firstName string) {
	f.t.Helper()
	f.say(owner, "/invite "+args)
	f.say(tgID, "/start "+inviteCode(f.text(owner)), first(firstName), noUsername())
}

type admins struct {
	*fixture
	a1, b2 groups.Group
}

func withGroups(t *testing.T) admins {
	f := setup(t)
	a1, _ := f.bot.Groups.Create("A1")
	b2, _ := f.bot.Groups.Create("B2")
	f.comeIn(ada, "A1 админ", "Ада")
	f.comeIn(bob, "A1", "Боб")
	f.comeIn(eva, "B2", "Ева")
	return admins{f, a1, b2}
}

// commandsSet are the commands of the last menu set for a chat.
func (f *fixture) commandsSet(chat int64) []string {
	var last []string
	found := false
	for _, c := range f.api.Calls {
		if c.Method != "setMyCommands" {
			continue
		}
		if scope, _ := c.Params["scope"].(map[string]any); scope["chat_id"] == chat {
			last, found = nil, true
			for _, cmd := range c.Params["commands"].([]map[string]string) {
				last = append(last, cmd["command"])
			}
		}
	}
	if !found {
		return nil
	}
	return last
}

func TestAnAdminsCode(t *testing.T) {
	g := withGroups(t)
	a, _ := g.user(ada)
	if a.Grp != g.a1.ID || a.Rank != access.Admin {
		t.Fatal(a)
	}
	if b, _ := g.user(bob); b.Rank != access.Member {
		t.Fatal(b)
	}
	contains(t, g.api.Sent(ada)[0].Text, "Ты админ группы")
	if !slices.Contains(g.commandsSet(ada), "invite") || g.commandsSet(bob) != nil {
		t.Fatal(g.commandsSet(ada))
	}
	g.say(owner, "/invite админ") // an admin's code needs a group
	contains(t, g.text(owner), "Код админа — только в группу")
}

func TestTheOwnerMakesAndUnmakesAnAdmin(t *testing.T) {
	g := withGroups(t)
	g.press(owner, "uc:3002")
	if g.buttons(owner)["Сделать админом группы"] != "ra:3002" {
		t.Fatal(g.buttons(owner))
	}
	g.press(owner, "ra:3002")
	if b, _ := g.user(bob); b.Rank != access.Admin {
		t.Fatal(b)
	}
	contains(t, g.text(bob), "Владелец сделал тебя админом группы <b>A1</b>")
	if !slices.Contains(g.commandsSet(bob), "groupsheet") {
		t.Fatal(g.commandsSet(bob))
	}
	g.press(owner, "ra:3002")
	if b, _ := g.user(bob); b.Rank != access.Trusted {
		t.Fatal(b)
	}
	contains(t, g.text(bob), "больше не админ")
	if slices.Contains(g.commandsSet(bob), "groupsheet") {
		t.Fatal(g.commandsSet(bob))
	}
}

func TestMovedOrGroupGoneNoAdmin(t *testing.T) {
	g := withGroups(t)
	g.press(owner, "um:3001."+itoa(g.b2.ID))
	if a, _ := g.user(ada); a.Rank != access.Trusted || slices.Contains(g.commandsSet(ada), "invite") {
		t.Fatal(a)
	}
	g.press(owner, "ra:3001") // the admin of B2 now
	g.bot.Groups.Delete(g.b2.ID)
	if a, _ := g.user(ada); a.Rank != access.Trusted {
		t.Fatal(a)
	}
}

func TestAdminCodesWithinLimits(t *testing.T) {
	g := withGroups(t)
	before := len(g.Access.OpenInvites())
	g.as(ada, "/invite x50")
	contains(t, g.text(ada), "не больше 30 человек")
	g.as(ada, "/invite 10д")
	contains(t, g.text(ada), "не больше 7 дней")
	if len(g.Access.OpenInvites()) != before {
		t.Fatal("made")
	}
	g.as(ada, "/invite x5 2д админ курс") // «админ» is only a word of the note here
	reply := g.text(ada)
	contains(t, reply, "Код «админ курс» в группу <b>A1</b>: вошли 0 из 5")
	inv := g.Access.OpenInvites()[0]
	if inviteCode(reply) == "" || inv.Grp != g.a1.ID || inv.Rank != access.Member || inv.Uses != 5 {
		t.Fatal(inv)
	}
	contains(t, g.text(owner), "Ада, админ <b>A1</b>, создал(а) код «админ курс» на 5 чел.")
	g.Access.Prolong(ada, 10) // access for 10 days: codes give no more
	g.as(ada, "/invite +30д")
	a, _ := g.user(ada)
	if got := g.Access.OpenInvites()[0].AccessUntil; got != a.ExpiresAt {
		t.Fatal(got, a.ExpiresAt)
	}
}

func TestAdminInviteStepByStep(t *testing.T) {
	g := withGroups(t)
	g.as(ada, "/invite")
	contains(t, g.text(ada), "Сколько человек")
	if _, ok := g.buttons(ada)["Сколько угодно"]; ok {
		t.Fatal(g.buttons(ada))
	}
	g.answer(ada, "x10")
	got := g.buttons(ada)
	if _, ok := got["1 день"]; !ok {
		t.Fatal(got)
	}
	if _, ok := got["7 дней"]; !ok {
		t.Fatal(got)
	}
	if _, ok := got["30 дней"]; ok {
		t.Fatal(got)
	}
	g.answer(ada, "")
	g.answer(ada, "")
	g.as(ada, "Новенькие")
	contains(t, g.text(ada), "Код «Новенькие» в группу <b>A1</b>: вошли 0 из 10")
}

func TestAdminCodesOfTheirGroupOnly(t *testing.T) {
	g := withGroups(t)
	g.say(owner, "/invite B2 x3 чужой")
	g.say(owner, "/invite A1 x3 свой")
	g.say(owner, "/invite A1 админ второй админ")
	g.as(ada, "/codes")
	contains(t, g.text(ada), "свой")
	lacks(t, g.text(ada), "чужой", "второй админ")
	byNote := func(note string) access.Invite {
		for _, inv := range g.Access.OpenInvites() {
			if inv.Note == note {
				return inv
			}
		}
		return access.Invite{}
	}
	theirs := byNote("чужой")
	g.press(ada, "av:"+itoa(theirs.ID))
	if byNote("чужой").ID == 0 {
		t.Fatal("revoked another group's")
	}
	mine := byNote("свой")
	g.press(ada, "aq:"+itoa(mine.ID))
	if g.buttons(ada)["Да, отозвать"] != "av:"+itoa(mine.ID) {
		t.Fatal(g.buttons(ada))
	}
	g.press(ada, "av:"+itoa(mine.ID))
	if byNote("свой").ID != 0 {
		t.Fatal("kept")
	}
}

func TestAdminSeesTheirGroup(t *testing.T) {
	g := withGroups(t)
	g.as(ada, "/users")
	contains(t, g.text(ada), "Боб")
	lacks(t, g.text(ada), "Ева")
	if g.buttons(ada)["Боб"] != "mc:3002" {
		t.Fatal(g.buttons(ada))
	}
	g.press(ada, "mc:3002")
	contains(t, g.text(ada), "<h3>Боб</h3>", "обычный")
	for _, data := range g.buttons(ada) {
		for _, prefix := range []string{"du:", "rk:", "ae:"} {
			if strings.HasPrefix(data, prefix) {
				t.Fatal(data)
			}
		}
	}
	g.press(ada, "mc:3003") // not in the group: the list again
	lacks(t, g.text(ada), "Ева")
	contains(t, g.text(ada), "Группа A1")
}

func TestMembersCannotActAsAdmins(t *testing.T) {
	g := withGroups(t)
	g.as(bob, "/invite x5")
	contains(t, g.text(bob), "Это не команда")
	g.press(bob, "mc:3001")
	if g.lastNote() != "Это может только админ группы" {
		t.Fatal(g.lastNote())
	}
}

func TestAdminSharedSheets(t *testing.T) {
	g := withGroups(t)
	g.as(ada, "/groupsheet "+calc)
	contains(t, g.text(ada), "общая таблица группы <b>A1</b>")
	s, _ := g.Sheets.ByURL(calc)
	if list := g.bot.Groups.SheetsOf(g.a1.ID); len(list) != 1 || list[0].URL != s.URL ||
		!slices.Contains(ids(g.Sheets.Followed(bob)), s.ID) {
		t.Fatal(list)
	}
	contains(t, g.text(owner), "добавил(а) общую таблицу")
	g.press(ada, "gr:"+itoa(g.b2.ID)+"."+itoa(s.ID)) // another group's: nothing
	if len(g.bot.Groups.SheetsOf(g.a1.ID)) == 0 {
		t.Fatal("removed")
	}
	g.press(ada, "gr:"+itoa(g.a1.ID)+"."+itoa(s.ID))
	if len(g.bot.Groups.SheetsOf(g.a1.ID)) != 0 {
		t.Fatal("kept")
	}
	contains(t, g.text(owner), "убрал(а) общую таблицу")
}

func TestAdminApprovesMembersSheets(t *testing.T) {
	g := withGroups(t)
	g.as(bob, "/sheet "+alg)
	contains(t, g.text(bob), "владелец или админ группы")
	contains(t, g.text(ada), "просьба подписать на таблицу")
	g.press(ada, g.buttons(ada)["Одобрить"])
	contains(t, g.text(ada), "Одобрено")
	if _, ok := g.buttons(ada)["Задать имя"]; ok {
		t.Fatal(g.buttons(ada))
	}
	contains(t, g.text(bob), "Админ группы одобрил таблицу")
	s, _ := g.Sheets.ByURL(alg)
	if !slices.Contains(ids(g.Sheets.Followed(bob)), s.ID) {
		t.Fatal("not followed")
	}
	contains(t, g.text(owner), "админ группы, одобрил(а)")
	g.as(eva, "/sheet "+calc) // B2 has no admin: only the owner hears
	request := g.Sheets.Requests(eva)[0]
	g.press(ada, "sd:"+itoa(request.ID))
	if len(g.Sheets.Requests(eva)) == 0 {
		t.Fatal("declined")
	}
}

func TestGroupNewsAsTheAdminLikes(t *testing.T) {
	g := withGroups(t)
	var news []string
	var last map[string]string
	for _, m := range g.api.Sent(ada) {
		if strings.Contains(m.Text, "Группа A1") {
			news = append(news, m.Text)
			last = fakeButtons(m)
		}
	}
	if len(news) == 0 || last["Настроить"] != "nm" {
		t.Fatal(news)
	}
	contains(t, news[len(news)-1], "Боб: вход по коду")
	g.as(bob, "/name Бобров Боб")
	digest := g.last(ada)
	if digest.Method != "editMessageText" {
		t.Fatal(digest)
	}
	contains(t, digest.Text, "Боб: ФИО <b>Бобров Боб</b>")
	lacks(t, digest.Text, "Ева") // B2's news are not A1's
	g.as(ada, "/news")
	contains(t, g.text(ada), "Сейчас: <b>тихой сводкой</b>")
	g.press(ada, "nw:each")
	g.as(bob, "/stop")
	g.press(bob, "stop!")
	each := g.last(ada)
	if each.Method != "sendRichMessage" && each.Method != "sendMessage" {
		t.Fatal(each)
	}
	contains(t, each.Text, "вышел(а) из бота")
	g.press(ada, "nw:off")
	count := len(g.api.Sent(ada))
	g.comeIn(3004, "A1", "Вера")
	if len(g.api.Sent(ada)) != count {
		t.Fatal("told")
	}
}

func TestTheHelpOfAnAdmin(t *testing.T) {
	g := withGroups(t)
	g.as(ada, "/help")
	contains(t, g.text(ada), "Админ группы A1")
	g.as(bob, "/help")
	lacks(t, g.text(bob), "Админ группы")
}

func TestLimitArgs(t *testing.T) {
	uses, days, err := limitArgs("x50 14д")
	if err != nil || *uses != 50 || *days != 14 {
		t.Fatal(uses, days, err)
	}
	if uses, days, _ := limitArgs("x*"); *uses != 0 || days != nil {
		t.Fatal(uses, days)
	}
	if uses, days, _ := limitArgs("10"); uses != nil || *days != 10 {
		t.Fatal(uses, days)
	}
	for _, bad := range []string{"", "x0", "x1001", "0д", "400d", "много"} {
		if _, _, err := limitArgs(bad); err == nil {
			t.Error(bad)
		}
	}
}

func limitsOf(b *Bot, tgID int64) [2]int {
	uses, days := b.limits(tgID)
	return [2]int{uses, days}
}

func TestTheOwnerSetsAdminsLimits(t *testing.T) {
	g := withGroups(t)
	g.say(owner, "/limits")
	contains(t, g.text(owner), "<b>до 30 человек</b>", "до <b>7 дней</b>")
	view := g.buttons(owner)
	if view["50"] != "al:u.50" {
		t.Fatal(view)
	}
	if _, ok := view["✓ 30"]; !ok {
		t.Fatal(view)
	}
	if _, ok := view["✓ 7 дней"]; !ok {
		t.Fatal(view)
	}
	g.press(owner, "al:u.5")
	if limitsOf(g.bot, 0) != [2]int{5, 7} {
		t.Fatal(limitsOf(g.bot, 0))
	}
	if _, ok := g.buttons(owner)["✓ 5"]; ok { // not a button: set anyway
		t.Fatal(g.buttons(owner))
	}
	g.as(ada, "/invite x10")
	contains(t, g.text(ada), "не больше 5 человек", "до 5 человек")
	g.say(owner, "/limits")
	g.say(owner, "много") // wrong: asked again
	contains(t, g.text(owner), "Например x50 или x*")
	g.say(owner, "x* 14д")
	if limitsOf(g.bot, 0) != [2]int{0, 14} {
		t.Fatal(limitsOf(g.bot, 0))
	}
	contains(t, g.text(owner), "сколько угодно человек")
	g.as(ada, "/invite x* 10д")
	contains(t, g.text(ada), "без ограничения")
	if inviteCode(g.text(ada)) == "" {
		t.Fatal(g.text(ada))
	}
	g.press(owner, "al:d.0")
	if limitsOf(g.bot, 0) != [2]int{0, 14} {
		t.Fatal(limitsOf(g.bot, 0))
	}
}

func TestEachAdminTheirOwnLimits(t *testing.T) {
	g := withGroups(t)
	g.press(owner, "ra:3002") // a second admin of A1
	g.press(owner, "uc:3001")
	contains(t, g.text(owner), "до 30 человек, 7 дней (общие)")
	if g.buttons(owner)["Лимиты кодов"] != "al:3001" {
		t.Fatal(g.buttons(owner))
	}
	g.press(owner, "al:3001")
	contains(t, g.text(owner), "Общие лимиты, как у всех")
	if g.buttons(owner)["50"] != "al:3001.u.50" {
		t.Fatal(g.buttons(owner))
	}
	g.say(owner, "x5 3д") // typed under her limits
	if limitsOf(g.bot, ada) != [2]int{5, 3} || limitsOf(g.bot, bob) != [2]int{30, 7} ||
		limitsOf(g.bot, 0) != [2]int{30, 7} {
		t.Fatal(limitsOf(g.bot, ada))
	}
	contains(t, g.text(owner), "Свои лимиты")
	g.press(owner, "al:3001.u.10")
	if limitsOf(g.bot, ada) != [2]int{10, 3} {
		t.Fatal(limitsOf(g.bot, ada))
	}
	g.as(ada, "/invite x20")
	contains(t, g.text(ada), "не больше 10 человек")
	g.as(bob, "/invite x20")
	if inviteCode(g.text(bob)) == "" {
		t.Fatal(g.text(bob))
	}
	g.press(owner, "al:u.50") // the shared ones: Bob's, not Ada's
	if limitsOf(g.bot, bob) != [2]int{50, 7} || limitsOf(g.bot, ada) != [2]int{10, 3} {
		t.Fatal(limitsOf(g.bot, bob))
	}
	g.say(owner, "/limits")
	contains(t, g.text(owner), "до 10 человек, 3 дня", "общие")
	if g.buttons(owner)["Ада"] != "al:3001" {
		t.Fatal(g.buttons(owner))
	}
	g.press(owner, "al:3001.r")
	if limitsOf(g.bot, ada) != [2]int{50, 7} || g.bot.hasOwnLimits(ada) {
		t.Fatal(limitsOf(g.bot, ada))
	}
	g.press(owner, "al:3003.u.5") // not an admin
	if g.lastNote() != "Не админ" || g.bot.hasOwnLimits(eva) {
		t.Fatal(g.lastNote())
	}
}
