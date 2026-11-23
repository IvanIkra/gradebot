// Package poller reads the sheets.
//
// Every interval (30 minutes unless the owner sets another with /interval) each sheet that has a reader (a follower
// with a full name who has not blocked the bot) is downloaded as xlsx once, its title fetched once from the HTML
// view, the workbook parsed once, and every reader's rows looked for in it. Each tab with a reader's row is laid out
// once for all of them (package layout), then each reader's grades are recorded (package grades). A new follower or
// a new full name asks for a read at once (Request): only those sheets are read then, and the users who asked are
// told how it went.
//
// A sheet fails as a whole on the download, on a file that is no workbook (a sheet closed to the link gives a login
// page) or cannot be parsed, on the layout (the network, the limit) and on any other error, which must not stop the
// other sheets; its grades stay as they were, and it is read again at the next poll. A reader whose row is on no tab
// fails alone; a tab with their name in two rows is left out for them, and they are told so. A sheet failing for 12
// hours is told once to the owner and its followers, and once more when it reads again (SheetRead.Stale, Back).
package poller

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/IvanIkra/gradebot/internal/grades"
	"github.com/IvanIkra/gradebot/internal/layout"
	"github.com/IvanIkra/gradebot/internal/llm"
	"github.com/IvanIkra/gradebot/internal/sheets"
	"github.com/IvanIkra/gradebot/internal/store"
	"github.com/IvanIkra/gradebot/internal/workbook"
)

const (
	Interval       = "poll.minutes" // kv: how often the sheets are read, set by the owner
	DefaultMinutes = 30
	MinMinutes     = 5
	MaxMinutes     = 240
	Timeout        = 120 * time.Second // for a download
)

// Errors are kept as "<kind>: <detail>"; the kinds are what the bot explains to a user.
const (
	Download  = "download" // of a sheet
	Closed    = "closed"
	Broken    = "broken"
	Layout    = "layout"
	Budget    = "budget"
	NoRow     = "no_row" // of a user's rows
	Ambiguous = "ambiguous"
)

// IntervalOf is the minutes between the reads of every sheet.
func IntervalOf(db *store.Store) int {
	minutes := DefaultMinutes
	db.GetJSON(Interval, &minutes)
	return minutes
}

// SetInterval sets the minutes; an error for minutes out of MinMinutes..MaxMinutes.
func SetInterval(db *store.Store, minutes int) error {
	if minutes < MinMinutes || minutes > MaxMinutes {
		return fmt.Errorf("from %d to %d minutes", MinMinutes, MaxMinutes)
	}
	db.PutJSON(Interval, minutes)
	return nil
}

// UserRead is how a reader's rows were read.
type UserRead struct {
	TgID  int64
	Error string // NoRow, or Ambiguous with the tabs left out (the others are read)
	Tabs  int    // with the user's row
	Items int    // grades in the sheet now
	First bool   // the baseline was made now
}

// SheetRead is how a sheet was read.
type SheetRead struct {
	Sheet     sheets.Sheet
	Error     string // the sheet failed: "<kind>: <detail>"
	Stale     string // failing since this moment for 12 hours: told now
	Back      bool   // read again after it was told as failing
	Users     []UserRead
	Unread    []string       // tabs the model could not lay out: skipped until their top rows change
	Requested map[int64]bool // users who asked for this read and are waiting for its outcome
}

// Layouts is what the poller needs of the layouts.
type Layouts interface {
	Get(sheetID string, tab *workbook.Tab, rows []*workbook.Row, labelBudget int, others []string) (layout.Spec, error)
	Forget(sheetID string) int64
}

// Poller reads the sheets.
type Poller struct {
	db      *store.Store
	sheets  *sheets.Sheets
	grades  *grades.Grades
	Layouts Layouts
	// OnRead is told what came of every poll.
	OnRead func([]SheetRead)
	// Lock is held while the poller touches the database or the bot; the downloads go without it.
	Lock sync.Locker
	// What tests replace.
	ExportURL, TitleURL string
	Minute              time.Duration
	Load                func([]byte) ([]*workbook.Tab, error)
	Client              *http.Client

	mu      sync.Mutex
	pending map[int64]map[int64]bool // sheet -> users waiting for its outcome
	wake    chan struct{}
}

// New makes a poller.
func New(db *store.Store, s *sheets.Sheets, g *grades.Grades, l Layouts) *Poller {
	return &Poller{db: db, sheets: s, grades: g, Layouts: l, Lock: &sync.Mutex{},
		ExportURL: "https://docs.google.com/spreadsheets/d/%s/export?format=xlsx",
		TitleURL:  "https://docs.google.com/spreadsheets/d/%s/htmlview",
		Minute:    time.Minute, Load: workbook.Load, Client: &http.Client{Timeout: Timeout},
		pending: map[int64]map[int64]bool{}, wake: make(chan struct{}, 1)}
}

func (p *Poller) signal() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Request reads a sheet now, not at the next poll; tgID (0: nobody) is told how it went.
func (p *Poller) Request(sheet, tgID int64) {
	p.mu.Lock()
	if p.pending[sheet] == nil {
		p.pending[sheet] = map[int64]bool{}
	}
	if tgID != 0 {
		p.pending[sheet][tgID] = true
	}
	p.mu.Unlock()
	p.signal()
}

// Relayout drops the sheet's cached layouts and reads it again: the owner's /relayout.
func (p *Poller) Relayout(sheet sheets.Sheet) {
	p.Layouts.Forget(sheet.SheetID)
	p.Request(sheet.ID, 0)
}

// Rescheduled: the interval changed, the next poll is counted from the last one with the new interval.
func (p *Poller) Rescheduled() { p.signal() }

func (p *Poller) hasPending() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.pending) > 0
}

func (p *Poller) interval() time.Duration {
	p.Lock.Lock()
	defer p.Lock.Unlock()
	return time.Duration(IntervalOf(p.db)) * p.Minute
}

// Run polls every sheet each interval, counted from the start of the last one; requested sheets between them. A read
// on request does not move the schedule. It returns when ctx is done.
func (p *Poller) Run(ctx context.Context, poll func(full bool)) {
	if poll == nil {
		poll = func(full bool) { p.Poll(full) }
	}
	var last time.Time
	for {
		full := last.IsZero() || time.Since(last) >= p.interval()
		if full {
			last = time.Now()
		}
		if full || p.hasPending() {
			poll(full)
		}
		wait := max(0, time.Until(last.Add(p.interval())))
		timer := time.NewTimer(wait)
		select { // a request during a poll gives one more poll
		case <-ctx.Done():
			timer.Stop()
			return
		case <-p.wake:
		case <-timer.C:
		}
		timer.Stop()
	}
}

func (p *Poller) takePending() map[int64]map[int64]bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	requested := p.pending
	p.pending = map[int64]map[int64]bool{}
	return requested
}

// Poll reads every sheet with a reader, or only the requested ones; OnRead gets what came of them.
func (p *Poller) Poll(full bool) []SheetRead {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("grades poll failed", "panic", r)
		}
	}()
	requested := p.takePending()
	p.Lock.Lock()
	wanted := p.sheets.ToRead()
	p.Lock.Unlock()
	if !full {
		var only []sheets.Sheet
		for _, s := range wanted {
			if _, ok := requested[s.ID]; ok {
				only = append(only, s)
			}
		}
		wanted = only
	}
	var results []SheetRead
	for _, s := range wanted {
		read := p.safeRead(s)
		read.Requested = requested[s.ID]
		if read.Requested == nil {
			read.Requested = map[int64]bool{}
		}
		results = append(results, read)
	}
	if len(results) > 0 && p.OnRead != nil {
		func() {
			p.Lock.Lock()
			defer p.Lock.Unlock()
			defer func() {
				if r := recover(); r != nil {
					slog.Error("telling the outcome of a poll failed", "panic", r)
				}
			}()
			p.OnRead(results)
		}()
	}
	return results
}

// safeRead reads a sheet; one sheet must not stop the others.
func (p *Poller) safeRead(s sheets.Sheet) (read SheetRead) {
	locked := false
	defer func() {
		if r := recover(); r != nil {
			slog.Error("reading a sheet failed", "sheet", s.SheetID, "panic", r)
			if !locked {
				p.Lock.Lock()
			}
			read = p.failed(s, fmt.Sprintf("%s: %s", Broken, panicName(r)), nil)
			p.Lock.Unlock()
		}
	}()
	return p.read(s, &locked)
}

// panicName is what Python called the type of the error.
func panicName(r any) string {
	if e, ok := r.(error); ok {
		var name interface{ Name() string }
		if errors.As(e, &name) {
			return name.Name()
		}
		return strings.TrimPrefix(fmt.Sprintf("%T", e), "*")
	}
	return fmt.Sprint(r)
}

type httpError struct {
	status int
	text   string
}

func (e *httpError) Error() string { return e.text }

func (p *Poller) get(url string) ([]byte, error) {
	resp, err := p.Client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, &httpError{resp.StatusCode, strings.TrimSpace(fmt.Sprintf("HTTP %d %s", resp.StatusCode,
			http.StatusText(resp.StatusCode)))}
	}
	return io.ReadAll(resp.Body)
}

func (p *Poller) read(s sheets.Sheet, locked *bool) SheetRead {
	sid := s.SheetID
	lock := func() {
		p.Lock.Lock()
		*locked = true
	}
	unlock := func() {
		*locked = false
		p.Lock.Unlock()
	}
	data, err := p.get(fmt.Sprintf(p.ExportURL, sid))
	var title string
	if err == nil && s.Title == "" { // after the file: a sheet closed to the link has the title of a login page
		title = p.fetchTitle(sid)
	}
	var tabs []*workbook.Tab
	var loadErr error
	if err == nil {
		tabs, loadErr = p.Load(data)
	}
	lock()
	defer func() {
		if *locked {
			unlock()
		}
	}()
	if title != "" {
		p.sheets.SetTitle(s.ID, title)
		s.Title = title
	}
	if err != nil {
		var he *httpError
		if errors.As(err, &he) { // its text carries the URL: the status alone
			return p.failed(s, Download+": "+he.text, nil)
		}
		return p.failed(s, strings.TrimSpace(Download+": "+err.Error()), nil)
	}
	if loadErr != nil {
		var nw *workbook.NotWorkbookError
		var bw *workbook.BrokenWorkbookError
		switch {
		case errors.As(loadErr, &nw):
			return p.failed(s, Closed+": "+nw.Error(), nil)
		case errors.As(loadErr, &bw):
			return p.failed(s, Broken+": "+bw.Error(), nil)
		}
		return p.failed(s, Broken+": "+loadErr.Error(), nil)
	}

	// Every reader's rows; the layout of each tab one of them is in, once for all.
	var users []UserRead
	rows, names := map[int64][]*workbook.Row{}, map[int64]string{}
	for _, reader := range p.sheets.Readers(s.ID) {
		user := UserRead{TgID: reader.TgID}
		names[user.TgID] = reader.Student
		found, twice := workbook.Find(tabs, reader.Student)
		if len(twice) > 0 {
			user.Error = Ambiguous + ": " + strings.Join(twice, ", ")
		}
		if len(found) > 0 {
			rows[user.TgID] = found
		} else if len(twice) == 0 {
			user.Error = NoRow
		}
		users = append(users, user)
	}
	budget := layout.Budget(sheets.CourseOf(s))
	specs := map[string]layout.Spec{}
	var order []string
	for _, tab := range tabs {
		var inTab []*workbook.Row
		for _, user := range users {
			for _, row := range rows[user.TgID] {
				if row.Tab == tab {
					inTab = append(inTab, row)
				}
			}
		}
		if len(inTab) == 0 {
			continue
		}
		var others []string
		for _, other := range tabs {
			if other != tab {
				others = append(others, other.Title)
			}
		}
		spec, err := p.Layouts.Get(sid, tab, inTab, budget, others)
		switch {
		case err == nil:
			specs[tab.Title] = spec
			order = append(order, tab.Title)
		case llm.IsBudget(err):
			return p.failed(s, Budget+": "+err.Error(), users)
		default:
			return p.failed(s, Layout+": "+err.Error(), users)
		}
	}

	// The database may have changed while the tabs were laid out: a reader who left the sheet, or has another name
	// now, gets nothing recorded from this read.
	for n := range users {
		user := &users[n]
		if name, ok := p.sheets.ReaderName(user.TgID, s.ID); !ok || name != names[user.TgID] {
			continue
		}
		if found := rows[user.TgID]; len(found) > 0 {
			var items []layout.Item
			for _, row := range found {
				items = append(items, layout.Extract(row, specs[row.Tab.Title], len(items))...)
			}
			user.First = p.grades.Record(user.TgID, s.ID, items, len(found), user.Error)
			user.Tabs, user.Items = len(found), p.grades.Count(user.TgID, s.ID)
		} else {
			p.grades.Failed(user.TgID, s.ID, user.Error)
		}
	}
	// Every work of the laid-out tabs, graded for someone or not yet: what a subject's formula is made of.
	var plan []sheets.Work
	for _, tab := range tabs {
		if spec, ok := specs[tab.Title]; ok {
			for _, w := range layout.Plan(tab, spec) {
				plan = append(plan, sheets.Work{Key: w.Key, Tab: w.Tab, Label: w.Label, LabelRu: w.LabelRu, Max: w.Max})
			}
		}
	}
	p.sheets.SetPlan(s.ID, plan)
	var unread []string
	for _, title := range order {
		if specs[title].Failed {
			unread = append(unread, title)
		}
	}
	back := p.sheets.ReadOK(s.ID)
	if now, ok := p.sheets.Get(s.ID); ok {
		s = now
	}
	return SheetRead{Sheet: s, Back: back, Users: users, Unread: unread}
}

func (p *Poller) failed(s sheets.Sheet, errorText string, users []UserRead) SheetRead {
	slog.Warn("sheet failed", "sheet", s.SheetID, "error", errorText)
	stale := p.sheets.ReadFailed(s.ID, errorText)
	return SheetRead{Sheet: s, Error: errorText, Stale: stale, Users: users}
}

var titleRe = regexp.MustCompile(`(?s)<title>(.*?)</title>`)

// fetchTitle gives the sheet's title from its HTML view; "" on any failure (asked again at the next read).
func (p *Poller) fetchTitle(sid string) string {
	resp, err := p.Client.Get(fmt.Sprintf(p.TitleURL, sid))
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ""
	}
	page, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}
	found := titleRe.FindSubmatch(page)
	if found == nil {
		return ""
	}
	title := strings.TrimSpace(html.UnescapeString(string(found[1])))
	for _, suffix := range []string{" - Google Диск", " - Google Drive", " - Google Таблицы", " - Google Sheets"} {
		title = strings.TrimSuffix(title, suffix)
	}
	return strings.TrimSpace(title)
}
