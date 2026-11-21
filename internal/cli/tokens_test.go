package cli

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

var plain = runOpts{tty: true, env: map[string]string{"NO_COLOR": "1"}}

func TestTokensListsOnlyTokens(t *testing.T) {
	f := newFake(t)
	out, errOut, code := f.run(t, runOpts{}, "tokens")
	var list []map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &list) != nil {
		t.Fatalf("exit %d: %q %s", code, out, errOut)
	}
	if len(list) != 2 || list[0]["kind"] != "token" || list[1]["kind"] != "token" {
		t.Errorf("want the two tokens, got %v", list)
	}
	out, _, _ = f.run(t, plain, "tokens")
	golden(t, "tokens.human", out)
	if !strings.Contains(out, "⚠ never") || !strings.Contains(out, "Feb 3, 2027") {
		t.Errorf("expiries: %s", out)
	}
}

func TestAccountSessionsLeaveTokensOut(t *testing.T) {
	f := newFake(t)
	out, _, code := f.run(t, runOpts{}, "account", "sessions")
	var list []map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &list) != nil {
		t.Fatalf("exit %d: %q", code, out)
	}
	for _, s := range list {
		if s["kind"] == "token" {
			t.Errorf("a token in account sessions: %v", s)
		}
	}
	if len(list) != 5 {
		t.Errorf("%d sessions, want 5", len(list))
	}
	out, _, _ = f.run(t, plain, "account", "sessions")
	if strings.Contains(out, "GitHub Actions") {
		t.Errorf("a token in the table: %s", out)
	}
	if !strings.Contains(out, "kept to Jan 30, 2027") || !strings.Contains(out, "kept, never ends") {
		t.Errorf("kept sign-ins: %s", out)
	}
}

func TestTokenCreate(t *testing.T) {
	f := newFake(t)
	out, errOut, code := f.run(t, plain, "tokens", "create", "--name", "GitHub Actions")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if body := f.bodies["POST /api/account/tokens"]; body != `{"expires_in_days":90,"name":"GitHub Actions"}` {
		t.Errorf("body %s", body)
	}
	if !strings.Contains(out, "lyr_tok_made_once_0000000000000000") || !strings.Contains(out, "store it in your CI's secrets as LYRELOG_TOKEN") {
		t.Errorf("the token, once, with where to keep it: %s", out)
	}
	if strings.Contains(out, "never expires") {
		t.Errorf("a warning for a token that expires: %s", out)
	}
	golden(t, "token-create.human", out)

	out, _, code = f.run(t, plain, "tokens", "create", "--name", "nightly", "--expires", "never")
	if code != 0 || !strings.Contains(out, "⚠ This token never expires") {
		t.Errorf("exit %d, no warning: %s", code, out)
	}
	if body := f.bodies["POST /api/account/tokens"]; body != `{"expires_in_days":0,"name":"nightly"}` {
		t.Errorf("body %s", body)
	}
	for expires, days := range map[string]int{"30d": 30, "1y": 365} {
		f.run(t, plain, "tokens", "create", "--name", "x", "--expires", expires)
		if body := f.bodies["POST /api/account/tokens"]; !strings.Contains(body, `"expires_in_days":`+strconv.Itoa(days)+`,`) {
			t.Errorf("%s: body %s", expires, body)
		}
	}
}

func TestTokenCreateForPrograms(t *testing.T) {
	f := newFake(t)
	out, errOut, code := f.run(t, runOpts{}, "tokens", "create", "--name", "ci")
	var got map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil {
		t.Fatalf("exit %d: %q %s", code, out, errOut)
	}
	if got["token"] != "lyr_tok_made_once_0000000000000000" || got["never_expires"] != false || got["session"] == nil {
		t.Errorf("%v", got)
	}
	// Never, from a program, needs --yes.
	before := len(f.requests)
	_, errOut, code = f.run(t, runOpts{}, "tokens", "create", "--name", "ci", "--expires", "never")
	if e := parseErr(t, errOut); code != ExitUsage || !strings.Contains(e.Hint, "--yes") {
		t.Errorf("exit %d, %+v", code, e)
	}
	for _, r := range f.requests[before:] {
		if r.URL.Path == "/api/account/tokens" {
			t.Error("made a token that never expires without --yes")
		}
	}
	out, _, code = f.run(t, runOpts{}, "tokens", "create", "--name", "ci", "--expires", "never", "--yes")
	if code != 0 || !strings.Contains(out, `"never_expires":true`) {
		t.Errorf("exit %d: %s", code, out)
	}
}

func TestTokenUsageErrors(t *testing.T) {
	f := newFake(t)
	for _, args := range [][]string{
		{"tokens", "create"},
		{"tokens", "create", "--name", "ci", "--expires", "2w"},
		{"tokens", "expiry", "45"},
		{"tokens", "expiry", "45", "--expires", "forever"},
	} {
		_, errOut, code := f.run(t, runOpts{}, args...)
		if code != ExitUsage {
			t.Errorf("%v: exit %d, %s", args, code, errOut)
		}
	}
}

func TestTokenRefusals(t *testing.T) {
	f := newFake(t)
	f.tokenRefusal = "reauth"
	for _, args := range [][]string{{"tokens", "create", "--name", "ci"}, {"tokens", "expiry", "45", "--expires", "1y"}} {
		_, errOut, code := f.run(t, runOpts{}, args...)
		if e := parseErr(t, errOut); code != ExitReauth || e.Code != "reauth_required" || !strings.Contains(e.Hint, "lyrelog login") {
			t.Errorf("%v: exit %d, %+v", args, code, e)
		}
	}
	f.tokenRefusal = "browser_only"
	_, errOut, code := f.run(t, runOpts{}, "tokens", "create", "--name", "ci")
	if e := parseErr(t, errOut); code != ExitForbidden || e.Code != "browser_only" || !strings.Contains(e.Hint, "access token") || strings.Contains(e.Hint, "delete") {
		t.Errorf("exit %d, %+v", code, e)
	}
}

func TestTokenExpiry(t *testing.T) {
	f := newFake(t)
	out, errOut, code := f.run(t, plain, "tokens", "expiry", "45", "--expires", "30d")
	if code != 0 || !strings.Contains(out, "GitHub Actions, deploy now expires Dec 19, 2026") {
		t.Fatalf("exit %d: %s %s", code, out, errOut)
	}
	if body := f.bodies["PATCH /api/account/tokens/45"]; body != `{"expires_in_days":30}` {
		t.Errorf("body %s", body)
	}
	out, _, code = f.run(t, plain, "tokens", "expiry", "45", "--expires", "never")
	if code != 0 || !strings.Contains(out, "never expires now") || !strings.Contains(out, "⚠ This token never expires") {
		t.Errorf("exit %d: %s", code, out)
	}
	_, errOut, code = f.run(t, runOpts{}, "tokens", "expiry", "45", "--expires", "never")
	if code != ExitUsage {
		t.Errorf("never without --yes from a program: exit %d %s", code, errOut)
	}
	out, _, code = f.run(t, runOpts{}, "tokens", "expiry", "45", "--expires", "never", "--yes")
	if code != 0 || !strings.Contains(out, `"never_expires":true`) {
		t.Errorf("exit %d: %s", code, out)
	}
}

func TestTokenRevoke(t *testing.T) {
	f := newFake(t)
	_, errOut, code := f.run(t, runOpts{}, "tokens", "revoke", "45")
	if code != ExitUsage {
		t.Errorf("no --yes from a program: exit %d %s", code, errOut)
	}
	if f.last("/api/account/sessions/45") != nil {
		t.Fatal("revoked without --yes")
	}
	out, errOut, code := f.run(t, runOpts{tty: true, env: map[string]string{"NO_COLOR": "1"}, stdin: "y\n"}, "tokens", "revoke", "45")
	if code != 0 || !strings.Contains(out, "Revoked GitHub Actions, deploy") || !strings.Contains(errOut, "Revoke the access token GitHub Actions, deploy?") {
		t.Errorf("exit %d: %s %s", code, out, errOut)
	}
	if r := f.last("/api/account/sessions/45"); r == nil || r.Method != "DELETE" {
		t.Error("no DELETE")
	}
	// A sign-in isn't a token: lyrelog account sign-out is for those.
	_, errOut, code = f.run(t, runOpts{}, "tokens", "revoke", "43", "--yes")
	if e := parseErr(t, errOut); code != ExitNotFound || !strings.Contains(e.Hint, "lyrelog tokens") {
		t.Errorf("exit %d, %+v", code, e)
	}
	if f.last("/api/account/sessions/43") != nil {
		t.Error("signed a CLI session out through tokens revoke")
	}
}

// Finding a project by slug or name only needs the list, not its 24 hours of numbers.
func TestResolvingAProjectSkipsTheStats(t *testing.T) {
	f := newFake(t)
	if _, errOut, code := f.run(t, runOpts{}, "events", "-p", "tilefall"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if r := f.last("/api/projects"); r == nil || r.URL.Query().Get("stats") != "0" {
		t.Errorf("resolving a slug asked for the stats: %v", r.URL)
	}
	f.run(t, runOpts{}, "projects")
	if r := f.last("/api/projects"); r.URL.Query().Get("stats") != "" {
		t.Errorf("lyrelog projects left the numbers out: %v", r.URL)
	}
}

func TestEventGetCoversTheRetention(t *testing.T) {
	f := newFake(t)
	_, errOut, code := f.run(t, runOpts{}, "events", "get", "9f9f9f9f-0000-4000-8000-000000000000")
	e := parseErr(t, errOut)
	if code != ExitNotFound || strings.Contains(e.Error+e.Hint, "last 30 days") || !strings.Contains(e.Error, "keeps events for 30 days") {
		t.Errorf("exit %d, %+v", code, e)
	}
	out, _, _ := f.run(t, runOpts{}, "events", "get", "--help")
	if strings.Contains(out, "30 days") || !strings.Contains(out, "whole retention") {
		t.Errorf("help still says 30 days: %s", out)
	}
}

func TestFollowUsesArrivalTime(t *testing.T) {
	f := newFake(t)
	old := followInterval
	followInterval = 20 * time.Millisecond
	defer func() { followInterval = old }()
	// Two arrive while following: a crash, then a batch from a phone that was offline, its
	// event 5 hours old. Both are new; they show in the order they arrived.
	crash := event("3eab6085-bf4d-4c3b-8a6e-8d5c1b4f0a73", 0, "critical", "app", "app.crashed", "u_8f2a", `{}`)
	crash["received_at"] = ms(0) + 1000
	late := event("4fbc7196-c05e-4d4c-9b7f-9e6d2c5a1b84", 5*time.Hour, "error", "sync", "sync.failed", "u_77c1", `{}`)
	late["received_at"] = ms(0) + 3000
	f.arrived = []map[string]any{crash, late}
	out, errOut := f.runFor(t, 300*time.Millisecond, "events", "-f")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 5 {
		t.Fatalf("%d lines (%s): %q", len(lines), errOut, out)
	}
	var messages []string
	for _, line := range lines {
		var e map[string]any
		json.Unmarshal([]byte(line), &e)
		messages = append(messages, e["message"].(string))
	}
	if got := strings.Join(messages, " "); got != "sync.slow game.finished purchase.failed app.crashed sync.failed" {
		t.Errorf("order: %s", got)
	}
	f.mu.Lock()
	followed := append([]int64(nil), f.followed...)
	f.mu.Unlock()
	if len(followed) < 2 {
		t.Fatalf("polls: %v", followed)
	}
	// From the newest arrival it has seen, with an overlap for batches still being written.
	newestBacklog := ms(40*time.Second) + 1000
	if followed[0] != newestBacklog-followOverlap.Milliseconds() {
		t.Errorf("first poll received_after %d, want %d", followed[0], newestBacklog-followOverlap.Milliseconds())
	}
	if last := followed[len(followed)-1]; last != late["received_at"].(int64)-followOverlap.Milliseconds() {
		t.Errorf("later poll received_after %d", last)
	}
	r := f.last("/api/projects/3f9a1c7e20bd/events")
	from, _ := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	if window := time.Since(time.UnixMilli(from)); window < 47*time.Hour || window > 49*time.Hour {
		t.Errorf("from is %s back, want 2 days", window)
	}
}

func TestVersionFromLdflags(t *testing.T) {
	f := newFake(t)
	oldV, oldC, oldD := Version, Commit, Date
	Version, Commit, Date = "v0.3.0", "4678ef1", "2026-11-19T10:00:00Z"
	defer func() { Version, Commit, Date = oldV, oldC, oldD }()
	out, _, code := f.run(t, runOpts{}, "version")
	var got map[string]string
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil {
		t.Fatalf("exit %d: %s", code, out)
	}
	if got["version"] != "v0.3.0" || got["commit"] != "4678ef1" || got["date"] != "2026-11-19T10:00:00Z" {
		t.Errorf("%v", got)
	}
	out, _, _ = f.run(t, plain, "version")
	if !strings.HasPrefix(out, "lyrelog v0.3.0 (4678ef1, built 2026-11-19, ") {
		t.Errorf("%q", out)
	}
}
