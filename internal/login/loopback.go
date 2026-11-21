package login

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrTimeout: nobody came back to the loopback server in time.
var ErrTimeout = errors.New("the browser didn't come back in time")

// Loopback is a one-shot server on 127.0.0.1 that waits for the browser's redirect.
type Loopback struct {
	// Redirect is what the authorize page sends the browser to (state included).
	Redirect string
	listener net.Listener
	server   *http.Server
	result   chan result
	state    string
}

type result struct {
	code string
	err  error
}

// Listen opens the server on a free port of 127.0.0.1 (never a public address).
func Listen(state string) (*Loopback, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	l := &Loopback{listener: ln, state: state, result: make(chan result, 1)}
	port := ln.Addr().(*net.TCPAddr).Port
	l.Redirect = fmt.Sprintf("http://127.0.0.1:%d/callback?state=%s", port, url.QueryEscape(state))
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", l.callback)
	l.server = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = l.server.Serve(ln) }()
	return l, nil
}

func (l *Loopback) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !sameState(q.Get("state"), l.state) {
		// Not our sign-in (an old tab, or another page guessing the port): ignore it and keep waiting.
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(page("This link isn't for this sign-in", "Go back to your terminal and run lyrelog login again."))
		return
	}
	if problem := q.Get("error"); problem != "" {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(page("Sign-in cancelled", "Nothing was approved. You can close this tab."))
		l.deliver(result{err: fmt.Errorf("the sign-in was not approved (%s)", strings.TrimSpace(problem))})
		return
	}
	code := q.Get("code")
	if code == "" || len(code) > 512 {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(page("Something is missing", "The dashboard didn't send a code. Run lyrelog login again."))
		return
	}
	_, _ = w.Write(page("You're signed in", "You can close this tab and go back to your terminal."))
	l.deliver(result{code: code})
}

func (l *Loopback) deliver(r result) {
	select {
	case l.result <- r:
	default: // already have one
	}
}

// Wait returns the code, ErrTimeout after timeout, or the context's error.
func (l *Loopback) Wait(ctx context.Context, timeout time.Duration) (string, error) {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case r := <-l.result:
		return r.code, r.err
	case <-t.C:
		return "", ErrTimeout
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Close stops the server, letting the last answer reach the browser.
func (l *Loopback) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = l.server.Shutdown(ctx)
}

// page is the small page the browser shows, in the dashboard's "Studio" look.
func page(title, body string) []byte {
	return []byte(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>` + html.EscapeString(title) + ` · Lyrelog</title>
<style>
:root{color-scheme:light dark;--bg:#fbfbfc;--fg:#0b0d12;--muted:#5b6472;--primary:#2b59ff;--card:#fff;--border:#e4e7ec}
@media (prefers-color-scheme:dark){:root{--bg:#0a0b0e;--fg:#eef0f4;--muted:#8d94a1;--primary:#6d8bff;--card:#111318;--border:#23262d}}
body{margin:0;min-height:100vh;display:grid;place-items:center;background:var(--bg);color:var(--fg);font:16px/1.5 "Instrument Sans",ui-sans-serif,system-ui,-apple-system,sans-serif}
main{max-width:420px;margin:16px;padding:32px;border:1px solid var(--border);border-radius:16px;background:var(--card);text-align:center}
.mark{width:40px;height:40px;margin:0 auto 16px;border-radius:12px;background:var(--primary);display:grid;place-items:center}
.mark svg{width:22px;height:22px}
h1{font-size:1.25rem;margin:0 0 8px;font-weight:600}
p{margin:0;color:var(--muted)}
</style></head><body><main>
<div class="mark"><svg viewBox="0 0 24 24" fill="none" stroke="#fff" stroke-width="2" stroke-linecap="round"><path d="M3 12h3l2-6 4 12 3-9 2 3h4"/></svg></div>
<h1>` + html.EscapeString(title) + `</h1><p>` + html.EscapeString(body) + `</p>
</main></body></html>`)
}
