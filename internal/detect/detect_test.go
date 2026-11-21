package detect

import "testing"

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestAgent(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{}, ""},
		{map[string]string{"CLAUDECODE": "1"}, "claude-code"},
		{map[string]string{"CLAUDE_CODE_ENTRYPOINT": "cli"}, "claude-code"},
		{map[string]string{"AI_AGENT": "Claude-Code_2-1_agent"}, "claude-code_2-1_agent"},
		{map[string]string{"AGENT": "amp"}, "amp"},
		{map[string]string{"AGENT": "goose"}, "goose"},
		{map[string]string{"AGENT": "Goose"}, "goose"},
		{map[string]string{"AGENT": "1"}, "agent"},
		{map[string]string{"AGENT": "true"}, "agent"},
		{map[string]string{"AGENT": "smith"}, ""}, // someone else's AGENT, not a coding agent
		{map[string]string{"AGENT": "0"}, ""},
		{map[string]string{"CURSOR_AGENT": "1"}, "cursor"},
		{map[string]string{"CURSOR_EXTENSION_HOST_ROLE": "agent-exec"}, "cursor"},
		{map[string]string{"CURSOR_EXTENSION_HOST_ROLE": "user"}, ""},
		{map[string]string{"CURSOR_TRACE_ID": "abc"}, ""}, // any Cursor terminal, a person's too
		{map[string]string{"CODEX_SANDBOX": "seatbelt"}, "codex"},
		{map[string]string{"CODEX_THREAD_ID": "t"}, "codex"},
		{map[string]string{"GEMINI_CLI": "1"}, "gemini-cli"},
		{map[string]string{"OPENCODE": "1"}, "opencode"},
		{map[string]string{"OPENCODE": "yes"}, ""},
		{map[string]string{"CI": "true"}, "ci"},
		{map[string]string{"CI": "false"}, ""},
		{map[string]string{"CI": "0"}, ""},
		{map[string]string{"AIDER_MODEL": "x"}, ""}, // aider's settings, not a marker
	}
	for _, c := range cases {
		if got := Agent(env(c.env)); got != c.want {
			t.Errorf("Agent(%v) = %q, want %q", c.env, got, c.want)
		}
	}
}

func TestDetect(t *testing.T) {
	person := Inputs{Getenv: env(nil), StdoutTTY: true, StdinTTY: true, StderrTTY: true}
	cases := []struct {
		name string
		in   Inputs
		want Mode
	}{
		{"a person at a terminal", person, Mode{Color: true, Interactive: true}},
		{"a pipe", Inputs{Getenv: env(nil), StdinTTY: true, StderrTTY: true}, Mode{JSON: true}},
		{"an agent with a pty", Inputs{Getenv: env(map[string]string{"CLAUDECODE": "1"}), StdoutTTY: true, StdinTTY: true, StderrTTY: true}, Mode{JSON: true, Agent: "claude-code"}},
		{"--human in an agent: tables, no questions", Inputs{Getenv: env(map[string]string{"CLAUDECODE": "1"}), StdoutTTY: true, StdinTTY: true, StderrTTY: true, ForceHuman: true}, Mode{Color: true, Agent: "claude-code"}},
		{"--human into a pipe: no colour", Inputs{Getenv: env(nil), StdinTTY: true, StderrTTY: true, ForceHuman: true}, Mode{Interactive: true}},
		{"--json at a terminal", Inputs{Getenv: env(nil), StdoutTTY: true, StdinTTY: true, StderrTTY: true, ForceJSON: true}, Mode{JSON: true}},
		{"NO_COLOR", Inputs{Getenv: env(map[string]string{"NO_COLOR": "1"}), StdoutTTY: true, StdinTTY: true, StderrTTY: true}, Mode{Interactive: true}},
		{"TERM=dumb", Inputs{Getenv: env(map[string]string{"TERM": "dumb"}), StdoutTTY: true, StdinTTY: true, StderrTTY: true}, Mode{Interactive: true}},
		{"--no-color", Inputs{Getenv: env(nil), StdoutTTY: true, StdinTTY: true, StderrTTY: true, NoColor: true}, Mode{Interactive: true}},
		{"no stdin: nobody to ask", Inputs{Getenv: env(nil), StdoutTTY: true, StderrTTY: true}, Mode{Color: true}},
		{"CI", Inputs{Getenv: env(map[string]string{"CI": "true"}), StdoutTTY: true, StdinTTY: true, StderrTTY: true}, Mode{JSON: true, Agent: "ci"}},
	}
	for _, c := range cases {
		if got := Detect(c.in); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}
