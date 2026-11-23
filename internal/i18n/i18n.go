// Package i18n holds the languages: every user reads the bot in English or Russian; the first guess comes from their
// Telegram client.
package i18n

import (
	"fmt"
	"strings"
)

// Languages the bot speaks.
var Languages = []string{"en", "ru"}

// Tr picks the text of a language.
func Tr(lang, en, ru string) string {
	if lang == "ru" {
		return ru
	}
	return en
}

// Guess is Russian for a Russian-speaking client (ru, uk, be, kk), English otherwise.
func Guess(languageCode string) string {
	switch strings.Split(languageCode, "-")[0] {
	case "ru", "uk", "be", "kk":
		return "ru"
	}
	return "en"
}

// Days is "1 day", "3 days"; "1 день", "3 дня", "7 дней".
func Days(lang string, n int) string {
	if lang != "ru" {
		if n == 1 {
			return fmt.Sprintf("%d day", n)
		}
		return fmt.Sprintf("%d days", n)
	}
	return fmt.Sprintf("%d %s", n, Plural(n, "день", "дня", "дней"))
}

// Plural picks the Russian form for n: one, few, many.
func Plural(n int, one, few, many string) string {
	m10, m100 := mod(n, 10), mod(n, 100)
	switch {
	case m10 == 1 && m100 != 11:
		return one
	case m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14):
		return few
	}
	return many
}

// mod is Python's %: never negative for a positive divisor.
func mod(a, b int) int {
	return ((a % b) + b) % b
}
