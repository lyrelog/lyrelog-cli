// Package detect decides how the CLI talks: to a person at a terminal (tables, colours, a
// spinner, questions) or to a program (JSON, nothing else). A coding agent counts as a program
// even when it gives the CLI a pseudo-terminal.
package detect

import "strings"

// Mode is how one run of the CLI writes.
type Mode struct {
	// JSON: stdout carries JSON only, errors go to stderr as {"error","hint"}.
	JSON bool
	// Color: ANSI styling on stdout and stderr (human mode, a terminal, NO_COLOR unset).
	Color bool
	// Interactive: the CLI may ask questions and show a spinner (a person at a terminal).
	Interactive bool
	// Agent names what was detected ("claude-code", "ci"...), "" for none.
	Agent string
}

// Inputs are what Detect looks at; Getenv is os.Getenv in the binary, a map in tests.
type Inputs struct {
	Getenv    func(string) string
	StdoutTTY bool
	StdinTTY  bool
	StderrTTY bool
	// ForceJSON and ForceHuman are --json and --human. When both are given the last one on the
	// command line wins; the caller (cli.lastModeFlag) settles that, so at most one is set here.
	ForceJSON, ForceHuman bool
	NoColor               bool // --no-color
}

// marker is one environment variable a coding agent or CI sets in the shells it runs.
type marker struct {
	name  string
	value string // "" means any non-empty value; "a|b" means one of these (case ignored)
	agent string
}

// Markers are the variables checked, in order. Each comes from the tool's own source or docs;
// see README.md ("How the CLI spots an agent") for where, and for what is left out on purpose.
var markers = []marker{
	{"LYRELOG_AGENT", "", "lyrelog-agent"}, // ours: set it to force agent mode
	{"AI_AGENT", "", ""},                   // the cross-tool convention (Claude Code sets it, Vercel's detect-agent reads it)
	{"AGENT", "amp|goose|1|true", ""},      // Amp (AGENT=amp), Goose (AGENT=goose, block/goose#7017); any other value is someone else's AGENT
	{"CLAUDECODE", "", "claude-code"},      // Claude Code: CLAUDECODE=1 in every shell it runs
	{"CLAUDE_CODE_ENTRYPOINT", "", "claude-code"},
	{"CURSOR_AGENT", "", "cursor"}, // Cursor's agent (CLI and editor)
	{"CURSOR_EXTENSION_HOST_ROLE", "agent-exec", "cursor"},
	{"CODEX_SANDBOX", "", "codex"}, // OpenAI Codex CLI: seatbelt / landlock sandbox
	{"CODEX_CI", "", "codex"},
	{"CODEX_THREAD_ID", "", "codex"},
	{"CODEX_SANDBOX_NETWORK_DISABLED", "", "codex"},
	{"GEMINI_CLI", "", "gemini-cli"}, // Gemini CLI: GEMINI_CLI=1 in its shell tool
	{"OPENCODE", "1", "opencode"},
	{"OPENCODE_CLIENT", "", "opencode"},
	{"AMP_CURRENT_THREAD_ID", "", "amp"},
	{"AUGMENT_AGENT", "", "augment"},
	{"ANTIGRAVITY_AGENT", "", "antigravity"},
	{"CI", "", "ci"}, // GitHub Actions, GitLab, CircleCI, Buildkite... all set CI
}

// Agent returns the name of the agent or CI the environment points to, or "".
func Agent(getenv func(string) string) string {
	for _, m := range markers {
		v := strings.TrimSpace(getenv(m.name))
		if v == "" || (m.value != "" && !oneOf(v, m.value)) {
			continue
		}
		// CI=false (or 0) is someone saying "not CI".
		if m.name == "CI" && (strings.EqualFold(v, "false") || v == "0") {
			continue
		}
		if m.agent != "" {
			return m.agent
		}
		if m.name == "AGENT" && (v == "1" || strings.EqualFold(v, "true")) {
			return "agent"
		}
		return strings.ToLower(v) // AI_AGENT and AGENT carry the name
	}
	return ""
}

func oneOf(v, values string) bool {
	for _, want := range strings.Split(values, "|") {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}

// Detect decides the mode. JSON when forced, when an agent or CI is detected, or when stdout is
// not a terminal; --human wins over the detection (and over --json if a caller sets both). Colour only for people at a terminal, never with
// NO_COLOR (any value) or TERM=dumb. Questions and spinners only when a person can answer.
func Detect(in Inputs) Mode {
	getenv := in.Getenv
	m := Mode{Agent: Agent(getenv)}
	switch {
	case in.ForceHuman:
		m.JSON = false
	case in.ForceJSON:
		m.JSON = true
	default:
		m.JSON = m.Agent != "" || !in.StdoutTTY
	}
	noColor := in.NoColor || getenv("NO_COLOR") != "" || getenv("TERM") == "dumb"
	m.Color = !m.JSON && in.StdoutTTY && !noColor
	m.Interactive = !m.JSON && m.Agent == "" && in.StdinTTY && in.StderrTTY
	return m
}
