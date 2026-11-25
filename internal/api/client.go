// Package api talks to the dashboard's JSON API (../lyrelog/internal/api) with a CLI session's
// bearer token: Authorization on every request, X-Lyrelog: 1 on every one that changes something.
// No cookies, no Origin.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client is one server and one sign-in.
type Client struct {
	Base      string // "https://app.lyrelog.com", no trailing slash
	Token     string // "" when signed out; never printed
	UserAgent string
	HTTP      *http.Client
	// Sleep waits before a retry (time.Sleep; tests make it instant).
	Sleep func(context.Context, time.Duration) error
}

// New returns a client with sane timeouts that never follows redirects (a redirect would
// mean the address is wrong, and the token must not travel to wherever it points).
func New(base, token, userAgent string) *Client {
	return &Client{Base: base, Token: token, UserAgent: userAgent, HTTP: &http.Client{
		Timeout: 60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

// Error is an answer the server gave that wasn't a success, or a failure to reach it.
type Error struct {
	Status  int    // 0 when the server couldn't be reached
	Message string // the server's "error", or what went wrong
	Reauth  bool   // 403 {"reauth":true}: the action needs a recent sign-in
	// BrowserOnly: 403 {"browser_only":true}, the server keeps this action to the dashboard
	// (deleting the account or a project, changing sign-in methods).
	BrowserOnly bool
	Path        string // the request's path (/api/...), to point at the right page
	RetryAfter  time.Duration
	Location    string // where a redirect pointed
	Body        []byte // the raw answer (for lyrelog api)
	Err         error  // the network error, if any
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Err }

// ErrNoToken: the CLI has no sign-in for this server.
var ErrNoToken = errors.New("not signed in")

// Request is one call. Path starts with /api/; Query is added to it; Body is JSON-encoded
// unless it is already []byte or json.RawMessage.
type Request struct {
	Method string
	Path   string
	Query  url.Values
	Body   any
	// NoAuth sends no token (the token exchange).
	NoAuth bool
	// ContentType sends a []byte Body as it is, with this type (a symbol map's gzip).
	ContentType string
}

// Response is a successful answer.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

func (c *Client) build(ctx context.Context, r Request) (*http.Request, error) {
	target := c.Base + r.Path
	if len(r.Query) > 0 {
		sep := "?"
		if strings.Contains(r.Path, "?") {
			sep = "&"
		}
		target += sep + r.Query.Encode()
	}
	var body io.Reader
	var isJSON bool
	switch b := r.Body.(type) {
	case nil:
	case []byte:
		body, isJSON = bytes.NewReader(b), r.ContentType == ""
	case json.RawMessage:
		body, isJSON = bytes.NewReader(b), true
	default:
		data, err := json.Marshal(b)
		if err != nil {
			return nil, err
		}
		body, isJSON = bytes.NewReader(data), true
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, target, body)
	if err != nil {
		return nil, err
	}
	if !r.NoAuth {
		if c.Token == "" {
			return nil, ErrNoToken
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		req.Header.Set("X-Lyrelog", "1")
	}
	if isJSON {
		req.Header.Set("Content-Type", "application/json")
	} else if r.ContentType != "" {
		req.Header.Set("Content-Type", r.ContentType)
	}
	req.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	return req, nil
}

// Do sends a request. A 429 or 503 on a GET is retried once when the server asks to wait 10
// seconds or less. Anything but 2xx comes back as *Error.
func (c *Client) Do(ctx context.Context, r Request) (*Response, error) {
	if r.Method == "" {
		r.Method = http.MethodGet
	}
	for attempt := 0; ; attempt++ {
		req, err := c.build(ctx, r)
		if err != nil {
			return nil, err
		}
		res, err := c.HTTP.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, &Error{Message: "couldn't reach " + c.Base + ": " + networkReason(err), Err: err}
		}
		data, err := io.ReadAll(io.LimitReader(res.Body, 256<<20))
		res.Body.Close()
		if err != nil {
			return nil, &Error{Message: "the answer from " + c.Base + " broke off: " + err.Error(), Err: err}
		}
		if res.StatusCode >= 200 && res.StatusCode < 300 {
			return &Response{Status: res.StatusCode, Header: res.Header, Body: data}, nil
		}
		apiErr := parseError(res, data)
		apiErr.Path = r.Path
		if attempt == 0 && r.Method == http.MethodGet && (res.StatusCode == 429 || res.StatusCode == 503) &&
			apiErr.RetryAfter > 0 && apiErr.RetryAfter <= 10*time.Second {
			if err := c.sleep(ctx, apiErr.RetryAfter); err != nil {
				return nil, err
			}
			continue
		}
		return nil, apiErr
	}
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Stream sends a GET and hands back the open body (exports), or an *Error.
func (c *Client) Stream(ctx context.Context, r Request) (io.ReadCloser, error) {
	r.Method = http.MethodGet
	req, err := c.build(ctx, r)
	if err != nil {
		return nil, err
	}
	client := *c.HTTP
	client.Timeout = 0 // an export can take a while; the context ends it
	res, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &Error{Message: "couldn't reach " + c.Base + ": " + networkReason(err), Err: err}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		e := parseError(res, data)
		e.Path = r.Path
		return nil, e
	}
	return res.Body, nil
}

func parseError(res *http.Response, data []byte) *Error {
	e := &Error{Status: res.StatusCode, Body: data}
	var problem struct {
		Error       string `json:"error"`
		Reauth      bool   `json:"reauth"`
		BrowserOnly bool   `json:"browser_only"`
	}
	if json.Unmarshal(data, &problem) == nil && problem.Error != "" {
		e.Message, e.Reauth, e.BrowserOnly = problem.Error, problem.Reauth, problem.BrowserOnly
	} else {
		e.Message = fmt.Sprintf("the server answered %d %s", res.StatusCode, http.StatusText(res.StatusCode))
	}
	if s := res.Header.Get("Retry-After"); s != "" {
		if secs, err := strconv.Atoi(s); err == nil && secs >= 0 {
			e.RetryAfter = time.Duration(secs) * time.Second
		}
	}
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		e.Location = res.Header.Get("Location")
		e.Message = "the server sent the request elsewhere"
		if e.Location != "" {
			e.Message += " (" + e.Location + ")"
		}
	}
	return e
}

func networkReason(err error) string {
	var dns *net.DNSError
	var op *net.OpError
	switch {
	case errors.As(err, &dns):
		return "no such host"
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "Client.Timeout"):
		return "it took too long to answer"
	case errors.As(err, &op) && strings.Contains(op.Error(), "refused"):
		return "connection refused"
	}
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return uerr.Err.Error()
	}
	return err.Error()
}

// JSON sends a request and decodes the answer into out (nil: ignore it).
func (c *Client) JSON(ctx context.Context, r Request, out any) error {
	res, err := c.Do(ctx, r)
	if err != nil {
		return err
	}
	if out == nil || len(res.Body) == 0 {
		return nil
	}
	if err := json.Unmarshal(res.Body, out); err != nil {
		return &Error{Status: res.Status, Message: "the server's answer isn't the JSON this CLI expects: " + err.Error(), Body: res.Body}
	}
	return nil
}

// Get is JSON with GET.
func (c *Client) Get(ctx context.Context, path string, query url.Values, out any) error {
	return c.JSON(ctx, Request{Method: http.MethodGet, Path: path, Query: query}, out)
}

// PathEscape escapes one path segment (a person's id may hold anything).
func PathEscape(s string) string { return url.PathEscape(s) }
