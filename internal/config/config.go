// Package config reads secrets and limits from the environment. Everything users set lives in the database, not here.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// LoadEnv sets KEY=VALUE lines from a .env file; real environment variables win.
func LoadEnv(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		key, value, _ := strings.Cut(line, "=")
		key, value = strings.TrimSpace(key), strings.Trim(strings.TrimSpace(value), `'"`)
		if _, set := os.LookupEnv(key); !set {
			if err := os.Setenv(key, value); err != nil {
				return err
			}
		}
	}
	return nil
}

// Env is what the service needs from the environment.
type Env struct {
	BotToken      string
	OwnerID       int64
	InviteDays    int     // how long an invite code works
	TZ            string  // quiet hours and the dates the bot shows
	APIURL        string  // a local Bot API server may stand in
	LLMMonthlyUSD float64 // Haiku lays out the tabs within this much a month
}

// String keeps the token out of logs.
func (e Env) String() string {
	return fmt.Sprintf("Env{OwnerID: %d, InviteDays: %d, TZ: %q, APIURL: %q, LLMMonthlyUSD: %g}",
		e.OwnerID, e.InviteDays, e.TZ, e.APIURL, e.LLMMonthlyUSD)
}

// Zone is the bot's time zone.
func (e Env) Zone() *time.Location {
	zone, err := time.LoadLocation(e.TZ)
	if err != nil {
		panic(err) // FromEnviron checked it
	}
	return zone
}

// FromEnviron reads the environment, as a lookup of variables.
func FromEnviron(lookup func(string) string) (Env, error) {
	get := func(name string) string { return strings.TrimSpace(lookup(name)) }
	var missing []string
	for _, name := range []string{"BOT_TOKEN", "OWNER_ID"} {
		if get(name) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return Env{}, fmt.Errorf("not set in the environment or .env: %s", strings.Join(missing, ", "))
	}
	env := Env{BotToken: get("BOT_TOKEN"), InviteDays: 7, TZ: "Europe/Moscow", APIURL: "https://api.telegram.org",
		LLMMonthlyUSD: 1}
	owner, err := strconv.ParseInt(get("OWNER_ID"), 10, 64)
	if err != nil {
		return Env{}, fmt.Errorf("OWNER_ID must be an integer, got %q", get("OWNER_ID"))
	}
	env.OwnerID = owner
	if raw := get("INVITE_DAYS"); raw != "" {
		days, err := strconv.Atoi(raw)
		if err != nil {
			return Env{}, fmt.Errorf("INVITE_DAYS must be an integer, got %q", raw)
		}
		env.InviteDays = days
	}
	if env.InviteDays < 1 || env.InviteDays > 90 {
		return Env{}, fmt.Errorf("INVITE_DAYS must be from 1 to 90")
	}
	if tz := get("TZ"); tz != "" {
		if _, err := time.LoadLocation(tz); err != nil || tz == "Local" {
			return Env{}, fmt.Errorf("TZ is not a time zone: %q", tz)
		}
		env.TZ = tz
	}
	if raw := get("LLM_MONTHLY_USD"); raw != "" {
		usd, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return Env{}, fmt.Errorf("LLM_MONTHLY_USD must be a number, got %q", raw)
		}
		if usd < 0 || usd > 100 {
			return Env{}, fmt.Errorf("LLM_MONTHLY_USD must be from 0 to 100")
		}
		env.LLMMonthlyUSD = usd
	}
	if url := get("BOT_API_URL"); url != "" {
		if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
			return Env{}, fmt.Errorf("BOT_API_URL must be an http(s):// address")
		}
		env.APIURL = strings.TrimRight(url, "/")
	}
	return env, nil
}
