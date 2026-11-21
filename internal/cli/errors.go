package cli

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"go.lyrelog.com/cli/internal/api"
	"go.lyrelog.com/cli/internal/ui"
)

// Exit codes. Stable: scripts and agents branch on them (README.md, "Exit codes").
const (
	ExitOK          = 0
	ExitError       = 1 // anything else: the server failed, a file couldn't be written...
	ExitUsage       = 2 // a wrong command, flag or value
	ExitAuth        = 3 // not signed in, or the session ended: lyrelog login
	ExitReauth      = 4 // the action needs a recent sign-in: lyrelog login, then retry
	ExitForbidden   = 5 // signed in, but not allowed (an owner's action, or one kept to the dashboard)
	ExitNotFound    = 6 // no such project, event, key... (or not yours)
	ExitRefused     = 7 // the server refused the request as it is (400, 409, 413, 422)
	ExitRateLimited = 8 // too many requests or the server is busy: wait and retry
	ExitNetwork     = 9 // the server couldn't be reached
	ExitInterrupted = 130
)

// cliError is a failure told to a person: what happened, what to do next, and a stable code.
type cliError struct {
	msg    string
	hintTx string
	code   string
	exit   int
	status int
	retry  time.Duration
}

func (e *cliError) Error() string { return e.msg }

func (e *cliError) hint(h string) *cliError { e.hintTx = h; return e }

func failf(exit int, format string, args ...any) *cliError {
	return &cliError{msg: fmt.Sprintf(format, args...), exit: exit, code: codeFor(exit)}
}

func usagef(format string, args ...any) *cliError { return failf(ExitUsage, format, args...) }

func codeFor(exit int) string {
	switch exit {
	case ExitUsage:
		return "usage"
	case ExitAuth:
		return "not_signed_in"
	case ExitReauth:
		return "reauth_required"
	case ExitForbidden:
		return "forbidden"
	case ExitNotFound:
		return "not_found"
	case ExitRefused:
		return "refused"
	case ExitRateLimited:
		return "rate_limited"
	case ExitNetwork:
		return "network"
	case ExitInterrupted:
		return "interrupted"
	}
	return "error"
}

// classify turns any error into a cliError.
func (a *App) classify(err error) *cliError {
	var ce *cliError
	if errors.As(err, &ce) {
		return ce
	}
	if errors.Is(err, api.ErrNoToken) {
		e := failf(ExitAuth, "You're not signed in to %s", a.base)
		if a.mode.JSON && a.mode.Agent != "" {
			return e.hint("A person must run `lyrelog login` in a terminal (an agent can't sign in by itself). In CI, set LYRELOG_TOKEN to an access token from your account page: https://docs.lyrelog.com/cli/login#scripts-and-ci")
		}
		return e.hint("Run `lyrelog login`. In CI, set LYRELOG_TOKEN to an access token from your account page: https://docs.lyrelog.com/cli/login#scripts-and-ci")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return failf(ExitNetwork, "%s took too long to answer", a.base).hint("Try again in a moment.")
	}
	var ae *api.Error
	if errors.As(err, &ae) {
		return a.fromAPI(ae)
	}
	msg := err.Error()
	if e := a.argumentError(msg); e != nil {
		return e
	}
	// cobra's own messages: unknown command, flag, argument count.
	if strings.HasPrefix(msg, "unknown command") || strings.HasPrefix(msg, "unknown flag") || strings.HasPrefix(msg, "unknown shorthand") ||
		strings.Contains(msg, "flag needs an argument") || strings.Contains(msg, "invalid argument") ||
		strings.HasPrefix(msg, "accepts ") || strings.HasPrefix(msg, "requires ") || strings.Contains(msg, "required flag") {
		return usagef("%s", ui.Capitalize(msg)).hint("Run `lyrelog --help`, or add --help to the command.")
	}
	return failf(ExitError, "%s", ui.Capitalize(msg))
}

func (a *App) fromAPI(ae *api.Error) *cliError {
	msg := ui.Capitalize(ae.Message)
	e := &cliError{msg: msg, status: ae.Status, retry: ae.RetryAfter}
	switch s := ae.Status; {
	case s == 0:
		e.exit = ExitNetwork
		e.hintTx = "Check your connection, or the address (--url, LYRELOG_URL). The local stack is http://127.0.0.1:8090 (make up)."
	case s >= 300 && s < 400:
		e.exit = ExitError
		e.hintTx = "The address is probably an old one. Use --url https://app.lyrelog.com."
	case s == 401:
		e.exit = ExitAuth
		e.msg = "Your session ended (sessions end after 14 days unused)"
		if a.tokenFromEnv {
			e.msg = "LYRELOG_TOKEN doesn't work any more (revoked or expired)"
			e.hintTx = "Make a new access token on your account page, or unset LYRELOG_TOKEN to use lyrelog login's sign-in."
		} else {
			e.hintTx = "Your session ended: run `lyrelog login`."
		}
	case s == 403 && ae.Reauth:
		e.exit = ExitReauth
		e.msg = "This needs a recent sign-in"
		e.hintTx = "Run `lyrelog login` again, then retry within 10 minutes."
	case s == 403 && ae.BrowserOnly && strings.HasPrefix(ae.Path, "/api/account/tokens"):
		e.exit = ExitForbidden
		e.code = "browser_only"
		e.msg = "An access token can't make or change tokens"
		e.hintTx = "With an access token in LYRELOG_TOKEN, unset it, run `lyrelog login` and retry within 10 minutes; or use the account page: " + a.base + "/account"
		return e
	case s == 403 && ae.BrowserOnly:
		e.exit = ExitForbidden
		e.code = "browser_only"
		e.msg = "This can only be done in the dashboard: " + a.dashboardPageFor(ae.Path)
		e.hintTx = "Open that page in a browser signed in to Lyrelog. The command line doesn't delete accounts or projects, or change how you sign in."
		return e
	case s == 403:
		e.exit = ExitForbidden
		e.hintTx = "Ask an owner of the organization (lyrelog orgs show lists them)."
	case s == 404:
		e.exit = ExitNotFound
		if strings.Contains(ae.Message, "project") {
			e.hintTx = "`lyrelog projects` lists the projects you can see."
		}
		if ae.Message == "no such endpoint" {
			e.hintTx = "This server doesn't have that route. Update the CLI, or check the path."
		}
	case s == 429 || s == 503:
		e.exit = ExitRateLimited
		e.hintTx = "Wait a little and try again."
		if ae.RetryAfter > 0 {
			e.hintTx = fmt.Sprintf("Try again in %s.", ui.Duration(ae.RetryAfter))
		}
	case s >= 500:
		e.exit = ExitError
		e.hintTx = "Something broke on Lyrelog's side. Try again; if it keeps happening, write to hello@lyrelog.com."
	default:
		e.exit = ExitRefused
	}
	e.code = codeFor(e.exit)
	return e
}

// dashboardPageFor is the dashboard page where an API route's action lives: a project's
// settings for /api/projects/<id>/..., else the account page.
func (a *App) dashboardPageFor(path string) string {
	if rest, ok := strings.CutPrefix(path, "/api/projects/"); ok {
		if id, _, _ := strings.Cut(rest, "/"); publicID.MatchString(id) {
			return a.base + "/p/" + id + "/settings"
		}
	}
	return a.base + "/account"
}

// report prints the error and returns its exit code. In JSON mode stderr gets one line:
// {"error","hint","code","status"}.
func (a *App) report(err error) int {
	e := a.classify(err)
	if a.mode.JSON {
		out := map[string]any{"error": e.msg, "code": e.code}
		if e.hintTx != "" {
			out["hint"] = strings.ReplaceAll(e.hintTx, "`", "")
		}
		if e.status != 0 {
			out["status"] = e.status
		}
		if e.retry > 0 {
			out["retry_after"] = int(e.retry.Seconds())
		}
		_ = writeJSON(a.Stderr, out, false)
		return e.exit
	}
	th := a.theme
	fmt.Fprintf(a.errOut, "%s %s\n", th.Error("✗"), th.Bold(e.msg))
	if e.hintTx != "" {
		fmt.Fprintf(a.errOut, "  %s\n", a.styleHint(e.hintTx))
	}
	return e.exit
}

// styleHint shows `commands` in cobalt (and drops the backticks when there's colour).
func (a *App) styleHint(h string) string {
	parts := strings.Split(h, "`")
	var b strings.Builder
	for i, p := range parts {
		if i%2 == 1 {
			if a.theme.Color {
				b.WriteString(a.theme.Primary(p))
			} else {
				b.WriteString(p)
			}
		} else {
			b.WriteString(a.theme.Dim(p))
		}
	}
	return b.String()
}

var (
	argCount       = regexp.MustCompile(`^(?:requires at least|accepts at most|accepts) (\d+) arg\(s\), (?:only )?received (\d+)$`)
	argRange       = regexp.MustCompile(`^accepts between (\d+) and (\d+) arg\(s\), received (\d+)$`)
	unknownCommand = regexp.MustCompile(`^unknown command "([^"]*)" for "([^"]*)"`)
	suggestion     = regexp.MustCompile(`(?m)^\t(\S+)$`)
)

// argumentError rewrites cobra's argument messages ("accepts 1 arg(s), received 0", an unknown
// command with its padded "Did you mean this?") as a person would say them, with the command's
// usage line. nil when msg isn't one of them.
func (a *App) argumentError(msg string) *cliError {
	usage := func() string {
		if a.failed == nil {
			return "Run `lyrelog --help`, or add --help to the command."
		}
		return "Usage: `" + strings.TrimSuffix(a.failed.UseLine(), " [flags]") + "` (--help tells more)"
	}
	arguments := func(n string) string {
		if n == "1" {
			return "1 argument"
		}
		return n + " arguments"
	}
	if m := argCount.FindStringSubmatch(msg); m != nil {
		verb := "Needs "
		switch {
		case strings.HasPrefix(msg, "requires at least"):
			verb = "Needs at least "
		case strings.HasPrefix(msg, "accepts at most"):
			verb = "Takes at most "
		}
		return usagef("%s%s, got %s", verb, arguments(m[1]), m[2]).hint(usage())
	}
	if m := argRange.FindStringSubmatch(msg); m != nil {
		return usagef("Takes %s to %s arguments, got %s", m[1], m[2], m[3]).hint(usage())
	}
	if m := unknownCommand.FindStringSubmatch(msg); m != nil {
		if m[2] != "lyrelog" && a.failed != nil && !a.failed.HasAvailableSubCommands() {
			// cobra.NoArgs: a command that takes no arguments was given one.
			return usagef("Unexpected argument %q", m[1]).hint(usage())
		}
		e := usagef("Unknown command %q", m[1])
		if s := suggestion.FindStringSubmatch(msg); s != nil {
			return e.hint("Did you mean `" + m[2] + " " + s[1] + "`? Run `" + m[2] + " --help` for the list.")
		}
		return e.hint("Run `" + m[2] + " --help` for the list.")
	}
	return nil
}
