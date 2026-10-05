package testkit

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// FakeSheets is a Sheets API server with one sheet in memory.
type FakeSheets struct {
	t    testing.TB
	srv  *httptest.Server
	mu   sync.Mutex
	rows map[string][]string // by range: A1, A2…
}

func newFakeSheets(t testing.TB) *FakeSheets {
	f := &FakeSheets{t: t, rows: make(map[string][]string)}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// Row returns the cells of row n as text.
func (f *FakeSheets) Row(n int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rows[fmt.Sprintf("A%d", n)]
}

func (f *FakeSheets) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v4/spreadsheets/sheet/values:batchUpdate" || r.Header.Get("Authorization") != "Bearer token" {
		f.t.Errorf("got %s with auth %q", r.URL.Path, r.Header.Get("Authorization"))
	}
	var req struct {
		Data []struct {
			Range  string  `json:"range"`
			Values [][]any `json:"values"`
		} `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		f.t.Errorf("cannot read Sheets call: %v", err)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range req.Data {
		cells := make([]string, len(d.Values[0]))
		for i, v := range d.Values[0] {
			cells[i] = fmt.Sprint(v)
		}
		f.rows[d.Range] = cells
	}
	answer(f.t, w, struct{}{})
}
