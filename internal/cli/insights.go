package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"go.lyrelog.com/cli/internal/api"
	"go.lyrelog.com/cli/internal/ui"
)

// joinEvents takes events as separate arguments or "a|b" and makes the API's "a|b".
func joinEvents(args []string) string {
	var out []string
	for _, a := range args {
		for _, p := range strings.Split(a, "|") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return strings.Join(out, "|")
}

func (a *App) insightsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "insights",
		Short: "Trends, rates and funnels over events",
		Long: `The Insights page: how often something happens (trend), how often it fails (rate), and
how far people get through a flow (funnel). For flows that break, not marketing.`,
		Example: `  lyrelog insights trend game.finished --by version
  lyrelog insights trend game.finished --metric users --since 30d
  lyrelog insights trend game.finished --metric value --value-path duration_ms --stat p90
  lyrelog insights rate --hits purchase.failed --of "purchase.started"
  lyrelog insights funnel onboarding.start onboarding.name onboarding.done --window 1h
  lyrelog insights views`,
		Args: cobra.NoArgs,
		RunE: noArgsHelp,
	}

	var tf filters
	var metric, stat, valuePath, by string
	trend := &cobra.Command{
		Use:   "trend <event>...",
		Short: "How often events happen over time, optionally broken down",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			extra := url.Values{"events": {joinEvents(args)}}
			if metric != "" {
				extra.Set("metric", metric)
			}
			if stat != "" {
				extra.Set("stat", stat)
			}
			if valuePath != "" {
				extra.Set("value_path", valuePath)
			}
			if by != "" {
				extra.Set("breakdown", by)
			}
			return a.series(cmd, &tf, "trends", extra, metric)
		},
	}
	tf.register(trend, "7d", "all")
	trend.Flags().StringVar(&metric, "metric", "", "events (default), users, or value (a number attribute)")
	trend.Flags().StringVar(&stat, "stat", "", "with --metric value: avg, p50, p90, p95, p99 or max")
	trend.Flags().StringVar(&valuePath, "value-path", "", "with --metric value: the attribute holding the number")
	trend.Flags().StringVar(&by, "by", "", "break down by version, country or attr:<path>")

	var rf filters
	var hits, of, rateBy string
	rate := &cobra.Command{
		Use:   "rate",
		Short: "The share of one set of events over another (failures over attempts)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if hits == "" || of == "" {
				return usagef("A rate needs --hits and --of").hint(`For example --hits purchase.failed --of "purchase.started".`)
			}
			extra := url.Values{"hits": {joinEvents([]string{hits})}, "of": {joinEvents([]string{of})}}
			if rateBy != "" {
				extra.Set("breakdown", rateBy)
			}
			return a.series(cmd, &rf, "rates", extra, "rate")
		},
	}
	rf.register(rate, "7d", "all")
	rate.Flags().StringVar(&hits, "hits", "", `the events counted (failures): "a|b"`)
	rate.Flags().StringVar(&of, "of", "", `the events they're out of (attempts): "c|d"`)
	rate.Flags().StringVar(&rateBy, "by", "", "break down by version, country or attr:<path>")

	var ff filters
	var window string
	funnel := &cobra.Command{
		Use:   "funnel <step> <step>...",
		Short: "How many people reach each step of a flow, and how long it takes",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			extra := url.Values{"steps": {joinEvents(args)}}
			if window != "" {
				d, ok := parseSpan(window)
				if !ok {
					return usagef("--window %q: use 10m, 1h, 1d (a minute to 30 days)", window)
				}
				extra.Set("window_ms", strconv.FormatInt(d.Milliseconds(), 10))
			}
			sp := a.spin("Following the flow")
			p, q, err := a.pageQuery(ctx, &ff)
			var raw map[string]json.RawMessage
			if err == nil {
				for k, v := range extra {
					q[k] = v
				}
				raw, err = a.getMap(ctx, projectPath(p, "funnel"), q)
			}
			sp.Stop()
			if err != nil {
				return err
			}
			if a.mode.JSON {
				return a.printJSON(raw)
			}
			steps := decodeInto[api.FunnelStep](raw["steps"])
			var windowMS int64
			_ = json.Unmarshal(raw["window_ms"], &windowMS)
			th := a.theme
			_, many := personWords(p)
			a.println(a.heading(p, &ff) + th.Dim(" · within "+ui.Duration(time.Duration(windowMS)*time.Millisecond)))
			a.println()
			var top int64
			if len(steps) > 0 {
				top = steps[0].People
			}
			t := ui.Table{Columns: []ui.Column{{Header: "Step", Flex: true}, {Header: ""}, {Header: ui.Capitalize(many), Right: true}, {Header: "Of the first", Right: true}, {Header: "Median time"}}}
			for i, s := range steps {
				share := 0.0
				if top > 0 {
					share = float64(s.People) / float64(top)
				}
				filled, empty := ui.Bar(share, 20)
				median := ""
				if i > 0 && s.MedianMS != nil {
					median = ui.Duration(time.Duration(*s.MedianMS) * time.Millisecond)
				}
				t.Add(fmt.Sprintf("%d. %s", i+1, th.Bold(s.Event)), th.Primary(filled)+th.Dim(empty), ui.Number(s.People), ui.Percent(share), median)
			}
			t.Render(a.out, th, a.Width)
			return nil
		},
	}
	ff.register(funnel, "7d", "all")
	funnel.Flags().StringVar(&window, "window", "", "time allowed from the first step to the last (default 1d)")

	views := &cobra.Command{
		Use:   "views",
		Short: "The project's saved views",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			sp := a.spin("Loading views")
			p, err := a.project(ctx, a.projectRef(nil))
			var raw map[string]json.RawMessage
			if err == nil {
				raw, err = a.getMap(ctx, projectPath(p, "views"), nil)
			}
			sp.Stop()
			if err != nil {
				return err
			}
			if a.mode.JSON {
				return a.printRaw(rawList(raw, "views"))
			}
			list := decodeInto[api.SavedView](raw["views"])
			if len(list) == 0 {
				a.println("No saved views in " + p.Name + ".")
				return nil
			}
			t := ui.Table{Columns: []ui.Column{{Header: "ID", Right: true}, {Header: "Name", Flex: true}, {Header: "Kind"}, {Header: "By"}, {Header: "Saved"}}}
			for _, v := range list {
				t.Add(strconv.FormatInt(v.ID, 10), a.theme.Bold(v.Name), v.Kind, v.CreatedBy, ui.Ago(v.CreatedAt, a.Now()))
			}
			t.Render(a.out, a.theme, a.Width)
			return nil
		},
	}
	var yes bool
	deleteView := &cobra.Command{
		Use:   "delete-view <id>",
		Short: "Delete a saved view",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			p, err := a.project(ctx, a.projectRef(nil))
			if err != nil {
				return err
			}
			if err := a.confirm(ctx, "Delete saved view "+args[0]+"?", yes); err != nil {
				return err
			}
			if err := a.client.JSON(ctx, api.Request{Method: http.MethodDelete, Path: projectPath(p, "views/"+api.PathEscape(args[0]))}, nil); err != nil {
				return err
			}
			if a.mode.JSON {
				return a.printJSON(map[string]any{"deleted": true, "id": args[0]})
			}
			a.done("Deleted view " + args[0])
			return nil
		},
	}
	deleteView.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask to confirm")
	cmd.AddCommand(trend, rate, funnel, views, deleteView)
	return cmd
}

// series runs a trend or a rate and draws each series' total and its shape.
func (a *App) series(cmd *cobra.Command, f *filters, endpoint string, extra url.Values, metric string) error {
	ctx := cmd.Context()
	sp := a.spin("Counting")
	p, q, err := a.pageQuery(ctx, f)
	var raw map[string]json.RawMessage
	if err == nil {
		for k, v := range extra {
			q[k] = v
		}
		raw, err = a.getMap(ctx, projectPath(p, endpoint), q)
	}
	sp.Stop()
	if err != nil {
		return err
	}
	if a.mode.JSON {
		return a.printJSON(raw)
	}
	series := decodeInto[api.TrendSeries](raw["series"])
	grouped := false
	for _, s := range series {
		grouped = grouped || s.Group != ""
	}
	th := a.theme
	a.println(a.heading(p, f))
	a.println()
	if len(series) == 0 {
		a.println("Nothing to count in this window.")
		return nil
	}
	_, many := personWords(p)
	header := map[string]string{"": "Events", "events": "Events", "users": ui.Capitalize(many), "value": "Value", "rate": "Rate"}[metric]
	cols := []ui.Column{{Header: "Event", Flex: true}}
	if grouped {
		cols = append(cols, ui.Column{Header: "Group", Flex: true})
	}
	cols = append(cols, ui.Column{Header: header, Right: true})
	if metric == "rate" {
		cols = append(cols, ui.Column{Header: "Hits / tries", Right: true})
	}
	t := ui.Table{Columns: append(cols, ui.Column{Header: "Over time"})}
	for _, s := range series {
		total := th.Dim("–")
		if s.Total != nil {
			switch metric {
			case "rate":
				total = trimFloat(*s.Total) + "%"
			case "value":
				total = trimFloat(*s.Total)
			default:
				total = ui.Number(int64(*s.Total))
			}
		}
		points := make([]int64, len(s.Points))
		for i, pt := range s.Points {
			if pt.V != nil {
				points[i] = int64(*pt.V*100) + 1
			}
		}
		name := th.Bold(s.Event)
		switch {
		case metric == "rate":
			// A rate's series carry no event: it is the hits over the tries asked for.
			name = th.Bold(strings.ReplaceAll(extra.Get("hits"), "|", ", ")) + th.Dim(" of ") + strings.ReplaceAll(extra.Get("of"), "|", ", ")
		case s.Event == "":
			name = th.Dim("all")
		}
		row := []string{name}
		if grouped {
			row = append(row, s.Group)
		}
		row = append(row, total)
		if metric == "rate" {
			ht := ""
			if s.Hits != nil && s.Tries != nil {
				ht = ui.Number(*s.Hits) + " / " + ui.Number(*s.Tries)
			}
			row = append(row, ht)
		}
		row = append(row, th.Primary(ui.Sparkline(squeeze(points, 40))))
		t.Add(row...)
	}
	t.Render(a.out, th, a.Width)
	return nil
}

func trimFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', 2, 64)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	return s
}

// squeeze sums neighbouring points so a line fits in n cells.
func squeeze(points []int64, n int) []int64 {
	if len(points) <= n {
		return points
	}
	out := make([]int64, n)
	for i, v := range points {
		out[i*n/len(points)] += v
	}
	return out
}
