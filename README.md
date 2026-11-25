<p align="center">
  <a href="https://lyrelog.com">
    <picture>
      <source media="(prefers-color-scheme: dark)" srcset="https://lyrelog.com/readme/cli-dark.png">
      <img alt="Lyrelog command line" src="https://lyrelog.com/readme/cli-light.png" width="600">
    </picture>
  </a>
</p>

<p align="center">
  <a href="https://docs.lyrelog.com/cli"><b>Documentation</b></a> ·
  <a href="https://docs.lyrelog.com/quickstart">Quickstart</a> ·
  <a href="https://lyrelog.com">lyrelog.com</a>
</p>

[Lyrelog](https://lyrelog.com/cli) from your terminal: search events, follow them live, read errors,
sessions and people, manage projects, keys and organizations. Everything the dashboard does,
except deleting your account or a project and changing how you sign in (those stay on the web).

It draws tables for people and writes JSON for programs, so a coding agent can move around
Lyrelog as easily as you.

## Install

With Homebrew (macOS and Linux):

```bash
brew install lyrelog/tap/lyrelog
```

With Go 1.26 or later:

```bash
go install go.lyrelog.com/cli/cmd/lyrelog@latest
```

Or download a build for macOS, Linux or Windows from the
[releases page](https://github.com/lyrelog/lyrelog-cli/releases), or build it from this
repository: `make build` puts it at `bin/lyrelog`. `lyrelog version` shows the version, commit
and build date.

## Sign in

```bash
lyrelog login
```

Your browser opens the dashboard; approve this computer and the browser hands the sign-in back
to the terminal. Over SSH, without a display, or with `--no-browser`, the page shows a code to
paste instead.

The session ends after 14 days unused (30 at most). On your own computer, `lyrelog login --keep
90d` (or `30d`, `1y`, `never`) keeps it that long whether used or not, until you sign it out from
your account page; the approval page shows the choice and asks for a dashboard sign-in from the
last 10 minutes. A sign-in that never expires works for anyone who copies the settings file.
Creating a server, read or admin key needs
a sign-in from the last 10 minutes: run `lyrelog login` again just before. `lyrelog whoami` shows
who you are; `lyrelog logout` ends the session on the server and forgets it here.

Then pick the project commands use by default:

```bash
lyrelog projects
lyrelog use tilefall           # its slug, its name or its id
```

Any command takes `-p <project>` for another one.

## For people

```bash
lyrelog events                                  # the last 24 hours, newest first
lyrelog events --level error --since 1h
lyrelog events --level warning+ --version "2.4.0|2.4.1"
lyrelog events -q "card declined" --category checkout
lyrelog events --attr board.size:9 --attr result!:win
lyrelog events -f                                # follow new events as they arrive, late ones too
lyrelog events get 0b7e3d52-8c1a-4f0e-9d3b-5a2f8e1c7d40
lyrelog errors                                   # problems grouped, worst first
lyrelog sessions --errors --since 24h
lyrelog people --sort errors                     # in the project's own word: players, users...
lyrelog person u_8f2a                            # one person: totals, sessions, latest events
lyrelog releases
lyrelog insights trend game.finished --by version
lyrelog insights funnel onboarding.start onboarding.done --window 1h
lyrelog usage                                    # this month against the plan
lyrelog open errors                              # the same page in the dashboard
```

```
$ lyrelog events
Tilefall · production · last 24 hours

11:59:20 ▌error    purchase.failed  checkout  amount=4.99 reason="card declined"
11:57:00 ▌info     game.finished    game      board.size=9 result=win
10:00:00 ▌warning  sync.slow        sync      ms=5400

$ lyrelog errors
Tilefall · production · last 7 days

   Event            Area      Count  Change  Players  Last seen  Versions      Trend
●  purchase.failed  checkout    214    ↑78%       61  40s ago    2.4.1, 2.4.0  ▁▂▁▃▄▆█
●  app.crashed      app          12     new        9  3h ago     2.4.1             ▂▅█
```

Filters take what the dashboard's address takes: `"a|b"` for any of several, `"!a"` for not.
The environment is the project's default, as in the dashboard; `--env all` searches every one.
Times: `--since 15m|1h|6h|24h|7d|30d` (any number of s, m, h, d, w), or `--from` and `--to` as
RFC 3339, a date, Unix milliseconds or how long ago (`2h`).

Colours follow the dashboard and adapt to light and dark terminals (`LYRELOG_THEME=dark` or
`light` if the guess is wrong). `NO_COLOR=1` or `--no-color` turns them off.

Setting things up:

```bash
lyrelog projects create --name "Tilefall web" --slug tilefall-web
lyrelog projects set --retention-days 30 --default-env production --people player,players
lyrelog projects platform add web --url https://tilefall.example.com
lyrelog keys create --type ingest --name "iOS app"     # the secret is shown once
lyrelog keys revoke <key-id>
lyrelog orgs show
lyrelog orgs invite --role member --email ana@example.com
lyrelog person u_8f2a --export u_8f2a.ndjson           # an access request
lyrelog person u_8f2a --delete                         # an erasure request (owners)
```

Readable crashes: upload each build's dSYMs (iOS, Mac), or R8 mapping and native libraries (Android), once (only builds the project doesn't
have are sent). The CLI reads them here and uploads a map of function and file names and lines,
Swift names demangled with Xcode's tools:

```bash
lyrelog symbols upload path/to/Tilefall.xcarchive        # or a .dSYM, or a folder of them
lyrelog symbols upload app/build                         # an Android app: its R8 mapping and native libraries
lyrelog symbols                                          # what's uploaded
```

Flutter releases built with `--split-debug-info` (and `--obfuscate`) print Dart stacks by
address: `lyrelog symbols upload build/debug-info` uploads that folder's symbols, and with the
`--save-obfuscation-map` file beside it an error's obfuscated type reads as its real name.

Android releases shrunk by R8 name their mapping in every frame (`r8-map-id-…`, Android Gradle
Plugin 8+), so uploading each release's `mapping.txt` once is all: its crashes read as your
source again. Native libraries (C, C++) are matched by their build id: the command finds the
unstripped copies Gradle keeps under `build/intermediates` (or `native-debug-symbols.zip`) and
uploads each library once.

In Xcode, a Run Script phase can do it on every archive, with an access token in
`LYRELOG_TOKEN` (`lyrelog tokens create`). Add
`${DWARF_DSYM_FOLDER_PATH}/${DWARF_DSYM_FILE_NAME}` to the phase's Input Files, so Xcode's script
sandbox lets it read the dSYM:

```bash
[ "$CONFIGURATION" = "Release" ] || exit 0
export PATH="$PATH:/opt/homebrew/bin:/usr/local/bin"
lyrelog symbols upload "$DWARF_DSYM_FOLDER_PATH" -p tilefall || echo "warning: Lyrelog symbols not uploaded"
```

Readable website errors: after the bundler and before deploying, give each bundle its debug id,
then upload the source maps (the JavaScript SDK 1.1.0 or later sends each bundle's id with the
stack):

```bash
lyrelog sourcemaps inject dist     # a debug id in each bundle and its map, and a line that tells the SDK
lyrelog sourcemaps upload dist     # each map the project doesn't have, with the bundle's functions
```

Frames then read as the source: `Board.weight src/board.ts:14` rather than
`n.weight main.js:1:139`. The sources' text stays on your machine (Lyrelog keeps no source
code), and you don't need to deploy the `.map` files.

React Native gives its release bundles their ids in `metro.config.js` (`withLyrelog` from
`@lyrelog/react-native/metro`); after the release build, `lyrelog sourcemaps upload
android/app/build` (or the iOS map at `$SOURCEMAP_FILE`) finds Hermes's source maps.

## For agents and scripts

When stdout isn't a terminal, or the CLI runs inside a coding agent or CI, it writes JSON and
nothing else: no colours, no spinner, no questions. `--json` and `--human` force either.

```bash
lyrelog events --level error --since 1h | jq -r '.[] | "\(.ts) \(.message)"'
lyrelog errors --json | jq '.[0]'
lyrelog events -f --level error --json          # one JSON object a line
lyrelog keys create --type server --name tilefall-api --json | jq -r .secret
lyrelog api GET projects/{project}/regions       # any dashboard route, signed in
lyrelog api PATCH projects/{project} --data '{"retention_days":30}'
```

An agent can't sign in by itself: a person runs `lyrelog login` in a terminal on that computer,
and the agent then uses that sign-in.

### Skills for your agent

[Lyrelog's agent skills](https://github.com/lyrelog/skills) teach Claude Code, Codex, Cursor,
Gemini CLI, GitHub Copilot, OpenCode and others to add the SDK, investigate an error with this
CLI, get the right key or token, and set up alerts:

```bash
lyrelog skills install            # asks where and for which agents, then remembers
lyrelog skills install --project  # into this project (.claude/skills and .agents/skills)
lyrelog skills install --choose   # choose again
lyrelog skills                    # what's installed where
```

Offline, it installs the copy that came with the CLI. They're on skills.sh too:
`npx skills add lyrelog/skills`.

In CI, set `LYRELOG_TOKEN` to an access token (`lyr_tok_...`), with an expiry of 30 days, 90, a
year, or never. It wins over the saved sign-in and doesn't end when unused. Make one right after
`lyrelog login` (it needs a sign-in from the last 10 minutes), or on your account page in the
dashboard (Access tokens):

```bash
lyrelog login
lyrelog tokens create --name "GitHub Actions, deploy"    # shown once: store it as LYRELOG_TOKEN
lyrelog tokens                                           # name, id, expiry, last used
lyrelog tokens expiry 45 --expires 30d                   # 30d, 90d, 1y or never, from now
lyrelog tokens revoke 45
```

In JSON mode `tokens create` prints `{"token","session","never_expires"}`, and `--expires never`
needs `--yes`. An access token can't make or change tokens. Scripts that only read events can also use the
[read API](https://docs.lyrelog.com/read-api) with a read key.

### The JSON contract

- stdout carries only the data. A list is a JSON array of the server's own objects (the
  dashboard API's field names, unchanged); one thing is an object. `--follow` writes one object
  a line (NDJSON), in the order the events arrived: an event sent late (a phone that was
  offline) shows when it arrives, if its own time is within the last 2 days.
- Indented at a terminal (`--json`), one line otherwise.
- `--json` and `--human` together: the last one on the command line wins.
- When a list was cut (`--limit`, pages), stderr gets one line: `{"more":true,"next":"--cursor ..."}`
  or `{"more":true,"next":"--page 2"}`. Pass `next` to get the rest.
- A failure writes one line to stderr and exits with a code below:
  `{"error":"This needs a recent sign-in","hint":"Run lyrelog login again, then retry within 10 minutes.","code":"reauth_required","status":403}`.
  `error` and `hint` are for people; `code` is stable for programs; `status` is the HTTP status
  when the server answered; `retry_after` (seconds) when it asked to wait.
- Something that can't be undone (revoking a key or a token, deleting a person's data, removing someone
  from an organization) needs `--yes`, since nobody can answer a question.

### Exit codes

| Code | `code` | Meaning |
|---|---|---|
| 0 | | Done |
| 1 | `error` | Something else failed (the server, a file) |
| 2 | `usage` | A wrong command, flag or value; or `--yes` is needed |
| 3 | `not_signed_in` | Not signed in, or the session ended: `lyrelog login` |
| 4 | `reauth_required` | The action needs a sign-in from the last 10 minutes: `lyrelog login`, then retry |
| 5 | `forbidden` | Signed in, but not allowed (an owner's action) |
| 5 | `browser_only` | Only the dashboard does this (deleting the account or a project, passkeys, linked accounts, two-step): `error` names the page |
| 6 | `not_found` | No such project, event, key, person... (or not yours) |
| 7 | `refused` | The server refused the request as it is (400, 409) |
| 8 | `rate_limited` | Too many requests or the server is busy: wait `retry_after` |
| 9 | `network` | The server couldn't be reached |
| 130 | `interrupted` | Ctrl-C |

### How the CLI spots an agent

JSON mode turns on when stdout isn't a terminal, or when one of these is set (agents often give
the CLI a pseudo-terminal, so the terminal check alone isn't enough):

| Variable | Set by |
|---|---|
| `AI_AGENT` | The cross-tool convention: Claude Code sets it, Vercel's `@vercel/detect-agent` reads it |
| `AGENT` | Amp (`AGENT=amp`), Goose (`AGENT=goose`); also `1` or `true`. Any other value is ignored (`AGENT` is a common name) |
| `CLAUDECODE`, `CLAUDE_CODE_ENTRYPOINT` | Claude Code, in every shell it runs |
| `CURSOR_AGENT`, `CURSOR_EXTENSION_HOST_ROLE=agent-exec` | Cursor's agent (CLI and editor) |
| `CODEX_SANDBOX`, `CODEX_SANDBOX_NETWORK_DISABLED`, `CODEX_CI`, `CODEX_THREAD_ID` | OpenAI Codex CLI |
| `GEMINI_CLI` | Gemini CLI's shell tool |
| `OPENCODE=1`, `OPENCODE_CLIENT` | opencode |
| `AMP_CURRENT_THREAD_ID` | Amp |
| `AUGMENT_AGENT`, `ANTIGRAVITY_AGENT` | Augment, Antigravity |
| `CI` | GitHub Actions, GitLab CI, CircleCI, Buildkite and most others (`CI=false` doesn't count) |
| `LYRELOG_AGENT` | You: force agent mode anywhere |

Left out on purpose: `CURSOR_TRACE_ID` (set in every Cursor terminal, a person's too),
`REPL_ID` (every Replit shell) and `AIDER_*` (aider's own settings, which people export in their
shell profile; aider runs commands without a terminal, so the pipe check catches it anyway).

## Settings

`~/.config/lyrelog/config.json` (`$XDG_CONFIG_HOME/lyrelog`, `%AppData%\lyrelog` on Windows,
or `LYRELOG_CONFIG_DIR`), mode 0600: the server, and for each server the session, who it
belongs to and the default project. The token is never printed.

| Variable | What |
|---|---|
| `LYRELOG_TOKEN` | An access token from the account page (`lyr_tok_...`), for CI; wins over the saved sign-in |
| `COLUMNS` | The width tables fit to. With `--human` into a pipe or a file, 120 when unset |
| `LYRELOG_URL` | The dashboard's address (`--url` wins over it). Default `https://app.lyrelog.com`; the local stack is `http://127.0.0.1:8090` |
| `LYRELOG_THEME` | `dark` or `light`, when the terminal's background is guessed wrong |
| `NO_COLOR` | No colours |

Shell completion: `lyrelog completion zsh|bash|fish|powershell --help`.

## License

MIT
