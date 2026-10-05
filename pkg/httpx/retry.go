package httpx

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

// Default policy values, taken for any field left zero.
const (
	DefaultRetries = 5
	DefaultBase    = time.Second
	DefaultCap     = 30 * time.Second
)

// Policy says what is worth asking again and how long to wait first.
type Policy struct {
	Max   int           // retries after the first attempt
	Base  time.Duration // the first pause, doubling from there
	Cap   time.Duration // ceiling for a pause, ours or the server's
	Retry func(req *Request, resp *Response, err error) bool
}

// DefaultPolicy is five retries on a ladder of 1, 2, 4, 8, 16 seconds, for
// idempotent methods only.
func DefaultPolicy() Policy {
	return Policy{
		Max:   DefaultRetries,
		Base:  DefaultBase,
		Cap:   DefaultCap,
		Retry: RetryIdempotent,
	}
}

// worthRepeating reports whether the answer was a 429, a 5xx, or no answer.
func worthRepeating(resp *Response, err error) bool {
	if err != nil {
		return true
	}
	return resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
}

// RetryAny repeats regardless of method.
func RetryAny(req *Request, resp *Response, err error) bool {
	return worthRepeating(resp, err)
}

// RetryIdempotent repeats only the methods RFC 9110 calls idempotent; an empty
// method counts as GET.
func RetryIdempotent(req *Request, resp *Response, err error) bool {
	switch req.Method {
	case "", http.MethodGet, http.MethodHead, http.MethodPut,
		http.MethodDelete, http.MethodOptions, http.MethodTrace:
		return worthRepeating(resp, err)
	}
	return false
}

// waitFunc is the pause between attempts.
type waitFunc func(ctx context.Context, d time.Duration) error

// climb runs attempt until the policy stops asking, calling discard on every
// rejected result. discard may be nil.
func climb[T any](ctx context.Context, req *Request, p Policy, wait waitFunc,
	discard func(T), attempt func() (T, *Response, error),
) (T, error) {
	var zero T
	for i := 0; ; i++ {
		got, seen, err := attempt()
		if !p.Retry(req, seen, err) {
			return got, err
		}
		if discard != nil {
			discard(got)
		}
		if i == p.Max {
			return zero, &RetriesExhaustedError{Retries: p.Max, Response: seen, Err: err}
		}
		d := backoff(i, p.Base, p.Cap)
		if after, ok := retryAfter(seen); ok {
			d = min(after, p.Cap)
		}
		if err := wait(ctx, d); err != nil {
			return zero, err
		}
	}
}

// Retrying wraps a Doer in a ladder of retries; a Max of 0 or less returns
// inner unwrapped, and a zero Base, Cap or Retry takes its default.
func Retrying(inner Doer, p Policy) Doer {
	if p.Max <= 0 {
		return inner
	}
	if p.Base <= 0 {
		p.Base = DefaultBase
	}
	if p.Cap <= 0 {
		p.Cap = DefaultCap
	}
	if p.Retry == nil {
		p.Retry = RetryIdempotent
	}
	return &retrier{inner: inner, policy: p, wait: sleep}
}

type retrier struct {
	inner  Doer
	policy Policy
	wait   waitFunc
}

func (r *retrier) Do(ctx context.Context, req *Request) (*Response, error) {
	return climb(ctx, req, r.policy, r.wait, nil,
		func() (*Response, *Response, error) {
			resp, err := r.inner.Do(ctx, req)
			return resp, resp, err
		})
}

// RetryingOpen wraps an Opener in a ladder of retries; a body that dies
// mid-read is not retried.
func RetryingOpen(inner Opener, p Policy) Opener {
	if p.Max <= 0 {
		return inner
	}
	if p.Base <= 0 {
		p.Base = DefaultBase
	}
	if p.Cap <= 0 {
		p.Cap = DefaultCap
	}
	if p.Retry == nil {
		p.Retry = RetryIdempotent
	}
	return &openRetrier{inner: inner, policy: p, wait: sleep}
}

type openRetrier struct {
	inner  Opener
	policy Policy
	wait   waitFunc
}

func (o *openRetrier) Open(ctx context.Context, req *Request) (*Stream, error) {
	discard := func(s *Stream) { s.Close() }
	return climb(ctx, req, o.policy, o.wait, discard,
		func() (*Stream, *Response, error) {
			s, err := o.inner.Open(ctx, req)
			return s, s.seen(), err
		})
}

// RetriesExhaustedError is what is left when every attempt failed; exactly one of
// Response and Err is set.
type RetriesExhaustedError struct {
	Retries  int       // retries made, not attempts: the sentence counts retries
	Response *Response // the last answer, when there was one
	Err      error     // the last transport failure, when there was not
}

// Error names the last failure.
func (e *RetriesExhaustedError) Error() string {
	if e.Response != nil {
		return fmt.Sprintf("HTTP %d after %d retries", e.Response.StatusCode, e.Retries)
	}
	return fmt.Sprintf("%v after %d retries", e.Err, e.Retries)
}

// Unwrap gives the transport failure, or nil when the ladder ended on a status.
func (e *RetriesExhaustedError) Unwrap() error { return e.Err }

// retryAfter reads Retry-After; only the delta-seconds form is read.
func retryAfter(resp *Response) (time.Duration, bool) {
	if resp == nil {
		return 0, false
	}
	v := resp.Headers.Get("Retry-After")
	if v == "" {
		return 0, false
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs < 0 {
		return 0, false
	}
	return time.Duration(secs) * time.Second, true
}

// backoff is one pause of the ladder: full jitter over a base that doubles.
func backoff(attempt int, base, ceiling time.Duration) time.Duration {
	d := base << attempt
	if d > ceiling || d <= 0 { // <= 0 catches the shift running off the end
		d = ceiling
	}
	return time.Duration(rand.Int64N(int64(d)))
}

// sleep waits, or returns the context's error if it ends first.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
