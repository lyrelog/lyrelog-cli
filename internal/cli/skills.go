package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"go.lyrelog.com/cli/internal/config"
	"go.lyrelog.com/cli/internal/skills"
)

// Lyrelog's agent skills (github.com/lyrelog/skills): listed, installed into the folders coding
// agents read skills from, and removed. The latest come from GitHub, so they improve without a
// new CLI; offline, or with --offline, the copy inside the binary. No sign-in is needed. The
// folders are the ones skills.sh uses, so either installs the same way.

// skillAgent is where one coding agent reads skills: under the home folder for the user, under
// the project for one project. detect is a folder whose presence means the agent is installed.
type skillAgent struct {
	id, name        string
	global, project string
	detect          []string
	also            string // other agents that read the same folder
}

var skillAgents = []skillAgent{
	{id: "claude-code", name: "Claude Code", global: ".claude/skills", project: ".claude/skills", detect: []string{".claude"}},
	{id: "codex", name: "Codex", global: ".agents/skills", project: ".agents/skills", detect: []string{".codex", ".agents"}, also: "Cline, Warp, Zed and others read it too"},
	{id: "cursor", name: "Cursor", global: ".cursor/skills", project: ".agents/skills", detect: []string{".cursor"}},
	{id: "gemini-cli", name: "Gemini CLI", global: ".gemini/skills", project: ".agents/skills", detect: []string{".gemini"}},
	{id: "github-copilot", name: "GitHub Copilot", global: ".copilot/skills", project: ".agents/skills", detect: []string{".copilot"}},
	{id: "opencode", name: "OpenCode", global: ".config/opencode/skills", project: ".agents/skills", detect: []string{".config/opencode"}},
	{id: "windsurf", name: "Windsurf", global: ".codeium/windsurf/skills", project: ".windsurf/skills", detect: []string{".codeium/windsurf"}},
	{id: "amp", name: "Amp", global: ".config/agents/skills", project: ".agents/skills", detect: []string{".config/amp"}},
}

func agentIDs() string {
	ids := make([]string, len(skillAgents))
	for i, a := range skillAgents {
		ids[i] = a.id
	}
	return strings.Join(ids, ", ")
}

func (a *App) skillsCommand() *cobra.Command {
	var offline bool
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Lyrelog's skills for coding agents: list, install, remove",
		Long: `Skills teach a coding agent (Claude Code, Codex, Cursor, Gemini CLI, GitHub Copilot,
OpenCode...) to work with Lyrelog: add the SDK to an app, investigate an error with this
CLI, get the right key or token, set up alerts. Install and the list use the
newest from github.com/lyrelog/skills; without a network (or with --offline), the copy that
came inside this CLI. No sign-in needed.

They're also installable with skills.sh:
  npx skills add lyrelog/skills`,
		Example: `  lyrelog skills
  lyrelog skills install
  lyrelog skills install --agent claude-code --agent codex
  lyrelog skills install --project
  lyrelog skills install --skill lyrelog-investigate
  lyrelog skills remove`,
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"no-setup": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.listSkills(a.loadSkills(cmd.Context(), offline))
		},
	}
	cmd.Flags().BoolVar(&offline, "offline", false, "use the skills inside the CLI instead of the newest from GitHub")
	cmd.AddCommand(a.skillsInstallCommand(), a.skillsRemoveCommand())
	return cmd
}

// loadSkills gets the newest skills from GitHub (LYRELOG_SKILLS_URL points elsewhere, for a
// mirror), or the copy inside the binary when offline is set or the download fails, saying so.
func (a *App) loadSkills(ctx context.Context, offline bool) skills.Set {
	if offline {
		return skills.Bundled()
	}
	url := a.Getenv("LYRELOG_SKILLS_URL")
	if url == "" {
		url = skills.ArchiveURL
	}
	set, err := func() (skills.Set, error) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return skills.Set{}, err
		}
		client := a.HTTP
		if client == nil {
			client = http.DefaultClient
		}
		res, err := client.Do(req)
		if err != nil {
			return skills.Set{}, err
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return skills.Set{}, fmt.Errorf("GitHub answered %d", res.StatusCode)
		}
		return skills.FromArchive(res.Body)
	}()
	if err != nil {
		fmt.Fprintf(a.errOut, "Couldn't get the newest skills (%v): using the ones that came with the CLI.\n", err)
		return skills.Bundled()
	}
	return set
}

type skillTargetFlags struct {
	agents  []string
	project bool
	skills  []string
	offline bool
}

func (f *skillTargetFlags) bind(cmd *cobra.Command, verb string) {
	cmd.Flags().StringSliceVar(&f.agents, "agent", nil, "agents to "+verb+" for: "+agentIDs()+", or all (default: the ones on this computer)")
	cmd.Flags().BoolVar(&f.project, "project", false, verb+" in this project's folder (.claude/skills and .agents/skills) instead of your home")
	cmd.Flags().StringSliceVar(&f.skills, "skill", nil, "only these skills (default: all of them)")
}

// skillDir is one folder that gets the skills, and the agents that read it.
type skillDir struct {
	path   string
	agents []string
}

// targets resolves the flags to folders, each once (several agents share .agents/skills).
func (a *App) skillTargets(set skills.Set, f skillTargetFlags, verb string) ([]skillDir, []skills.Skill, error) {
	all := set.All()
	chosen := all
	if len(f.skills) > 0 {
		chosen = nil
		for _, name := range f.skills {
			i := slices.IndexFunc(all, func(s skills.Skill) bool { return s.Name == name })
			if i < 0 {
				names := make([]string, len(all))
				for j, s := range all {
					names[j] = s.Name
				}
				return nil, nil, usagef("No skill %q. The skills are %s.", name, strings.Join(names, ", "))
			}
			chosen = append(chosen, all[i])
		}
	}

	var base string
	if f.project {
		wd, err := os.Getwd()
		if err != nil {
			return nil, nil, err
		}
		base = wd
	} else {
		base = a.Getenv("HOME")
		if base == "" {
			base = a.Getenv("USERPROFILE")
		}
		if base == "" {
			return nil, nil, fmt.Errorf("can't tell where your home folder is: set HOME")
		}
	}

	var agents []skillAgent
	switch {
	case slices.Contains(f.agents, "all"):
		agents = skillAgents
	case len(f.agents) > 0:
		for _, id := range f.agents {
			i := slices.IndexFunc(skillAgents, func(s skillAgent) bool { return s.id == id })
			if i < 0 {
				return nil, nil, usagef("No agent %q. The agents are %s, or all.", id, agentIDs())
			}
			agents = append(agents, skillAgents[i])
		}
	case f.project:
		// A project's folders: Claude Code's, and the shared .agents/skills most others read.
		agents = []skillAgent{skillAgents[0], skillAgents[1]}
	default:
		for _, ag := range skillAgents {
			if agentFound(base, ag) {
				agents = append(agents, ag)
			}
		}
		if len(agents) == 0 {
			return nil, nil, usagef("No coding agent found in your home folder. Choose one with --agent (%s), or %s into this project with --project.", agentIDs(), verb)
		}
	}

	var dirs []skillDir
	for _, ag := range agents {
		rel := ag.global
		if f.project {
			rel = ag.project
		}
		p := filepath.Join(base, filepath.FromSlash(rel))
		if i := slices.IndexFunc(dirs, func(d skillDir) bool { return d.path == p }); i >= 0 {
			dirs[i].agents = append(dirs[i].agents, ag.name)
			continue
		}
		dirs = append(dirs, skillDir{path: p, agents: []string{ag.name}})
	}
	return dirs, chosen, nil
}

type skillResult struct {
	Skill   string   `json:"skill"`
	Version string   `json:"version"`
	Path    string   `json:"path"`
	Agents  []string `json:"agents"`
}

func (a *App) skillsInstallCommand() *cobra.Command {
	var f skillTargetFlags
	var choose bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the skills for your coding agents",
		Long: `Copies the skills into the folders your coding agents read. The first time, at a
terminal, it asks where (for you, in every project, or only in this project) and for which
agents, with the ones found on this computer ticked; the answer is saved and used from then
on (--choose asks again). --agent and --project override it. Without a person to ask (an
agent, CI), it uses the saved answer, else the agents found in your home folder.

A skill already there is replaced by the newest; nothing else in those folders is touched.`,
		Example: `  lyrelog skills install
  lyrelog skills install --choose
  lyrelog skills install --agent claude-code
  lyrelog skills install --project --skill lyrelog-integrate`,
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"no-setup": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.applySkillsChoice(cmd, &f, choose, true); err != nil {
				return err
			}
			set := a.loadSkills(cmd.Context(), f.offline)
			dirs, chosen, err := a.skillTargets(set, f, "install")
			if err != nil {
				return err
			}
			var done []skillResult
			for _, d := range dirs {
				for _, s := range chosen {
					dest := filepath.Join(d.path, s.Name)
					if err := writeSkill(set, s, dest); err != nil {
						return fmt.Errorf("installing %s in %s: %w", s.Name, d.path, err)
					}
					done = append(done, skillResult{Skill: s.Name, Version: s.Version, Path: dest, Agents: d.agents})
				}
			}
			if a.mode.JSON {
				return a.printJSON(done)
			}
			for _, d := range dirs {
				a.done(fmt.Sprintf("%s %s", a.homeRel(d.path), a.theme.Dim("("+strings.Join(d.agents, ", ")+")")))
				for _, s := range chosen {
					a.println("   " + s.Name + a.theme.Dim(" "+s.Version))
				}
			}
			a.tip("Restart your agent, or start a new session, so it picks them up. Try asking it", `"Investigate our top Lyrelog error"`)
			return nil
		},
	}
	f.bind(cmd, "install")
	cmd.Flags().BoolVar(&f.offline, "offline", false, "install the skills inside the CLI instead of the newest from GitHub")
	cmd.Flags().BoolVar(&choose, "choose", false, "ask again where and for which agents (the answer is saved)")
	return cmd
}

func (a *App) skillsRemoveCommand() *cobra.Command {
	var f skillTargetFlags
	cmd := &cobra.Command{
		Use:         "remove",
		Short:       "Remove the skills from your coding agents' folders",
		Long:        `Removes Lyrelog's skills (and only them) from the folders install writes to.`,
		Example:     "  lyrelog skills remove\n  lyrelog skills remove --project",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"no-setup": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.applySkillsChoice(cmd, &f, false, false); err != nil {
				return err
			}
			// Removing needs only the names, which the bundled copy has.
			dirs, chosen, err := a.skillTargets(skills.Bundled(), f, "remove")
			if err != nil {
				return err
			}
			removed := []skillResult{}
			for _, d := range dirs {
				for _, s := range chosen {
					dest := filepath.Join(d.path, s.Name)
					if _, err := os.Stat(filepath.Join(dest, "SKILL.md")); err != nil {
						continue
					}
					if err := os.RemoveAll(dest); err != nil {
						return err
					}
					removed = append(removed, skillResult{Skill: s.Name, Path: dest, Agents: d.agents})
				}
			}
			if a.mode.JSON {
				return a.printJSON(removed)
			}
			if len(removed) == 0 {
				a.println("No Lyrelog skills were installed there.")
				return nil
			}
			for _, r := range removed {
				a.done("Removed " + a.homeRel(r.Path))
			}
			return nil
		},
	}
	f.bind(cmd, "remove")
	return cmd
}

// listSkills shows the bundled skills and, for each agent found, the version installed for the
// user.
func (a *App) listSkills(set skills.Set) error {
	type installed struct {
		Agent   string `json:"agent"`
		Path    string `json:"path"`
		Version string `json:"version"`
	}
	type row struct {
		skills.Skill
		Installed []installed `json:"installed"`
	}
	home := a.Getenv("HOME")
	if home == "" {
		home = a.Getenv("USERPROFILE")
	}
	var rows []row
	for _, s := range set.All() {
		r := row{Skill: s, Installed: []installed{}}
		seen := map[string]bool{}
		for _, ag := range skillAgents {
			p := filepath.Join(home, filepath.FromSlash(ag.global), s.Name)
			if seen[p] {
				continue
			}
			seen[p] = true
			if text, err := os.ReadFile(filepath.Join(p, "SKILL.md")); err == nil && home != "" {
				r.Installed = append(r.Installed, installed{Agent: ag.id, Path: p, Version: installedVersion(string(text))})
			}
		}
		rows = append(rows, r)
	}
	if a.mode.JSON {
		return a.printJSON(rows)
	}
	th := a.theme
	for _, r := range rows {
		fmt.Fprintf(a.out, "%s %s\n", th.Bold(r.Name), th.Dim(r.Version))
		desc, _, _ := strings.Cut(r.Description, ". Use when")
		fmt.Fprintf(a.out, "  %s\n", desc+".")
		if len(r.Installed) == 0 {
			fmt.Fprintf(a.out, "  %s\n\n", th.Dim("not installed"))
			continue
		}
		for _, in := range r.Installed {
			state := th.Mark("ok") + " " + in.Version
			if in.Version != r.Version {
				state = th.Warning(in.Version + ", " + r.Version + " here")
			}
			fmt.Fprintf(a.out, "  %s %s\n", state, th.Dim(a.homeRel(in.Path)))
		}
		fmt.Fprintln(a.out)
	}
	a.loadConfig()
	if saved := a.savedSkillsChoice(); saved != nil {
		where := "for you, in every project"
		if saved.Scope == "project" {
			where = "in the project you run it in"
		}
		names := make([]string, 0, len(saved.Agents))
		for _, id := range saved.Agents {
			if i := slices.IndexFunc(skillAgents, func(s skillAgent) bool { return s.id == id }); i >= 0 {
				names = append(names, skillAgents[i].name)
			}
		}
		fmt.Fprintf(a.out, "%s %s, %s\n\n", th.Dim("Install puts them:"), strings.Join(names, ", "), where)
		a.tip("Update them with", "lyrelog skills install, or choose again with --choose")
		return nil
	}
	a.tip("Install or update them for your agents with", "lyrelog skills install")
	return nil
}

// applySkillsChoice settles where and for which agents when --agent and --project don't say:
// the saved answer, or (ask, at a terminal, the first time or with --choose) the person's
// answer, saved for next time. Otherwise it leaves the defaults: the agents found at home.
func (a *App) applySkillsChoice(cmd *cobra.Command, f *skillTargetFlags, choose, ask bool) error {
	if cmd.Flags().Changed("agent") || cmd.Flags().Changed("project") {
		return nil
	}
	a.loadConfig()
	if saved := a.savedSkillsChoice(); saved != nil && !choose {
		f.project, f.agents = saved.Scope == "project", saved.Agents
		return nil
	}
	if !ask {
		return nil
	}
	if !a.mode.Interactive {
		if choose {
			return usagef("--choose asks you, and nobody can answer here").hint("Pass --agent and --project instead.")
		}
		return nil
	}
	ctx := cmd.Context()
	home := a.homeDir()
	wd, _ := os.Getwd()
	where, err := a.choose(ctx, "Where should the skills go?", []choice{
		{label: "For you, in every project", hint: "your home folder"},
		{label: "Only in this project", hint: a.homeRel(wd) + ", for everyone who works on it"},
	}, false)
	if err != nil {
		return err
	}
	project := where[0] == 1
	items := make([]choice, len(skillAgents))
	for i, ag := range skillAgents {
		rel := ag.global
		folder := filepath.Join("~", filepath.FromSlash(rel))
		if project {
			rel = ag.project
			folder = filepath.FromSlash(rel)
		}
		hint := folder
		if ag.also != "" {
			hint += ", " + ag.also
		}
		items[i] = choice{label: ag.name, hint: hint, checked: home != "" && agentFound(home, ag)}
	}
	picked, err := a.choose(ctx, "For which agents?", items, true)
	if err != nil {
		return err
	}
	f.project, f.agents = project, nil
	for _, i := range picked {
		f.agents = append(f.agents, skillAgents[i].id)
	}
	scope := "user"
	if project {
		scope = "project"
	}
	if a.cfg != nil {
		a.cfg.Skills = &config.SkillsChoice{Scope: scope, Agents: f.agents}
		if err := a.cfg.Save(a.cfgPath); err == nil {
			fmt.Fprintln(a.errOut, a.theme.Dim("Saved for next time; lyrelog skills install --choose changes it."))
		}
	}
	return nil
}

// loadConfig reads the settings file for commands that skip setup (no sign-in needed); a.cfg
// stays nil if it can't be read.
func (a *App) loadConfig() {
	if a.cfg != nil {
		return
	}
	dir, err := config.Dir(a.Getenv)
	if err != nil {
		return
	}
	a.cfgPath = filepath.Join(dir, "config.json")
	a.cfg, _ = config.Load(a.cfgPath)
}

func (a *App) savedSkillsChoice() *config.SkillsChoice {
	if a.cfg == nil || a.cfg.Skills == nil || len(a.cfg.Skills.Agents) == 0 {
		return nil
	}
	return a.cfg.Skills
}

func (a *App) homeDir() string {
	if h := a.Getenv("HOME"); h != "" {
		return h
	}
	return a.Getenv("USERPROFILE")
}

func agentFound(home string, ag skillAgent) bool {
	for _, d := range ag.detect {
		if st, err := os.Stat(filepath.Join(home, filepath.FromSlash(d))); err == nil && st.IsDir() {
			return true
		}
	}
	return false
}

// writeSkill replaces dest with the skill's files: written next to it first, then swapped in,
// so an agent never reads half a skill.
func writeSkill(set skills.Set, s skills.Skill, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dest), "."+s.Name+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	for _, file := range s.Files {
		data, err := set.Read(s.Name, file)
		if err != nil {
			return err
		}
		p := filepath.Join(tmp, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return err
		}
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

// installedVersion reads metadata.version from an installed SKILL.md ("?" when it has none).
func installedVersion(text string) string {
	if _, v := skills.Frontmatter(text); v != "" {
		return v
	}
	return "?"
}

// homeRel shows a path under the home folder as ~/...
func (a *App) homeRel(p string) string {
	if home := a.Getenv("HOME"); home != "" {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.Join("~", rel)
		}
	}
	return p
}
