package cli

import (
	"encoding/json"
	"net/http"

	"github.com/spf13/cobra"

	"go.lyrelog.com/cli/internal/api"
	"go.lyrelog.com/cli/internal/ui"
)

var keyTypes = map[string]string{
	"ingest": "public, write-only: it ships in your app",
	"server": "secret: sends verified events from your servers",
	"read":   "secret: reads every event of the project (the /v1 read API)",
	"admin":  "secret: reads and manages the project through the API",
}

func (a *App) keysCommand() *cobra.Command {
	list := func(cmd *cobra.Command, args []string) error {
		sp := a.spin("Loading keys")
		p, err := a.project(cmd.Context(), a.projectRef(nil))
		var raw map[string]json.RawMessage
		if err == nil {
			raw, err = a.getMap(cmd.Context(), projectPath(p, "keys"), nil)
		}
		sp.Stop()
		if err != nil {
			return err
		}
		if a.mode.JSON {
			return a.printRaw(rawList(raw, "keys"))
		}
		keys := decodeInto[api.APIKey](raw["keys"])
		th, now := a.theme, a.Now()
		if len(keys) == 0 {
			a.println("No keys in " + p.Name + ".")
			a.tip("Make one:", "lyrelog keys create --type ingest --name iOS")
			return nil
		}
		t := ui.Table{Columns: []ui.Column{{Header: "ID", Keep: true}, {Header: "Name", Keep: true}, {Header: "Type"}, {Header: "Starts with", Drop: 1}, {Header: "Created", Drop: 2}, {Header: "Last used"}, {Header: "Status"}}}
		for _, k := range keys {
			used := th.Dim("never")
			if k.LastUsedAt != nil {
				used = ui.Ago(*k.LastUsedAt, now)
			}
			status := th.Success("active")
			if k.RevokedAt != nil {
				status = th.Dim("revoked " + ui.Ago(*k.RevokedAt, now))
			}
			t.Add(th.Dim(k.ID), th.Bold(k.Name), k.Scope, k.Prefix+"…", ui.Ago(k.CreatedAt, now), used, status)
		}
		t.Render(a.out, th, a.Width)
		return nil
	}
	cmd := &cobra.Command{
		Use:     "keys",
		Aliases: []string{"key"},
		Short:   "The project's API keys: list, create, revoke",
		Long: `The project's API keys. Types:
  ingest  public, write-only: it ships in your app
  server  secret: sends verified events from your servers
  read    secret: reads every event of the project
  admin   secret: reads and manages the project through the API

Creating a server, read or admin key needs a recent sign-in (lyrelog login, then within 10 minutes).`,
		Example: `  lyrelog keys
  lyrelog keys create --type ingest --name "iOS app"
  lyrelog keys create --type server --name "tilefall-api" --json | jq -r .secret
  lyrelog keys revoke 8c1d2e3f-...`,
		Args: cobra.NoArgs,
		RunE: list,
	}
	cmd.AddCommand(&cobra.Command{Use: "list", Short: "List the project's keys", Args: cobra.NoArgs, RunE: list})

	var scope, name string
	create := &cobra.Command{
		Use:   "create",
		Short: "Make a key (owners); its secret is shown once",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, ok := keyTypes[scope]; !ok {
				return usagef("--type is ingest, server, read or admin")
			}
			if name == "" {
				return usagef("Give the key a --name").hint(`Say where it's used: --name "iOS app".`)
			}
			sp := a.spin("Making the key")
			p, err := a.project(cmd.Context(), a.projectRef(nil))
			var out struct {
				Key    json.RawMessage `json:"key"`
				Secret string          `json:"secret"`
			}
			if err == nil {
				err = a.client.JSON(cmd.Context(), api.Request{Method: http.MethodPost, Path: projectPath(p, "keys"),
					Body: map[string]string{"scope": scope, "name": name}}, &out)
			}
			sp.Stop()
			if err != nil {
				return err
			}
			if a.mode.JSON {
				return a.printJSON(map[string]any{"key": out.Key, "secret": out.Secret})
			}
			var k api.APIKey
			_ = json.Unmarshal(out.Key, &k)
			a.done("Made the " + scope + " key " + a.theme.Bold(k.Name) + " in " + p.Name)
			note := "Copy it now: Lyrelog shows it only once."
			if scope != "ingest" {
				note += " It's a secret: keep it out of apps and repositories."
			}
			a.showSecret("Secret", out.Secret, note)
			return nil
		},
	}
	create.Flags().StringVar(&scope, "type", "ingest", "ingest, server, read or admin")
	create.Flags().StringVar(&name, "name", "", "where the key is used")
	_ = create.RegisterFlagCompletionFunc("type", cobra.FixedCompletions([]string{"ingest", "server", "read", "admin"}, cobra.ShellCompDirectiveNoFileComp))

	var yes bool
	revoke := &cobra.Command{
		Use:   "revoke <key-id>",
		Short: "Revoke a key (owners); whatever uses it stops working",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			p, err := a.project(ctx, a.projectRef(nil))
			if err != nil {
				return err
			}
			if err := a.confirm(ctx, "Revoke key "+args[0]+" of "+p.Name+"? Whatever uses it stops working.", yes); err != nil {
				return err
			}
			if err := a.client.JSON(ctx, api.Request{Method: http.MethodDelete, Path: projectPath(p, "keys/"+api.PathEscape(args[0]))}, nil); err != nil {
				return err
			}
			if a.mode.JSON {
				return a.printJSON(map[string]any{"revoked": true, "id": args[0]})
			}
			a.done("Revoked " + args[0])
			return nil
		},
	}
	revoke.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask to confirm")
	cmd.AddCommand(create, revoke)
	return cmd
}
