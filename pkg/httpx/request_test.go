package httpx

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func build(t *testing.T, b *RequestBuilder) *Request {
	t.Helper()
	req, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// TestBuildURL compares whole strings, twice, so that the same parts always
// give the same bytes.
func TestBuildURL(t *testing.T) {
	cases := []struct {
		name string
		make func() *RequestBuilder
		want string
	}{
		// A JSON value in a query parameter is the shape that shows the escaping.
		{
			"a path and a query",
			func() *RequestBuilder {
				return NewRequest(http.MethodGet, "http://stub").
					Segments("widgets", "blue-large").
					Query("filter", `{"since":"2026-03-01"}`)
			},
			"http://stub/widgets/blue-large?filter=" +
				"%7B%22since%22%3A%222026-03-01%22%7D",
		},
		{
			"no query, no question mark",
			func() *RequestBuilder {
				return NewRequest(http.MethodGet, "http://stub").Segments("widgets", "blue")
			},
			"http://stub/widgets/blue",
		},
		// A slash inside a segment must stay inside it, not become a level.
		{
			"a segment is escaped whole",
			func() *RequestBuilder {
				return NewRequest(http.MethodGet, "http://stub").Segments("odd/name with space")
			},
			"http://stub/odd%2Fname%20with%20space",
		},
		// At escapes nothing, so the slashes are the caller's.
		{
			"a given path is left alone",
			func() *RequestBuilder {
				return NewRequest(http.MethodGet, "http://stub").At("/api/v1/widgets/blue")
			},
			"http://stub/api/v1/widgets/blue",
		},
		{
			"a trailing slash does not double",
			func() *RequestBuilder {
				return NewRequest(http.MethodGet, "http://stub/").Segments("widgets")
			},
			"http://stub/widgets",
		},
		{
			"a key named twice keeps both",
			func() *RequestBuilder {
				return NewRequest(http.MethodGet, "http://stub").Query("id", "1").Query("id", "2")
			},
			"http://stub?id=1&id=2",
		},
		{
			"keys come out sorted",
			func() *RequestBuilder {
				return NewRequest(http.MethodGet, "http://stub").
					Queries(url.Values{"zulu": {"1"}, "alpha": {"2"}})
			},
			"http://stub?alpha=2&zulu=1",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := build(t, c.make()).URL; got != c.want {
				t.Errorf("url  = %s\nwant = %s", got, c.want)
			}
			if again := build(t, c.make()).URL; again != c.want {
				t.Errorf("a second build gave %s", again)
			}
		})
	}
}

func TestBuildHeadersAndBody(t *testing.T) {
	req := build(t, NewRequest(http.MethodGet, "http://stub").
		Header("Authorization", "Bearer one").
		Header("Authorization", "Bearer two"))
	if got := req.Headers["Authorization"]; len(got) != 1 || got[0] != "Bearer two" {
		t.Errorf("Authorization = %v, want the second one alone", got)
	}

	req = build(t, NewRequest(http.MethodPost, "http://stub").
		JSON(map[string]int{"n": 1}))
	if string(req.Body) != `{"n":1}` {
		t.Errorf("body = %s", req.Body)
	}
	if got := req.Headers.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}

	if got := build(t, NewRequest(http.MethodPost, "http://stub").Bytes([]byte("raw"))); string(got.Body) != "raw" {
		t.Errorf("body = %s", got.Body)
	}
	if got := build(t, NewRequest(http.MethodGet, "http://stub")); got.Body != nil {
		t.Errorf("body = %v, want none", got.Body)
	}
}

func TestBuildRefuses(t *testing.T) {
	if _, err := NewRequest(http.MethodPost, "http://stub").JSON(make(chan int)).Build(); err == nil {
		t.Error("a channel marshalled into a body")
	}
	if _, err := NewRequest(http.MethodGet, "http://a\x00b").Build(); err == nil {
		t.Error("a url with a control character was built")
	}
}

// TestBuilderPartsAreReadable is the reason the fields are exported: a
// signature is computed over them before they become a string.
func TestBuilderPartsAreReadable(t *testing.T) {
	b := NewRequest(http.MethodGet, "http://stub").
		Segments("order").
		Query("size", "large").
		Query("colour", "blue")

	if b.Method != http.MethodGet {
		t.Errorf("Method = %q", b.Method)
	}
	if got, want := b.Params.Encode(), "colour=blue&size=large"; got != want {
		t.Errorf("Params.Encode() = %q, want %q", got, want)
	}
	b.Header("Signature", "deadbeef")
	if got := build(t, b).Headers.Get("Signature"); got != "deadbeef" {
		t.Errorf("Signature = %q", got)
	}
	if !strings.HasPrefix(build(t, b).URL, "http://stub/order?") {
		t.Errorf("url = %s", build(t, b).URL)
	}
}
