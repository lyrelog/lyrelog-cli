package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/charmbracelet/colorprofile"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go.lyrelog.com/cli/internal/login"
)

var update = flag.Bool("update", false, "rewrite the golden files")

const testToken = "lyr_cli_test_secret_value"

// now is when every test runs: 2026-11-19 12:00 UTC.
var now = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

func TestMain(m *testing.M) {
	time.Local = time.UTC
	os.Exit(m.Run())
}

func ms(d time.Duration) int64 { return now.Add(-d).UnixMilli() }

// fake is the dashboard's API with a little data, as ../lyrelog/internal/api answers it.
type fake struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	requests []*http.Request
	// challenge the authorize page was given, and the code it hands out.
	challenge string
	polls     int
	// dir is the last run's config folder.
	dir string
	// maxRetention, when set, is the project's max_retention_days (its plan's).
	maxRetention int
	// followMessage, when set, is the message of the event that arrives while following.
	followMessage string
	// bodies are the request bodies, by method and path ("DELETE /api/...").
	bodies map[string]string
	// tokenRefusal, when set, is how POST and PATCH /api/account/tokens refuse: "reauth" (the
	// sign-in isn't recent) or "browser_only" (an access token asked).
	tokenRefusal string
	// followed are the received_after values the follow polls sent.
	followed []int64
	// arrived are the events that reach the server while following (received_at set).
	arrived []map[string]any
}

func (f *fake) setChallenge(c string) {
	f.mu.Lock()
	f.challenge = c
	f.mu.Unlock()
}

func newFake(t *testing.T) *fake {
	f := &fake{t: t}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) last(path string) *http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.requests) - 1; i >= 0; i-- {
		if f.requests[i].URL.Path == path {
			return f.requests[i]
		}
	}
	return nil
}

var project = projectData

var projectData = map[string]any{
	"id": "3f9a1c7e20bd", "organization_id": 7, "slug": "tilefall", "name": "Tilefall", "retention_days": 30, "max_retention_days": 30,
	"created_at": "2026-03-02T10:00:00Z", "role": "owner", "platform": "ios", "store_app": nil, "icon_version": 0,
	"person_one": "player", "person_many": "players", "default_environment": "production", "platforms": []string{"ios", "api"},
	"platform_details": map[string]any{}, "icon_sources": []string{}, "attest_app_ids": []string{}, "require_verified": []string{},
}

func summary(p map[string]any, events, errors, users int64, last *int64, hours []map[string]int64) map[string]any {
	out := map[string]any{}
	for k, v := range p {
		out[k] = v
	}
	out["events_24h"], out["errors_24h"], out["users_24h"], out["last_event"], out["hours"] = events, errors, users, last, hours
	return out
}

func hours(pattern ...int64) []map[string]int64 {
	out := make([]map[string]int64, 24)
	for i := range out {
		out[i] = map[string]int64{"events": pattern[i%len(pattern)], "errors": 0}
	}
	return out
}

func event(id string, ago time.Duration, level, category, message, user string, attrs string) map[string]any {
	return map[string]any{"event_id": id, "ts": ms(ago), "level": level, "category": category, "message": message,
		"user_id": user, "install_id": "inst-1", "session_id": "s-" + user, "app_id": "com.example.tilefall", "app_version": "2.4.1",
		"app_build": "241", "os_name": "iOS", "os_version": "26.1", "device_model": "iPhone17,1", "locale": "es_ES",
		"country": "ES", "environment": "production", "platform": "ios", "verified_by": "", "attrs": json.RawMessage(attrs),
		"received_at": ms(ago) + 1000}
}

var events = []map[string]any{
	event("0b7e3d52-8c1a-4f0e-9d3b-5a2f8e1c7d40", 40*time.Second, "error", "checkout", "purchase.failed", "u_8f2a", `{"reason":"card declined","amount":4.99}`),
	event("1c8f4e63-9d2b-4a1f-8e4c-6b3a9f2d8e51", 3*time.Minute, "info", "game", "game.finished", "u_8f2a", `{"board":{"size":9},"result":"win"}`),
	event("2d9a5f74-ae3c-4b2a-9f5d-7c4b0a3e9f62", 2*time.Hour, "warning", "sync", "sync.slow", "u_77c1", `{"ms":5400}`),
}

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	f.mu.Lock()
	f.requests = append(f.requests, r)
	if f.bodies == nil {
		f.bodies = map[string]string{}
	}
	f.bodies[r.Method+" "+r.URL.Path] = string(body)
	maxRetention := f.maxRetention
	f.mu.Unlock()
	project := project
	if maxRetention > 0 {
		project = map[string]any{}
		for k, v := range projectData {
			project[k] = v
		}
		project["max_retention_days"], project["retention_days"] = maxRetention, maxRetention
	}
	w.Header().Set("Content-Type", "application/json")
	reply := func(status int, v any) {
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(v)
	}
	if r.URL.Path == "/api/cli/token" {
		var body struct{ Code, Verifier, Name string }
		json.NewDecoder(r.Body).Decode(&body)
		if r.Header.Get("X-Lyrelog") != "1" {
			reply(403, map[string]string{"error": "cross-site request refused"})
			return
		}
		f.mu.Lock()
		challenge := f.challenge
		f.mu.Unlock()
		if body.Code != "the-code" || login.Challenge(body.Verifier) != challenge {
			reply(400, map[string]string{"error": "that code is unknown, used or expired"})
			return
		}
		reply(200, map[string]any{"token": testToken, "expires_at": now.Add(30 * 24 * time.Hour).Unix(),
			"user": map[string]any{"id": 1, "email": "ana@example.com", "name": "Ana"}})
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		reply(401, map[string]string{"error": "your session ended, sign in again"})
		return
	}
	if r.Method != "GET" && r.Header.Get("X-Lyrelog") != "1" {
		reply(403, map[string]string{"error": "cross-site request refused"})
		return
	}
	last := ms(40 * time.Second)
	switch p := r.URL.Path; {
	case p == "/api/me":
		reply(200, map[string]any{"user": map[string]any{"id": 1, "email": "ana@example.com", "name": "Ana"},
			"organizations": []map[string]any{{"id": 7, "name": "Ana's apps", "role": "owner", "plan": "free", "plan_until": nil}}, "admin": false})
	case p == "/api/logout":
		w.WriteHeader(204)
	case p == "/api/projects":
		web := map[string]any{}
		for k, v := range project {
			web[k] = v
		}
		web["id"], web["slug"], web["name"], web["default_environment"] = "a1b2c3d4e5f6", "tilefall-web", "Tilefall web", ""
		if r.URL.Query().Get("stats") == "0" {
			reply(200, map[string]any{"projects": []any{project, web}})
			return
		}
		reply(200, map[string]any{"projects": []any{
			summary(project, 18_420, 37, 412, &last, hours(0, 120, 800, 1400, 900, 300)),
			summary(web, 0, 0, 0, nil, hours(0)),
		}})
	case p == "/api/projects/3f9a1c7e20bd":
		if r.Method == "DELETE" {
			reply(403, map[string]any{"error": "do this in the browser: deleting a project needs a fresh passkey or provider sign-in", "browser_only": true})
			return
		}
		if r.Method == "PATCH" {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			out := map[string]any{}
			for k, v := range project {
				out[k] = v
			}
			if n, ok := body["name"]; ok {
				out["name"] = n
			}
			reply(200, map[string]any{"project": out})
			return
		}
		reply(200, map[string]any{"project": project})
	case p == "/api/projects/3f9a1c7e20bd/events":
		list := events
		if after := r.URL.Query().Get("received_after"); after != "" {
			// A follow poll: what arrived after received_after, whatever its own time.
			n, _ := strconv.ParseInt(after, 10, 64)
			f.mu.Lock()
			f.polls++
			f.followed = append(f.followed, n)
			arrived := f.arrived
			message := f.followMessage
			f.mu.Unlock()
			if arrived == nil {
				if message == "" {
					message = "app.crashed"
				}
				crash := event("3eab6085-bf4d-4c3b-8a6e-8d5c1b4f0a73", 0, "critical", "app", message, "u_8f2a", `{}`)
				crash["received_at"] = ms(0) + 2000
				arrived = []map[string]any{crash}
			}
			list = nil
			for _, e := range arrived {
				if e["received_at"].(int64) > n {
					list = append(list, e)
				}
			}
			// The server sorts by the events' own time.
			sort.Slice(list, func(i, j int) bool { return list[i]["ts"].(int64) < list[j]["ts"].(int64) })
		}
		reply(200, map[string]any{"events": list, "next_cursor": nil})
	case p == "/api/projects/3f9a1c7e20bd/lookup":
		if r.URL.Query().Get("q") != events[0]["event_id"] {
			reply(200, map[string]any{"event": nil, "players": []any{}, "sessions": []any{}, "messages": []any{}, "areas": []any{}})
			return
		}
		reply(200, map[string]any{"event": events[0], "players": []any{}, "sessions": []any{}, "messages": []any{}, "areas": []any{}})
	case strings.HasPrefix(p, "/api/projects/3f9a1c7e20bd/events/"):
		e := map[string]any{}
		for k, v := range events[0] {
			e[k] = v
		}
		e["payload"] = json.RawMessage(`{"stripe":{"code":"card_declined","decline_code":"insufficient_funds"}}`)
		reply(200, map[string]any{"event": e})
	case p == "/api/projects/3f9a1c7e20bd/issues":
		reply(200, map[string]any{"issues": []map[string]any{
			{"issue": "i1", "area": "checkout", "name": "purchase.failed", "count": 214, "players": 61, "sessions": 80, "previous": 120,
				"first_seen": ms(6 * 24 * time.Hour), "last_seen": ms(40 * time.Second), "worst": "error", "versions": []string{"2.4.1", "2.4.0"},
				"trend": []int64{3, 5, 2, 8, 13, 21, 30}},
			{"issue": "i2", "area": "app", "name": "app.crashed", "count": 12, "players": 9, "sessions": 12, "previous": 0,
				"first_seen": ms(26 * time.Hour), "last_seen": ms(3 * time.Hour), "worst": "critical", "versions": []string{"2.4.1"},
				"trend": []int64{0, 0, 0, 0, 1, 4, 7}},
		}, "step_ms": 3600000, "first_bucket": ms(7 * 24 * time.Hour)})
	case p == "/api/projects/3f9a1c7e20bd/keys":
		if r.Method == "POST" {
			var body struct{ Scope, Name string }
			json.NewDecoder(r.Body).Decode(&body)
			if body.Scope != "ingest" {
				reply(403, map[string]any{"error": "for this, sign in again first", "reauth": true})
				return
			}
			reply(201, map[string]any{"key": map[string]any{"id": "k3", "scope": "ingest", "name": body.Name, "prefix": "lyr_ing_9f",
				"created_at": now.Format(time.RFC3339), "last_used_at": nil, "revoked_at": nil}, "secret": "lyr_ing_9f00000000000000000000"})
			return
		}
		used := now.Add(-5 * time.Minute).Format(time.RFC3339)
		reply(200, map[string]any{"keys": []map[string]any{
			{"id": "k1", "scope": "ingest", "name": "iOS app", "prefix": "lyr_ing_3a", "created_at": "2026-03-02T10:00:00Z", "last_used_at": used, "revoked_at": nil},
			{"id": "k2", "scope": "server", "name": "tilefall-api", "prefix": "lyr_srv_c4", "created_at": "2026-10-01T10:00:00Z", "last_used_at": nil, "revoked_at": "2026-11-01T10:00:00Z"},
		}})
	case p == "/api/projects/3f9a1c7e20bd/sessions":
		reply(200, map[string]any{"sessions": []map[string]any{
			{"sid": "s-u_8f2a-0001", "who": "u_8f2a", "started": ms(20 * time.Minute), "ended": ms(40 * time.Second), "duration_ms": 1160000,
				"events": 48, "errors": 1, "first_event": "app.opened", "last_event": "purchase.failed", "first_error": "purchase.failed",
				"on_device": "iPhone17,1", "on_version": "2.4.1", "place": "ES", "total": 120},
		}, "total": 120, "page": 0, "page_size": 50})
	case p == "/api/projects/3f9a1c7e20bd/end-users/u_8f2a":
		reply(200, map[string]any{"events": 1530, "first": ms(40 * 24 * time.Hour), "last": ms(40 * time.Second)})
	case strings.HasPrefix(p, "/api/projects/3f9a1c7e20bd/end-users/"):
		// Someone the project never heard from: the server answers with zeros.
		reply(200, map[string]any{"events": 0, "first": 0, "last": 0})
	case p == "/api/account/passkeys/3" && r.Method == "DELETE":
		reply(403, map[string]any{"error": "do this in the browser: sign-in methods change on the account page", "browser_only": true})
	case p == "/api/account/sessions":
		reply(200, map[string]any{"sessions": []map[string]any{
			{"id": 41, "kind": "browser", "name": "", "user_agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
				"created_at": "2026-11-10T09:00:00Z", "last_used_at": "2026-11-19T11:50:00Z", "expires_at": "2026-12-03T11:50:00Z", "idle_ends": true, "current": false},
			{"id": 42, "kind": "browser", "name": "", "user_agent": "Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1",
				"created_at": "2026-11-12T09:00:00Z", "last_used_at": "2026-11-18T20:00:00Z", "expires_at": "2026-12-02T20:00:00Z", "idle_ends": true, "current": false},
			{"id": 43, "kind": "cli", "name": "anas-mac", "user_agent": "lyrelog-cli/dev (darwin/arm64)",
				"created_at": "2026-11-19T10:00:00Z", "last_used_at": "2026-11-19T12:00:00Z", "expires_at": "2026-12-19T10:00:00Z", "idle_ends": true, "current": true},
			{"id": 44, "kind": "cli", "name": "build-box", "user_agent": "lyrelog-cli/dev (linux/amd64)",
				"created_at": "2026-11-01T10:00:00Z", "last_used_at": "2026-11-18T10:00:00Z", "expires_at": "2027-01-30T10:00:00Z", "idle_ends": false, "current": false},
			{"id": 47, "kind": "cli", "name": "studio", "user_agent": "lyrelog-cli/dev (darwin/arm64)",
				"created_at": "2026-11-02T10:00:00Z", "last_used_at": "2026-11-17T10:00:00Z", "expires_at": "9999-12-31T00:00:00Z", "idle_ends": false, "current": false},
			{"id": 45, "kind": "token", "name": "GitHub Actions, deploy", "user_agent": "",
				"created_at": "2026-11-05T10:00:00Z", "last_used_at": "2026-11-19T11:00:00Z", "expires_at": "2027-02-03T10:00:00Z", "idle_ends": false, "current": false},
			{"id": 46, "kind": "token", "name": "nightly export", "user_agent": "",
				"created_at": "2026-10-01T10:00:00Z", "last_used_at": "2026-10-01T10:00:00Z", "expires_at": "9999-12-31T00:00:00Z", "idle_ends": false, "current": false},
		}})
	case p == "/api/account/sessions/45" && r.Method == "DELETE":
		w.WriteHeader(204)
	case p == "/api/account/tokens" && r.Method == "POST", p == "/api/account/tokens/45" && r.Method == "PATCH":
		f.mu.Lock()
		refusal := f.tokenRefusal
		f.mu.Unlock()
		switch refusal {
		case "reauth":
			reply(403, map[string]any{"error": "for this, sign in again first", "reauth": true})
			return
		case "browser_only":
			reply(403, map[string]any{"error": "an access token can't make or change tokens: do it from the dashboard or after lyrelog login", "browser_only": true})
			return
		}
		var body struct {
			Name          string `json:"name"`
			ExpiresInDays *int   `json:"expires_in_days"`
		}
		json.Unmarshal(body0(r), &body)
		if body.ExpiresInDays == nil {
			reply(400, map[string]any{"error": "expires_in_days is 30, 90, 365, or 0 for never"})
			return
		}
		expires := "9999-12-31T00:00:00Z"
		if *body.ExpiresInDays > 0 {
			expires = now.AddDate(0, 0, *body.ExpiresInDays).Format(time.RFC3339)
		}
		session := map[string]any{"id": 48, "kind": "token", "name": body.Name, "user_agent": "", "created_at": now.Format(time.RFC3339),
			"last_used_at": now.Format(time.RFC3339), "expires_at": expires, "idle_ends": false, "current": false}
		if r.Method == "PATCH" {
			session["id"], session["name"] = 45, "GitHub Actions, deploy"
			reply(200, map[string]any{"session": session, "never_expires": *body.ExpiresInDays == 0})
			return
		}
		reply(201, map[string]any{"token": "lyr_tok_made_once_0000000000000000", "session": session, "never_expires": *body.ExpiresInDays == 0})
	case p == "/api/projects/3f9a1c7e20bd/values":
		reply(200, map[string]any{"values": []map[string]any{{"value": "9", "count": 820, "players": 140}, {"value": "13", "count": 310, "players": 77}}})
	case p == "/api/projects/3f9a1c7e20bd/summary":
		reply(200, map[string]any{"totals": map[string]int64{"events": 18420, "users": 412, "sessions": 1300, "errors": 37},
			"facets": map[string]any{
				"app_version":  []map[string]any{{"value": "2.4.1", "count": 12000}, {"value": "2.4.0", "count": 6000}},
				"device_model": []map[string]any{{"value": "iPhone17,1", "count": 9000}},
				"os_version":   []map[string]any{{"value": "26.1", "count": 15000}},
				"level":        []map[string]any{{"value": "info", "count": 17000}, {"value": "error", "count": 37}},
			}})
	case p == "/api/projects/3f9a1c7e20bd/rates":
		pts := []map[string]any{{"t": ms(48 * time.Hour), "v": 2.5}, {"t": ms(24 * time.Hour), "v": 4.0}}
		series := []map[string]any{{"event": "", "group": "", "total": 3.2, "hits": 32, "tries": 1000, "points": pts}}
		if r.URL.Query().Get("breakdown") != "" {
			series = []map[string]any{{"event": "", "group": "2.4.1", "total": 4.1, "hits": 29, "tries": 700, "points": pts},
				{"event": "", "group": "2.4.0", "total": 1, "hits": 3, "tries": 300, "points": pts}}
		}
		reply(200, map[string]any{"series": series, "step_ms": 86400000, "metric": "rate"})
	case p == "/api/projects/3f9a1c7e20bd/releases":
		reply(200, map[string]any{"releases": []map[string]any{
			{"version": "2.4.1", "events": 12000, "players": 380, "sessions": 1400, "error_sessions": 52, "errors": 190, "criticals": 12, "first_seen": ms(5 * 24 * time.Hour), "last_seen": ms(time.Minute)},
			{"version": "2.4.0", "events": 6000, "players": 120, "sessions": 700, "error_sessions": 3, "errors": 24, "criticals": 0, "first_seen": ms(20 * 24 * time.Hour), "last_seen": ms(2 * time.Hour)},
		}, "adoption": []any{}, "step_ms": 86400000})
	case p == "/api/projects/3f9a1c7e20bd/funnel":
		reply(200, map[string]any{"steps": []map[string]any{
			{"event": "onboarding.start", "people": 400, "median_ms": nil},
			{"event": "onboarding.name", "people": 310, "median_ms": 42000},
			{"event": "onboarding.done", "people": 188, "median_ms": 95000},
		}, "window_ms": 3600000})
	case p == "/api/projects/3f9a1c7e20bd/trends":
		pts := func(vs ...float64) []map[string]any {
			out := []map[string]any{}
			for i, v := range vs {
				out = append(out, map[string]any{"t": ms(time.Duration(len(vs)-i) * 24 * time.Hour), "v": v})
			}
			return out
		}
		reply(200, map[string]any{"series": []map[string]any{
			{"event": "game.finished", "group": "2.4.1", "total": 5200, "points": pts(100, 400, 900, 1200, 1300, 1300)},
			{"event": "game.finished", "group": "2.4.0", "total": 1900, "points": pts(700, 500, 300, 200, 120, 80)},
		}, "step_ms": 86400000, "metric": "events"})
	case p == "/api/projects/3f9a1c7e20bd/platforms/ios" && r.Method == http.MethodDelete:
		// The project's last platform gone.
		left := map[string]any{}
		for k, v := range projectData {
			left[k] = v
		}
		left["platforms"] = []string{}
		reply(200, map[string]any{"project": left})
	case p == "/api/organizations/7/usage":
		reply(200, map[string]any{"organization": "Ana's apps", "plan": "free", "plan_until": nil,
			"limits": map[string]int64{"hard_events": 1_000_000, "included_events": 1_000_000}, "events": 412_380,
			"month_start": "2026-11-01T00:00:00Z", "next_month": "2026-12-01T00:00:00Z",
			"projects": []map[string]any{{"id": "3f9a1c7e20bd", "name": "Tilefall", "events": 401_200}, {"id": "a1b2c3d4e5f6", "name": "Tilefall web", "events": 11_180}},
			"days":     []map[string]any{{"day": "2026-11-17", "events": 21000}, {"day": "2026-11-18", "events": 30000}, {"day": "2026-11-19", "events": 12000}}, "days_in_month": 30})
	default:
		reply(404, map[string]string{"error": "no such endpoint"})
	}
}

// body0 is a request's body, read again.
func body0(r *http.Request) []byte {
	b, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(b))
	return b
}

// run runs the CLI as a person at a terminal (tty) or as a program, with a saved sign-in.
type runOpts struct {
	tty      bool
	env      map[string]string
	token    string // "" uses the test token; "-" none
	stdin    string
	open     func(string) error
	project  string // default project ("" = tilefall)
	noConfig bool
	noWidth  bool // no terminal width (a pipe): the CLI picks one
}

func (f *fake) run(t *testing.T, o runOpts, args ...string) (string, string, int) {
	t.Helper()
	dir := t.TempDir()
	env := map[string]string{"LYRELOG_CONFIG_DIR": dir, "LYRELOG_URL": f.srv.URL}
	for k, v := range o.env {
		env[k] = v
	}
	if !o.noConfig {
		token := testToken
		if o.token == "-" {
			token = ""
		} else if o.token != "" {
			token = o.token
		}
		proj := `"project":"3f9a1c7e20bd","project_name":"Tilefall"`
		if o.project == "-" {
			proj = `"project":""`
		}
		cfg := fmt.Sprintf(`{"hosts":{%q:{"token":%q,"expires_at":%d,"user_email":"ana@example.com",%s}}}`, f.srv.URL, token, now.Add(30*24*time.Hour).Unix(), proj)
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	dark := false
	profile := colorprofile.TrueColor
	if env["TERM"] == "dumb" {
		profile = colorprofile.NoTTY
	}
	a := &App{Profile: &profile,
		Stdin: strings.NewReader(o.stdin), Stdout: &stdout, Stderr: &stderr,
		Getenv: func(k string) string { return env[k] }, Now: func() time.Time { return now },
		StdinTTY: o.tty, StdoutTTY: o.tty, StderrTTY: o.tty, Width: 100, Dark: &dark,
		HTTP: f.srv.Client(), OpenBrowser: o.open, Hostname: func() (string, error) { return "anas-mac.local", nil },
	}
	if o.noWidth {
		a.Width = 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	code := a.Run(ctx, args)
	f.dir = dir
	return stdout.String(), stderr.String(), code
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update to make it)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from the golden file.\ngot:\n%s\nwant:\n%s", name, got, want)
	}
}

var _ = io.Discard

// runWithStdin runs as a person at a terminal with stdin from r, signed out.
func (f *fake) runWithStdin(t *testing.T, r io.Reader, stderr *lockedBuffer, args ...string) (string, string, int) {
	dir := t.TempDir()
	env := map[string]string{"LYRELOG_CONFIG_DIR": dir, "LYRELOG_URL": f.srv.URL, "NO_COLOR": "1"}
	var stdout bytes.Buffer
	dark := false
	a := &App{Stdin: r, Stdout: &stdout, Stderr: stderr, Getenv: func(k string) string { return env[k] }, Now: func() time.Time { return now },
		StdinTTY: true, StdoutTTY: true, StderrTTY: true, Width: 100, Dark: &dark, HTTP: f.srv.Client(),
		Hostname: func() (string, error) { return "anas-mac", nil }}
	code := a.Run(context.Background(), args)
	return stdout.String(), stderr.String(), code
}

// printedChallenge reads the challenge from the URL the CLI printed so far.
func printedChallenge(stderr *lockedBuffer) string {
	text := stderr.String()
	i := strings.Index(text, "challenge=")
	if i < 0 {
		return ""
	}
	rest := text[i+len("challenge="):]
	if j := strings.IndexAny(rest, "&\n "); j >= 0 {
		return rest[:j]
	}
	return ""
}

// runFor runs a command in JSON mode for a while, then stops it as Ctrl-C would.
func (f *fake) runFor(t *testing.T, d time.Duration, args ...string) (string, string) {
	dir := t.TempDir()
	cfg := fmt.Sprintf(`{"hosts":{%q:{"token":%q,"project":"3f9a1c7e20bd"}}}`, f.srv.URL, testToken)
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o600)
	env := map[string]string{"LYRELOG_CONFIG_DIR": dir, "LYRELOG_URL": f.srv.URL}
	out, errOut := &lockedBuffer{}, &lockedBuffer{}
	a := &App{Stdin: strings.NewReader(""), Stdout: out, Stderr: errOut, Getenv: func(k string) string { return env[k] },
		Now: time.Now, HTTP: f.srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	a.Run(ctx, args)
	return out.String(), errOut.String()
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
