package httpx

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// RequestBuilder assembles one Request.
type RequestBuilder struct {
	BaseURL string
	Method  string
	Path    string
	Params  url.Values
	Headers http.Header
	Body    []byte

	err error // deferred from JSON, surfaced by Build
}

// NewRequest starts a request against baseURL; a trailing slash is dropped.
func NewRequest(method, baseURL string) *RequestBuilder {
	return &RequestBuilder{
		BaseURL: strings.TrimSuffix(baseURL, "/"),
		Method:  method,
		Params:  make(url.Values),
		Headers: make(http.Header),
	}
}

// At sets the path as given, escaping nothing.
func (b *RequestBuilder) At(path string) *RequestBuilder {
	b.Path = path
	return b
}

// Segments appends path segments, escaping each one.
func (b *RequestBuilder) Segments(seg ...string) *RequestBuilder {
	for _, s := range seg {
		b.Path += "/" + url.PathEscape(s)
	}
	return b
}

// Query adds one pair; a key named twice keeps both values.
func (b *RequestBuilder) Query(k, v string) *RequestBuilder {
	b.Params.Add(k, v)
	return b
}

// Queries adds several pairs at once.
func (b *RequestBuilder) Queries(v url.Values) *RequestBuilder {
	for k, vs := range v {
		for _, s := range vs {
			b.Params.Add(k, s)
		}
	}
	return b
}

// Header sets a header, replacing any previous value.
func (b *RequestBuilder) Header(k, v string) *RequestBuilder {
	b.Headers.Set(k, v)
	return b
}

// Bytes sets the body as it is.
func (b *RequestBuilder) Bytes(p []byte) *RequestBuilder {
	b.Body = p
	return b
}

// JSON marshals v into the body and sets Content-Type; a value that will not
// marshal is returned by Build.
func (b *RequestBuilder) JSON(v any) *RequestBuilder {
	p, err := json.Marshal(v)
	if err != nil {
		b.err = err
		return b
	}
	b.Body = p
	b.Headers.Set("Content-Type", "application/json")
	return b
}

// Build assembles the URL and returns the request.
func (b *RequestBuilder) Build() (*Request, error) {
	if b.err != nil {
		return nil, b.err
	}
	if _, err := url.Parse(b.BaseURL + b.Path); err != nil {
		return nil, fmt.Errorf("cannot build URL from %q: %w", b.BaseURL+b.Path, err)
	}
	u := b.BaseURL + b.Path
	if len(b.Params) > 0 {
		u += "?" + b.Params.Encode()
	}
	return &Request{Method: b.Method, URL: u, Headers: b.Headers, Body: b.Body}, nil
}
