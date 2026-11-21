package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"go.lyrelog.com/cli/internal/api"
	"go.lyrelog.com/cli/internal/ui"
)

func (a *App) projectsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "projects",
		Aliases: []string{"project"},
		Short:   "List your projects with their last 24 hours",
		Long:    "List the projects you can see, with events, people and errors in the last 24 hours, as the dashboard's project switcher shows them.",
		Example: `  lyrelog projects
  lyrelog projects show tilefall
  lyrelog projects set --retention-days 30
  lyrelog projects create --name "Tilefall web" --slug tilefall-web`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			sp := a.spin("Loading projects")
			list, raw, err := a.projectList(cmd.Context(), true)
			sp.Stop()
			if err != nil {
				return err
			}
			if a.mode.JSON {
				return a.printJSON(raw)
			}
			a.renderProjects(list)
			return nil
		},
	}
	cmd.AddCommand(a.projectShowCommand(), a.projectSetCommand(), a.projectCreateCommand(), a.platformCommand())
	return cmd
}

func (a *App) renderProjects(list []api.ProjectSummary) {
	th := a.theme
	if len(list) == 0 {
		a.println("No projects yet.")
		a.tip("Make one:", "lyrelog projects create --name <name> --slug <slug>")
		return
	}
	now := a.Now()
	current := a.host().Project
	t := ui.Table{Columns: []ui.Column{{Header: ""}, {Header: "Project", Flex: true}, {Header: "Slug", Keep: true}, {Header: "Events", Right: true},
		{Header: "People", Right: true}, {Header: "Errors", Right: true}, {Header: "Last event"}, {Header: "24 hours"}}}
	for _, p := range list {
		mark := " "
		if p.ID == current {
			mark = th.Primary("●")
		}
		last := th.Dim("none yet")
		if p.LastEvent != nil && *p.LastEvent > 0 {
			last = ui.AgoMillis(*p.LastEvent, now)
		}
		errs := ui.Compact(p.Errors24h)
		if p.Errors24h > 0 {
			errs = th.Error(errs)
		} else {
			errs = th.Dim(errs)
		}
		counts := make([]int64, len(p.Hours))
		for i, h := range p.Hours {
			counts[i] = h.Events
		}
		spark := th.Primary(ui.Sparkline(counts))
		events, people := ui.Compact(p.Events24h), ui.Compact(p.Users24h)
		if p.StatsUnavailable {
			events, people, errs, last, spark = "–", "–", "–", th.Dim("unavailable"), ""
		}
		t.Add(mark, th.Bold(p.Name), th.Dim(p.Slug), events, people, errs, last, spark)
	}
	t.Render(a.out, th, a.Width)
	if current == "" {
		a.println()
		a.tip("Pick the one commands use by default:", "lyrelog use <project>")
	}
}

func (a *App) projectShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "show [project]",
		Short:             "Show a project's settings",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: a.completeProjects,
		RunE: func(cmd *cobra.Command, args []string) error {
			sp := a.spin("Loading the project")
			p, err := a.project(cmd.Context(), a.projectRef(args))
			var raw map[string]json.RawMessage
			if err == nil {
				raw, err = a.getMap(cmd.Context(), projectPath(p, ""), nil)
			}
			sp.Stop()
			if err != nil {
				return err
			}
			if a.mode.JSON {
				return a.printRaw(raw["project"])
			}
			a.renderProject(p)
			return nil
		},
	}
}

func (a *App) renderProject(p api.Project) {
	th := a.theme
	fmt.Fprintf(a.out, "%s %s\n\n", th.Bold(p.Name), th.Dim(p.ID))
	row := func(k, v string) {
		if v == "" {
			v = th.Dim("none")
		}
		fmt.Fprintf(a.out, "  %s %s\n", th.Dim(fmt.Sprintf("%-20s", k)), v)
	}
	one, many := personWords(p)
	row("Slug", p.Slug)
	row("Your role", p.Role)
	retention := strconv.Itoa(p.RetentionDays) + " days"
	if p.MaxRetentionDays > 0 {
		retention += th.Dim(fmt.Sprintf(" (your plan keeps up to %d)", p.MaxRetentionDays))
	}
	row("Keeps events for", retention)
	env := p.DefaultEnvironment
	if env == "" {
		env = "all environments"
	}
	row("Opens on", env)
	row("Platforms", strings.Join(p.Platforms, ", "))
	row("Its people are", one+" / "+many)
	row("App Attest App IDs", strings.Join(p.AttestAppIDs, ", "))
	verified := strings.Join(p.RequireVerified, ", ")
	if verified == "" {
		verified = "everything is accepted"
	} else {
		verified = "only verified events from " + verified
	}
	row("Verified events", verified)
	if len(p.StoreApp) > 0 && string(p.StoreApp) != "null" {
		var app struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		}
		if json.Unmarshal(p.StoreApp, &app) == nil {
			row("App Store", app.Name+" "+th.Dim(app.URL))
		}
	}
	row("Created", p.CreatedAt.Local().Format("Jan 2, 2006"))
}

func (a *App) projectSetCommand() *cobra.Command {
	var (
		name, defaultEnv, people, platform string
		retention                          int
		platforms, attest, requireVerified []string
	)
	cmd := &cobra.Command{
		Use:   "set [project]",
		Short: "Change a project's settings (owners)",
		Long: `Change a project's settings, as the dashboard's settings page does. Only the flags you pass change.
Lists replace what's there: pass --platforms "" (or --require-verified "") to empty one.`,
		Example: `  lyrelog projects set --name "Tilefall"
  lyrelog projects set tilefall --retention-days 30 --default-env production
  lyrelog projects set --people player,players
  lyrelog projects set --require-verified ios,android
  lyrelog projects set --attest-app-ids ABCDE12345.com.example.tilefall`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: a.completeProjects,
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{}
			fl := cmd.Flags()
			if fl.Changed("name") {
				body["name"] = name
			}
			if fl.Changed("retention-days") {
				body["retention_days"] = retention
			}
			if fl.Changed("default-env") {
				if defaultEnv == "all" {
					defaultEnv = ""
				}
				body["default_environment"] = defaultEnv
			}
			if fl.Changed("platform") {
				body["platform"] = platform
			}
			if fl.Changed("platforms") {
				body["platforms"] = cleanList(platforms)
			}
			if fl.Changed("attest-app-ids") {
				body["attest_app_ids"] = cleanList(attest)
			}
			if fl.Changed("require-verified") {
				body["require_verified"] = cleanList(requireVerified)
			}
			if fl.Changed("people") {
				one, many, ok := strings.Cut(people, ",")
				if !ok {
					return usagef("--people takes the singular and the plural: player,players")
				}
				body["people"] = map[string]string{"one": strings.TrimSpace(one), "many": strings.TrimSpace(many)}
			}
			if len(body) == 0 {
				return usagef("Nothing to change").hint("Pass at least one flag: lyrelog projects set --help lists them.")
			}
			sp := a.spin("Saving")
			p, err := a.project(cmd.Context(), a.projectRef(args))
			var raw map[string]json.RawMessage
			if err == nil {
				err = a.client.JSON(cmd.Context(), api.Request{Method: http.MethodPatch, Path: projectPath(p, ""), Body: body}, &raw)
			}
			sp.Stop()
			if err != nil {
				return err
			}
			if a.mode.JSON {
				return a.printRaw(raw["project"])
			}
			var fresh api.Project
			_ = json.Unmarshal(raw["project"], &fresh)
			a.done("Saved " + a.theme.Bold(fresh.Name))
			if _, ok := body["retention_days"]; ok {
				a.println(a.theme.Dim("  Stored events are rewritten to the new retention in the background; new ones take it within a minute."))
			}
			if fresh.ID == a.host().Project && fresh.Name != a.host().ProjectName {
				a.host().ProjectName = fresh.Name
				_ = a.save()
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "the project's name")
	f.IntVar(&retention, "retention-days", 0, "keep events this many days (up to what the plan keeps)")
	f.StringVar(&defaultEnv, "default-env", "", "the environment the dashboard opens on (\"all\" for every one)")
	f.StringVar(&people, "people", "", "what the project calls its people, singular,plural: player,players")
	f.StringVar(&platform, "platform", "", "what it mainly is: ios, android, web, api or other")
	f.StringSliceVar(&platforms, "platforms", nil, "what it's made of: ios, android, macos, web, api (comma separated)")
	f.StringSliceVar(&attest, "attest-app-ids", nil, "App IDs its apps attest as with Apple App Attest (TEAMID.bundle.id); \"\" turns it off")
	f.StringSliceVar(&requireVerified, "require-verified", nil, "platforms it accepts only verified events from; \"\" accepts everything")
	return cmd
}

func cleanList(in []string) []string {
	out := []string{}
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (a *App) projectCreateCommand() *cobra.Command {
	var name, slug, org string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Make a project (organization owners)",
		Long:  "Make a project in one of your organizations. Its first ingest key is printed once: put it in your app.",
		Example: `  lyrelog projects create --name "Tilefall web" --slug tilefall-web
  lyrelog projects create --name Tilefall --slug tilefall --org "Ana's apps"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" || slug == "" {
				return usagef("A project needs --name and --slug").hint("The slug is lowercase letters, digits and dashes: tilefall-web.")
			}
			ctx := cmd.Context()
			o, err := a.organization(ctx, org)
			if err != nil {
				return err
			}
			sp := a.spin("Making the project")
			var raw map[string]json.RawMessage
			err = a.client.JSON(ctx, api.Request{Method: http.MethodPost, Path: "/api/projects",
				Body: map[string]any{"organization_id": o.ID, "name": name, "slug": slug}}, &raw)
			sp.Stop()
			if err != nil {
				return err
			}
			if a.mode.JSON {
				return a.printJSON(raw)
			}
			var p api.Project
			var key string
			_ = json.Unmarshal(raw["project"], &p)
			_ = json.Unmarshal(raw["ingest_key"], &key)
			a.done("Made " + a.theme.Bold(p.Name) + " " + a.theme.Dim(p.ID) + " in " + o.Name)
			a.showSecret("Its ingest key", key, "It goes in your app. It's public (write-only), but Lyrelog shows it only now.")
			a.tip("Make it the default:", "lyrelog use "+p.Slug)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "the project's name")
	cmd.Flags().StringVar(&slug, "slug", "", "a short name for URLs: lowercase letters, digits and dashes")
	cmd.Flags().StringVar(&org, "org", "", "organization (id or name; default: your only one, or the default project's)")
	return cmd
}

// showSecret prints a secret once, set apart so it's easy to copy.
func (a *App) showSecret(label, secret, note string) {
	th := a.theme
	a.println()
	fmt.Fprintf(a.out, "  %s\n\n    %s\n\n  %s\n\n", th.Bold(label+":"), th.PrimaryBold(secret), th.Dim(note))
}

func (a *App) platformCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "platform",
		Short: "Add or remove the platforms a project is made of",
		Args:  cobra.NoArgs,
		RunE:  noArgsHelp,
	}
	var siteURL, pkg, runtimeName string
	add := &cobra.Command{
		Use:   "add <ios|android|macos|web|api>",
		Short: "Add a platform (owners)",
		Example: `  lyrelog projects platform add web --url https://tilefall.example.com
  lyrelog projects platform add android --package com.example.tilefall
  lyrelog projects platform add api --runtime python`,
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"ios", "android", "macos", "web", "api"},
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]string{}
			if siteURL != "" {
				body["url"] = siteURL
			}
			if pkg != "" {
				body["package"] = pkg
			}
			if runtimeName != "" {
				body["runtime"] = runtimeName
			}
			return a.platformChange(cmd, http.MethodPut, args[0], body)
		},
	}
	add.Flags().StringVar(&siteURL, "url", "", "web: the website's address")
	add.Flags().StringVar(&pkg, "package", "", "android: the Google Play package")
	add.Flags().StringVar(&runtimeName, "runtime", "", "api: the server's language")
	remove := &cobra.Command{
		Use:       "remove <platform>",
		Short:     "Remove a platform (owners)",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"ios", "android", "macos", "web", "api"},
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.platformChange(cmd, http.MethodDelete, args[0], nil)
		},
	}
	cmd.AddCommand(add, remove)
	return cmd
}

func (a *App) platformChange(cmd *cobra.Command, method, platform string, body map[string]string) error {
	sp := a.spin("Saving")
	p, err := a.project(cmd.Context(), a.projectRef(nil))
	var raw map[string]json.RawMessage
	if err == nil {
		req := api.Request{Method: method, Path: projectPath(p, "platforms/"+api.PathEscape(platform))}
		if len(body) > 0 {
			req.Body = body
		}
		err = a.client.JSON(cmd.Context(), req, &raw)
	}
	sp.Stop()
	if err != nil {
		return err
	}
	if a.mode.JSON {
		return a.printRaw(raw["project"])
	}
	var fresh api.Project
	_ = json.Unmarshal(raw["project"], &fresh)
	if len(fresh.Platforms) == 0 {
		a.done(fresh.Name + " has no platforms now: " + a.theme.Command("lyrelog projects platform add") + " sets one up")
		return nil
	}
	a.done(fresh.Name + " is made of: " + a.theme.Bold(strings.Join(fresh.Platforms, ", ")))
	return nil
}

func (a *App) useCommand() *cobra.Command {
	var clear bool
	cmd := &cobra.Command{
		Use:   "use [project]",
		Short: "Choose the project commands use when --project isn't given",
		Example: `  lyrelog use tilefall
  lyrelog use 3f9a1c7e20bd
  lyrelog use            # show the current one
  lyrelog use --clear`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: a.completeProjects,
		RunE: func(cmd *cobra.Command, args []string) error {
			h := a.host()
			if clear {
				h.Project, h.ProjectName = "", ""
				if err := a.save(); err != nil {
					return err
				}
				if a.mode.JSON {
					return a.printJSON(map[string]any{"project": nil})
				}
				a.done("No default project now")
				return nil
			}
			if len(args) == 0 {
				if a.mode.JSON {
					if h.Project == "" {
						return a.printJSON(map[string]any{"project": nil})
					}
					return a.printJSON(map[string]any{"project": map[string]string{"id": h.Project, "name": h.ProjectName}})
				}
				if h.Project == "" {
					a.println("No default project yet.")
					a.tip("Choose one:", "lyrelog use <project>")
					return nil
				}
				a.println("Using " + a.theme.Bold(h.ProjectName) + " " + a.theme.Dim(h.Project))
				return nil
			}
			sp := a.spin("Finding the project")
			p, err := a.project(cmd.Context(), args[0])
			sp.Stop()
			if err != nil {
				return err
			}
			h.Project, h.ProjectName = p.ID, p.Name
			if err := a.save(); err != nil {
				return err
			}
			if a.mode.JSON {
				return a.printJSON(map[string]any{"project": map[string]string{"id": p.ID, "name": p.Name, "slug": p.Slug}})
			}
			a.done("Using " + a.theme.Bold(p.Name) + " " + a.theme.Dim(p.ID))
			return nil
		},
	}
	cmd.Flags().BoolVar(&clear, "clear", false, "forget the default project")
	return cmd
}

var _ = sort.Strings
var _ = time.Now
