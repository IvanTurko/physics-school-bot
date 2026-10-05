package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fake is a Doer that answers from a script and remembers what it was asked.
type fake struct {
	calls  int
	bodies [][]byte
	reply  func(call int) (*Response, error)
}

func (f *fake) Do(ctx context.Context, req *Request) (*Response, error) {
	f.calls++
	f.bodies = append(f.bodies, req.Body)
	return f.reply(f.calls - 1)
}

// answering replies with the given statuses, repeating the last one for ever.
func answering(status ...int) *fake {
	return &fake{reply: func(call int) (*Response, error) {
		if call >= len(status) {
			call = len(status) - 1
		}
		return &Response{StatusCode: status[call], Headers: http.Header{}}, nil
	}}
}

// failing never answers at all.
func failing(err error) *fake {
	return &fake{reply: func(int) (*Response, error) { return nil, err }}
}

// ladder wraps a Doer and records the pauses instead of living through them.
func ladder(t *testing.T, inner Doer, p Policy) (Doer, *[]time.Duration) {
	t.Helper()
	var waited []time.Duration
	d := Retrying(inner, p)
	r, ok := d.(*retrier)
	if !ok {
		t.Fatalf("Retrying returned %T, and this policy asked for retries", d)
	}
	r.wait = func(ctx context.Context, d time.Duration) error {
		waited = append(waited, d)
		return nil
	}
	return r, &waited
}

func get() *Request  { return &Request{Method: http.MethodGet, URL: "http://stub"} }
func post() *Request { return &Request{Method: http.MethodPost, URL: "http://stub"} }

func TestRetryingWhatIsRepeated(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		inner *fake
		calls int
	}{
		{"too many requests", answering(429), DefaultRetries + 1},
		{"the server is down", answering(503), DefaultRetries + 1},
		{"no answer at all", failing(errors.New("dial tcp: refused")), DefaultRetries + 1},
		// A definite answer is a definite answer: asking again learns the same.
		{"not authorised", answering(401), 1},
		{"not found", answering(404), 1},
		{"fine", answering(200), 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, waited := ladder(t, c.inner, DefaultPolicy())
			_, _ = d.Do(ctx, get())
			if c.inner.calls != c.calls {
				t.Errorf("%d calls, want %d", c.inner.calls, c.calls)
			}
			if want := c.calls - 1; len(*waited) != want {
				t.Errorf("%d pauses, want %d", len(*waited), want)
			}
		})
	}
}

func TestRetryingSucceedsLate(t *testing.T) {
	inner := &fake{reply: func(call int) (*Response, error) {
		if call < 2 {
			return &Response{StatusCode: 429, Headers: http.Header{}}, nil
		}
		return &Response{StatusCode: 200, Headers: http.Header{}, Body: []byte(`{"say":"third"}`)}, nil
	}}
	d, waited := ladder(t, inner, DefaultPolicy())
	resp, err := d.Do(context.Background(), get())
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.Body) != `{"say":"third"}` {
		t.Errorf("body = %s, want the third attempt's answer", resp.Body)
	}
	if inner.calls != 3 || len(*waited) != 2 {
		t.Errorf("%d calls and %d pauses, want 3 and 2", inner.calls, len(*waited))
	}
}

func TestRetriesExhausted(t *testing.T) {
	ctx := context.Background()
	network := errors.New(`Get "http://stub": dial tcp: connection refused`)

	t.Run("a status names itself", func(t *testing.T) {
		d, _ := ladder(t, answering(429), DefaultPolicy())
		_, err := d.Do(ctx, get())
		if err == nil || err.Error() != "HTTP 429 after 5 retries" {
			t.Fatalf("error = %v", err)
		}
		var e *RetriesExhaustedError
		if !errors.As(err, &e) {
			t.Fatalf("error %v is not a *RetriesExhaustedError", err)
		}
		if e.Retries != DefaultRetries || e.Response == nil || e.Err != nil {
			t.Errorf("exhausted = %+v", e)
		}
		// There was no error underneath, only a status.
		if errors.Unwrap(err) != nil {
			t.Errorf("Unwrap = %v, want nil for a status", errors.Unwrap(err))
		}
	})

	t.Run("the trouble changes", func(t *testing.T) {
		d, _ := ladder(t, answering(429, 429, 500), DefaultPolicy())
		_, err := d.Do(ctx, get())
		if err == nil || err.Error() != "HTTP 500 after 5 retries" {
			t.Errorf("error = %v, want the last failure named", err)
		}
	})

	t.Run("no answer carries its own reason", func(t *testing.T) {
		d, _ := ladder(t, failing(network), DefaultPolicy())
		_, err := d.Do(ctx, get())
		want := network.Error() + " after 5 retries"
		if err == nil || err.Error() != want {
			t.Errorf("error  = %v\nwant   = %s", err, want)
		}
		if !errors.Is(err, network) {
			t.Error("the cause did not survive Unwrap")
		}
	})
}

func TestRetryAfter(t *testing.T) {
	cases := []struct {
		name   string
		status int
		header string
		want   time.Duration // 0 means the ladder decides
	}{
		{"the server names the pause", 429, "2", 2 * time.Second},
		{"and it is still capped", 429, "120", DefaultCap},
		{"a 503 is entitled to it too", 503, "2", 2 * time.Second},
		{"zero means now", 429, "0", 0},
		{"a date is not read", 429, "Wed, 21 Oct 2026 07:28:00 GMT", -1},
		{"nor is a negative", 429, "-1", -1},
		{"nor is nothing", 429, "", -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			inner := &fake{reply: func(int) (*Response, error) {
				h := http.Header{}
				if c.header != "" {
					h.Set("Retry-After", c.header)
				}
				return &Response{StatusCode: c.status, Headers: h}, nil
			}}
			d, waited := ladder(t, inner, DefaultPolicy())
			_, _ = d.Do(context.Background(), get())
			if len(*waited) != DefaultRetries {
				t.Fatalf("%d pauses, want %d", len(*waited), DefaultRetries)
			}
			for i, got := range *waited {
				switch {
				case c.want >= 0 && got != c.want:
					t.Errorf("pause %d = %v, want %v", i, got, c.want)
				case c.want < 0 && got >= min(DefaultBase<<i, DefaultCap):
					t.Errorf("pause %d = %v, and the ladder's base there is %v",
						i, got, min(DefaultBase<<i, DefaultCap))
				}
			}
		})
	}
}

func TestRetryAfterHeader(t *testing.T) {
	for _, c := range []struct {
		header string
		want   time.Duration
		ok     bool
	}{
		{"2", 2 * time.Second, true},
		{"0", 0, true}, // now is a real answer, and the budget is what bounds it
		{"600", 600 * time.Second, true},
		{"-1", 0, false},
		{"Wed, 21 Oct 2026 07:28:00 GMT", 0, false},
		{"soon", 0, false},
		{"", 0, false},
	} {
		h := http.Header{}
		if c.header != "" {
			h.Set("Retry-After", c.header)
		}
		got, ok := retryAfter(&Response{Headers: h})
		if got != c.want || ok != c.ok {
			t.Errorf("Retry-After %q = %v, %v; want %v, %v", c.header, got, ok, c.want, c.ok)
		}
	}
	if _, ok := retryAfter(nil); ok {
		t.Error("a missing answer offered a pause")
	}
}

// TestBackoff checks the shape of the ladder rather than its values, because
// the pauses are jittered.
func TestBackoff(t *testing.T) {
	for i := range DefaultRetries {
		base := min(DefaultBase<<i, DefaultCap)
		for range 20 {
			switch d := backoff(i, DefaultBase, DefaultCap); {
			case d < 0 || d >= base:
				t.Fatalf("pause %d = %v, want it inside [0, %v)", i, d, base)
			case d > DefaultCap:
				t.Fatalf("pause %d = %v, over the ceiling", i, d)
			}
		}
	}
	// A shift that runs off the end must land on the ceiling, not on a negative.
	if d := backoff(62, DefaultBase, DefaultCap); d < 0 || d >= DefaultCap {
		t.Errorf("a far rung gave %v", d)
	}
}

// TestPolicyZeroValues reads the policy itself and not the pauses it makes,
// because one sample of a jittered pause proves nothing.
func TestPolicyZeroValues(t *testing.T) {
	ctx := context.Background()

	t.Run("no retries means no wrapper", func(t *testing.T) {
		inner := answering(429)
		d := Retrying(inner, Policy{})
		if _, ok := d.(*retrier); ok {
			t.Error("a policy asking for no retries still wrapped the Doer")
		}
		_, _ = d.Do(ctx, get())
		if inner.calls != 1 {
			t.Errorf("%d calls, want 1", inner.calls)
		}
	})

	// A zero pause would be a busy loop against a 429.
	t.Run("the zeroes are filled in", func(t *testing.T) {
		r, ok := Retrying(answering(429), Policy{Max: 3}).(*retrier)
		if !ok {
			t.Fatal("a policy asking for retries did not wrap the Doer")
		}
		switch {
		case r.policy.Base != DefaultBase:
			t.Errorf("Base = %v, want %v", r.policy.Base, DefaultBase)
		case r.policy.Cap != DefaultCap:
			t.Errorf("Cap = %v, want %v", r.policy.Cap, DefaultCap)
		case r.policy.Max != 3:
			t.Errorf("Max = %d, want the 3 that was asked for", r.policy.Max)
		}
	})

	// A pause under a second is left as it was given.
	t.Run("what was said is kept", func(t *testing.T) {
		p := Policy{Max: 2, Base: 50 * time.Millisecond, Cap: time.Second, Retry: RetryAny}
		r := Retrying(answering(429), p).(*retrier)
		if r.policy.Base != 50*time.Millisecond || r.policy.Cap != time.Second {
			t.Errorf("policy = %+v, want the one that was given", r.policy)
		}
		d, waited := ladder(t, answering(429), p)
		_, _ = d.Do(ctx, get())
		for i, got := range *waited {
			if got >= 100*time.Millisecond {
				t.Errorf("pause %d = %v, and the base was 50ms", i, got)
			}
		}
	})

	// No policy means the careful one: a POST is not repeated by accident.
	t.Run("no policy means idempotent", func(t *testing.T) {
		inner := answering(429)
		d, _ := ladder(t, inner, Policy{Max: 3})
		_, _ = d.Do(ctx, post())
		if inner.calls != 1 {
			t.Errorf("%d calls on a POST, want 1", inner.calls)
		}
	})
}

func TestPolicyChoosesByMethod(t *testing.T) {
	ctx := context.Background()

	idempotent := answering(429)
	d, _ := ladder(t, idempotent, DefaultPolicy())
	_, _ = d.Do(ctx, get())
	if idempotent.calls != DefaultRetries+1 {
		t.Errorf("GET: %d calls, want %d", idempotent.calls, DefaultRetries+1)
	}

	careful := answering(429)
	d, _ = ladder(t, careful, DefaultPolicy())
	_, _ = d.Do(ctx, post())
	if careful.calls != 1 {
		t.Errorf("POST under the default policy: %d calls, want 1", careful.calls)
	}

	reckless := answering(429)
	p := DefaultPolicy()
	p.Retry = RetryAny
	d, _ = ladder(t, reckless, p)
	_, _ = d.Do(ctx, post())
	if reckless.calls != DefaultRetries+1 {
		t.Errorf("POST under RetryAny: %d calls, want %d", reckless.calls, DefaultRetries+1)
	}
}

func TestPolicyOfYourOwn(t *testing.T) {
	seen := 0
	p := DefaultPolicy()
	p.Max = 2
	p.Retry = func(req *Request, resp *Response, err error) bool {
		seen++
		return resp != nil && resp.StatusCode == http.StatusTeapot
	}

	inner := answering(http.StatusTeapot)
	d, _ := ladder(t, inner, p)
	_, err := d.Do(context.Background(), get())
	if inner.calls != 3 || seen != 3 {
		t.Errorf("%d calls and %d decisions, want 3 and 3", inner.calls, seen)
	}
	if err == nil || err.Error() != "HTTP 418 after 2 retries" {
		t.Errorf("error = %v", err)
	}

	// A 429 is left alone, though the stock policies would repeat it.
	quiet := answering(429)
	d, _ = ladder(t, quiet, p)
	if _, err := d.Do(context.Background(), get()); err != nil {
		t.Errorf("error = %v, want the answer itself", err)
	}
	if quiet.calls != 1 {
		t.Errorf("%d calls, want 1: the caller's policy said no", quiet.calls)
	}
}

// TestBodyIsSentEveryTime holds what an io.Reader would cost: every attempt
// after the first would carry an empty body.
func TestBodyIsSentEveryTime(t *testing.T) {
	inner := answering(429)
	p := DefaultPolicy()
	p.Max = 2
	p.Retry = RetryAny
	d, _ := ladder(t, inner, p)

	body := []byte(`{"order":1}`)
	_, _ = d.Do(context.Background(), &Request{Method: http.MethodPost, URL: "http://stub", Body: body})

	if len(inner.bodies) != 3 {
		t.Fatalf("%d attempts, want 3", len(inner.bodies))
	}
	for i, got := range inner.bodies {
		if string(got) != string(body) {
			t.Errorf("attempt %d carried %q, want %q", i, got, body)
		}
	}
}

func TestInterruptedPause(t *testing.T) {
	inner := answering(429)
	d := Retrying(inner, DefaultPolicy()).(*retrier)
	d.wait = func(ctx context.Context, _ time.Duration) error { return context.Canceled }

	_, err := d.Do(context.Background(), get())
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want the interruption itself", err)
	}
	if inner.calls != 1 {
		t.Errorf("%d calls, want 1", inner.calls)
	}
}

func TestSleep(t *testing.T) {
	if err := sleep(context.Background(), time.Millisecond); err != nil {
		t.Errorf("sleep: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("sleep on a cancelled context = %v, want it to give up at once", err)
	}
}

// TestNoHeaderInErrors covers every error the package makes, because a key
// travels in a header.
func TestNoHeaderInErrors(t *testing.T) {
	const key = "s3cr3t-never-logged"
	req := &Request{
		Method:  http.MethodGet,
		URL:     "http://stub",
		Headers: http.Header{"Authorization": {"Bearer " + key}},
	}
	d, _ := ladder(t, answering(429), DefaultPolicy())
	_, err := d.Do(context.Background(), req)
	if err == nil {
		t.Fatal("want an error to look inside of")
	}
	if got := fmt.Sprintf("%v", err); strings.Contains(got, key) {
		t.Errorf("the error carries the key: %s", got)
	}
}

// openLadder is ladder's twin.
func openLadder(t *testing.T, inner Opener, p Policy) (Opener, *[]time.Duration) {
	t.Helper()
	var waited []time.Duration
	o := RetryingOpen(inner, p)
	r, ok := o.(*openRetrier)
	if !ok {
		t.Fatalf("RetryingOpen returned %T, and this policy asked for retries", o)
	}
	r.wait = func(ctx context.Context, d time.Duration) error {
		waited = append(waited, d)
		return nil
	}
	return r, &waited
}

// refusing is an Opener that never gets an answer.
type refusing struct{ err error }

func (f refusing) Open(context.Context, *Request) (*Stream, error) { return nil, f.err }

func TestRetryingOpenSucceedsLate(t *testing.T) {
	var mu sync.Mutex
	n := 0
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		at := n
		mu.Unlock()
		if at < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, "the third answer")
	})

	o, _ := openLadder(t, New(), Policy{Max: 5, Base: time.Millisecond})
	st, err := o.Open(context.Background(), &Request{Method: http.MethodGet, URL: s.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	got, err := io.ReadAll(st.Body)
	if err != nil || string(got) != "the third answer" {
		t.Errorf("read %q, err %v", got, err)
	}
	if s.hits() != 3 {
		t.Errorf("%d requests, want 3", s.hits())
	}
}

func TestRetryingOpenExhausted(t *testing.T) {
	t.Run("a venue that kept saying no", func(t *testing.T) {
		s := serve(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		})
		o, waited := openLadder(t, New(), Policy{Max: 5, Base: time.Millisecond})
		st, err := o.Open(context.Background(), &Request{Method: http.MethodGet, URL: s.URL})
		if st != nil {
			t.Errorf("an exhausted ladder came back with a stream: %+v", st)
		}
		var ex *RetriesExhaustedError
		if !errors.As(err, &ex) {
			t.Fatalf("err = %T %v, want *RetriesExhaustedError", err, err)
		}
		if ex.Retries != 5 {
			t.Errorf("Retries = %d, want 5", ex.Retries)
		}
		if ex.Response == nil || ex.Response.StatusCode != http.StatusTooManyRequests {
			t.Errorf("Response = %+v, want the last answer", ex.Response)
		}
		if got := err.Error(); got != "HTTP 429 after 5 retries" {
			t.Errorf("error = %q", got)
		}
		if len(*waited) != 5 {
			t.Errorf("%d pauses, want 5", len(*waited))
		}
	})

	// The nil stream has to reach the policy without a panic.
	t.Run("a venue that never answered", func(t *testing.T) {
		boom := errors.New("dial tcp: no route to host")
		o, _ := openLadder(t, refusing{boom}, Policy{Max: 2, Base: time.Millisecond})
		_, err := o.Open(context.Background(), get())

		var ex *RetriesExhaustedError
		if !errors.As(err, &ex) {
			t.Fatalf("err = %T %v, want *RetriesExhaustedError", err, err)
		}
		if ex.Response != nil {
			t.Errorf("Response = %+v, want nothing: there was no answer", ex.Response)
		}
		if !errors.Is(err, boom) {
			t.Error("the cause did not survive")
		}
		if got := err.Error(); got != "dial tcp: no route to host after 2 retries" {
			t.Errorf("error = %q", got)
		}
	})
}

// TestRetryingOpenLeavesNoConnections counts the connections a refused body
// would leak, one per rung.
func TestRetryingOpenLeavesNoConnections(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, "slow down")
	})
	o, _ := openLadder(t, New(), Policy{Max: 4, Base: time.Millisecond})
	if _, err := o.Open(context.Background(), &Request{Method: http.MethodGet, URL: s.URL}); err == nil {
		t.Fatal("want an error")
	}
	if s.hits() != 5 {
		t.Fatalf("%d requests, want 5", s.hits())
	}
	if d := s.dials(); d != 1 {
		t.Errorf("%d connections for 5 attempts, want 1", d)
	}
}

// TestRetryingOpenReadsRetryAfter is the one test that says the headers reach
// the policy, and not only the status.
func TestRetryingOpenReadsRetryAfter(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	o, waited := openLadder(t, New(), Policy{Max: 1, Base: time.Millisecond, Cap: time.Minute})
	if _, err := o.Open(context.Background(), &Request{Method: http.MethodGet, URL: s.URL}); err == nil {
		t.Fatal("want an error")
	}
	if len(*waited) != 1 || (*waited)[0] != 2*time.Second {
		t.Errorf("waited %v, want one pause of 2s -- is Retry-After reaching this door?", *waited)
	}
}

func TestRetryingOpenZeroPolicy(t *testing.T) {
	inner := refusing{errors.New("no")}
	if o := RetryingOpen(inner, Policy{}); o != Opener(inner) {
		t.Errorf("a policy asking for no retries wrapped the opener in %T", o)
	}
	o, ok := RetryingOpen(inner, Policy{Max: 2}).(*openRetrier)
	if !ok {
		t.Fatal("a policy asking for retries did not wrap")
	}
	if o.policy.Base != DefaultBase || o.policy.Cap != DefaultCap || o.policy.Retry == nil {
		t.Errorf("policy = %+v, want the zeroes filled in", o.policy)
	}
}

// TestRetryingOpenClosesADiscardedStream counts closes on the body itself,
// because a body closed unread returns no connection to count.
func TestRetryingOpenClosesADiscardedStream(t *testing.T) {
	var closes atomic.Int32
	answers := &scripted{closed: &closes}

	seen := 0
	p := Policy{Max: 5, Base: time.Millisecond, Retry: func(*Request, *Response, error) bool {
		seen++
		return seen <= 2
	}}
	o, _ := openLadder(t, answers, p)
	st, err := o.Open(context.Background(), get())
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := io.ReadAll(st.Body); string(got) != "answer 3" {
		t.Errorf("read %q, want the third", got)
	}
	if n := closes.Load(); n != 2 {
		t.Errorf("%d of the 2 discarded streams were closed", n)
	}
	// And the one handed back is not closed on the way out: it is the caller's.
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if n := closes.Load(); n != 3 {
		t.Errorf("closes = %d after the caller closed, want 3", n)
	}
}

// scripted is an Opener whose bodies count their own closes.
type scripted struct {
	n      int
	closed *atomic.Int32
}

func (s *scripted) Open(context.Context, *Request) (*Stream, error) {
	s.n++
	return &Stream{
		StatusCode: http.StatusOK,
		Headers:    http.Header{},
		Body:       &countingCloser{Reader: strings.NewReader(fmt.Sprintf("answer %d", s.n)), closed: s.closed},
	}, nil
}

type countingCloser struct {
	io.Reader
	closed *atomic.Int32
}

func (c *countingCloser) Close() error {
	c.closed.Add(1)
	return nil
}
