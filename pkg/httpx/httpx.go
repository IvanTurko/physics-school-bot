// Package httpx sends HTTP requests, reads the answers whole or as a stream, and retries them.
package httpx

import (
	"context"
	"encoding/json"
	"net/http"
)

// Request is one HTTP request.
type Request struct {
	Method  string
	URL     string
	Headers http.Header
	Body    []byte
}

// Response is the answer, with its body read whole.
type Response struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

// OK reports whether the status is a 2xx.
func (r *Response) OK() bool { return r.StatusCode >= 200 && r.StatusCode < 300 }

// JSON decodes the body into v.
func (r *Response) JSON(v any) error { return json.Unmarshal(r.Body, v) }

// Doer performs a request.
type Doer interface {
	Do(ctx context.Context, req *Request) (*Response, error)
}
