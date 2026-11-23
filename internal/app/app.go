// Package app is the service: the database, the Bot API client, the long-polling loop and the reading of the sheets.
//
// Everything that touches the database or the bot runs under one lock, as the Python version ran on one event loop:
// an update, a poll's outcome, a look at the access, a backup. Only the long waits go without it: getUpdates, the
// downloads of the sheets and the sleeps.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/IvanIkra/gradebot/internal/access"
	"github.com/IvanIkra/gradebot/internal/backups"
	"github.com/IvanIkra/gradebot/internal/bot"
	"github.com/IvanIkra/gradebot/internal/config"
	"github.com/IvanIkra/gradebot/internal/grades"
	"github.com/IvanIkra/gradebot/internal/layout"
	"github.com/IvanIkra/gradebot/internal/llm"
	"github.com/IvanIkra/gradebot/internal/poller"
	"github.com/IvanIkra/gradebot/internal/sheets"
	"github.com/IvanIkra/gradebot/internal/store"
	"github.com/IvanIkra/gradebot/internal/telegram"
)

// Offset is the kv key of the next update to ask for, so a restart neither loses nor repeats updates.
const Offset = "updates.offset"

// What a stranger sees in the empty chat before /start (at most 512 characters), and on the bot's profile (120).
var description = map[string]string{
	"en": "I watch the Google Sheets of your courses and send you every new or changed grade as soon as it appears.\n\n" +
		"Access is by invite code: send me the code you got, or open the invite link.",
	"ru": "Слежу за таблицами Google с оценками твоих курсов и присылаю каждую новую или изменённую оценку, как только " +
		"она появится.\n\nВход по коду приглашения: пришли код, который тебе дали, или открой ссылку-приглашение.",
}

var shortDescription = map[string]string{
	"en": "New grades from the Google Sheets of your courses, right in Telegram.",
	"ru": "Новые оценки из таблиц Google твоих курсов — сразу в Telegram.",
}

// Service is the running bot.
type Service struct {
	Env     config.Env
	Data    string
	Store   *store.Store
	Access  *access.Access
	Sheets  *sheets.Sheets
	Journal *llm.Journal
	Budget  *llm.Budget
	Layouts *layout.Layouts
	Lock    sync.Mutex
}

// New opens the database in data.
func New(env config.Env, data string) (*Service, error) {
	if err := os.MkdirAll(data, 0o755); err != nil {
		return nil, err
	}
	db, err := store.Open(filepath.Join(data, "gradebot.db"))
	if err != nil {
		return nil, err
	}
	s := &Service{Env: env, Data: data, Store: db, Access: access.New(db, env.InviteDays, nil),
		Sheets: sheets.New(db, nil), Journal: llm.NewJournal(db)}
	s.Budget = llm.NewBudget(s.Journal, db, env.LLMMonthlyUSD, env.Zone(), nil)
	s.Layouts = layout.New(db, llm.NewHaiku(s.Budget, s.Journal))
	return s, nil
}

// Run runs until ctx is done.
func (s *Service) Run(ctx context.Context) error {
	api := telegram.New(s.Env.BotToken, s.Env.APIURL)
	username, err := api.GetMe()
	if err != nil {
		return fmt.Errorf("getMe: %w", err)
	}
	slog.Info("running", "as", "@"+username)
	b := bot.New(api, s.Store, s.Access, s.Sheets, s.Env.OwnerID, s.Env.Zone(), username, nil)
	p := poller.New(s.Store, s.Sheets, b.Grades, s.Layouts)
	p.Lock = &s.Lock
	b.Poller, b.Budget, b.Journal, b.LLM = p, s.Budget, s.Journal, s.Layouts.LLM
	p.OnRead, s.Budget.Notify = b.AfterRead, b.BudgetNotice
	s.locked(func() {
		s.setCommands(api, b)
		setProfile(api)
	})
	var wg sync.WaitGroup
	run := func(f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f()
		}()
	}
	run(func() { s.Poll(ctx, api, b) })
	run(func() { p.Run(ctx, nil) })
	run(func() {
		every(ctx, bot.AccessCheck, func() { s.locked(b.WatchAccess) })
	})
	run(func() {
		every(ctx, backups.Check, func() {
			s.locked(func() {
				if _, err := backups.Backup(s.Store, filepath.Join(s.Data, "backups"), time.Now(), s.Env.Zone()); err != nil {
					slog.Error("the database copy failed", "error", err)
				}
			})
		})
	})
	wg.Wait()
	return s.Store.Close()
}

// locked runs f under the lock; a panic in it is logged, as an exception was.
func (s *Service) locked(f func()) {
	s.Lock.Lock()
	defer s.Lock.Unlock()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("failed", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	f()
}

func every(ctx context.Context, period time.Duration, f func()) {
	for {
		f()
		select {
		case <-ctx.Done():
			return
		case <-time.After(period):
		}
	}
}

func (s *Service) setCommands(api *telegram.API, b *bot.Bot) {
	for _, lang := range []string{"en", "ru"} {
		code := lang
		if lang == "en" {
			code = ""
		}
		if err := api.SetCommands(bot.UserCommands.Commands(lang), 0, code); err != nil {
			slog.Warn("the menu not set", "error", err)
		}
		if err := api.SetCommands(bot.OwnerCommands.Commands(lang), s.Env.OwnerID, code); err != nil {
			slog.Warn("owner's commands not set", "error", err) // the owner has not opened the bot yet
		}
	}
	b.AdminMenus()
}

// setProfile sets the description a stranger reads before pressing Start: on every start, so it follows the code.
func setProfile(api *telegram.API) {
	for _, lang := range []string{"en", "ru"} {
		code := ""
		if lang != "en" {
			code = lang
		}
		params := telegram.Params{"description": description[lang], "language_code": code}
		if _, err := api.Call("setMyDescription", params); err != nil {
			slog.Warn("the bot's description not set", "error", err)
			continue
		}
		params = telegram.Params{"short_description": shortDescription[lang], "language_code": code}
		if _, err := api.Call("setMyShortDescription", params); err != nil {
			slog.Warn("the bot's description not set", "error", err)
		}
	}
}

// Poll is getUpdates until ctx is done; an update that fails is logged and skipped, not retried for ever.
func (s *Service) Poll(ctx context.Context, api *telegram.API, b *bot.Bot) {
	for ctx.Err() == nil {
		s.PollOnce(ctx, api, b)
	}
}

// PollOnce asks for updates once and handles them.
func (s *Service) PollOnce(ctx context.Context, api *telegram.API, b *bot.Bot) {
	var offset *int64
	s.Lock.Lock()
	var saved int64
	if s.Store.GetJSON(Offset, &saved) {
		offset = &saved
	}
	s.Lock.Unlock()
	updates, err := api.GetUpdates(offset, telegram.PollTimeout)
	if err != nil {
		wait := 5 * time.Second
		if e := telegram.AsError(err); e != nil && (e.Code == 401 || e.Code == 409) {
			wait = 30 * time.Second // a wrong token, or a webhook or another copy of the bot
		}
		slog.Error("getUpdates failed", "error", err, "again in", wait)
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
		return
	}
	for _, update := range updates {
		s.locked(func() {
			defer func() { s.Store.PutJSON(Offset, update.UpdateID+1) }()
			defer func() {
				if r := recover(); r != nil {
					slog.Error("an update failed", "update", update.UpdateID, "panic", r, "stack", string(debug.Stack()))
				}
			}()
			b.Handle(update)
		})
	}
}

// ReadOnce reads every sheet once and sends nothing: the check before a new version takes over a database. It gives
// how many new grades this read found and how many tabs it had to lay out again: both are 0 when the new version
// reads the database as the old one did.
func (s *Service) ReadOnce() (newGrades int, relaid []string, err error) {
	return s.readOnce(nil)
}

func (s *Service) readOnce(prepare func(*poller.Poller)) (int, []string, error) {
	count := func() int {
		n, _ := store.Scalar[int](s.Store, "select count(*) from grade_events")
		return n
	}
	before := count()
	asked := &recording{Asker: s.Layouts.LLM}
	layouts := layout.New(s.Store, asked)
	p := poller.New(s.Store, s.Sheets, grades.New(s.Store, nil), layouts)
	p.Lock = &s.Lock
	if prepare != nil {
		prepare(p)
	}
	for _, read := range p.Poll(true) {
		if read.Error != "" {
			slog.Warn("a sheet failed", "sheet", read.Sheet.SheetID, "error", read.Error)
		}
	}
	return count() - before, asked.tabs, s.Store.Close()
}

// recording is the LLM with the tabs it is asked about written down.
type recording struct {
	llm.Asker
	tabs []string
}

func (r *recording) JSON(task, system, prompt string, schema map[string]any, maxTokens int) (map[string]any, error) {
	tab, _, _ := strings.Cut(prompt, "\n")
	r.tabs = append(r.tabs, tab)
	return r.Asker.JSON(task, system, prompt, schema, maxTokens)
}
