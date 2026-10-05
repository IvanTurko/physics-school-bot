package httpx

import (
	"bytes"
	"context"
	"io"
	"net/http"
)

// maxErrorBody caps how much of a non-2xx body is kept.
const maxErrorBody = 64 << 10

// Stream is an answer whose body has not been read, and the caller closes it.
type Stream struct {
	StatusCode int
	Headers    http.Header
	Body       io.ReadCloser
}

// OK reports whether the status is a 2xx.
func (s *Stream) OK() bool { return s.StatusCode >= 200 && s.StatusCode < 300 }

// Close releases the body, and is safe on a nil Stream and on a second call.
func (s *Stream) Close() error {
	if s == nil || s.Body == nil {
		return nil
	}
	return s.Body.Close()
}

// seen is the status and the headers without the body.
func (s *Stream) seen() *Response {
	if s == nil {
		return nil
	}
	return &Response{StatusCode: s.StatusCode, Headers: s.Headers}
}

// Opener performs a request and returns the answer unread.
type Opener interface {
	Open(ctx context.Context, req *Request) (*Stream, error)
}

// Open performs the request and returns the answer with its body unread and
// undecoded.
func (c *Client) Open(ctx context.Context, req *Request) (*Stream, error) {
	var body io.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}
	r, err := http.NewRequestWithContext(ctx, req.Method, req.URL, body)
	if err != nil {
		return nil, err
	}
	if req.Headers != nil {
		r.Header = req.Headers.Clone()
	}
	if r.Header.Get("Accept-Encoding") == "" {
		r.Header.Set("Accept-Encoding", "gzip")
	}

	resp, err := c.http.Do(r)
	if err != nil {
		return nil, err
	}
	// No defer: the body outlives this call.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return refusal(resp), nil
	}
	return &Stream{StatusCode: resp.StatusCode, Headers: resp.Header, Body: resp.Body}, nil
}

// refusal reads a capped part of a non-2xx body and closes it.
func refusal(resp *http.Response) *Stream {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	resp.Body.Close()
	return &Stream{
		StatusCode: resp.StatusCode,
		Headers:    resp.Header,
		Body:       io.NopCloser(bytes.NewReader(b)),
	}
}
