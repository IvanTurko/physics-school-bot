package httpx

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// slowHeaders says nothing until d has passed.
func slowHeaders(d time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(d)
		fmt.Fprint(w, "late")
	}
}

// trickle sends the headers at once and then takes its time over the body.
func trickle(chunks int, pause time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		for range chunks {
			time.Sleep(pause)
			fmt.Fprint(w, "chunk")
			w.(http.Flusher).Flush()
		}
	}
}

func open(t *testing.T, c *Client, url string) *Stream {
	t.Helper()
	s, err := c.Open(context.Background(), &Request{Method: http.MethodGet, URL: url})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestOpenStreamsPastMaxBody(t *testing.T) {
	const size = 1 << 20
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(w, io.LimitReader(neverending('x'), size))
	})
	// A limit the answer below is far past.
	st := open(t, New(WithMaxBody(1024)), s.URL)
	defer st.Close()

	n, err := io.Copy(io.Discard, st.Body)
	if err != nil {
		t.Fatal(err)
	}
	if n != size {
		t.Errorf("read %d bytes, want %d: is maxBody being applied to the stream?", n, size)
	}
}

// TestOpenDoesNotUnpackGzip holds the bytes as they were sent, which is what a
// checksum is computed over.
func TestOpenDoesNotUnpackGzip(t *testing.T) {
	const text = `{"say":"packed"}`
	var packed bytes.Buffer
	z := gzip.NewWriter(&packed)
	fmt.Fprint(z, text)
	z.Close()

	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(packed.Bytes())
	})
	st := open(t, New(), s.URL)
	defer st.Close()

	got, err := io.ReadAll(st.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) == text {
		t.Fatal("the transport unpacked the answer: a checksum over this cannot match the venue's")
	}
	// Byte for byte what the server wrote.
	if want := packed.Bytes(); !bytes.Equal(got, want) {
		t.Errorf("read %d bytes, server wrote %d", len(got), len(want))
	}
	if md5.Sum(got) != md5.Sum(packed.Bytes()) {
		t.Error("the checksum of what arrived differs from the checksum of what was sent")
	}
}

// TestOpenAsksForGzip holds the choice of gzip over identity, which keeps the
// wire compressed.
func TestOpenAsksForGzip(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "hi") })
	open(t, New(), s.URL).Close()

	seen, _ := s.saw()
	if got := seen.Header.Get("Accept-Encoding"); got != "gzip" {
		t.Errorf("Accept-Encoding = %q, want gzip", got)
	}
}

func TestOpenKeepsYourEncoding(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "hi") })
	st, err := New().Open(context.Background(), &Request{
		Method:  http.MethodGet,
		URL:     s.URL,
		Headers: http.Header{"Accept-Encoding": {"identity"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	seen, _ := s.saw()
	if got := seen.Header.Get("Accept-Encoding"); got != "identity" {
		t.Errorf("Accept-Encoding = %q, want the caller's identity", got)
	}
}

// TestOpenDoesNotTouchYourRequest keeps the added header out of the caller's
// request, which is reused for every attempt.
func TestOpenDoesNotTouchYourRequest(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "hi") })

	mine := http.Header{"User-Agent": {"tsumi"}}
	req := &Request{Method: http.MethodGet, URL: s.URL, Headers: mine}
	st, err := New().Open(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if got := req.Headers.Get("Accept-Encoding"); got != "" {
		t.Errorf("the caller's request grew an Accept-Encoding: %q", got)
	}
	if len(req.Headers) != 1 {
		t.Errorf("the caller's headers are %v, want only their own", req.Headers)
	}

	bare := &Request{Method: http.MethodGet, URL: s.URL}
	st, err = New().Open(context.Background(), bare)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if bare.Headers != nil {
		t.Errorf("the caller's request grew a header map: %v", bare.Headers)
	}
}

func TestOpenBodyOutlivesTheHeaderTimeout(t *testing.T) {
	const chunks = 6
	s := serve(t, trickle(chunks, 20*time.Millisecond))

	st := open(t, New(WithTimeout(50*time.Millisecond)), s.URL)
	defer st.Close()

	got, err := io.ReadAll(st.Body)
	if err != nil {
		t.Fatalf("%v -- is there a deadline on the body?", err)
	}
	if want := strings.Repeat("chunk", chunks); string(got) != want {
		t.Errorf("read %q, want %q", got, want)
	}
}

func TestOpenHeaderTimeout(t *testing.T) {
	s := serve(t, slowHeaders(200*time.Millisecond))
	_, err := New(WithTimeout(5*time.Millisecond)).Open(context.Background(),
		&Request{Method: http.MethodGet, URL: s.URL})
	if err == nil {
		t.Fatal("a server that never answered came back without an error")
	}
	if s.hits() != 1 {
		t.Errorf("%d requests, want 1", s.hits())
	}
}

func TestOpenStatusIsNotAnError(t *testing.T) {
	const said = "subscription required"
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, said)
	})
	st := open(t, New(), s.URL)

	if st.OK() {
		t.Error("OK() says a 403 is fine")
	}
	if st.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d", st.StatusCode)
	}
	got, err := io.ReadAll(st.Body)
	if err != nil || string(got) != said {
		t.Errorf("read %q, err %v; want %q -- the venue's words are the reason", got, err, said)
	}
	// Closing twice is safe, so one defer covers both paths.
	if err := st.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if err := (*Stream)(nil).Close(); err != nil {
		t.Errorf("Close on nothing: %v", err)
	}
}

// TestOpenClosesWhatItRejects counts the connections a refused body would leak,
// and a 403 can arrive on hundreds of files.
func TestOpenClosesWhatItRejects(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "no")
	})
	c := New()
	for range 3 {
		st := open(t, c, s.URL)
		_ = st
	}
	if s.hits() != 3 {
		t.Fatalf("%d requests, want 3", s.hits())
	}
	if d := s.dials(); d != 1 {
		t.Errorf("%d connections for 3 refusals, want 1: is the refused body left open?", d)
	}
}

// TestOpenBodyIsTheCallersToClose proves the transport is built once: a new one
// per call would be a new pool, and three dials.
func TestOpenBodyIsTheCallersToClose(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"say":"hi"}`) })
	c := New()
	for range 3 {
		st := open(t, c, s.URL)
		if _, err := io.Copy(io.Discard, st.Body); err != nil {
			t.Fatal(err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if d := s.dials(); d != 1 {
		t.Errorf("%d connections for 3 streams, want 1: a fresh transport per call is a fresh pool", d)
	}
}

// TestOpenErrorBodyIsCapped holds the trade: past the cap the connection is
// dropped instead of drained.
func TestOpenErrorBodyIsCapped(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.Copy(w, io.LimitReader(neverending('e'), 1<<20))
	})
	c := New()
	st := open(t, c, s.URL)
	got, err := io.ReadAll(st.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != maxErrorBody {
		t.Errorf("kept %d bytes of the refusal, want the cap of %d", len(got), maxErrorBody)
	}
	// A body not read to EOF cannot be reused, so the next request dials again.
	open(t, c, s.URL)
	if d := s.dials(); d != 2 {
		t.Errorf("%d connections, want 2: a capped body leaves a connection that cannot be reused", d)
	}
}

func TestOpenReturnsNoStreamOnError(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "hi") })

	t.Run("a url that will not parse", func(t *testing.T) {
		st, err := New().Open(context.Background(),
			&Request{Method: http.MethodGet, URL: "http://a\x00b"})
		if err == nil {
			t.Fatal("a broken url was opened")
		}
		if st != nil {
			t.Errorf("an error came with a stream: %+v", st)
		}
		if s.hits() != 0 {
			t.Errorf("%d requests, want none: the refusal is before the dial", s.hits())
		}
	})

	t.Run("a server that is not there", func(t *testing.T) {
		st, err := New().Open(context.Background(),
			&Request{Method: http.MethodGet, URL: "http://127.0.0.1:1"})
		if err == nil {
			t.Fatal("a request to nowhere came back without an error")
		}
		if st != nil {
			t.Errorf("an error came with a stream: %+v", st)
		}
	})
}

func TestOpenKeepsHeadersOutOfErrors(t *testing.T) {
	const key = "s3cr3t-never-logged"
	s := serve(t, slowHeaders(time.Second))
	_, err := New(WithTimeout(5*time.Millisecond)).Open(context.Background(), &Request{
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

// neverending is a reader of one byte, forever.
type neverending byte

func (b neverending) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(b)
	}
	return len(p), nil
}
