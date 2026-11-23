package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"go.lyrelog.com/cli/internal/api"
)

// filters are the explorer's filters (../lyrelog/web/src/lib/search.ts apiParams), shared by
// every command that reads events. Values pass through as the dashboard's URL has them: "|"
// for any of several, a leading "!" for not.
type filters struct {
	defaultSince string
	since        string
	from, to     string
	query        string
	level        string
	user         string
	session      string
	install      string
	category     string
	message      string
	version      string
	os           string
	device       string
	locale       string
	country      string
	env          string
	platform     string
	verified     string
	attrs        []string
}

// register adds the flags; which: "all", or "window" for the time flags and the environment only.
func (f *filters) register(cmd *cobra.Command, defaultSince string, which string) {
	f.defaultSince = defaultSince
	fl := cmd.Flags()
	fl.StringVar(&f.since, "since", "", "time window ending now: 15m, 1h, 6h, 24h, 7d, 30d (default "+defaultSince+")")
	fl.StringVar(&f.from, "from", "", "start: RFC 3339 (2026-11-19T10:00:00Z), a date, Unix ms, or relative (2h = 2 hours ago)")
	fl.StringVar(&f.to, "to", "", "end, in the same forms as --from (default now)")
	fl.StringVar(&f.env, "env", "", "environment (default the project's default; \"all\" for every one)")
	fl.StringVar(&f.platform, "platform", "", "platform: ios, android, web, python... (\"a|b\" any of, \"!a\" not)")
	if which == "window" {
		return
	}
	fl.StringVarP(&f.query, "query", "q", "", "search the message, area and attributes")
	fl.StringVar(&f.level, "level", "", "levels: error, or error,critical, or warning+ (warning and worse)")
	fl.StringVar(&f.user, "user", "", "one person's id (the user_id the app sends)")
	fl.StringVar(&f.session, "session", "", "one session's id")
	fl.StringVar(&f.install, "install", "", "one install's id")
	fl.StringVar(&f.category, "category", "", "area (category): checkout, or \"a|b\", or \"!a\"")
	fl.StringVar(&f.message, "message", "", "event name (message), exactly")
	fl.StringVar(&f.version, "version", "", "app version: 2.4.0, or \"2.4.0|2.4.1\"")
	fl.StringVar(&f.os, "os", "", "OS version")
	fl.StringVar(&f.device, "device", "", "device model")
	fl.StringVar(&f.locale, "locale", "", "locale: es_ES")
	fl.StringVar(&f.country, "country", "", "country code: ES, or \"ES|PT\"")
	fl.StringVar(&f.verified, "verified", "", "how the sender was proven: server, app_attest, key_attestation, none (\"!none\": verified only)")
	fl.StringArrayVar(&f.attrs, "attr", nil, "attribute filter path:value (path!:value for not); repeat for more")
}

var levelOrder = []string{"trace", "debug", "info", "notice", "warning", "error", "critical"}

// parseLevels reads "error", "error,critical" or "warning+".
func parseLevels(text string) (string, error) {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return "", nil
	}
	if base, ok := strings.CutSuffix(text, "+"); ok {
		for i, l := range levelOrder {
			if l == base {
				return strings.Join(levelOrder[i:], ","), nil
			}
		}
		return "", usagef("Unknown level %q", base).hint("Levels: " + strings.Join(levelOrder, ", ") + ".")
	}
	parts := strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == '|' || r == ' ' })
	for _, p := range parts {
		known := false
		for _, l := range levelOrder {
			known = known || l == p
		}
		if !known {
			return "", usagef("Unknown level %q", p).hint("Levels: " + strings.Join(levelOrder, ", ") + ", or warning+ for warning and worse.")
		}
	}
	return strings.Join(parts, ","), nil
}

var spanPattern = regexp.MustCompile(`^(\d+(?:\.\d+)?)(ms|s|m|h|d|w)$`)

// parseSpan reads 90s, 15m, 1h, 7d, 2w.
func parseSpan(text string) (time.Duration, bool) {
	m := spanPattern.FindStringSubmatch(strings.ToLower(strings.TrimSpace(text)))
	if m == nil {
		return 0, false
	}
	n, _ := strconv.ParseFloat(m[1], 64)
	unit := map[string]time.Duration{"ms": time.Millisecond, "s": time.Second, "m": time.Minute, "h": time.Hour, "d": 24 * time.Hour, "w": 7 * 24 * time.Hour}[m[2]]
	return time.Duration(n * float64(unit)), n > 0
}

// parseTime reads RFC 3339, a date (local midnight), Unix milliseconds or seconds, "now", or a
// span meaning that long ago.
func parseTime(text string, now time.Time) (time.Time, error) {
	text = strings.TrimSpace(text)
	if text == "now" {
		return now, nil
	}
	if d, ok := parseSpan(strings.TrimSuffix(strings.TrimSpace(strings.TrimSuffix(text, "ago")), " ")); ok {
		return now.Add(-d), nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, text, time.Local); err == nil {
			return t, nil
		}
	}
	if t, err := time.ParseInLocation("2006-01-02", text, time.Local); err == nil {
		return t, nil
	}
	if n, err := strconv.ParseInt(text, 10, 64); err == nil {
		if n < 1e11 { // seconds
			return time.Unix(n, 0), nil
		}
		return time.UnixMilli(n), nil
	}
	return time.Time{}, usagef("%q isn't a time", text).hint("Use RFC 3339 (2026-11-19T10:00:00Z), a date (2026-11-19), Unix milliseconds, or how long ago (2h, 7d).")
}

// window is the time range the flags mean, as the dashboard does it: a span ending a minute
// from now (late clocks), or from/to.
func (f *filters) window(now time.Time) (time.Time, time.Time, error) {
	if f.since != "" && (f.from != "" || f.to != "") {
		return time.Time{}, time.Time{}, usagef("--since and --from/--to can't go together").hint("--since 1h means the last hour; --from and --to pick any window.")
	}
	if f.from != "" || f.to != "" {
		from, to := now.Add(-24*time.Hour), now.Add(time.Minute)
		var err error
		if f.to != "" {
			if to, err = parseTime(f.to, now); err != nil {
				return from, to, err
			}
		}
		if f.from != "" {
			if from, err = parseTime(f.from, now); err != nil {
				return from, to, err
			}
		} else {
			from = to.Add(-24 * time.Hour)
		}
		if !from.Before(to) {
			return from, to, usagef("--from must be before --to")
		}
		return from, to, nil
	}
	since := f.since
	if since == "" {
		since = f.defaultSince
	}
	d, ok := parseSpan(since)
	if !ok {
		return time.Time{}, time.Time{}, usagef("%q isn't a time window", since).hint("Use 15m, 1h, 6h, 24h, 7d or 30d (any number of s, m, h, d or w).")
	}
	return now.Add(-d), now.Add(time.Minute), nil
}

// describe says the window in words: "last 24 hours", "Nov 18 10:00 to Nov 19 10:00".
func (f *filters) describe(now time.Time) string {
	if f.from != "" || f.to != "" {
		from, to, err := f.window(now)
		if err != nil {
			return ""
		}
		return from.Local().Format("Jan 2 15:04") + " to " + to.Local().Format("Jan 2 15:04")
	}
	since := f.since
	if since == "" {
		since = f.defaultSince
	}
	words := map[string]string{"15m": "last 15 minutes", "1h": "last hour", "6h": "last 6 hours", "24h": "last 24 hours", "1d": "last 24 hours", "7d": "last 7 days", "30d": "last 30 days"}
	if w, ok := words[since]; ok {
		return w
	}
	return "last " + since
}

// environment is the env= value: the flag, else the project's default; "all" for none.
func (f *filters) environment(p api.Project) string {
	switch f.env {
	case "all", "*":
		return ""
	case "":
		return p.DefaultEnvironment
	}
	return f.env
}

// values builds the query string.
func (f *filters) values(p api.Project, now time.Time) (url.Values, error) {
	from, to, err := f.window(now)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("from", strconv.FormatInt(from.UnixMilli(), 10))
	q.Set("to", strconv.FormatInt(to.UnixMilli(), 10))
	levels, err := parseLevels(f.level)
	if err != nil {
		return nil, err
	}
	for _, kv := range [][2]string{
		{"level", levels}, {"category", f.category}, {"message", f.message}, {"q", strings.TrimSpace(f.query)},
		{"user", f.user}, {"session", f.session}, {"install", f.install}, {"version", f.version}, {"os", f.os},
		{"device", f.device}, {"locale", f.locale}, {"country", strings.ToUpper(f.country)}, {"platform", f.platform},
		{"verified", f.verified}, {"env", f.environment(p)},
	} {
		if kv[1] != "" {
			q.Set(kv[0], kv[1])
		}
	}
	if f.verified != "" {
		for _, v := range strings.Split(strings.TrimPrefix(f.verified, "!"), "|") {
			if v != "server" && v != "app_attest" && v != "key_attestation" && v != "none" {
				return nil, usagef("--verified %q: use server, app_attest, key_attestation or none", v).hint(`Combine with "|" (server|app_attest) and negate with "!" (!none: verified only).`)
			}
		}
	}
	for _, attr := range f.attrs {
		path, _, ok := strings.Cut(attr, ":")
		if !ok || strings.TrimSuffix(path, "!") == "" {
			return nil, usagef("--attr %q should look like path:value", attr).hint("For example --attr board.size:9, or --attr result!:ok for not ok.")
		}
		q.Add("attr", attr)
	}
	if len(f.attrs) > 8 {
		return nil, usagef("At most 8 --attr filters")
	}
	return q, nil
}

// heading is the dim line above a list: "tilefall · production · last 24 hours".
func (a *App) heading(p api.Project, f *filters) string {
	parts := []string{p.Name}
	if env := f.environment(p); env != "" {
		parts = append(parts, env)
	} else {
		parts = append(parts, "all environments")
	}
	if f.platform != "" {
		parts = append(parts, f.platform)
	}
	parts = append(parts, f.describe(a.Now()))
	return a.theme.Dim(strings.Join(parts, " · "))
}

func intFlag(cmd *cobra.Command, name string, v, low, high int) error {
	if v < low || v > high {
		return usagef("--%s must be between %d and %d", name, low, high)
	}
	return nil
}

var _ = fmt.Sprint

// fromView fills, from a saved explorer search (lyrelog insights views, kind events), every filter
// the command line didn't set: flags win. The search keeps the dashboard's URL state plus the
// environment and platform of its top bar (env, platform_shown).
func (f *filters) fromView(config json.RawMessage) error {
	var v map[string]any
	if err := json.Unmarshal(config, &v); err != nil {
		return failf(ExitError, "That saved search can't be read")
	}
	text := func(key string) string {
		switch x := v[key].(type) {
		case string:
			return x
		case float64:
			return strconv.FormatInt(int64(x), 10)
		}
		return ""
	}
	fill := func(target *string, value string) {
		if *target == "" && value != "" {
			*target = value
		}
	}
	if f.since == "" && f.from == "" && f.to == "" {
		if r := text("range"); r != "" {
			f.since = r
		} else {
			f.from, f.to = text("from"), text("to")
		}
	}
	fill(&f.level, text("level"))
	fill(&f.category, text("category"))
	fill(&f.message, text("message"))
	fill(&f.query, text("q"))
	fill(&f.user, text("user"))
	fill(&f.session, text("session"))
	fill(&f.version, text("version"))
	fill(&f.os, text("os"))
	fill(&f.device, text("device"))
	fill(&f.locale, text("locale"))
	fill(&f.country, text("country"))
	fill(&f.verified, text("verified"))
	fill(&f.platform, text("platform"))
	fill(&f.platform, text("platform_shown"))
	if _, ok := v["env"]; ok && f.env == "" {
		f.env = text("env")
		if f.env == "" {
			f.env = "all" // saved with every environment shown
		}
	}
	if len(f.attrs) == 0 {
		if list, ok := v["attr"].([]any); ok {
			for _, a := range list {
				if s, ok := a.(string); ok {
					f.attrs = append(f.attrs, s)
				}
			}
		}
	}
	return nil
}
