package httpx

import "testing"

func TestResponseOK(t *testing.T) {
	for _, c := range []struct {
		status int
		want   bool
	}{
		{199, false},
		{200, true},
		{204, true},
		{299, true},
		{300, false},
		{401, false},
		{404, false},
		{500, false},
	} {
		if got := (&Response{StatusCode: c.status}).OK(); got != c.want {
			t.Errorf("OK() on %d = %v, want %v", c.status, got, c.want)
		}
	}
}

func TestResponseJSON(t *testing.T) {
	var got struct {
		Say string `json:"say"`
	}
	resp := &Response{Body: []byte(`{"say":"hello"}`)}
	if err := resp.JSON(&got); err != nil {
		t.Fatal(err)
	}
	if got.Say != "hello" {
		t.Errorf("decoded %+v", got)
	}

	// Rubbish must not read as an empty value: those are different answers.
	if err := (&Response{Body: []byte(`{"say":`)}).JSON(&got); err == nil {
		t.Error("a half-written body decoded without complaint")
	}
}
