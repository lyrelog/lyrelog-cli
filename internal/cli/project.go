package cli

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"go.lyrelog.com/cli/internal/api"
)

var publicID = regexp.MustCompile(`^[0-9a-f]{12}$`)

// projectList is GET /api/projects: typed for people, raw for JSON. Without stats it's the list
// alone (?stats=0: no 24-hour numbers, so no ClickHouse query), for finding a project by name.
func (a *App) projectList(ctx context.Context, stats bool) ([]api.ProjectSummary, []json.RawMessage, error) {
	var body struct {
		Projects []json.RawMessage `json:"projects"`
	}
	var q url.Values
	if !stats {
		q = url.Values{"stats": {"0"}}
	}
	if err := a.client.Get(ctx, "/api/projects", q, &body); err != nil {
		return nil, nil, err
	}
	typed := make([]api.ProjectSummary, len(body.Projects))
	for i, raw := range body.Projects {
		_ = json.Unmarshal(raw, &typed[i])
	}
	return typed, body.Projects, nil
}

// projectRef is the project the command is about: the argument, else --project, else the default.
func (a *App) projectRef(args []string) string {
	if len(args) > 0 && args[0] != "" {
		return args[0]
	}
	if a.flagProject != "" {
		return a.flagProject
	}
	return a.host().Project
}

// project finds a project by public id, slug or name (exact, then a unique start).
func (a *App) project(ctx context.Context, ref string) (api.Project, error) {
	if a.token == "" {
		return api.Project{}, api.ErrNoToken // say "sign in" before "which project"
	}
	if ref == "" {
		return api.Project{}, usagef("Which project?").hint("Pass --project <name>, or choose one for every command with `lyrelog use <project>`. `lyrelog projects` lists yours.")
	}
	if publicID.MatchString(ref) {
		var body struct {
			Project api.Project `json:"project"`
		}
		err := a.client.Get(ctx, "/api/projects/"+ref, nil, &body)
		if ae, ok := err.(*api.Error); !ok || ae.Status != 404 {
			if err != nil {
				return api.Project{}, err
			}
			return body.Project, nil
		}
		// 404: maybe a slug that happens to look like an id; look in the list.
	}
	list, _, err := a.projectList(ctx, false)
	if err != nil {
		return api.Project{}, err
	}
	p, err := matchProject(list, ref)
	if err != nil {
		return api.Project{}, err
	}
	return p.Project, nil
}

func matchProject(list []api.ProjectSummary, ref string) (api.ProjectSummary, error) {
	lower := strings.ToLower(ref)
	for _, p := range list {
		if p.ID == ref || p.Slug == lower {
			return p, nil
		}
	}
	var hits []api.ProjectSummary
	for _, p := range list {
		if strings.EqualFold(p.Name, ref) {
			hits = append(hits, p)
		}
	}
	if len(hits) == 0 {
		for _, p := range list {
			if strings.HasPrefix(p.Slug, lower) || strings.HasPrefix(strings.ToLower(p.Name), lower) {
				hits = append(hits, p)
			}
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		e := failf(ExitNotFound, "No project called %q", ref)
		if len(list) == 0 {
			return api.ProjectSummary{}, e.hint("You have no projects yet. Make one with `lyrelog projects create`.")
		}
		return api.ProjectSummary{}, e.hint("Yours: " + projectNames(list) + ".")
	}
	return api.ProjectSummary{}, usagef("%q could be %s", ref, projectNames(hits)).hint("Use the slug or the id.")
}

func projectNames(list []api.ProjectSummary) string {
	names := make([]string, 0, len(list))
	for _, p := range list {
		names = append(names, p.Slug)
	}
	sort.Strings(names)
	if len(names) > 8 {
		names = append(names[:8], "…")
	}
	return strings.Join(names, ", ")
}

// projectPath is /api/projects/<id>/<rest>.
func projectPath(p api.Project, rest string) string {
	if rest == "" {
		return "/api/projects/" + p.ID
	}
	return "/api/projects/" + p.ID + "/" + rest
}

// personWords: what the project calls one and many of its people.
func personWords(p api.Project) (string, string) {
	one, many := p.PersonOne, p.PersonMany
	if one == "" {
		one = "person"
	}
	if many == "" {
		many = "people"
	}
	return one, many
}

// completeProjects completes --project and `use` with the projects' slugs.
func (a *App) completeProjects(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if a.setup() != nil || a.token == "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	list, _, err := a.projectList(cmd.Context(), false)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []cobra.Completion
	for _, p := range list {
		if strings.HasPrefix(p.Slug, toComplete) {
			out = append(out, cobra.CompletionWithDesc(p.Slug, p.Name))
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// rawList pulls one array out of an answer ({"issues":[...]} → [...]).
func rawList(body map[string]json.RawMessage, key string) json.RawMessage {
	if raw, ok := body[key]; ok && string(raw) != "null" {
		return raw
	}
	return json.RawMessage("[]")
}

// getMap GETs a path and returns the top-level object.
func (a *App) getMap(ctx context.Context, path string, q url.Values) (map[string]json.RawMessage, error) {
	var body map[string]json.RawMessage
	if err := a.client.Get(ctx, path, q, &body); err != nil {
		return nil, err
	}
	return body, nil
}

func decodeInto[T any](raw json.RawMessage) []T {
	var out []T
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// remarshal decodes a map answer into a struct.
func remarshal(m map[string]json.RawMessage, out any) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}
