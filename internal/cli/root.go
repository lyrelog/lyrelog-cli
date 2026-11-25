package cli

import (
	"github.com/spf13/cobra"
)

func (a *App) rootCommand() *cobra.Command {
	cobra.EnableCommandSorting = false
	root := &cobra.Command{
		Use:   "lyrelog",
		Short: "Lyrelog from your terminal: events, errors, sessions, people, keys",
		Long: `Lyrelog from your terminal: everything the dashboard does, except deleting your
account or a project and changing how you sign in (those stay on the web).

For people it draws tables and colours; for programs and coding agents it writes JSON.
It switches by itself: JSON when stdout isn't a terminal or an agent is detected.
Force either with --json or --human (given both, the last one wins).`,
		Example: `  lyrelog login
  lyrelog use tilefall
  lyrelog events --level error --since 1h
  lyrelog events -f
  lyrelog errors
  lyrelog person u_8f2a
  lyrelog events --json --attr board.size:9 | jq '.[].message'`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Both given: the last one on the command line wins.
			if a.flagJSON && a.flagHuman {
				a.flagJSON, a.flagHuman = a.lastMode == "json", a.lastMode == "human"
			}
			a.setMode()
			if cmd.Annotations["no-setup"] == "true" {
				return nil
			}
			return a.setup()
		},
	}
	root.CompletionOptions.HiddenDefaultCmd = false
	f := root.PersistentFlags()
	f.StringVarP(&a.flagProject, "project", "p", "", "project: its id, slug or name (default: the one chosen with lyrelog use)")
	f.BoolVar(&a.flagJSON, "json", false, "write JSON (the default when stdout isn't a terminal or an agent runs the CLI)")
	f.BoolVar(&a.flagHuman, "human", false, "write tables and colours, even into a pipe")
	f.StringVar(&a.flagURL, "url", "", "dashboard address (default https://app.lyrelog.com, or LYRELOG_URL)")
	f.BoolVar(&a.flagNoColor, "no-color", false, "no colours (also NO_COLOR=1)")
	_ = root.RegisterFlagCompletionFunc("project", a.completeProjects)

	root.AddGroup(&cobra.Group{ID: "data", Title: "Look at your data:"},
		&cobra.Group{ID: "manage", Title: "Set things up:"},
		&cobra.Group{ID: "account", Title: "Sign in and more:"})
	add := func(group string, cmds ...*cobra.Command) {
		for _, c := range cmds {
			c.GroupID = group
			root.AddCommand(c)
		}
	}
	add("data", a.eventsCommand(), a.errorsCommand(), a.sessionsCommand(), a.peopleCommand(), a.personCommand(),
		a.releasesCommand(), a.insightsCommand(), a.usageCommand())
	add("manage", a.projectsCommand(), a.useCommand(), a.keysCommand(), a.symbolsCommand(), a.orgsCommand())
	add("account", a.loginCommand(), a.logoutCommand(), a.whoamiCommand(), a.accountCommand(), a.tokensCommand(), a.openCommand(),
		a.apiCommand(), a.skillsCommand(), a.versionCommand())
	root.SetHelpCommandGroupID("account")
	root.SetCompletionCommandGroupID("account")
	return root
}
