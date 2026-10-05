// Package config reads the bot's settings from the environment.
package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Config is everything the bot needs to start.
type Config struct {
	TelegramToken  string
	TelegramAPIURL string
	AdminIDs       []int64

	LLMBaseURL string
	LLMAPIKey  string
	LLMModel   string

	SheetsAPIURL  string
	SpreadsheetID string // empty turns the sheet sync off
	GoogleKeyPath string // path to a service account's JSON key

	DBPath   string
	TZ       *time.Location
	DemoMode bool
}

// Load reads the config through getenv and reports every problem at once.
func Load(getenv func(string) string) (Config, error) {
	env := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}

	c := Config{
		TelegramToken:  env("TELEGRAM_TOKEN", ""),
		TelegramAPIURL: env("TELEGRAM_API_URL", "https://api.telegram.org"),
		LLMBaseURL:     env("LLM_BASE_URL", "https://openrouter.ai/api/v1"),
		LLMAPIKey:      env("LLM_API_KEY", ""),
		LLMModel:       env("LLM_MODEL", "deepseek/deepseek-v4.1-flash"),
		SheetsAPIURL:   env("SHEETS_API_URL", "https://sheets.googleapis.com"),
		SpreadsheetID:  env("GOOGLE_SPREADSHEET_ID", ""),
		GoogleKeyPath:  env("GOOGLE_APPLICATION_CREDENTIALS", ""),
		DBPath:         env("DB_PATH", "data/bot.db"),
	}

	var errs []error
	if c.TelegramToken == "" {
		errs = append(errs, errors.New("missing TELEGRAM_TOKEN"))
	}
	if c.LLMAPIKey == "" {
		errs = append(errs, errors.New("missing LLM_API_KEY"))
	}
	if c.SpreadsheetID != "" && c.GoogleKeyPath == "" {
		errs = append(errs, errors.New("missing GOOGLE_APPLICATION_CREDENTIALS for GOOGLE_SPREADSHEET_ID"))
	}

	ids, ok := parseIDs(env("ADMIN_IDS", ""))
	if !ok {
		errs = append(errs, fmt.Errorf("invalid ADMIN_IDS %q (want comma-separated Telegram IDs)", getenv("ADMIN_IDS")))
	}
	c.AdminIDs = ids

	tz, err := time.LoadLocation(env("SCHOOL_TZ", "Europe/Moscow"))
	if err != nil {
		errs = append(errs, fmt.Errorf("cannot load SCHOOL_TZ: %w", err))
	}
	c.TZ = tz

	demo, err := strconv.ParseBool(env("DEMO_MODE", "false"))
	if err != nil {
		errs = append(errs, fmt.Errorf("invalid DEMO_MODE %q (want true or false)", getenv("DEMO_MODE")))
	}
	c.DemoMode = demo

	return c, errors.Join(errs...)
}

func parseIDs(s string) ([]int64, bool) {
	var ids []int64
	for part := range strings.SplitSeq(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, false
		}
		ids = append(ids, id)
	}
	return ids, true
}
