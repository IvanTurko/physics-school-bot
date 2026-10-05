package config

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

var required = map[string]string{"TELEGRAM_TOKEN": "123:abc", "LLM_API_KEY": "key"}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(envOf(required))
	if err != nil {
		t.Fatal(err)
	}
	if c.TelegramAPIURL != "https://api.telegram.org" || c.LLMBaseURL != "https://openrouter.ai/api/v1" ||
		c.LLMModel != "deepseek/deepseek-v4.1-flash" || c.SheetsAPIURL != "https://sheets.googleapis.com" ||
		c.DBPath != "data/bot.db" || c.DemoMode {
		t.Errorf("defaults not applied: %+v", c)
	}
	if c.TZ.String() != "Europe/Moscow" {
		t.Errorf("TZ = %s, want Europe/Moscow", c.TZ)
	}
}

func TestLoadReadsValues(t *testing.T) {
	env := map[string]string{
		"ADMIN_IDS": " 1, 22 ,", "DEMO_MODE": " true ",
		"GOOGLE_SPREADSHEET_ID": "sheet", "GOOGLE_APPLICATION_CREDENTIALS": "/run/key.json",
	}
	maps.Copy(env, required)
	c, err := Load(envOf(env))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.AdminIDs, []int64{1, 22}) || !c.DemoMode {
		t.Errorf("AdminIDs = %v, DemoMode = %v; want [1 22] and true", c.AdminIDs, c.DemoMode)
	}
	if c.SpreadsheetID != "sheet" || c.GoogleKeyPath != "/run/key.json" {
		t.Errorf("SpreadsheetID = %q, GoogleKeyPath = %q; want sheet and /run/key.json", c.SpreadsheetID, c.GoogleKeyPath)
	}
}

func TestLoadReportsEveryProblem(t *testing.T) {
	_, err := Load(envOf(map[string]string{
		"ADMIN_IDS":             "1,abc",
		"SCHOOL_TZ":             "Mars/Olympus",
		"DEMO_MODE":             "yes please",
		"GOOGLE_SPREADSHEET_ID": "sheet",
	}))
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{
		"missing TELEGRAM_TOKEN",
		"missing LLM_API_KEY",
		"invalid ADMIN_IDS",
		"SCHOOL_TZ",
		"invalid DEMO_MODE",
		"missing GOOGLE_APPLICATION_CREDENTIALS",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
}
