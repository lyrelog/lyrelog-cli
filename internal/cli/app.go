// Package cli is the lyrelog command: every command, how it decides between people and
// programs, and how failures become messages and exit codes.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"charm.land/lipgloss/v2"

	"go.lyrelog.com/cli/internal/api"
	"go.lyrelog.com/cli/internal/config"
	"go.lyrelog.com/cli/internal/detect"
	"go.lyrelog.com/cli/internal/ui"
)

// Version, Commit and Date are set at build time by make and GoReleaser
// (-ldflags "-X go.lyrelog.com/cli/internal/cli.Version=v0.1.0 -X ...Commit=4678ef1 -X ...Date=2026-11-19T10:00:00Z").
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

// App is one run of the CLI, with everything it touches injectable for tests.
type App struct {
	// keep: lyrelog login --keep (30d, 90d, 1y, never; "" a browser's limits).
	keep string

	Stdin          io.Reader
	Stdout, Stderr io.Writer
	Getenv         func(string) string
	Environ        []string
	Now            func() time.Time
	// TTY says whether stdin, stdout and stderr are terminals.
	StdinTTY, StdoutTTY, StderrTTY bool
	// Width is the terminal's width (0: unknown). COLUMNS wins over it; in human mode into a
	// pipe or a file it is COLUMNS, else 120 (setMode).
	Width int
	// Dark: the terminal's background (nil: ask the terminal when colour is on).
	Dark *bool
	// Profile: the colours the terminal shows (nil: detected from stdout and the environment).
	Profile *colorprofile.Profile
	// HTTP is the client for the API (nil: the default).
	HTTP *http.Client
	// OpenBrowser opens a URL (tests replace it).
	OpenBrowser func(string) error
	// Hostname names this computer on the approval page.
	Hostname func() (string, error)

	// Set by the root command before any command runs.
	mode         detect.Mode
	theme        ui.Theme
	out          io.Writer // stdout, through the colour profile for people
	errOut       io.Writer
	cfgPath      string
	cfg          *config.File
	base         string
	token        string
	tokenFromEnv bool
	client       *api.Client
	lineCh       chan string

	flagProject         string
	flagJSON, flagHuman bool
	lastMode            string // "json" or "human": the last of the two flags on the command line
	flagURL             string
	flagNoColor         bool
	failed              *cobra.Command // the command that ran (or failed to parse), for usage errors
}

// Main runs the CLI with the process's own streams and returns the exit code.
func Main(args []string) int {
	isTTY := func(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }
	a := &App{
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
		Getenv: os.Getenv, Environ: os.Environ(), Now: time.Now,
		StdinTTY: isTTY(os.Stdin), StdoutTTY: isTTY(os.Stdout), StderrTTY: isTTY(os.Stderr),
		Hostname: os.Hostname,
	}
	if a.StdoutTTY {
		if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
			a.Width = w
		}
	}
	return a.Run(context.Background(), args)
}

// Run executes args and returns the exit code.
func (a *App) Run(ctx context.Context, args []string) int {
	if a.Now == nil {
		a.Now = time.Now
	}
	if a.Getenv == nil {
		a.Getenv = func(string) string { return "" }
	}
	ctx, stop := signalContext(ctx)
	defer stop()
	root := a.rootCommand()
	root.SetArgs(args)
	root.SetIn(a.Stdin)
	root.SetOut(a.Stdout)
	root.SetErr(a.Stderr)
	// Until flags are parsed, decide the mode from the arguments, so even a flag error comes out
	// in the right form.
	a.lastMode = lastModeFlag(args)
	a.flagJSON, a.flagHuman = a.lastMode == "json", a.lastMode == "human"
	a.flagNoColor = hasArg(args, "--no-color")
	a.setMode()
	failed, err := root.ExecuteContextC(ctx)
	a.failed = failed
	if err == nil {
		return ExitOK
	}
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return ExitInterrupted
	}
	return a.report(err)
}

// lastModeFlag is "json" or "human", whichever of --json and --human comes last ("" for
// neither): the rule when both are given.
func lastModeFlag(args []string) string {
	last := ""
	for _, arg := range args {
		if arg == "--" {
			break
		}
		for _, name := range []string{"json", "human"} {
			if arg == "--"+name {
				last = name
			} else if v, ok := strings.CutPrefix(arg, "--"+name+"="); ok {
				if on, err := strconv.ParseBool(v); err == nil && on {
					last = name
				}
			}
		}
	}
	return last
}

func hasArg(args []string, flag string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == flag || arg == flag+"=true" {
			return true
		}
	}
	return false
}

// setMode decides how to write, from the flags and the environment.
func (a *App) setMode() {
	a.mode = detect.Detect(detect.Inputs{
		Getenv: a.Getenv, StdoutTTY: a.StdoutTTY, StdinTTY: a.StdinTTY, StderrTTY: a.StderrTTY,
		ForceJSON: a.flagJSON, ForceHuman: a.flagHuman, NoColor: a.flagNoColor,
	})
	a.out, a.errOut = a.Stdout, a.Stderr
	a.Width = a.width()
	profile := colorprofile.TrueColor
	if a.mode.Color {
		if a.Profile != nil {
			profile = *a.Profile
		} else {
			profile = colorprofile.Detect(a.Stdout, a.Environ)
		}
		// No TERM, or a terminal without colours: plain text.
		if profile <= colorprofile.ASCII {
			a.mode.Color = false
		}
	}
	a.theme = ui.Theme{Color: a.mode.Color}
	if a.mode.Color {
		if a.Dark != nil {
			a.theme.Dark = *a.Dark
		} else {
			a.theme.Dark = a.darkBackground()
		}
		// Downsample to what the terminal shows (256 colours, 16...).
		a.out = &colorprofile.Writer{Forward: a.Stdout, Profile: profile}
		a.errOut = &colorprofile.Writer{Forward: a.Stderr, Profile: profile}
	}
}

// width is what tables and event lines fit to: COLUMNS when set, else the terminal's width;
// for people reading through a pipe or a file (--human), 120.
func (a *App) width() int {
	if cols, err := strconv.Atoi(strings.TrimSpace(a.Getenv("COLUMNS"))); err == nil && cols > 0 {
		return cols
	}
	if a.Width == 0 && !a.StdoutTTY && !a.mode.JSON {
		return 120
	}
	return a.Width
}

// darkBackground: LYRELOG_THEME (dark or light), else COLORFGBG, else the terminal's answer.
func (a *App) darkBackground() bool {
	switch strings.ToLower(a.Getenv("LYRELOG_THEME")) {
	case "dark":
		return true
	case "light":
		return false
	}
	if fgbg := a.Getenv("COLORFGBG"); fgbg != "" {
		parts := strings.Split(fgbg, ";")
		if bg, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
			return bg < 7 || bg == 8
		}
	}
	in, inOK := a.Stdin.(*os.File)
	out, outOK := a.Stdout.(*os.File)
	if inOK && outOK && a.StdinTTY {
		return lipgloss.HasDarkBackground(in, out)
	}
	return true
}

// setup reads the config and picks the server and the token; every command but version runs it.
func (a *App) setup() error {
	dir, err := config.Dir(a.Getenv)
	if err != nil {
		return failf(ExitError, "Couldn't find a place for the settings: %v", err).hint("Set LYRELOG_CONFIG_DIR to a folder the CLI can write to.")
	}
	a.cfgPath = filepath.Join(dir, "config.json")
	if a.cfg, err = config.Load(a.cfgPath); err != nil {
		return failf(ExitError, "%v", err)
	}
	raw := a.flagURL
	if raw == "" {
		raw = a.Getenv("LYRELOG_URL")
	}
	if raw == "" {
		raw = a.cfg.URL
	}
	if a.base, err = config.NormalizeURL(raw); err != nil {
		return failf(ExitUsage, "%s", ui.Capitalize(err.Error())).hint("Use the dashboard's address, like https://app.lyrelog.com (or http://127.0.0.1:8090 locally).")
	}
	a.token, a.tokenFromEnv = a.Getenv("LYRELOG_TOKEN"), false
	if a.token != "" {
		a.tokenFromEnv = true
	} else if h := a.cfg.Hosts[a.base]; h != nil {
		a.token = h.Token
	}
	a.client = api.New(a.base, a.token, fmt.Sprintf("lyrelog-cli/%s (%s/%s)", Version, runtime.GOOS, runtime.GOARCH))
	if a.HTTP != nil {
		a.client.HTTP = a.HTTP
	}
	return nil
}

func (a *App) host() *config.Host { return a.cfg.Host(a.base) }

func (a *App) save() error {
	if err := a.cfg.Save(a.cfgPath); err != nil {
		return failf(ExitError, "Couldn't save the settings to %s: %v", a.cfgPath, err)
	}
	return nil
}

// spin starts a spinner for people (never for programs).
func (a *App) spin(text string) *ui.Spinner {
	return ui.StartSpinner(a.errOut, a.theme, a.mode.Interactive, text)
}

// printJSON writes v as JSON: indented for a terminal, one line for programs.
func (a *App) printJSON(v any) error {
	return writeJSON(a.Stdout, v, a.StdoutTTY)
}

// writeJSON writes v and a newline, < > & as they are (it's read by programs, not put in HTML).
func writeJSON(w io.Writer, v any, indent bool) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(v); err != nil {
		return err
	}
	_, err := w.Write(buf.Bytes())
	return err
}

// printRaw writes JSON the server sent, re-indented for a terminal.
func (a *App) printRaw(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage("null")
	}
	return a.printJSON(raw)
}

// more tells that a list was cut: a line for people, a JSON line on stderr for programs.
func (a *App) more(human, next string) {
	if a.mode.JSON {
		_ = writeJSON(a.Stderr, map[string]any{"more": true, "next": next}, false)
		return
	}
	fmt.Fprintf(a.out, "\n%s %s\n", a.theme.Dim(human), a.theme.Command(next))
}

// println writes a line for people.
func (a *App) println(parts ...any) { fmt.Fprintln(a.out, parts...) }

// done prints "✓ text" for people.
func (a *App) done(text string) { fmt.Fprintf(a.out, "%s %s\n", a.theme.Mark("ok"), text) }

// tip prints a dim line with a command in cobalt.
func (a *App) tip(text, command string) {
	fmt.Fprintf(a.out, "%s %s\n", a.theme.Dim(text), a.theme.Command(command))
}

// noArgsHelp makes a group command show its help.
func noArgsHelp(cmd *cobra.Command, args []string) error { return cmd.Help() }
