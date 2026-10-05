package httpx

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type stub struct {
	*httptest.Server
	mu    sync.Mutex
	count int
	seen  *http.Request
	body  []byte
	conns int
}

func serve(t *testing.T, h http.HandlerFunc) *stub {
	t.Helper()
	s := &stub{}
	s.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
		}
		s.mu.Lock()
		s.count++
		s.seen = r.Clone(r.Context())
		s.body = body
		s.mu.Unlock()
		h(w, r)
	}))
	// New connections prove the previous body was fully read.
	s.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			s.mu.Lock()
			s.conns++
			s.mu.Unlock()
		}
	}
	s.Start()
	t.Cleanup(s.Close)
	return s
}

func (s *stub) hits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

func (s *stub) dials() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns
}

func (s *stub) saw() (*http.Request, []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen, s.body
}

func TestClientSendsTheRequest(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Answer", "42")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"say":"hello"}`)
	})
	c := New()

	resp, err := c.Do(context.Background(), &Request{
		Method:  http.MethodPost,
		URL:     s.URL + "/thing?q=1",
		Headers: http.Header{"X-Ask": {"please"}},
		Body:    []byte("the body"),
	})
	if err != nil {
		t.Fatal(err)
	}

	seen, body := s.saw()
	switch {
	case seen.Method != http.MethodPost:
		t.Errorf("method = %s", seen.Method)
	case seen.URL.String() != "/thing?q=1":
		t.Errorf("url = %s", seen.URL)
	case seen.Header.Get("X-Ask") != "please":
		t.Errorf("header = %q", seen.Header.Get("X-Ask"))
	case string(body) != "the body":
		t.Errorf("body = %q", body)
	}

	switch {
	case resp.StatusCode != http.StatusCreated:
		t.Errorf("status = %d", resp.StatusCode)
	case resp.Headers.Get("X-Answer") != "42":
		t.Errorf("headers = %v", resp.Headers)
	case string(resp.Body) != `{"say":"hello"}`:
		t.Errorf("body = %s", resp.Body)
	}
}

func TestClientSendsNoEmptyBody(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) })
	if _, err := New().Do(context.Background(), &Request{Method: http.MethodGet, URL: s.URL}); err != nil {
		t.Fatal(err)
	}
	if seen, _ := s.saw(); seen.ContentLength > 0 {
		t.Errorf("Content-Length = %d, want none", seen.ContentLength)
	}
}

func TestClientStatusIsNotAnError(t *testing.T) {
	for _, status := range []int{400, 401, 404, 429, 500, 503} {
		s := serve(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })
		resp, err := New().Do(context.Background(), &Request{Method: http.MethodGet, URL: s.URL})
		if err != nil {
			t.Errorf("%d came back as an error: %v", status, err)
			continue
		}
		if resp.StatusCode != status || resp.OK() {
			t.Errorf("status = %d, OK = %v", resp.StatusCode, resp.OK())
		}
	}
}

func TestClientDefaults(t *testing.T) {
	c := New()
	if c.timeout != DefaultTimeout {
		t.Errorf("timeout = %v, want %v", c.timeout, DefaultTimeout)
	}
	if c.maxBody != DefaultMaxBody {
		t.Errorf("maxBody = %d, want %d", c.maxBody, DefaultMaxBody)
	}
	if c.http.CheckRedirect != nil {
		t.Error("the client has a redirect policy, and Go's own strips Authorization across hosts")
	}
	if New(WithHTTPClient(nil)).http == nil {
		t.Error("a nil client was honoured")
	}

	tr, ok := c.http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, and Open has nothing to bound the silence with", c.http.Transport)
	}
	if tr.ResponseHeaderTimeout != DefaultTimeout {
		t.Errorf("ResponseHeaderTimeout = %v, want %v", tr.ResponseHeaderTimeout, DefaultTimeout)
	}
	// HTTP/2 reads the bound above through the transport it was configured from.
	if !tr.ForceAttemptHTTP2 {
		t.Error("the clone dropped ForceAttemptHTTP2")
	}
	zero := New(WithTimeout(0)).http.Transport.(*http.Transport)
	if zero.ResponseHeaderTimeout != 0 {
		t.Errorf("ResponseHeaderTimeout = %v for a zero timeout, want none", zero.ResponseHeaderTimeout)
	}
}

func TestClientTakesYourTransport(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) })
	used := false
	mine := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		used = true
		return http.DefaultTransport.RoundTrip(r)
	})}
	if _, err := New(WithHTTPClient(mine)).Do(context.Background(),
		&Request{Method: http.MethodGet, URL: s.URL}); err != nil {
		t.Fatal(err)
	}
	if !used {
		t.Error("the client given to WithHTTPClient never saw the request")
	}
	// A client given by hand keeps its own transport.
	if c := New(WithHTTPClient(mine)); c.http.Transport == nil {
		t.Error("the transport was taken away")
	} else if _, ours := c.http.Transport.(*http.Transport); ours {
		t.Error("New replaced the caller's transport with one of its own")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestClientReusesTheConnection does not hold the Close: a body read to EOF
// returns its connection either way.
func TestClientReusesTheConnection(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"say":"hi"}`) })
	c := New()
	for range 3 {
		if _, err := c.Do(context.Background(), &Request{Method: http.MethodGet, URL: s.URL}); err != nil {
			t.Fatal(err)
		}
	}
	if s.hits() != 3 {
		t.Fatalf("%d requests, want 3", s.hits())
	}
	if d := s.dials(); d != 1 {
		t.Errorf("%d connections for 3 requests, want 1: is the body left unclosed?", d)
	}
}

func TestClientMaxBody(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, strings.Repeat("x", 100))
	})

	// The limit itself is not over the limit.
	for _, limit := range []int64{100, 1000} {
		resp, err := New(WithMaxBody(limit)).Do(context.Background(),
			&Request{Method: http.MethodGet, URL: s.URL})
		if err != nil {
			t.Fatalf("limit %d: %v", limit, err)
		}
		if len(resp.Body) != 100 {
			t.Errorf("limit %d: read %d bytes", limit, len(resp.Body))
		}
	}

	// Never a shortened body: a JSON parser swallows a truncated answer.
	if _, err := New(WithMaxBody(99)).Do(context.Background(),
		&Request{Method: http.MethodGet, URL: s.URL}); err == nil {
		t.Error("a body over the limit came back without an error")
	}
	if _, err := New(WithMaxBody(0)).Do(context.Background(),
		&Request{Method: http.MethodGet, URL: s.URL}); err != nil {
		t.Errorf("no limit: %v", err)
	}
}

func TestClientBodyCutOff(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		fmt.Fprint(w, "only this much")
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	})
	if _, err := New().Do(context.Background(), &Request{Method: http.MethodGet, URL: s.URL}); err == nil {
		t.Error("a body that stopped halfway came back whole")
	}
}

func TestClientTimeout(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) { time.Sleep(200 * time.Millisecond) })
	_, err := New(WithTimeout(5*time.Millisecond)).Do(context.Background(),
		&Request{Method: http.MethodGet, URL: s.URL})
	if err == nil {
		t.Fatal("a request that never finished came back without an error")
	}
	if s.hits() != 1 {
		t.Errorf("%d requests, want 1", s.hits())
	}
}

// TestClientUnpacksGzip holds the case a hand-written Accept-Encoding breaks
// silently.
func TestClientUnpacksGzip(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		z := gzip.NewWriter(w)
		defer z.Close()
		fmt.Fprint(z, `{"say":"packed"}`)
	})
	resp, err := New().Do(context.Background(), &Request{Method: http.MethodGet, URL: s.URL})
	if err != nil {
		t.Fatalf("%v -- is Accept-Encoding being set by hand?", err)
	}
	if string(resp.Body) != `{"say":"packed"}` {
		t.Errorf("body = %s", resp.Body)
	}
}

func TestClientRefusesABadURL(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) })
	if _, err := New().Do(context.Background(),
		&Request{Method: http.MethodGet, URL: "http://a\x00b/"}); err == nil {
		t.Error("a url that cannot be parsed was sent")
	}
	if s.hits() != 0 {
		t.Errorf("%d requests reached the server, want none", s.hits())
	}
}

// TestClientDoesNotCarryTheKeyAcrossHosts gives the second server a hostname of
// its own, because two ports on 127.0.0.1 count as one host.
func TestClientDoesNotCarryTheKeyAcrossHosts(t *testing.T) {
	const key = "s3cr3t-never-logged"
	second := serve(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) })
	port := second.Listener.Addr().(*net.TCPAddr).Port
	first := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, fmt.Sprintf("http://elsewhere.invalid:%d/", port), http.StatusFound)
	})

	c := New(WithHTTPClient(&http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			_, p, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			var d net.Dialer
			return d.DialContext(ctx, network, net.JoinHostPort("127.0.0.1", p))
		},
	}}))

	if _, err := c.Do(context.Background(), &Request{
		Method:  http.MethodGet,
		URL:     first.URL,
		Headers: http.Header{"Authorization": {"Bearer " + key}},
	}); err != nil {
		t.Fatal(err)
	}
	if second.hits() != 1 {
		t.Fatalf("the redirect was not followed: %d requests", second.hits())
	}
	if seen, _ := second.saw(); seen.Header.Get("Authorization") != "" {
		t.Errorf("the key crossed to another host: %q", seen.Header.Get("Authorization"))
	}
}

// TestClientAndLadderTogether is the one place the two halves meet.
func TestClientAndLadderTogether(t *testing.T) {
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
		fmt.Fprint(w, `{"say":"third"}`)
	})

	d := Retrying(New(), Policy{Max: 5, Base: time.Millisecond})
	resp, err := d.Do(context.Background(), &Request{Method: http.MethodGet, URL: s.URL})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Say string `json:"say"`
	}
	if err := resp.JSON(&got); err != nil || got.Say != "third" {
		t.Errorf("decoded %+v, err %v", got, err)
	}
	if s.hits() != 3 {
		t.Errorf("%d requests, want 3", s.hits())
	}
}

func TestClientKeepsHeadersOutOfErrors(t *testing.T) {
	const key = "s3cr3t-never-logged"
	s := serve(t, func(w http.ResponseWriter, r *http.Request) { time.Sleep(time.Second) })
	_, err := New(WithTimeout(5*time.Millisecond)).Do(context.Background(), &Request{
		Method:  http.MethodGet,
		URL:     s.URL,
		Headers: http.Header{"Authorization": {"Bearer " + key}},
	})
	if err == nil {
		t.Fatal("want an error to look inside of")
	}
	if strings.Contains(err.Error(), key) {
		t.Errorf("the error carries the key: %v", err)
	}
}
