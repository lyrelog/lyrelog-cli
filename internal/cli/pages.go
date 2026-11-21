package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"go.lyrelog.com/cli/internal/api"
	"go.lyrelog.com/cli/internal/ui"
)

// pageQuery resolves the project and builds the filters' query string.
func (a *App) pageQuery(ctx context.Context, f *filters) (api.Project, url.Values, error) {
	p, err := a.project(ctx, a.projectRef(nil))
	if err != nil {
		return p, nil, err
	}
	q, err := f.values(p, a.Now())
	return p, q, err
}

func (a *App) errorsCommand() *cobra.Command {
	var f filters
	var warnings bool
	cmd := &cobra.Command{
		Use:     "errors",
		Aliases: []string{"issues"},
		Short:   "Problems grouped by area and event, worst first",
		Long:    "What the Errors page shows: error and critical events grouped by area and event name, with how many people they reached and how they trend.",
		Example: `  lyrelog errors
  lyrelog errors --since 24h --warnings
  lyrelog errors --version 2.4.1 --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if warnings && f.level == "" {
				f.level = "warning+"
			}
			ctx := cmd.Context()
			sp := a.spin("Grouping errors")
			p, q, err := a.pageQuery(ctx, &f)
			var raw map[string]json.RawMessage
			if err == nil {
				raw, err = a.getMap(ctx, projectPath(p, "issues"), q)
			}
			sp.Stop()
			if err != nil {
				return err
			}
			if a.mode.JSON {
				return a.printRaw(rawList(raw, "issues"))
			}
			issues := decodeInto[api.Issue](raw["issues"])
			a.println(a.heading(p, &f))
			a.println()
			if len(issues) == 0 {
				a.println(a.theme.Mark("ok") + " No errors. " + a.theme.Dim("Nothing at error or critical in this window."))
				return nil
			}
			a.renderIssues(p, issues)
			return nil
		},
	}
	f.register(cmd, "7d", "all")
	cmd.Flags().BoolVar(&warnings, "warnings", false, "count warnings too")
	return cmd
}

func (a *App) renderIssues(p api.Project, issues []api.Issue) {
	th, now := a.theme, a.Now()
	_, many := personWords(p)
	t := ui.Table{Columns: []ui.Column{{Header: ""}, {Header: "Event", Flex: true, Min: 16}, {Header: "Area", Flex: true}, {Header: "Count", Right: true},
		{Header: "Change", Right: true, Drop: 2}, {Header: ui.Capitalize(many), Right: true}, {Header: "Last seen"}, {Header: "Versions", Flex: true, Drop: 1}, {Header: "Trend", Drop: 3}}}
	for _, it := range issues {
		change := th.Dim("new")
		if it.Previous > 0 {
			delta := float64(it.Count-it.Previous) / float64(it.Previous)
			switch {
			case delta > 0.05:
				change = th.Error("↑" + ui.Percent(delta))
			case delta < -0.05:
				change = th.Success("↓" + ui.Percent(-delta))
			default:
				change = th.Dim("same")
			}
		}
		versions := strings.Join(it.Versions, ", ")
		t.Add(th.LevelFill(it.Worst, "●"), th.Bold(it.Name), th.Dim(it.Area), ui.Number(it.Count), change,
			ui.Number(it.Players), ui.AgoMillis(it.LastSeen, now), th.Dim(versions), th.Level(it.Worst, ui.Sparkline(it.Trend)))
	}
	t.Render(a.out, th, a.Width)
}

func (a *App) sessionsCommand() *cobra.Command {
	var (
		f          filters
		withEvent  string
		errorsOnly bool
		minimum    string
		sort       string
		page       int
	)
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "Sessions: who, how long, what happened, errors",
		Example: `  lyrelog sessions
  lyrelog sessions --errors --since 24h
  lyrelog sessions --with-event purchase.failed --sort longest
  lyrelog sessions --user u_8f2a --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			sp := a.spin("Loading sessions")
			p, q, err := a.pageQuery(ctx, &f)
			if err == nil {
				err = a.sessionQuery(q, withEvent, errorsOnly, minimum, sort, page)
			}
			var raw map[string]json.RawMessage
			if err == nil {
				raw, err = a.getMap(ctx, projectPath(p, "sessions"), q)
			}
			sp.Stop()
			if err != nil {
				return err
			}
			list := rawList(raw, "sessions")
			if a.mode.JSON {
				if err := a.printRaw(list); err != nil {
					return err
				}
			} else {
				a.println(a.heading(p, &f))
				a.println()
				sessions := decodeInto[api.Session](list)
				if len(sessions) == 0 {
					a.println("No sessions match.")
					return nil
				}
				a.renderSessions(p, sessions, true)
			}
			var total int64
			_ = json.Unmarshal(raw["total"], &total)
			if shown := int64((page + 1) * 50); total > shown {
				a.more(fmt.Sprintf("%s of %s sessions. Next:", ui.Number(shown), ui.Number(total)), "--page "+strconv.Itoa(page+1))
			}
			return nil
		},
	}
	f.register(cmd, "7d", "all")
	fl := cmd.Flags()
	fl.StringVar(&withEvent, "with-event", "", "only sessions with this event (message) in them")
	fl.BoolVar(&errorsOnly, "errors", false, "only sessions with errors")
	fl.StringVar(&minimum, "min-duration", "", "only sessions at least this long: 30s, 5m")
	fl.StringVar(&sort, "sort", "recent", "recent, longest, errors or events")
	fl.IntVar(&page, "page", 0, "page (50 a page, from 0)")
	return cmd
}

func (a *App) sessionQuery(q url.Values, withEvent string, errorsOnly bool, minimum, sort string, page int) error {
	if withEvent != "" {
		q.Set("with_event", withEvent)
	}
	if errorsOnly {
		q.Set("errors", "1")
	}
	if minimum != "" {
		d, ok := parseSpan(minimum)
		if !ok {
			return usagef("--min-duration %q: use 30s, 5m, 1h", minimum)
		}
		q.Set("min_ms", strconv.FormatInt(d.Milliseconds(), 10))
	}
	switch sort {
	case "", "recent", "longest", "errors", "events":
		if sort != "" {
			q.Set("sort", sort)
		}
	default:
		return usagef("--sort is recent, longest, errors or events")
	}
	if page < 0 {
		return usagef("--page starts at 0")
	}
	q.Set("page", strconv.Itoa(page))
	return nil
}

func (a *App) renderSessions(p api.Project, sessions []api.Session, withWho bool) {
	th, now := a.theme, a.Now()
	one, _ := personWords(p)
	cols := []ui.Column{{Header: "Started"}, {Header: ui.Capitalize(one), Keep: true}, {Header: "Length", Right: true}, {Header: "Events", Right: true, Drop: 4},
		{Header: "Errors", Right: true}, {Header: "Device", Flex: true, Drop: 1}, {Header: "Version", Drop: 2}, {Header: "From → to", Flex: true, Min: 12, Drop: 3}, {Header: "Session", Keep: true}}
	if !withWho {
		cols = append(cols[:1], cols[2:]...)
	}
	t := ui.Table{Columns: cols}
	for _, s := range sessions {
		errs := th.Dim("0")
		if s.Errors > 0 {
			errs = th.Error(ui.Number(s.Errors))
		}
		path := s.FirstEvent
		if s.LastEvent != "" && s.LastEvent != s.FirstEvent {
			path += th.Dim(" → ") + s.LastEvent
		}
		if s.FirstError != "" {
			path += "  " + th.Error("✗ "+s.FirstError)
		}
		row := []string{ui.AgoMillis(s.Started, now), s.Who, ui.Duration(time.Duration(s.DurationMS) * time.Millisecond),
			ui.Number(s.Events), errs, s.Device, s.Version, path, th.Dim(s.ID)}
		if !withWho {
			row = append(row[:1], row[2:]...)
		}
		t.Add(row...)
	}
	t.Render(a.out, th, a.Width)
}

func (a *App) peopleCommand() *cobra.Command {
	var (
		f      filters
		search string
		sort   string
		page   int
	)
	cmd := &cobra.Command{
		Use:     "people",
		Aliases: []string{"users", "players"},
		Short:   "The project's people (in its own word): activity, errors, device",
		Long:    "Who used the app in the window, with their events, sessions, errors and latest device and version. The project names them in its own word (players, users, customers...).",
		Example: `  lyrelog people
  lyrelog people --sort errors --since 30d
  lyrelog people --search u_8f --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			switch sort {
			case "last_seen", "events", "errors", "sessions", "first_seen":
			default:
				return usagef("--sort is last_seen, events, errors, sessions or first_seen")
			}
			sp := a.spin("Loading")
			p, q, err := a.pageQuery(ctx, &f)
			var raw map[string]json.RawMessage
			if err == nil {
				q.Set("sort", sort)
				q.Set("page", strconv.Itoa(page))
				if search != "" {
					q.Set("player_q", search)
				}
				raw, err = a.getMap(ctx, projectPath(p, "players"), q)
			}
			sp.Stop()
			if err != nil {
				return err
			}
			one, many := personWords(p)
			list := rawList(raw, "players")
			if a.mode.JSON {
				if err := a.printRaw(list); err != nil {
					return err
				}
			} else {
				a.println(a.heading(p, &f))
				a.println()
				people := decodeInto[api.Person](list)
				if len(people) == 0 {
					a.println("No " + many + " in this window.")
					return nil
				}
				th, now := a.theme, a.Now()
				t := ui.Table{Columns: []ui.Column{{Header: ui.Capitalize(one), Keep: true}, {Header: "Events", Right: true}, {Header: "Sessions", Right: true},
					{Header: "Errors", Right: true}, {Header: "Last seen"}, {Header: "First seen", Drop: 1}, {Header: "Device", Flex: true, Drop: 3}, {Header: "Version"}, {Header: "OS", Drop: 2}, {Header: "Country", Drop: 4}}}
				for _, pr := range people {
					errs := th.Dim("0")
					if pr.Errors > 0 {
						errs = th.Error(ui.Number(pr.Errors))
					}
					t.Add(th.Bold(pr.Player), ui.Number(pr.Events), ui.Number(pr.Sessions), errs, ui.AgoMillis(pr.LastSeen, now),
						ui.AgoMillis(pr.FirstSeen, now), pr.Device, pr.Version, strings.TrimSpace(pr.OSName+" "+pr.OS), pr.Country)
				}
				t.Render(a.out, th, a.Width)
			}
			var total int64
			_ = json.Unmarshal(raw["total"], &total)
			if shown := int64((page + 1) * 50); total > shown {
				a.more(fmt.Sprintf("%s of %s %s. Next:", ui.Number(shown), ui.Number(total), many), "--page "+strconv.Itoa(page+1))
			}
			return nil
		},
	}
	f.register(cmd, "7d", "all")
	cmd.Flags().StringVar(&search, "search", "", "ids starting with this")
	cmd.Flags().StringVar(&sort, "sort", "last_seen", "last_seen, events, errors, sessions or first_seen")
	cmd.Flags().IntVar(&page, "page", 0, "page (50 a page, from 0)")
	return cmd
}

func (a *App) personCommand() *cobra.Command {
	var (
		f        filters
		export   string
		deleting bool
		yes      bool
		limit    int
	)
	cmd := &cobra.Command{
		Use:     "person <user-id>",
		Aliases: []string{"user", "player"},
		Short:   "One person: totals, recent sessions and events; export or delete their data",
		Long: `One person of the project (by the user_id the app sends): how many events they have, their
recent sessions and latest events. The person's page in the dashboard.

--export writes every event of theirs as JSON lines (an access request); --delete deletes them
all (an erasure request, owners only, can't be undone).`,
		Example: `  lyrelog person u_8f2a
  lyrelog person u_8f2a --since 30d --json
  lyrelog person u_8f2a --export u_8f2a.ndjson
  lyrelog person u_8f2a --export - | jq .message
  lyrelog person u_8f2a --delete`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			id := args[0]
			p, err := a.project(ctx, a.projectRef(nil))
			if err != nil {
				return err
			}
			one, _ := personWords(p)
			base := projectPath(p, "end-users/"+api.PathEscape(id))
			if export != "" {
				return a.exportPerson(ctx, base, export, one, id)
			}
			if deleting {
				return a.deletePerson(ctx, p, base, one, id, yes)
			}
			sp := a.spin("Loading " + one + " " + id)
			defer sp.Stop()
			var total api.EndUser
			rawTotal, err := a.getMap(ctx, base, nil)
			if err == nil {
				err = remarshal(rawTotal, &total)
			}
			if err != nil {
				return err
			}
			if total.Events == 0 {
				e := failf(ExitNotFound, "No %s %q in %s", one, id, p.Name)
				if a.mode.JSON {
					e = failf(ExitNotFound, "No such %s", one)
				}
				return e.hint("`lyrelog people` lists who's there; ids are matched exactly.")
			}
			q, err := f.values(p, a.Now())
			if err != nil {
				return err
			}
			q.Set("user", id)
			sessRaw, err := a.getMap(ctx, projectPath(p, "sessions"), q)
			if err != nil {
				return err
			}
			events, _, err := a.fetchEvents(ctx, p, q, "", limit)
			if err != nil {
				return err
			}
			sp.Stop()
			if a.mode.JSON {
				if events == nil {
					events = []json.RawMessage{}
				}
				return a.printJSON(map[string]any{"user_id": id, "events": total.Events, "first": total.First, "last": total.Last,
					"sessions": rawList(sessRaw, "sessions"), "recent_events": events})
			}
			th, now := a.theme, a.Now()
			fmt.Fprintf(a.out, "%s %s\n", th.Bold(ui.Capitalize(one)+" "+id), th.Dim("in "+p.Name))
			fmt.Fprintf(a.out, "  %s in all, first %s, last %s\n\n", ui.Plural(total.Events, "event", "events"),
				ui.AgoMillis(total.First, now), ui.AgoMillis(total.Last, now))
			sessions := decodeInto[api.Session](rawList(sessRaw, "sessions"))
			fmt.Fprintf(a.out, "%s %s\n", th.Bold("Sessions"), th.Dim(f.describe(now)))
			if len(sessions) == 0 {
				a.println(th.Dim("  none in this window"))
			} else {
				a.renderSessions(p, sessions[:min(len(sessions), 10)], false)
			}
			fmt.Fprintf(a.out, "\n%s\n", th.Bold("Latest events"))
			a.eventLines(events, th, now)
			a.println()
			a.tip("Everything of theirs:", fmt.Sprintf("lyrelog events --user %s --since %dd", id, keptDays(p)))
			return nil
		},
	}
	f.register(cmd, "7d", "window")
	cmd.Flags().StringVar(&export, "export", "", "write all their events as JSON lines to this file (- for stdout)")
	cmd.Flags().BoolVar(&deleting, "delete", false, "delete all their events (owners; can't be undone)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "with --delete: don't ask to confirm")
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "how many recent events to show")
	return cmd
}

// keptDays is how far back the project's events can go: its plan's longest retention.
func keptDays(p api.Project) int {
	if p.MaxRetentionDays > 0 {
		return p.MaxRetentionDays
	}
	if p.RetentionDays > 0 {
		return p.RetentionDays
	}
	return 30
}

func (a *App) exportPerson(ctx context.Context, base, target, one, id string) error {
	sp := a.spin("Exporting")
	defer sp.Stop()
	body, err := a.client.Stream(ctx, api.Request{Path: base + "/export"})
	if err != nil {
		return err
	}
	defer body.Close()
	var w io.Writer = a.Stdout
	if target != "-" {
		file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return failf(ExitError, "Couldn't write %s: %v", target, err)
		}
		defer file.Close()
		w = file
	}
	n, err := io.Copy(w, body)
	if err != nil {
		return failf(ExitError, "The export broke off after %s: %v", ui.Number(n)+" bytes", err).hint("Run it again.")
	}
	sp.Stop()
	if target == "-" {
		return nil
	}
	if a.mode.JSON {
		return a.printJSON(map[string]any{"file": target, "bytes": n})
	}
	a.done(fmt.Sprintf("Wrote the %s's events to %s %s", one, a.theme.Bold(target), a.theme.Dim("("+ui.Number(n)+" bytes, JSON lines)")))
	return nil
}

func (a *App) deletePerson(ctx context.Context, p api.Project, base, one, id string, yes bool) error {
	var total api.EndUser
	if err := a.client.Get(ctx, base, nil, &total); err != nil {
		return err
	}
	if total.Events == 0 {
		return failf(ExitNotFound, "No events of %s %q in %s", one, id, p.Name)
	}
	if err := a.confirmTyped(ctx, fmt.Sprintf("Delete all %s of %s %s in %s? This can't be undone. Type the id to confirm:",
		ui.Plural(total.Events, "event", "events"), one, id, p.Name), id, yes); err != nil {
		return err
	}
	sp := a.spin("Deleting")
	var out struct {
		Deleted int64 `json:"deleted"`
	}
	err := a.client.JSON(ctx, api.Request{Method: "DELETE", Path: base}, &out)
	sp.Stop()
	if err != nil {
		return err
	}
	if a.mode.JSON {
		return a.printJSON(map[string]any{"user_id": id, "deleted": out.Deleted})
	}
	a.done(fmt.Sprintf("Deleted %s of %s %s", ui.Plural(out.Deleted, "event", "events"), one, id))
	a.println(a.theme.Dim("  ClickHouse removes them in the background, within minutes."))
	return nil
}

func (a *App) releasesCommand() *cobra.Command {
	var f filters
	cmd := &cobra.Command{
		Use:     "releases",
		Aliases: []string{"versions"},
		Short:   "Each app version's health: people, sessions, errors, the share of sessions without errors",
		Example: `  lyrelog releases
  lyrelog releases --since 30d --platform ios`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			sp := a.spin("Loading releases")
			p, q, err := a.pageQuery(ctx, &f)
			var raw map[string]json.RawMessage
			if err == nil {
				raw, err = a.getMap(ctx, projectPath(p, "releases"), q)
			}
			sp.Stop()
			if err != nil {
				return err
			}
			if a.mode.JSON {
				return a.printRaw(rawList(raw, "releases"))
			}
			releases := decodeInto[api.Release](raw["releases"])
			a.println(a.heading(p, &f))
			a.println()
			if len(releases) == 0 {
				a.println("No versions seen in this window.")
				return nil
			}
			th, now := a.theme, a.Now()
			_, many := personWords(p)
			t := ui.Table{Columns: []ui.Column{{Header: "Version", Flex: true}, {Header: ui.Capitalize(many), Right: true}, {Header: "Sessions", Right: true},
				{Header: "Without errors", Right: true}, {Header: "Errors", Right: true}, {Header: "Critical", Right: true}, {Header: "First seen"}, {Header: "Last seen"}}}
			for _, r := range releases {
				clean := th.Dim("–")
				if r.Sessions > 0 {
					share := 1 - float64(r.ErrorSessions)/float64(r.Sessions)
					clean = ui.Percent(share)
					switch {
					case share < 0.95:
						clean = th.Error(clean)
					case share < 0.99:
						clean = th.Warning(clean)
					default:
						clean = th.Success(clean)
					}
				}
				version := r.Version
				if version == "" {
					version = th.Dim("(none)")
				}
				crit := th.Dim("0")
				if r.Criticals > 0 {
					crit = th.Level("critical", ui.Number(r.Criticals))
				}
				t.Add(th.Bold(version), ui.Number(r.Players), ui.Number(r.Sessions), clean, ui.Number(r.Errors), crit,
					ui.AgoMillis(r.FirstSeen, now), ui.AgoMillis(r.LastSeen, now))
			}
			t.Render(a.out, th, a.Width)
			return nil
		},
	}
	f.register(cmd, "7d", "all")
	return cmd
}
