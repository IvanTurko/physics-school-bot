package sheets

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/IvanTurko/physics-school-bot/pkg/httpx"
	"golang.org/x/oauth2"
)

var ctx = context.Background()

// serve answers every call with status and answer and records the raw body
// of the last request.
func serve(t *testing.T, status int, answer string) (*Client, *[]byte) {
	sent := new([]byte)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v4/spreadsheets/sheet-id/values:batchUpdate" || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("got %s with auth %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		*sent, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		io.WriteString(w, answer)
	}))
	t.Cleanup(srv.Close)
	tokens := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "tok"})
	return New(srv.URL, "sheet-id", tokens, httpx.New()), sent
}

func TestWriteRowsPostsRawRows(t *testing.T) {
	c, raw := serve(t, 200, `{}`)
	if err := c.WriteRows(ctx, map[int64][]any{1: {"№"}, 13: {12, "Мария"}}); err != nil {
		t.Fatal(err)
	}
	var sent struct {
		ValueInputOption string       `json:"valueInputOption"`
		Data             []valueRange `json:"data"`
	}
	if err := json.Unmarshal(*raw, &sent); err != nil {
		t.Fatal(err)
	}
	ranges := make(map[string][][]any)
	for _, d := range sent.Data {
		ranges[d.Range] = d.Values
	}
	want := map[string][][]any{"A1": {{"№"}}, "A13": {{float64(12), "Мария"}}}
	if sent.ValueInputOption != "RAW" || !reflect.DeepEqual(ranges, want) {
		t.Errorf("sent %s, want RAW rows at A1 and A13", *raw)
	}
}

func TestWriteRowsReportsRefusal(t *testing.T) {
	c, _ := serve(t, 403, `{"error":{"code":403,"message":"The caller does not have permission"}}`)
	err := c.WriteRows(ctx, map[int64][]any{1: {"№"}})
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "The caller does not have permission") {
		t.Errorf("err = %v, want the status and Google's message", err)
	}
}

// brokenTokens fails every token request.
type brokenTokens struct{ err error }

func (b brokenTokens) Token() (*oauth2.Token, error) { return nil, b.err }

func TestWriteRowsNeedsToken(t *testing.T) {
	revoked := errors.New("key revoked")
	c := New("", "sheet-id", brokenTokens{revoked}, httpx.New())
	if err := c.WriteRows(ctx, map[int64][]any{1: {"№"}}); !errors.Is(err, revoked) {
		t.Errorf("err = %v, want the token failure", err)
	}
}
