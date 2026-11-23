package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"go.lyrelog.com/cli/internal/api"
	"go.lyrelog.com/cli/internal/ui"
)

// followInterval: how often --follow asks, as the dashboard's live mode (4 s).
var followInterval = 4 * time.Second

// followWindow: each --follow poll asks for what arrived since the last one (received_after)
// among events whose own time is in the last 2 days, so a batch a phone sends late, with the
// times it had, still shows. followAhead covers clocks that run ahead (the server keeps up to
// 24 hours ahead as sent).
const (
	followWindow = 48 * time.Hour
	followAhead  = 25 * time.Hour
)

// followOverlap: each poll asks from a little before the newest arrival seen, since a big batch
// received earlier can become visible after a small one received later; event ids dedupe.
const followOverlap = 30 * time.Second

func (a *App) eventsCommand() *cobra.Command {
	var (
		f      filters
		limit  int
		cursor string
		oldest bool
		follow bool
		view   string
	)
	cmd := &cobra.Command{
		Use:     "events",
		Aliases: []string{"logs", "log"},
		Short:   "Search events, as the explorer does; -f follows new ones",
		Long: `Search a project's events with the explorer's filters, newest first.

Filters take what the dashboard's URL takes: "a|b" for any of several, "!a" for not.
The environment is the project's default (as in the dashboard) unless --env says otherwise;
--env all searches every one.

-f (--follow) prints the latest events of the last hour, then new ones in the order they
arrive, every 4 seconds: an event sent late (a phone that was offline) shows when it arrives,
if its own time is within the last 2 days.`,
		Example: `  lyrelog events
  lyrelog events --level error --since 1h
  lyrelog events --level warning+ --version "2.4.0|2.4.1"
  lyrelog events -q "payment failed" --category checkout
  lyrelog events --user u_8f2a --since 7d --limit 500
  lyrelog events --attr board.size:9 --attr result!:win
  lyrelog events --verified '!none'           # verified senders only
  lyrelog events -f --level error            # follow new errors
  lyrelog events --view "Failed checkouts"   # a search saved in the dashboard
  lyrelog events --json | jq -r '.[].message'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := intFlag(cmd, "limit", limit, 1, 100000); err != nil {
				return err
			}
			ctx := cmd.Context()
			sp := a.spin("Loading events")
			p, err := a.project(ctx, a.projectRef(nil))
			if err == nil && view != "" {
				err = a.applyView(ctx, p, view, &f)
			}
			sp.Stop()
			if err != nil {
				return err
			}
			if follow {
				if !cmd.Flags().Changed("limit") {
					limit = 20
				}
				return a.follow(ctx, p, &f, limit)
			}
			q, err := f.values(p, a.Now())
			if err != nil {
				return err
			}
			if oldest {
				q.Set("order", "asc")
			}
			sp = a.spin("Loading events")
			events, next, err := a.fetchEvents(ctx, p, q, cursor, limit)
			sp.Stop()
			if err != nil {
				return err
			}
			if a.mode.JSON {
				if events == nil {
					events = []json.RawMessage{}
				}
				if err := a.printJSON(events); err != nil {
					return err
				}
			} else {
				a.renderEvents(p, &f, events)
			}
			if next != "" {
				a.more(fmt.Sprintf("Showing %s. For more:", ui.Plural(int64(len(events)), "event", "events")), "--cursor "+next)
			}
			return nil
		},
	}
	f.register(cmd, "24h", "all")
	fl := cmd.Flags()
	fl.IntVarP(&limit, "limit", "n", 100, "how many events at most (pages of up to 1,000 are fetched as needed)")
	fl.StringVar(&cursor, "cursor", "", "carry on from where a previous list stopped")
	fl.BoolVar(&oldest, "oldest-first", false, "oldest first (from the start of the window)")
	fl.StringVar(&view, "view", "", "a search saved in the explorer, by its name or id (lyrelog insights views); flags given win")
	fl.BoolVarP(&follow, "follow", "f", false, "keep going: print new events as they arrive, late ones too (NDJSON in JSON mode); Ctrl-C stops")
	cmd.AddCommand(a.eventGetCommand(), a.eventSummaryCommand(), a.eventValuesCommand(), a.eventAttributesCommand())
	return cmd
}

// fetchEvents pages through /events until limit; next is the cursor when there are more.
func (a *App) fetchEvents(ctx context.Context, p api.Project, q url.Values, cursor string, limit int) ([]json.RawMessage, string, error) {
	var out []json.RawMessage
	for len(out) < limit {
		page := url.Values{}
		for k, v := range q {
			page[k] = v
		}
		page.Set("limit", strconv.Itoa(min(1000, limit-len(out))))
		if cursor != "" {
			page.Set("cursor", cursor)
		}
		var body api.EventPage
		if err := a.client.Get(ctx, projectPath(p, "events"), page, &body); err != nil {
			return out, "", err
		}
		out = append(out, body.Events...)
		if body.NextCursor == nil || *body.NextCursor == "" {
			return out, "", nil
		}
		cursor = *body.NextCursor
	}
	return out, cursor, nil
}

func (a *App) renderEvents(p api.Project, f *filters, raws []json.RawMessage) {
	th, now := a.theme, a.Now()
	a.println(a.heading(p, f))
	if len(raws) == 0 {
		a.println("No events match.")
		if f.since == "" && f.from == "" {
			a.tip("Look further back with", "--since 7d")
		}
		if p.DefaultEnvironment != "" && f.env == "" {
			a.tip("Or in every environment with", "--env all")
		}
		return
	}
	a.println()
	a.eventLines(raws, th, now)
}

// eventLines prints a batch of events with their columns lined up.
func (a *App) eventLines(raws []json.RawMessage, th ui.Theme, now time.Time) {
	events := make([]api.Event, len(raws))
	for i, raw := range raws {
		_ = json.Unmarshal(raw, &events[i])
	}
	cols := ui.ColumnsFor(events)
	for _, e := range events {
		fmt.Fprintln(a.out, ui.EventLineIn(th, e, now, a.Width, cols))
	}
}

// follow prints the latest events, then new ones every few seconds, in the order they arrive,
// until Ctrl-C.
func (a *App) follow(ctx context.Context, p api.Project, f *filters, backlog int) error {
	if f.since != "" || f.from != "" || f.to != "" {
		return usagef("--follow takes no --since, --from or --to").hint("--follow starts from now; drop --since, or run without -f to list the past.")
	}
	th := a.theme
	// seen: event id → when it arrived, for the ids a poll's overlap can return again.
	seen := map[string]int64{}
	// newest: the latest arrival (received_at) seen, where the next poll starts.
	var newest int64
	// Lines line up as the list's do: the backlog sets the widths, and a longer message or
	// area later is cut at the list's caps.
	var cols ui.Columns
	emit := func(raw json.RawMessage, e api.Event) {
		seen[e.EventID] = e.ReceivedAt
		newest = max(newest, e.ReceivedAt)
		if a.mode.JSON {
			a.Stdout.Write(append(compact(raw), '\n'))
			return
		}
		fmt.Fprintln(a.out, ui.EventLineIn(th, e, a.Now(), a.Width, cols.Widen(e)))
	}
	f.since = "1h"
	q, err := f.values(p, a.Now())
	f.since = ""
	if err != nil {
		return err
	}
	// The latest few, oldest first, then new ones.
	q.Set("limit", strconv.Itoa(min(backlog, 1000)))
	var first api.EventPage
	if err := a.client.Get(ctx, projectPath(p, "events"), q, &first); err != nil {
		return err
	}
	if !a.mode.JSON {
		parts := strings.Split(ansiStrip(a.heading(p, f)), " · ")
		fmt.Fprintln(a.out, th.Dim(strings.Join(parts[:len(parts)-1], " · ")+" · following, Ctrl-C to stop"))
		fmt.Fprintln(a.out)
	}
	latest := make([]api.Event, len(first.Events))
	for i, raw := range first.Events {
		_ = json.Unmarshal(raw, &latest[i])
	}
	cols = ui.ColumnsFor(latest)
	for i := len(first.Events) - 1; i >= 0; i-- {
		emit(first.Events[i], latest[i])
	}
	if newest == 0 {
		newest = a.Now().UnixMilli()
	}
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(followInterval):
		}
		now := a.Now()
		after := newest - followOverlap.Milliseconds()
		q.Set("received_after", strconv.FormatInt(after, 10))
		q.Set("from", strconv.FormatInt(now.Add(-followWindow).UnixMilli(), 10))
		q.Set("to", strconv.FormatInt(now.Add(followAhead).UnixMilli(), 10))
		q.Set("order", "asc")
		q.Set("limit", "1000")
		fresh, _, err := a.fetchEvents(ctx, p, q, "", 5000)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ae *api.Error
			if errors.As(err, &ae) && (ae.Status == 401 || ae.Status == 403 || ae.Status == 404) {
				return err
			}
			failures++
			if failures == 1 || failures%15 == 0 {
				ce := a.classify(err)
				if a.mode.JSON {
					data, _ := json.Marshal(map[string]any{"warning": ce.msg, "hint": "still trying"})
					fmt.Fprintln(a.Stderr, string(data))
				} else {
					fmt.Fprintf(a.errOut, "%s %s %s\n", th.Warning("!"), ce.msg, th.Dim("(still trying)"))
				}
			}
			continue
		}
		failures = 0
		type item struct {
			raw json.RawMessage
			e   api.Event
		}
		var items []item
		for _, raw := range fresh {
			var e api.Event
			if json.Unmarshal(raw, &e) == nil {
				if _, ok := seen[e.EventID]; !ok {
					items = append(items, item{raw, e})
				}
			}
		}
		// In the order they arrived (the server sorts by the events' own time).
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].e.ReceivedAt != items[j].e.ReceivedAt {
				return items[i].e.ReceivedAt < items[j].e.ReceivedAt
			}
			return items[i].e.TS < items[j].e.TS
		})
		for _, it := range items {
			emit(it.raw, it.e)
		}
		// Forget what the next poll can't return again.
		for id, received := range seen {
			if received <= after {
				delete(seen, id)
			}
		}
	}
}

func compact(raw json.RawMessage) []byte {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return raw
	}
	return b.Bytes()
}

func (a *App) eventGetCommand() *cobra.Command {
	var ts string
	cmd := &cobra.Command{
		Use:   "get <event-id>",
		Short: "Show one event with its attributes and payload",
		Long: `Show one event in full. The CLI finds it by its id among everything the project keeps
(its whole retention). --ts (Unix ms, as the JSON's "ts") goes straight to it, skipping that lookup.`,
		Example: `  lyrelog events get 0b7e3d52-8c1a-4f0e-9d3b-5a2f8e1c7d40
  lyrelog events get 0b7e3d52-8c1a-4f0e-9d3b-5a2f8e1c7d40 --ts 1763550000000 --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			id := strings.ToLower(strings.TrimSpace(args[0]))
			if !uuidPattern.MatchString(id) {
				return usagef("%q isn't an event id", args[0]).hint("An event id is a UUID, like 0b7e3d52-8c1a-4f0e-9d3b-5a2f8e1c7d40 (event_id in the JSON).")
			}
			sp := a.spin("Finding the event")
			defer sp.Stop()
			p, err := a.project(ctx, a.projectRef(nil))
			if err != nil {
				return err
			}
			if ts == "" {
				var found struct {
					Event *api.Event `json:"event"`
				}
				if err := a.client.Get(ctx, projectPath(p, "lookup"), url.Values{"q": {id}}, &found); err != nil {
					return err
				}
				if found.Event == nil {
					return failf(ExitNotFound, "No event %s in %s, which keeps events for %d days", id, p.Name, p.RetentionDays).
						hint("Check the id and the project (-p): an event older than the project keeps is gone.")
				}
				ts = strconv.FormatInt(found.Event.TS, 10)
			}
			var body map[string]json.RawMessage
			if err := a.client.Get(ctx, projectPath(p, "events/"+id), url.Values{"ts": {ts}}, &body); err != nil {
				return err
			}
			sp.Stop()
			if a.mode.JSON {
				return a.printRaw(body["event"])
			}
			var e api.Event
			_ = json.Unmarshal(body["event"], &e)
			one, _ := personWords(p)
			ui.EventDetail(a.out, a.theme, e, a.Now(), one)
			return nil
		},
	}
	cmd.Flags().StringVar(&ts, "ts", "", "the event's time in Unix milliseconds (skips the lookup)")
	return cmd
}

func (a *App) eventSummaryCommand() *cobra.Command {
	var f filters
	cmd := &cobra.Command{
		Use:   "summary",
		Short: "Totals and the top values of each field, for the same filters",
		Long:  "Totals (events, people, sessions, errors) and the most frequent values of each field, as the explorer's side panel shows them.",
		Example: `  lyrelog events summary --since 7d
  lyrelog events summary --level error --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			sp := a.spin("Counting")
			p, err := a.project(ctx, a.projectRef(nil))
			var raw map[string]json.RawMessage
			if err == nil {
				var q url.Values
				if q, err = f.values(p, a.Now()); err == nil {
					raw, err = a.getMap(ctx, projectPath(p, "summary"), q)
				}
			}
			sp.Stop()
			if err != nil {
				return err
			}
			if a.mode.JSON {
				return a.printJSON(raw)
			}
			var s struct {
				Totals struct{ Events, Users, Sessions, Errors int64 } `json:"totals"`
				Facets map[string][]struct {
					Value string `json:"value"`
					Count int64  `json:"count"`
				} `json:"facets"`
			}
			_ = remarshal(raw, &s)
			th := a.theme
			_, many := personWords(p)
			a.println(a.heading(p, &f))
			a.println()
			fmt.Fprintf(a.out, "  %s events  %s %s  %s sessions  %s errors\n\n", th.Bold(ui.Number(s.Totals.Events)),
				th.Bold(ui.Number(s.Totals.Users)), many, th.Bold(ui.Number(s.Totals.Sessions)), th.Error(ui.Number(s.Totals.Errors)))
			fields := make([]string, 0, len(s.Facets))
			for k := range s.Facets {
				fields = append(fields, k)
			}
			sort.Slice(fields, func(i, j int) bool { return facetLabel(fields[i]) < facetLabel(fields[j]) })
			for _, field := range fields {
				values := s.Facets[field]
				if len(values) == 0 {
					continue
				}
				var parts []string
				for _, v := range values {
					name := v.Value
					if name == "" {
						name = "(none)"
					}
					if field == "level" {
						name = th.Level(v.Value, name)
					}
					parts = append(parts, name+" "+th.Dim(ui.Compact(v.Count)))
				}
				line := "  " + th.Dim(fmt.Sprintf("%-12s", facetLabel(field))) + " " + strings.Join(parts, "  ")
				fmt.Fprintln(a.out, ui.Truncate(line, a.Width))
			}
			return nil
		},
	}
	f.register(cmd, "24h", "all")
	return cmd
}

// facetLabel names a summary field by the flag that filters on it (app_version is --version).
func facetLabel(field string) string {
	switch field {
	case "app_version":
		return "version"
	case "device_model":
		return "device"
	case "os_version":
		return "os"
	}
	return field
}

func (a *App) eventValuesCommand() *cobra.Command {
	var f filters
	var search string
	cmd := &cobra.Command{
		Use:   "values <field | attr.path>",
		Short: "The most frequent values of a field or attribute",
		Long: `The most frequent values of one field (level, category, message, version, device, country,
environment, platform, verified...) or attribute (board.size), within the filters: what the
explorer's filter menu offers.`,
		Example: `  lyrelog events values message --level error
  lyrelog events values version --since 30d
  lyrelog events values attr.board.size`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			sp := a.spin("Counting values")
			p, err := a.project(ctx, a.projectRef(nil))
			var raw map[string]json.RawMessage
			if err == nil {
				var q url.Values
				if q, err = f.values(p, a.Now()); err == nil {
					if path, ok := strings.CutPrefix(args[0], "attr."); ok {
						q.Set("attr_path", path)
					} else if strings.HasPrefix(args[0], "attrs.") {
						q.Set("attr_path", strings.TrimPrefix(args[0], "attrs."))
					} else {
						q.Set("field", args[0])
					}
					if search != "" {
						q.Set("value_q", search)
					}
					raw, err = a.getMap(ctx, projectPath(p, "values"), q)
				}
			}
			sp.Stop()
			if err != nil {
				return err
			}
			if a.mode.JSON {
				return a.printRaw(rawList(raw, "values"))
			}
			values := decodeInto[struct {
				Value   string `json:"value"`
				Count   int64  `json:"count"`
				Players int64  `json:"players"`
			}](raw["values"])
			_, many := personWords(p)
			t := ui.Table{Columns: []ui.Column{{Header: valuesHeader(args[0]), Flex: true}, {Header: "Events", Right: true}, {Header: ui.Capitalize(many), Right: true}}}
			for _, v := range values {
				name := v.Value
				if name == "" {
					name = a.theme.Dim("(none)")
				}
				t.Add(name, ui.Number(v.Count), ui.Number(v.Players))
			}
			a.println(a.heading(p, &f))
			a.println()
			t.Render(a.out, a.theme, a.Width)
			return nil
		},
	}
	f.register(cmd, "24h", "all")
	cmd.Flags().StringVar(&search, "search", "", "only values containing this")
	return cmd
}

// valuesHeader names the values column: a field as a word ("Version"), an attribute by its
// path as typed, without the attr. prefix ("board.size").
func valuesHeader(arg string) string {
	for _, prefix := range []string{"attr.", "attrs."} {
		if path, ok := strings.CutPrefix(arg, prefix); ok {
			return path
		}
	}
	return ui.Capitalize(arg)
}

func (a *App) eventAttributesCommand() *cobra.Command {
	var f filters
	var names string
	var numeric bool
	cmd := &cobra.Command{
		Use:     "attributes",
		Aliases: []string{"attrs"},
		Short:   "The attribute paths events carry, most common first",
		Example: `  lyrelog events attributes
  lyrelog events attributes --of "game.finished|game.started" --numeric`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			sp := a.spin("Reading attributes")
			p, err := a.project(ctx, a.projectRef(nil))
			var raw map[string]json.RawMessage
			if err == nil {
				var q url.Values
				if q, err = f.values(p, a.Now()); err == nil {
					if names != "" {
						q.Set("events", names)
					}
					if numeric {
						q.Set("numeric", "1")
					}
					raw, err = a.getMap(ctx, projectPath(p, "attributes"), q)
				}
			}
			sp.Stop()
			if err != nil {
				return err
			}
			if a.mode.JSON {
				return a.printRaw(rawList(raw, "attributes"))
			}
			attrs := decodeInto[struct {
				Path  string `json:"path"`
				Count int64  `json:"count"`
			}](raw["attributes"])
			t := ui.Table{Columns: []ui.Column{{Header: "Attribute", Flex: true}, {Header: "Events", Right: true}}}
			for _, at := range attrs {
				t.Add(at.Path, ui.Number(at.Count))
			}
			t.Render(a.out, a.theme, a.Width)
			return nil
		},
	}
	f.register(cmd, "24h", "all")
	cmd.Flags().StringVar(&names, "of", "", `only in these events (messages), "a|b"`)
	cmd.Flags().BoolVar(&numeric, "numeric", false, "only attributes with numbers (for insights' values)")
	return cmd
}

// applyView finds a saved explorer search by id or name (case aside) and fills f from it.
func (a *App) applyView(ctx context.Context, p api.Project, ref string, f *filters) error {
	raw, err := a.getMap(ctx, projectPath(p, "views"), nil)
	if err != nil {
		return err
	}
	var found *api.SavedView
	for _, v := range decodeInto[api.SavedView](raw["views"]) {
		if v.Kind == "events" && (strconv.FormatInt(v.ID, 10) == ref || strings.EqualFold(v.Name, ref)) {
			found = &v
			break
		}
	}
	if found == nil {
		return failf(ExitNotFound, "No saved search %q in %s", ref, p.Name).hint("lyrelog insights views lists them; only explorer searches (kind events) apply here.")
	}
	return f.fromView(found.Config)
}
