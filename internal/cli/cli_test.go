package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"go.lyrelog.com/cli/internal/ui"
)

// Human output, as a person at a terminal sees it (colours off so the files are readable;
// TestColor checks the colours).
func TestHumanGolden(t *testing.T) {
	f := newFake(t)
	cases := map[string][]string{
		"projects":         {"projects"},
		"project-show":     {"projects", "show"},
		"events":           {"events"},
		"event-get":        {"events", "get", "0b7e3d52-8c1a-4f0e-9d3b-5a2f8e1c7d40"},
		"errors":           {"errors"},
		"keys":             {"keys"},
		"key-create":       {"keys", "create", "--type", "ingest", "--name", "Android app"},
		"sessions":         {"sessions"},
		"person":           {"person", "u_8f2a"},
		"releases":         {"releases"},
		"usage":            {"usage"},
		"whoami":           {"whoami"},
		"orgs":             {"orgs"},
		"funnel":           {"insights", "funnel", "onboarding.start", "onboarding.name", "onboarding.done", "--window", "1h"},
		"trend":            {"insights", "trend", "game.finished", "--by", "version"},
		"account-sessions": {"account", "sessions"},
		"rate":             {"insights", "rate", "--hits", "purchase.failed", "--of", "purchase.started"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			out, errOut, code := f.run(t, runOpts{tty: true, env: map[string]string{"NO_COLOR": "1"}}, args...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, errOut)
			}
			if strings.Contains(out, testToken) || strings.Contains(errOut, testToken) {
				t.Fatal("the token was printed")
			}
			golden(t, name+".human", strings.ReplaceAll(out, hostOf(f.srv.URL), "127.0.0.1:8090"))
		})
	}
}

// JSON output: what programs and agents get, the server's objects as arrays.
func TestJSONGolden(t *testing.T) {
	f := newFake(t)
	cases := map[string][]string{
		"projects": {"projects"},
		"events":   {"events"},
		"errors":   {"errors"},
		"keys":     {"keys"},
		"whoami":   {"whoami"},
		"person":   {"person", "u_8f2a"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			out, errOut, code := f.run(t, runOpts{}, args...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, errOut)
			}
			if !json.Valid([]byte(out)) || strings.Count(strings.TrimSpace(out), "\n") != 0 {
				t.Fatalf("not one line of JSON: %q", out)
			}
			if strings.Contains(out, "\x1b") || strings.Contains(out, testToken) {
				t.Fatal("escape codes or the token in JSON")
			}
			golden(t, name+".json", strings.ReplaceAll(out, hostOf(f.srv.URL), "127.0.0.1:8090"))
		})
	}
}

func TestListsAreArrays(t *testing.T) {
	f := newFake(t)
	for _, args := range [][]string{{"projects"}, {"events"}, {"errors"}, {"keys"}, {"sessions"}, {"releases"}, {"orgs"}, {"usage"}} {
		out, errOut, code := f.run(t, runOpts{}, args...)
		var list []json.RawMessage
		if code != 0 || json.Unmarshal([]byte(out), &list) != nil {
			t.Errorf("%v: exit %d, %q %q", args, code, out, errOut)
		}
	}
}

func TestAgentGetsJSONEvenWithATerminal(t *testing.T) {
	f := newFake(t)
	for _, env := range []map[string]string{{"CLAUDECODE": "1"}, {"CURSOR_AGENT": "1"}, {"CODEX_SANDBOX": "seatbelt"}, {"GEMINI_CLI": "1"}, {"CI": "true"}} {
		out, _, code := f.run(t, runOpts{tty: true, env: env}, "projects")
		var list []json.RawMessage
		if code != 0 || json.Unmarshal([]byte(out), &list) != nil {
			t.Errorf("%v: exit %d, not JSON: %q", env, code, out)
		}
	}
	// --human wins.
	out, _, _ := f.run(t, runOpts{tty: true, env: map[string]string{"CLAUDECODE": "1"}}, "projects", "--human")
	if !strings.Contains(out, "Project") || strings.HasPrefix(out, "[") {
		t.Errorf("--human ignored: %q", out)
	}
	// --json at a terminal.
	out, _, _ = f.run(t, runOpts{tty: true}, "projects", "--json")
	if !strings.HasPrefix(out, "[\n") {
		t.Errorf("--json at a terminal should be indented JSON: %q", out[:min(len(out), 40)])
	}
}

func TestColor(t *testing.T) {
	f := newFake(t)
	out, _, _ := f.run(t, runOpts{tty: true}, "events")
	if !strings.Contains(out, "\x1b[") {
		t.Fatal("no colour at a terminal")
	}
	for _, env := range []map[string]string{{"NO_COLOR": "1"}, {"TERM": "dumb"}} {
		out, _, _ = f.run(t, runOpts{tty: true, env: env}, "events")
		if strings.Contains(out, "\x1b") {
			t.Errorf("%v: escape codes in %q", env, out)
		}
	}
	out, _, _ = f.run(t, runOpts{tty: true}, "events", "--no-color")
	if strings.Contains(out, "\x1b") {
		t.Error("--no-color kept colours")
	}
}

type jsonError struct {
	Error  string `json:"error"`
	Hint   string `json:"hint"`
	Code   string `json:"code"`
	Status int    `json:"status"`
}

func parseErr(t *testing.T, stderr string) jsonError {
	t.Helper()
	var e jsonError
	if err := json.Unmarshal([]byte(strings.TrimSpace(stderr)), &e); err != nil || e.Error == "" {
		t.Fatalf("stderr isn't a JSON error: %q", stderr)
	}
	return e
}

func TestErrorsAndExitCodes(t *testing.T) {
	f := newFake(t)
	// Not signed in.
	out, errOut, code := f.run(t, runOpts{token: "-"}, "events")
	if code != ExitAuth || out != "" {
		t.Fatalf("exit %d, stdout %q", code, out)
	}
	if e := parseErr(t, errOut); e.Code != "not_signed_in" || !strings.Contains(e.Hint, "lyrelog login") {
		t.Errorf("%+v", e)
	}
	// An agent is told a person must sign in.
	_, errOut, _ = f.run(t, runOpts{token: "-", env: map[string]string{"CLAUDECODE": "1"}}, "events")
	if e := parseErr(t, errOut); !strings.Contains(e.Hint, "A person must run lyrelog login") {
		t.Errorf("%+v", e)
	}
	// A session that ended (401).
	_, errOut, code = f.run(t, runOpts{token: "lyr_cli_old"}, "projects")
	if e := parseErr(t, errOut); code != ExitAuth || e.Status != 401 || !strings.Contains(e.Hint, "lyrelog login") {
		t.Errorf("exit %d, %+v", code, e)
	}
	// Recent sign-in needed (403 reauth).
	_, errOut, code = f.run(t, runOpts{}, "keys", "create", "--type", "server", "--name", "api")
	if e := parseErr(t, errOut); code != ExitReauth || e.Code != "reauth_required" || !strings.Contains(e.Hint, "lyrelog login again") {
		t.Errorf("exit %d, %+v", code, e)
	}
	// Not found.
	_, errOut, code = f.run(t, runOpts{}, "events", "-p", "nope")
	if e := parseErr(t, errOut); code != ExitNotFound || !strings.Contains(e.Hint, "tilefall") {
		t.Errorf("exit %d, %+v", code, e)
	}
	// Usage.
	_, errOut, code = f.run(t, runOpts{}, "events", "--level", "loud")
	if e := parseErr(t, errOut); code != ExitUsage || e.Code != "usage" {
		t.Errorf("exit %d, %+v", code, e)
	}
	_, _, code = f.run(t, runOpts{}, "nonsense")
	if code != ExitUsage {
		t.Errorf("unknown command: exit %d", code)
	}
	// People get words, not JSON.
	_, errOut, _ = f.run(t, runOpts{tty: true, env: map[string]string{"NO_COLOR": "1"}}, "keys", "create", "--type", "read", "--name", "x")
	if !strings.HasPrefix(errOut, "✗ This needs a recent sign-in\n  Run lyrelog login again") {
		t.Errorf("human error: %q", errOut)
	}
}

func TestExplorerFiltersReachTheAPI(t *testing.T) {
	f := newFake(t)
	_, errOut, code := f.run(t, runOpts{}, "events", "--level", "warning+", "--since", "1h", "--attr", "board.size:9", "--attr", "result!:win",
		"--verified", "!none", "--version", "2.4.0|2.4.1", "-q", "card", "--country", "es", "--limit", "5")
	if code != 0 {
		t.Fatal(errOut)
	}
	q := f.last("/api/projects/3f9a1c7e20bd/events").URL.Query()
	want := url.Values{"level": {"warning,error,critical"}, "attr": {"board.size:9", "result!:win"}, "verified": {"!none"},
		"version": {"2.4.0|2.4.1"}, "q": {"card"}, "country": {"ES"}, "env": {"production"}, "limit": {"5"}}
	for k, v := range want {
		if strings.Join(q[k], ",") != strings.Join(v, ",") {
			t.Errorf("%s = %v, want %v", k, q[k], v)
		}
	}
	from, to := q.Get("from"), q.Get("to")
	if from != itoa(now.Add(-time.Hour).UnixMilli()) || to != itoa(now.Add(time.Minute).UnixMilli()) {
		t.Errorf("window %s..%s", from, to)
	}
	// --env all searches every environment.
	f.run(t, runOpts{}, "events", "--env", "all")
	if q := f.last("/api/projects/3f9a1c7e20bd/events").URL.Query(); q.Has("env") {
		t.Errorf("--env all sent env=%s", q.Get("env"))
	}
	// An absolute window.
	f.run(t, runOpts{}, "events", "--from", "2026-11-18T10:00:00Z", "--to", "2h")
	q = f.last("/api/projects/3f9a1c7e20bd/events").URL.Query()
	if q.Get("from") != itoa(time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC).UnixMilli()) || q.Get("to") != itoa(now.Add(-2*time.Hour).UnixMilli()) {
		t.Errorf("from %s to %s", q.Get("from"), q.Get("to"))
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestLoginWithTheBrowser(t *testing.T) {
	f := newFake(t)
	var opened string
	open := func(target string) error {
		opened = target
		u, err := url.Parse(target)
		if err != nil {
			return err
		}
		q := u.Query()
		if u.Path != "/cli/authorize" || q.Get("name") != "anas-mac" || q.Get("challenge") == "" {
			t.Errorf("authorize URL %s", target)
		}
		f.setChallenge(q.Get("challenge"))
		// The dashboard approves and sends the browser back with a code.
		go func() {
			res, err := http.Get(q.Get("redirect") + "&code=the-code")
			if err == nil {
				res.Body.Close()
			}
		}()
		return nil
	}
	out, errOut, code := f.run(t, runOpts{tty: true, token: "-", project: "-", open: open, env: map[string]string{"NO_COLOR": "1"}}, "login")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(errOut, opened) {
		t.Error("the URL wasn't printed in case the browser didn't open")
	}
	if !strings.Contains(out, "Signed in as Ana (ana@example.com) on 127.0.0.1") {
		t.Errorf("%q", out)
	}
	if strings.Contains(out+errOut, testToken) {
		t.Fatal("the token was printed")
	}
	path := filepath.Join(f.dir, "config.json")
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), testToken) {
		t.Fatalf("token not saved: %s", data)
	}
	if info, _ := os.Stat(path); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("config mode %v", info.Mode().Perm())
	}
}

func TestLoginWithACode(t *testing.T) {
	f := newFake(t)
	// --no-browser: the page shows a code and the person pastes it. The challenge is the
	// printed URL's.
	pr, pw, _ := os.Pipe()
	stderr := &lockedBuffer{}
	defer pr.Close()
	done := make(chan struct{})
	var out, errOut string
	var code int
	go func() {
		defer close(done)
		out, errOut, code = f.runWithStdin(t, pr, stderr, "login", "--no-browser")
	}()
	// Wait for the URL to be printed, read the challenge from the fake's record, then answer.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c := printedChallenge(stderr); c != "" {
			f.setChallenge(c)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	pw.Write([]byte("the-code\n"))
	pw.Close()
	<-done
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(errOut, "/cli/authorize?challenge=") || strings.Contains(errOut, "redirect=") {
		t.Errorf("manual URL: %q", errOut)
	}
	if !strings.Contains(out, "Signed in as Ana") {
		t.Errorf("%q", out)
	}
}

func TestLoginRefusedWithoutAPerson(t *testing.T) {
	f := newFake(t)
	_, errOut, code := f.run(t, runOpts{tty: true, token: "-", env: map[string]string{"CLAUDECODE": "1"}}, "login")
	e := parseErr(t, errOut)
	if code != ExitAuth || !strings.Contains(e.Error, "claude-code") || !strings.Contains(e.Hint, "https://docs.lyrelog.com/cli/login#scripts-and-ci") {
		t.Errorf("exit %d, %+v", code, e)
	}
	if f.last("/api/cli/token") != nil {
		t.Error("an agent reached the token exchange")
	}
}

func TestLogout(t *testing.T) {
	f := newFake(t)
	out, errOut, code := f.run(t, runOpts{}, "logout")
	if code != 0 || !strings.Contains(out, `"signed_out":true`) {
		t.Fatalf("exit %d: %s %s", code, out, errOut)
	}
	r := f.last("/api/logout")
	if r == nil || r.Header.Get("Authorization") != "Bearer "+testToken || r.Header.Get("X-Lyrelog") != "1" {
		t.Fatal("logout didn't end the session on the server")
	}
	data, _ := os.ReadFile(filepath.Join(f.dir, "config.json"))
	if strings.Contains(string(data), testToken) {
		t.Fatal("the token is still saved")
	}
}

func TestFollowPrintsNDJSON(t *testing.T) {
	f := newFake(t)
	old := followInterval
	followInterval = 20 * time.Millisecond
	defer func() { followInterval = old }()
	out, errOut := f.runFor(t, 300*time.Millisecond, "events", "-f")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	// The backlog (3, oldest first), then the new crash once, however many polls saw it.
	if len(lines) != 4 {
		t.Fatalf("%d lines (%s): %q", len(lines), errOut, out)
	}
	var first, lastOne map[string]any
	json.Unmarshal([]byte(lines[0]), &first)
	json.Unmarshal([]byte(lines[3]), &lastOne)
	if first["message"] != "sync.slow" || lastOne["message"] != "app.crashed" {
		t.Errorf("order: %v ... %v", first["message"], lastOne["message"])
	}
}

func TestDeletingAPersonNeedsYes(t *testing.T) {
	f := newFake(t)
	_, errOut, code := f.run(t, runOpts{}, "person", "u_8f2a", "--delete")
	if code != ExitUsage || !strings.Contains(parseErr(t, errOut).Hint, "--yes") {
		t.Fatalf("exit %d: %s", code, errOut)
	}
}

func TestAPICommand(t *testing.T) {
	f := newFake(t)
	out, errOut, code := f.run(t, runOpts{}, "api", "PATCH", "projects/{project}", "-F", "name=Tilefall 2", "-F", "retention_days=7")
	if code != 0 || !strings.Contains(out, `"name":"Tilefall 2"`) {
		t.Fatalf("exit %d: %s %s", code, out, errOut)
	}
	r := f.last("/api/projects/3f9a1c7e20bd")
	if r.Method != "PATCH" || r.Header.Get("X-Lyrelog") != "1" {
		t.Errorf("%s, X-Lyrelog %q", r.Method, r.Header.Get("X-Lyrelog"))
	}
	_, errOut, code = f.run(t, runOpts{}, "api", "GET", "/api/nothing")
	if code != ExitNotFound || parseErr(t, errOut).Status != 404 {
		t.Errorf("exit %d %s", code, errOut)
	}
}

func TestUnknownPersonIsNotFoundInJSONToo(t *testing.T) {
	f := newFake(t)
	out, errOut, code := f.run(t, runOpts{}, "person", "nobody")
	if code != ExitNotFound || out != "" {
		t.Fatalf("exit %d, stdout %q", code, out)
	}
	if e := parseErr(t, errOut); e.Error != "No such player" || e.Code != "not_found" {
		t.Errorf("%+v", e)
	}
	if f.last("/api/projects/3f9a1c7e20bd/sessions") != nil {
		t.Error("asked for the sessions of someone who isn't there")
	}
	_, errOut, code = f.run(t, runOpts{tty: true, env: map[string]string{"NO_COLOR": "1"}}, "person", "nobody")
	if code != ExitNotFound || !strings.HasPrefix(errOut, `✗ No player "nobody" in Tilefall`) {
		t.Errorf("exit %d: %q", code, errOut)
	}
}

func TestBrowserOnlyActions(t *testing.T) {
	f := newFake(t)
	_, errOut, code := f.run(t, runOpts{}, "api", "DELETE", "projects/{project}")
	e := parseErr(t, errOut)
	if code != ExitForbidden || e.Code != "browser_only" || e.Status != 403 ||
		e.Error != "This can only be done in the dashboard: "+f.srv.URL+"/p/3f9a1c7e20bd/settings" {
		t.Errorf("exit %d, %+v", code, e)
	}
	_, errOut, code = f.run(t, runOpts{tty: true, env: map[string]string{"NO_COLOR": "1"}}, "api", "DELETE", "account/passkeys/3")
	if code != ExitForbidden || !strings.HasPrefix(errOut, "✗ This can only be done in the dashboard: "+f.srv.URL+"/account\n") {
		t.Errorf("exit %d: %q", code, errOut)
	}
}

func TestSignInHintsAreHonest(t *testing.T) {
	f := newFake(t)
	_, errOut, _ := f.run(t, runOpts{token: "lyr_cli_old"}, "projects")
	if e := parseErr(t, errOut); e.Hint != "Your session ended: run lyrelog login." {
		t.Errorf("%+v", e)
	}
	_, errOut, _ = f.run(t, runOpts{env: map[string]string{"LYRELOG_TOKEN": "lyr_cli_old"}}, "projects")
	if e := parseErr(t, errOut); !strings.Contains(e.Error, "revoked or expired") || !strings.Contains(e.Hint, "access token") {
		t.Errorf("%+v", e)
	}
	_, errOut, _ = f.run(t, runOpts{token: "-"}, "events")
	if e := parseErr(t, errOut); !strings.Contains(e.Hint, "lyrelog login") || !strings.Contains(e.Hint, "https://docs.lyrelog.com/cli/login#scripts-and-ci") {
		t.Errorf("%+v", e)
	}
}

func TestTablesKeepIDsAndFitTheWidth(t *testing.T) {
	f := newFake(t)
	// Narrow: the path is cut, the session id isn't.
	out, _, _ := f.run(t, runOpts{tty: true, env: map[string]string{"NO_COLOR": "1", "COLUMNS": "90"}}, "sessions")
	if !strings.Contains(out, "s-u_8f2a-0001") || !strings.Contains(out, "…") {
		t.Errorf("%s", out)
	}
	// --human into a pipe honours COLUMNS.
	out, _, code := f.run(t, runOpts{noWidth: true, env: map[string]string{"COLUMNS": "40"}}, "events", "--human")
	if code != 0 {
		t.Fatal(out)
	}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if ui.Width(line) > 40 {
			t.Errorf("wider than COLUMNS=40: %q", line)
		}
	}
}

func TestHumanWidthIntoAPipe(t *testing.T) {
	a := &App{Getenv: func(string) string { return "" }, Stdout: io.Discard, Stderr: io.Discard}
	a.flagHuman = true
	a.setMode()
	if a.Width != 120 {
		t.Errorf("--human into a pipe: width %d, want 120", a.Width)
	}
	b := &App{Getenv: func(string) string { return "" }, Stdout: io.Discard, Stderr: io.Discard}
	b.setMode()
	if b.Width != 0 {
		t.Errorf("JSON: width %d", b.Width)
	}
}

func TestBrowserNames(t *testing.T) {
	for ua, want := range map[string]string{
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36":                   "Chrome on macOS",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 Edg/131.0.0.0":           "Edge on Windows",
		"Mozilla/5.0 (X11; Linux x86_64; rv:133.0) Gecko/20100101 Firefox/133.0":                                                                  "Firefox on Linux",
		"Mozilla/5.0 (Linux; Android 15) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36":                            "Chrome on Android",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1": "Safari on iOS",
		"curl/8.7.1": "A browser",
	} {
		if got := browserName(ua); got != want {
			t.Errorf("browserName(%q) = %q, want %q", ua, got, want)
		}
	}
}

func TestLabelsMatchTheFlags(t *testing.T) {
	f := newFake(t)
	human := runOpts{tty: true, env: map[string]string{"NO_COLOR": "1"}}
	out, _, _ := f.run(t, human, "events", "values", "attr.board.size")
	if !strings.Contains(out, "\nboard.size ") || strings.Contains(out, "Attr.") {
		t.Errorf("values header: %s", out)
	}
	out, _, _ = f.run(t, human, "events", "summary")
	for _, label := range []string{"  version ", "  device ", "  os "} {
		if !strings.Contains(out, label) {
			t.Errorf("summary has no %q label: %s", label, out)
		}
	}
	if strings.Contains(out, "app_version") || strings.Contains(out, "device_model") || strings.Contains(out, "os_version") {
		t.Errorf("summary uses column names: %s", out)
	}
	out, _, _ = f.run(t, human, "releases", "--help")
	if strings.Contains(out, "crash-free") || !strings.Contains(out, "sessions without errors") {
		t.Errorf("releases help: %s", out)
	}
	out, _, _ = f.run(t, human, "insights", "views", "--help")
	if !strings.Contains(out, "saved views") {
		t.Errorf("views help: %s", out)
	}
	out, _, _ = f.run(t, human, "open", "--help")
	if !strings.Contains(out, "no address for one open event") {
		t.Errorf("open help: %s", out)
	}
}

func TestRateColumns(t *testing.T) {
	f := newFake(t)
	human := runOpts{tty: true, env: map[string]string{"NO_COLOR": "1"}}
	out, errOut, code := f.run(t, human, "insights", "rate", "--hits", "purchase.failed", "--of", "purchase.started")
	if code != 0 {
		t.Fatal(errOut)
	}
	if !strings.Contains(out, "purchase.failed of purchase.started") || strings.Contains(out, "Group") || strings.Contains(out, " all ") {
		t.Errorf("%s", out)
	}
	out, _, _ = f.run(t, human, "insights", "rate", "--hits", "purchase.failed", "--of", "purchase.started", "--by", "version")
	if !strings.Contains(out, "Group") || !strings.Contains(out, "2.4.1") {
		t.Errorf("grouped: %s", out)
	}
}

func TestLastModeFlagWins(t *testing.T) {
	f := newFake(t)
	agent := runOpts{tty: true, env: map[string]string{"CLAUDECODE": "1"}}
	out, errOut, code := f.run(t, agent, "projects", "--json", "--human")
	if code != 0 || strings.HasPrefix(out, "[") || !strings.Contains(out, "Project") {
		t.Errorf("--json --human: exit %d, %q %q", code, out, errOut)
	}
	out, errOut, code = f.run(t, runOpts{tty: true}, "projects", "--human", "--json")
	if code != 0 || !strings.HasPrefix(out, "[") {
		t.Errorf("--human --json: exit %d, %q %q", code, out, errOut)
	}
	// A flag error comes out in the last flag's form too.
	_, errOut, _ = f.run(t, agent, "projects", "--json", "--human", "--nope")
	if strings.HasPrefix(errOut, "{") {
		t.Errorf("flag error in JSON after --human: %q", errOut)
	}
}

func TestAPIFieldsOnDeleteGoInTheBody(t *testing.T) {
	f := newFake(t)
	f.run(t, runOpts{}, "api", "DELETE", "projects/{project}", "-F", "reason=test")
	r := f.last("/api/projects/3f9a1c7e20bd")
	if r == nil || r.Method != "DELETE" || r.URL.RawQuery != "" {
		t.Fatalf("query %q", r.URL.RawQuery)
	}
	if body := f.bodies["DELETE /api/projects/3f9a1c7e20bd"]; body != `{"reason":"test"}` {
		t.Errorf("body %q", body)
	}
	f.run(t, runOpts{}, "api", "GET", "projects/{project}/events", "-F", "level=error")
	if q := f.last("/api/projects/3f9a1c7e20bd/events").URL.Query(); q.Get("level") != "error" {
		t.Errorf("GET query %v", q)
	}
}

func TestFollowCutsLongMessagesForPeople(t *testing.T) {
	f := newFake(t)
	f.followMessage = "purchase.failed.after.three.retries.with.a.declined.card"
	old := followInterval
	followInterval = 20 * time.Millisecond
	defer func() { followInterval = old }()
	out, errOut := f.runFor(t, 300*time.Millisecond, "events", "-f", "--human")
	if !strings.Contains(out, "purchase.failed.after.three.retries…") || strings.Contains(out, "declined.card") {
		t.Errorf("%s %s", out, errOut)
	}
}

func TestFollowWithSinceSaysWhatToDo(t *testing.T) {
	f := newFake(t)
	_, errOut, code := f.run(t, runOpts{}, "events", "-f", "--since", "1h")
	if e := parseErr(t, errOut); code != ExitUsage || e.Hint != "--follow starts from now; drop --since, or run without -f to list the past." {
		t.Errorf("exit %d, %+v", code, e)
	}
}

func TestPersonSuggestsThePlansRetention(t *testing.T) {
	f := newFake(t)
	f.maxRetention = 7
	out, errOut, code := f.run(t, runOpts{tty: true, env: map[string]string{"NO_COLOR": "1"}}, "person", "u_8f2a")
	if code != 0 || !strings.Contains(out, "lyrelog events --user u_8f2a --since 7d") {
		t.Errorf("exit %d: %s %s", code, out, errOut)
	}
}

// The usage sparkline's label sits by its bars (not past the month's days still to come), and
// nothing goes past the width, wide or narrow.
func TestUsageSparklineLabelSitsByTheBars(t *testing.T) {
	f := newFake(t)
	for _, cols := range []string{"100", "40"} {
		out, errOut, code := f.run(t, runOpts{tty: true, env: map[string]string{"NO_COLOR": "1", "COLUMNS": cols}}, "usage")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errOut)
		}
		width, _ := strconv.Atoi(cols)
		lines := strings.Split(out, "\n")
		at := -1
		for i, l := range lines {
			if ui.Width(l) > width {
				t.Errorf("COLUMNS=%s: %q is wider than %d", cols, l, width)
			}
			if strings.Contains(l, "per day") {
				at = i
			}
		}
		if at < 0 {
			t.Fatalf("COLUMNS=%s: no label in %q", cols, out)
		}
		label := lines[at]
		bars := label
		if !strings.ContainsAny(label, "▁▂▃▄▅▆▇█") {
			bars = lines[at-1] // the label went under the bars
		}
		bars = strings.TrimRight(strings.SplitN(bars, "per day", 2)[0], " ")
		if strings.Contains(bars, "  ") && strings.Contains(strings.TrimLeft(bars, " "), "  ") {
			t.Errorf("COLUMNS=%s: gaps in the bars: %q", cols, bars)
		}
		// Nov 1 to the 19th (the fake's today): 19 days, not the month's 30.
		if n := len([]rune(strings.TrimLeft(bars, " "))); n != 19 {
			t.Errorf("COLUMNS=%s: %d days drawn in %q, want 19", cols, n, bars)
		}
	}
}

// At 80 columns the page tables leave columns out rather than cut their headers.
func TestTablesAt80Columns(t *testing.T) {
	f := newFake(t)
	for _, args := range [][]string{{"sessions"}, {"errors"}, {"person", "u_8f2a"}} {
		out, errOut, code := f.run(t, runOpts{tty: true, env: map[string]string{"NO_COLOR": "1", "COLUMNS": "80"}}, args...)
		if code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, errOut)
		}
		for _, l := range strings.Split(out, "\n") {
			if ui.Width(l) > 80 {
				t.Errorf("%v: wider than 80: %q", args, l)
			}
			if strings.Contains(l, "Session") && strings.Contains(l, "…") || strings.HasPrefix(strings.TrimSpace(l), "Event") && strings.Contains(l, "…") || strings.HasPrefix(l, "Player ") && strings.Contains(l, "…") {
				t.Errorf("%v: a header was cut: %q", args, l)
			}
		}
	}
}

// Android's Key Attestation is one of the ways a sender is proven (the server's
// VerifiedKeyAttestation), so --verified takes it like the explorer does.
func TestVerifiedTakesKeyAttestation(t *testing.T) {
	f := newFake(t)
	_, errOut, code := f.run(t, runOpts{}, "events", "--verified", "app_attest|key_attestation")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if q := f.last("/api/projects/3f9a1c7e20bd/events").URL.Query(); q.Get("verified") != "app_attest|key_attestation" {
		t.Errorf("verified = %q", q.Get("verified"))
	}
}

// The help and the README are public copy: their examples use made-up people and the docs'
// made-up game (Tilefall), never a real person or their apps. The words to keep out are listed by
// their SHA-256, so this test doesn't print them either.
var privateWords = map[string]bool{
	"4859d1729b697a9ccc9bf17b6a3c676bc935834e5640d203a7cfa489bd45d619": true,
	"976c617a20d12e4261e40bd8e4ce88493578ec463d082af3b2784b642b82ef0d": true,
	"b9e5b115929357704655d70d95b79daf630c3cea6e084adec79b4bcb7e0cd7b1": true,
	"34eb8131c759d8ea0bdf432f943e6e78d8bd2e1d9ad50f2bf166fad074edcea8": true,
	"7e421181fc7b52ab66f2c22bd396acce92c3cdd9a29122dde0f1b7ddc0eb4435": true,
}

// namesNoOne says which private word a text holds, if any (as its hash).
func namesNoOne(text string) string {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
	for _, w := range words {
		sum := sha256.Sum256([]byte(w))
		if h := hex.EncodeToString(sum[:]); privateWords[h] {
			return h
		}
	}
	return ""
}

func TestHelpNamesNoRealPerson(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, text := range []string{c.Short, c.Long, c.Example} {
			if h := namesNoOne(text); h != "" {
				t.Errorf("%s names someone (sha256 %s)", c.CommandPath(), h)
			}
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk((&App{}).rootCommand())
	for _, file := range []string{"../../README.md", "../../LICENSE", "../../.goreleaser.yaml"} {
		text, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if h := namesNoOne(string(text)); h != "" {
			t.Errorf("%s names someone (sha256 %s)", file, h)
		}
	}
}

// Removing a project's last platform says so, instead of "is made of: " and nothing.
func TestRemovingTheLastPlatform(t *testing.T) {
	f := newFake(t)
	out, errOut, code := f.run(t, runOpts{tty: true, env: map[string]string{"NO_COLOR": "1"}}, "projects", "platform", "remove", "ios")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if out != "✓ Tilefall has no platforms now: lyrelog projects platform add sets one up\n" {
		t.Errorf("got %q", out)
	}
}

// cobra's own messages, said for someone new: which arguments the command takes, and the command
// meant when one is mistyped (no "arg(s)", no padded "Did you mean this?" block).
func TestArgumentErrorsSayWhatTheCommandTakes(t *testing.T) {
	f := newFake(t)
	cases := []struct {
		args       []string
		want, hint string
	}{
		{[]string{"insights", "funnel", "a"}, "✗ Needs at least 2 arguments, got 1", "  Usage: lyrelog insights funnel <step> <step>... (--help tells more)"},
		{[]string{"events", "get"}, "✗ Needs 1 argument, got 0", "  Usage: lyrelog events get <event-id> (--help tells more)"},
		{[]string{"use", "a", "b"}, "✗ Takes at most 1 argument, got 2", "  Usage: lyrelog use [project] (--help tells more)"},
		{[]string{"evnets"}, `✗ Unknown command "evnets"`, "  Did you mean lyrelog events? Run lyrelog --help for the list."},
	}
	for _, c := range cases {
		_, errOut, code := f.run(t, runOpts{tty: true, env: map[string]string{"NO_COLOR": "1"}}, c.args...)
		if code != ExitUsage || errOut != c.want+"\n"+c.hint+"\n" {
			t.Errorf("%v: exit %d\n%q\nwant\n%q", c.args, code, errOut, c.want+"\n"+c.hint+"\n")
		}
	}
}
