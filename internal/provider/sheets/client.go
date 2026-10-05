// Package sheets writes rows to a Google Sheets spreadsheet.
package sheets

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/IvanTurko/physics-school-bot/pkg/httpx"
	"golang.org/x/oauth2"
)

// Scope lets a token edit spreadsheets.
const Scope = "https://www.googleapis.com/auth/spreadsheets"

// Timeout bounds one attempt, not the whole run of retries.
const Timeout = 10 * time.Second

// Policy retries on 429, 5xx and network failures: writing the same cells again is safe.
func Policy() httpx.Policy {
	return httpx.Policy{Max: 2, Retry: httpx.RetryAny}
}

// Client writes to one spreadsheet.
type Client struct {
	doer   httpx.Doer
	base   string
	id     string
	tokens oauth2.TokenSource
}

// New creates a client of the spreadsheet with this ID at baseURL, normally
// https://sheets.googleapis.com.
func New(baseURL, spreadsheetID string, tokens oauth2.TokenSource, doer httpx.Doer) *Client {
	return &Client{doer: doer, base: strings.TrimSuffix(baseURL, "/"), id: spreadsheetID, tokens: tokens}
}

// valueRange holds cells written starting at the cell Range names.
type valueRange struct {
	Range  string  `json:"range"`
	Values [][]any `json:"values"`
}

// WriteRows writes each row's cells from column A of the first sheet; rows are
// numbered from 1.
func (c *Client) WriteRows(ctx context.Context, rows map[int64][]any) error {
	tok, err := c.tokens.Token()
	if err != nil {
		return fmt.Errorf("sheets: cannot get token: %w", err)
	}
	data := make([]valueRange, 0, len(rows))
	for n, cells := range rows {
		data = append(data, valueRange{Range: fmt.Sprintf("A%d", n), Values: [][]any{cells}})
	}
	req, err := httpx.NewRequest(http.MethodPost, c.base).
		Segments("v4", "spreadsheets", c.id, "values:batchUpdate").
		Header("Authorization", "Bearer "+tok.AccessToken).
		JSON(map[string]any{
			"valueInputOption": "RAW", // USER_ENTERED would turn 05.10 14:02 into a date
			"data":             data,
		}).
		Build()
	if err != nil {
		return fmt.Errorf("sheets: %w", err)
	}

	resp, err := c.doer.Do(ctx, req)
	if err != nil {
		return fmt.Errorf("sheets: %w", err)
	}
	if !resp.OK() {
		var body struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = resp.JSON(&body) // the status is enough without a message
		return fmt.Errorf("sheets: HTTP %d %s", resp.StatusCode, body.Error.Message)
	}
	return nil
}
