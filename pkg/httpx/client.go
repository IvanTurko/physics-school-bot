package httpx

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Default client limits for the per-attempt timeout and the response body size.
const (
	DefaultTimeout = 60 * time.Second
	DefaultMaxBody = 64 << 20
)

// Client performs one request each time it is asked, and is safe for concurrent use.
type Client struct {
	http    *http.Client
	timeout time.Duration // per attempt, never per run
	maxBody int64
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient sets a client of your own; a nil one is ignored.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) {
		if h != nil {
			c.http = h
		}
	}
}

// WithTimeout bounds one attempt, and for Open only the wait for the headers;
// 0 means no limit.
func WithTimeout(d time.Duration) Option { return func(c *Client) { c.timeout = d } }

// WithMaxBody refuses an answer larger than n bytes; 0 means no limit.
func WithMaxBody(n int64) Option { return func(c *Client) { c.maxBody = n } }

// New creates a Client with the defaults, then the options.
func New(opts ...Option) *Client {
	c := &Client{timeout: DefaultTimeout, maxBody: DefaultMaxBody}
	for _, o := range opts {
		o(c)
	}
	if c.http == nil {
		// No CheckRedirect: the standard one already drops Authorization across hosts.
		c.http = &http.Client{Transport: transport(c.timeout)}
	}
	return c
}

// transport clones DefaultTransport and bounds the wait for response headers.
func transport(headerTimeout time.Duration) http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = headerTimeout
	return t
}

// Do performs the request and reads the whole body into memory.
func (c *Client) Do(ctx context.Context, req *Request) (*Response, error) {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	var body io.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}
	r, err := http.NewRequestWithContext(ctx, req.Method, req.URL, body)
	if err != nil {
		return nil, err
	}
	// No Accept-Encoding: the transport sets it and unpacks the answer itself.
	if req.Headers != nil {
		r.Header = req.Headers.Clone()
	}

	resp, err := c.http.Do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	read := io.Reader(resp.Body)
	if c.maxBody > 0 {
		read = io.LimitReader(resp.Body, c.maxBody+1)
	}
	b, err := io.ReadAll(read)
	if err != nil {
		return nil, fmt.Errorf("cannot read response body: %w", err)
	}
	if c.maxBody > 0 && int64(len(b)) > c.maxBody {
		return nil, fmt.Errorf("response body is larger than %d bytes", c.maxBody)
	}
	return &Response{StatusCode: resp.StatusCode, Headers: resp.Header, Body: b}, nil
}
