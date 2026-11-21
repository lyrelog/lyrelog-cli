package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHeaders(t *testing.T) {
	var seen []*http.Request
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = append(seen, r)
		bodies = append(bodies, string(b))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "lyr_cli_abc", "lyrelog-cli/test")
	ctx := context.Background()
	if err := c.Get(ctx, "/api/me", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.JSON(ctx, Request{Method: "POST", Path: "/api/logout"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.JSON(ctx, Request{Method: "PATCH", Path: "/api/projects/x", Body: map[string]int{"retention_days": 30}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.JSON(ctx, Request{Method: "POST", Path: "/api/cli/token", NoAuth: true, Body: map[string]string{"code": "c"}}, nil); err != nil {
		t.Fatal(err)
	}
	get, post, patch, token := seen[0], seen[1], seen[2], seen[3]
	for i, r := range seen[:3] {
		if r.Header.Get("Authorization") != "Bearer lyr_cli_abc" {
			t.Errorf("request %d: Authorization %q", i, r.Header.Get("Authorization"))
		}
	}
	for _, r := range seen {
		if r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" {
			t.Errorf("%s %s sent a cookie or an Origin", r.Method, r.URL.Path)
		}
		if r.Header.Get("User-Agent") != "lyrelog-cli/test" {
			t.Errorf("User-Agent %q", r.Header.Get("User-Agent"))
		}
	}
	if get.Header.Get("X-Lyrelog") != "" {
		t.Error("a GET carried X-Lyrelog")
	}
	for _, r := range []*http.Request{post, patch, token} {
		if r.Header.Get("X-Lyrelog") != "1" {
			t.Errorf("%s %s: no X-Lyrelog: 1", r.Method, r.URL.Path)
		}
	}
	if patch.Header.Get("Content-Type") != "application/json" || bodies[2] != `{"retention_days":30}` {
		t.Errorf("PATCH body %q, type %q", bodies[2], patch.Header.Get("Content-Type"))
	}
	if token.Header.Get("Authorization") != "" {
		t.Error("the token exchange sent a token")
	}
}

func TestNoTokenNoRequest(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	err := New(srv.URL, "", "").Get(context.Background(), "/api/me", nil, nil)
	if !errors.Is(err, ErrNoToken) || hits.Load() != 0 {
		t.Fatalf("got %v after %d requests", err, hits.Load())
	}
}

func TestErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/401":
			w.WriteHeader(401)
			w.Write([]byte(`{"error":"your session ended, sign in again"}`))
		case "/reauth":
			w.WriteHeader(403)
			w.Write([]byte(`{"error":"for this, sign in again first","reauth":true}`))
		case "/403":
			w.WriteHeader(403)
			w.Write([]byte(`{"error":"only an owner can create keys"}`))
		case "/html":
			w.WriteHeader(502)
			w.Write([]byte(`<html>bad gateway</html>`))
		case "/redirect":
			http.Redirect(w, r, "https://elsewhere.example/api/me", http.StatusPermanentRedirect)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "t", "")
	check := func(path string, status int, reauth bool, msg string) {
		t.Helper()
		err := c.JSON(context.Background(), Request{Method: "POST", Path: path}, nil)
		var ae *Error
		if !errors.As(err, &ae) {
			t.Fatalf("%s: %v", path, err)
		}
		if ae.Status != status || ae.Reauth != reauth || !strings.Contains(ae.Message, msg) {
			t.Errorf("%s: got %d %v %q", path, ae.Status, ae.Reauth, ae.Message)
		}
	}
	check("/401", 401, false, "session ended")
	check("/reauth", 403, true, "sign in again")
	check("/403", 403, false, "only an owner")
	check("/html", 502, false, "502")
	check("/redirect", 308, false, "elsewhere.example")
}

func TestRetryAfterOnce(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(429)
			w.Write([]byte(`{"error":"too many queries"}`))
			return
		}
		w.Write([]byte(`{"n":1}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "t", "")
	var waited time.Duration
	c.Sleep = func(_ context.Context, d time.Duration) error { waited = d; return nil }
	var out struct{ N int }
	if err := c.Get(context.Background(), "/x", nil, &out); err != nil || out.N != 1 {
		t.Fatalf("%v %v", out, err)
	}
	if waited != 2*time.Second || hits.Load() != 2 {
		t.Fatalf("waited %v, %d requests", waited, hits.Load())
	}
	// A POST is never retried.
	hits.Store(0)
	err := c.JSON(context.Background(), Request{Method: "POST", Path: "/x"}, nil)
	var ae *Error
	if !errors.As(err, &ae) || ae.Status != 429 || ae.RetryAfter != 2*time.Second || hits.Load() != 1 {
		t.Fatalf("POST: %v, %d requests", err, hits.Load())
	}
}

func TestUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	err := New(srv.URL, "t", "").Get(context.Background(), "/api/me", nil, nil)
	var ae *Error
	if !errors.As(err, &ae) || ae.Status != 0 || !strings.Contains(ae.Message, "couldn't reach") {
		t.Fatalf("got %v", err)
	}
}
