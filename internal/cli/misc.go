package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"go.lyrelog.com/cli/internal/api"
	"go.lyrelog.com/cli/internal/login"
	"go.lyrelog.com/cli/internal/ui"
)

func (a *App) accountCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "account",
		Short: "Your account: sign-in methods, passkeys, two-step sign-in",
		Long: `Your account as the account page shows it. Adding passkeys or sign-in methods and turning
on two-step sign-in happen in the browser (lyrelog open account); deleting the account stays
on the web.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			sp := a.spin("Loading your account")
			raw, err := a.getMap(cmd.Context(), "/api/account", nil)
			sp.Stop()
			if err != nil {
				return err
			}
			if a.mode.JSON {
				return a.printJSON(raw)
			}
			var acc api.Account
			_ = remarshal(raw, &acc)
			th, now := a.theme, a.Now()
			name := acc.User.Name
			if name == "" {
				name = acc.User.Email
			}
			fmt.Fprintf(a.out, "%s %s\n\n", th.Bold(name), th.Dim(acc.User.Email))
			row := func(k, v string) { fmt.Fprintf(a.out, "  %s %s\n", th.Dim(fmt.Sprintf("%-16s", k)), v) }
			if acc.EmailVerified != nil {
				row("Email", map[bool]string{true: th.Mark("ok") + " confirmed", false: th.Warning("not confirmed yet")}[*acc.EmailVerified])
			}
			for i, id := range acc.Identities {
				label := ""
				if i == 0 {
					label = "Signs in with"
				}
				used := ""
				if id.LastUsedAt != nil {
					used = th.Dim(" · used " + ui.Ago(*id.LastUsedAt, now))
				}
				row(label, ui.Capitalize(id.Provider)+" "+th.Dim(id.Email)+used)
			}
			passkeys := make([]string, 0, len(acc.Passkeys))
			for _, p := range acc.Passkeys {
				passkeys = append(passkeys, p.Name)
			}
			if len(passkeys) == 0 {
				row("Passkeys", th.Dim("none"))
			} else {
				row("Passkeys", strings.Join(passkeys, ", "))
			}
			if acc.TwoFactor {
				row("Two-step", th.Mark("ok")+" on"+th.Dim(fmt.Sprintf(" · %d recovery codes left", acc.RecoveryCodesLeft)))
			} else {
				row("Two-step", th.Dim("off"))
			}
			return nil
		},
	}
	var name string
	set := &cobra.Command{
		Use:     "set",
		Short:   "Change your name",
		Example: `  lyrelog account set --name "Ana García"`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("name") {
				return usagef("Nothing to change").hint("Pass --name.")
			}
			if err := a.client.JSON(cmd.Context(), api.Request{Method: http.MethodPatch, Path: "/api/account", Body: map[string]string{"name": name}}, nil); err != nil {
				return err
			}
			if a.mode.JSON {
				return a.printJSON(map[string]any{"name": name})
			}
			a.done("Saved your name")
			return nil
		},
	}
	set.Flags().StringVar(&name, "name", "", "your name")

	sessions := &cobra.Command{
		Use:   "sessions",
		Short: "Where you're signed in: browsers and computers with the CLI (tokens: lyrelog tokens)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			sp := a.spin("Loading sessions")
			raw, list, err := a.sessions(cmd.Context(), false)
			sp.Stop()
			if err != nil {
				return err
			}
			if a.mode.JSON {
				return a.printJSON(raw)
			}
			th, now := a.theme, a.Now()
			t := ui.Table{Columns: []ui.Column{{Header: "ID", Right: true}, {Header: "Where", Flex: true}, {Header: "Kind"}, {Header: "Signed in"}, {Header: "Last used"}, {Header: "Ends"}, {Header: ""}}}
			for _, s := range list {
				where := s.Name
				if s.Kind == "browser" || where == "" {
					where = browserName(s.UserAgent)
				}
				if s.Kind == "cli" && s.Name == "" {
					where = "a computer"
				}
				kind := map[string]string{"cli": "command line", "browser": "browser"}[s.Kind]
				if kind == "" {
					kind = s.Kind
				}
				// A CLI sign-in kept with lyrelog login --keep doesn't end when unused.
				ends := ""
				if s.Kind == "cli" && s.IdleEnds != nil && !*s.IdleEnds {
					ends = "kept to " + day(s.ExpiresAt)
					if s.never() {
						ends = th.Warning("kept, never ends")
					}
				}
				current := ""
				if s.Current {
					current = th.Primary("this one")
				}
				t.Add(strconv.FormatInt(s.ID, 10), where, kind, ui.Ago(s.CreatedAt, now), ui.Ago(s.LastUsedAt, now), ends, current)
			}
			t.Render(a.out, th, a.Width)
			return nil
		},
	}
	signOut := &cobra.Command{
		Use:   "sign-out <session-id>",
		Short: "Sign one of your sessions out (lyrelog account sessions lists them)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.client.JSON(cmd.Context(), api.Request{Method: http.MethodDelete, Path: "/api/account/sessions/" + api.PathEscape(args[0])}, nil); err != nil {
				return err
			}
			if a.mode.JSON {
				return a.printJSON(map[string]any{"signed_out": true, "session": args[0]})
			}
			a.done("Signed session " + args[0] + " out")
			return nil
		},
	}
	cmd.AddCommand(set, sessions, signOut)
	return cmd
}

// browserName is a coarse name from a browser's User-Agent, "Chrome on macOS", as the
// dashboard's account page writes it (../lyrelog/web/src/pages/account.tsx browserName).
func browserName(ua string) string {
	has := func(s string) bool { return strings.Contains(ua, s) }
	browser := "A browser"
	switch {
	case has("Edg/"):
		browser = "Edge"
	case has("Firefox/"):
		browser = "Firefox"
	case has("Chrome/"):
		browser = "Chrome"
	case has("Safari/"):
		browser = "Safari"
	}
	system := ""
	switch {
	case has("iPhone") || has("iPad"):
		system = "iOS"
	case has("Android"):
		system = "Android"
	case has("Mac OS X"):
		system = "macOS"
	case has("Windows"):
		system = "Windows"
	case has("Linux"):
		system = "Linux"
	}
	if system == "" {
		return browser
	}
	return browser + " on " + system
}

// pages are what lyrelog open knows, and where each one lives in the dashboard.
var pages = []struct {
	name, path, about string
	project           bool
}{
	{"events", "", "the explorer", true},
	{"errors", "/errors", "the Errors page", true},
	{"sessions", "/sessions", "sessions", true},
	{"people", "/users", "the project's people", true},
	{"insights", "/insights", "Insights", true},
	{"releases", "/releases", "Releases", true},
	{"keys", "/keys", "API keys", true},
	{"settings", "/settings", "project settings", true},
	{"projects", "/projects", "all projects", false},
	{"usage", "/usage", "usage and plan", false},
	{"account", "/account", "your account", false},
	{"organization", "/organization", "your organization", false},
}

func (a *App) openCommand() *cobra.Command {
	var names []string
	for _, p := range pages {
		names = append(names, p.name)
	}
	long := "Open a dashboard page in the browser (or print its address, for agents and with --print). Pages:\n"
	for _, p := range pages {
		long += fmt.Sprintf("  %-13s %s\n", p.name, p.about)
	}
	long += "  person <id>   one person's page\n  event <id>    the explorer, searching for that event id over the last 30 days\n" +
		"                (the dashboard has no address for one open event: click it in the list)"
	var printOnly bool
	cmd := &cobra.Command{
		Use:   "open [page] [id]",
		Short: "Open the dashboard on a page of the project",
		Long:  long,
		Example: `  lyrelog open
  lyrelog open errors
  lyrelog open person u_8f2a
  lyrelog open settings --print`,
		Args:      cobra.MaximumNArgs(2),
		ValidArgs: append(names, "person"),
		RunE: func(cmd *cobra.Command, args []string) error {
			page := "events"
			if len(args) > 0 {
				page = args[0]
			}
			target := ""
			needsProject := true
			switch page {
			case "person", "user", "player":
				if len(args) < 2 {
					return usagef("Which person? lyrelog open person <user-id>")
				}
				target = "/users/" + url.PathEscape(args[1])
			case "event":
				if len(args) < 2 {
					return usagef("Which event? lyrelog open event <event-id>")
				}
				target = "?q=" + url.QueryEscape(args[1]) + "&range=30d"
			default:
				found := false
				for _, p := range pages {
					if p.name == page {
						target, needsProject, found = p.path, p.project, true
					}
				}
				if !found {
					return usagef("No page %q", page).hint("Pages: " + strings.Join(names, ", ") + ", person <id>.")
				}
			}
			address := a.base + target
			if needsProject {
				p, err := a.project(cmd.Context(), a.projectRef(nil))
				if err != nil {
					return err
				}
				address = a.base + "/p/" + p.ID + target
			}
			if a.mode.JSON {
				return a.printJSON(map[string]string{"url": address})
			}
			if !printOnly && a.mode.Interactive {
				open := a.OpenBrowser
				if open == nil {
					open = func(u string) error { return login.OpenBrowser(a.Getenv, u) }
				}
				if login.CanOpenBrowser(a.Getenv) || a.OpenBrowser != nil {
					if err := open(address); err == nil {
						a.done("Opened " + a.theme.Primary(address))
						return nil
					}
				}
			}
			fmt.Fprintln(a.out, a.theme.Primary(address))
			return nil
		},
	}
	cmd.Flags().BoolVar(&printOnly, "print", false, "print the address instead of opening it")
	return cmd
}

func (a *App) apiCommand() *cobra.Command {
	var data string
	var fields []string
	cmd := &cobra.Command{
		Use:   "api <method> <path>",
		Short: "Call any dashboard API route, signed in (like gh api)",
		Long: `Call any route of the dashboard's API with your sign-in: for anything the commands don't
wrap yet. The path may leave out /api, and {project} becomes the current project's id.

The answer is printed as it comes (JSON). A failure exits with the codes every command uses
and the error as JSON on stderr.

-F key=value adds a query parameter to a GET, and a field to the JSON body for every other
method, DELETE included, as gh api does (numbers, true, false and null are sent as such). --data sends a body as it is (@file reads a file, @- stdin).`,
		Example: `  lyrelog api GET /api/me
  lyrelog api GET projects/{project}/regions -F from=1763500000000
  lyrelog api GET projects/{project}/series -F user=u_8f2a
  lyrelog api PATCH projects/{project} --data '{"retention_days":30}'
  lyrelog api POST projects/{project}/views --data @view.json`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			method := strings.ToUpper(args[0])
			switch method {
			case "GET", "POST", "PUT", "PATCH", "DELETE":
			default:
				return usagef("The method is GET, POST, PUT, PATCH or DELETE")
			}
			path := args[1]
			if strings.Contains(path, "{project}") {
				p, err := a.project(cmd.Context(), a.projectRef(nil))
				if err != nil {
					return err
				}
				path = strings.ReplaceAll(path, "{project}", p.ID)
			}
			if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
				return usagef("Give the path only, like /api/me").hint("The server comes from --url or LYRELOG_URL.")
			}
			path = "/" + strings.TrimPrefix(path, "/")
			if !strings.HasPrefix(path, "/api/") {
				path = "/api" + path
			}
			req := api.Request{Method: method, Path: path}
			if data != "" && len(fields) > 0 {
				return usagef("Use --data or -F, not both")
			}
			if data != "" {
				body, err := a.readData(data)
				if err != nil {
					return err
				}
				if !json.Valid(body) {
					return usagef("--data isn't valid JSON")
				}
				req.Body = body
			}
			if len(fields) > 0 {
				obj := map[string]any{}
				q := url.Values{}
				for _, f := range fields {
					k, v, ok := strings.Cut(f, "=")
					if !ok {
						return usagef("-F %q should look like key=value", f)
					}
					q.Add(k, v)
					obj[k] = typed(v)
				}
				if method == "GET" {
					req.Query = q
				} else {
					req.Body = obj
				}
			}
			res, err := a.client.Do(cmd.Context(), req)
			if err != nil {
				return err
			}
			if len(res.Body) == 0 {
				if a.mode.JSON {
					return a.printJSON(map[string]int{"status": res.Status})
				}
				a.done(fmt.Sprintf("%d %s", res.Status, http.StatusText(res.Status)))
				return nil
			}
			if json.Valid(res.Body) && !strings.Contains(res.Header.Get("Content-Type"), "ndjson") {
				if !a.mode.JSON && a.theme.Color {
					ui.PrettyJSON(a.out, a.theme, res.Body, "")
					return nil
				}
				return a.printRaw(res.Body)
			}
			_, err = a.Stdout.Write(res.Body)
			return err
		},
	}
	cmd.Flags().StringVarP(&data, "data", "d", "", "request body: JSON, @file, or @- for stdin")
	cmd.Flags().StringArrayVarP(&fields, "field", "F", nil, "key=value: a query parameter for GET, a JSON body field for POST, PUT, PATCH and DELETE; repeat for more")
	return cmd
}

func (a *App) readData(data string) ([]byte, error) {
	name, ok := strings.CutPrefix(data, "@")
	if !ok {
		return []byte(data), nil
	}
	if name == "-" {
		b, err := io.ReadAll(io.LimitReader(a.Stdin, 16<<20))
		if err != nil {
			return nil, failf(ExitError, "Couldn't read stdin: %v", err)
		}
		return b, nil
	}
	b, err := os.ReadFile(name)
	if err != nil {
		return nil, failf(ExitUsage, "Couldn't read %s: %v", name, err)
	}
	return b, nil
}

// typed reads -F values like gh: numbers, booleans and null as themselves.
func typed(v string) any {
	switch v {
	case "true":
		return true
	case "false":
		return false
	case "null":
		return nil
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil && strings.ContainsAny(v, ".eE") {
		return f
	}
	return v
}

func (a *App) versionCommand() *cobra.Command {
	return &cobra.Command{
		Use:         "version",
		Short:       "Show the CLI's version, commit and build date",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"no-setup": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			v, commit, date := Version, Commit, Date
			// go install builds have no ldflags: the module version and the commit come from the
			// build info.
			if info, ok := debug.ReadBuildInfo(); ok {
				if v == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
					v = info.Main.Version
				}
				for _, s := range info.Settings {
					if s.Key == "vcs.revision" && len(s.Value) >= 7 && commit == "" {
						commit = s.Value[:7]
					}
					if s.Key == "vcs.time" && date == "" {
						date = s.Value
					}
				}
			}
			if len(commit) > 7 {
				commit = commit[:7]
			}
			if a.mode.JSON {
				return a.printJSON(map[string]string{"version": v, "commit": commit, "date": date, "go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH})
			}
			extra := runtime.Version() + ", " + runtime.GOOS + "/" + runtime.GOARCH
			if date != "" {
				built := date
				if t, err := time.Parse(time.RFC3339, date); err == nil {
					built = t.UTC().Format("2006-01-02")
				}
				extra = "built " + built + ", " + extra
			}
			if commit != "" {
				extra = commit + ", " + extra
			}
			fmt.Fprintf(a.out, "lyrelog %s %s\n", a.theme.Bold(v), a.theme.Dim("("+extra+")"))
			return nil
		},
	}
}
