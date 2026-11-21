package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"go.lyrelog.com/cli/internal/api"
	"go.lyrelog.com/cli/internal/ui"
)

// session is one of GET /api/account/sessions: a browser, a CLI sign-in or an access token.
type session struct {
	ID         int64     `json:"id"`
	Kind       string    `json:"kind"`
	Name       string    `json:"name"`
	UserAgent  string    `json:"user_agent"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	// IdleEnds: it also ends after 14 days unused. nil from a server that doesn't say.
	IdleEnds *bool `json:"idle_ends"`
	Current  bool  `json:"current"`
}

// never: the server writes "no expiry" as the year 9999.
func (s session) never() bool { return s.ExpiresAt.Year() >= 9999 }

// sessions is GET /api/account/sessions, split: the server's own objects (for JSON) and typed,
// keeping those whose kind is (token) or isn't (anything else) a token.
func (a *App) sessions(ctx context.Context, tokens bool) ([]json.RawMessage, []session, error) {
	raw, err := a.getMap(ctx, "/api/account/sessions", nil)
	if err != nil {
		return nil, nil, err
	}
	var raws []json.RawMessage
	_ = json.Unmarshal(rawList(raw, "sessions"), &raws)
	outRaw, out := []json.RawMessage{}, []session{}
	for _, r := range raws {
		var s session
		if json.Unmarshal(r, &s) != nil || (s.Kind == "token") != tokens {
			continue
		}
		outRaw, out = append(outRaw, r), append(out, s)
	}
	return outRaw, out, nil
}

// expiryDays reads --expires: 30d, 90d, 1y or never, as days (0 for never).
func expiryDays(v string) (int, error) {
	switch v {
	case "30d":
		return 30, nil
	case "90d":
		return 90, nil
	case "1y":
		return 365, nil
	case "never":
		return 0, nil
	}
	return 0, usagef("--expires is 30d, 90d, 1y or never").hint("Prefer an expiry: a token that never expires works for anyone who gets hold of it, until you revoke it.")
}

// day writes an expiry date as the account page does: "Feb 3, 2027".
func day(t time.Time) string { return t.Local().Format("Jan 2, 2006") }

const neverWarning = "This token never expires. Keep it in your CI's secrets, never in a repository, and revoke it (lyrelog tokens revoke) when you no longer need it."

func (a *App) neverExpires() {
	fmt.Fprintf(a.out, "%s %s\n", a.theme.Warning("⚠"), a.theme.Warning(neverWarning))
}

// neverNeedsYes: a token that never expires is made (or kept) on purpose; a program says so
// with --yes, since nobody reads the warning.
func (a *App) neverNeedsYes(days int, yes bool) error {
	if days == 0 && a.mode.JSON && !yes {
		return usagef("A token that never expires needs --yes here").hint("Add --yes to confirm, or give it an expiry: --expires 30d, 90d or 1y.")
	}
	return nil
}

func (a *App) tokensCommand() *cobra.Command {
	list := func(cmd *cobra.Command, args []string) error {
		sp := a.spin("Loading access tokens")
		raw, list, err := a.sessions(cmd.Context(), true)
		sp.Stop()
		if err != nil {
			return err
		}
		if a.mode.JSON {
			return a.printJSON(raw)
		}
		if len(list) == 0 {
			a.println("No access tokens.")
			a.tip("Make one for CI, right after lyrelog login:", `lyrelog tokens create --name "GitHub Actions"`)
			return nil
		}
		th, now := a.theme, a.Now()
		t := ui.Table{Columns: []ui.Column{{Header: "Name", Keep: true}, {Header: "ID", Keep: true}, {Header: "Expires"}, {Header: "Last used"}}}
		for _, s := range list {
			expires := day(s.ExpiresAt)
			if s.never() {
				expires = th.Warning("⚠ never")
			}
			t.Add(th.Bold(s.Name), th.Dim(strconv.FormatInt(s.ID, 10)), expires, ui.Ago(s.LastUsedAt, now))
		}
		t.Render(a.out, th, a.Width)
		return nil
	}
	cmd := &cobra.Command{
		Use:     "tokens",
		Aliases: []string{"token"},
		Short:   "Access tokens for CI and scripts (LYRELOG_TOKEN): list, create, expiry, revoke",
		Long: `Access tokens (lyr_tok_...) let CI and scripts use the CLI or the API as you: set one as
LYRELOG_TOKEN. Unlike a sign-in, a token doesn't end when unused; it lasts until it expires or
you revoke it. The account page in the dashboard lists and makes them too.

Making a token, or making one last longer, needs a sign-in from the last 10 minutes: run
lyrelog login just before. A token can't make or change tokens.`,
		Example: `  lyrelog tokens
  lyrelog tokens create --name "GitHub Actions, deploy"
  lyrelog tokens create --name nightly-export --expires 1y
  lyrelog tokens expiry 45 --expires 30d
  lyrelog tokens revoke 45`,
		Args: cobra.NoArgs,
		RunE: list,
	}
	cmd.AddCommand(&cobra.Command{Use: "list", Short: "List your access tokens", Args: cobra.NoArgs, RunE: list})

	var name, expires string
	var yesCreate bool
	create := &cobra.Command{
		Use:   "create",
		Short: "Make an access token; it's shown once",
		Long: `Make an access token for CI or a script. It's shown once: store it in your CI's secrets as
LYRELOG_TOKEN. It expires after 30 days, 90 (the default), a year, or never.

Needs a sign-in from the last 10 minutes (run lyrelog login just before), and can't be done
with an access token. In JSON mode the token is the "token" field, and --expires never needs --yes.`,
		Example: `  lyrelog tokens create --name "GitHub Actions, deploy"
  lyrelog tokens create --name nightly-export --expires 1y
  lyrelog tokens create --name ci --json | jq -r .token`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return usagef("Give the token a --name").hint(`Say what will use it: --name "GitHub Actions, deploy".`)
			}
			days, err := expiryDays(expires)
			if err != nil {
				return err
			}
			if err := a.neverNeedsYes(days, yesCreate); err != nil {
				return err
			}
			sp := a.spin("Making the token")
			var out struct {
				Token        string          `json:"token"`
				Session      json.RawMessage `json:"session"`
				NeverExpires bool            `json:"never_expires"`
			}
			err = a.client.JSON(cmd.Context(), api.Request{Method: http.MethodPost, Path: "/api/account/tokens",
				Body: map[string]any{"name": name, "expires_in_days": days}}, &out)
			sp.Stop()
			if err != nil {
				return err
			}
			if a.mode.JSON {
				return a.printJSON(map[string]any{"token": out.Token, "session": out.Session, "never_expires": out.NeverExpires})
			}
			var s session
			_ = json.Unmarshal(out.Session, &s)
			when := "never expires"
			if !out.NeverExpires && !s.never() {
				when = "expires " + day(s.ExpiresAt)
			}
			a.done("Made the access token " + a.theme.Bold(s.Name) + ", it " + when)
			a.showSecret("Token", out.Token, "Copy it now: Lyrelog shows it only once. It's a secret: store it in your CI's secrets as LYRELOG_TOKEN.")
			if out.NeverExpires {
				a.neverExpires()
			}
			return nil
		},
	}
	create.Flags().StringVar(&name, "name", "", "what will use it, like the CI")
	create.Flags().StringVar(&expires, "expires", "90d", "30d, 90d, 1y or never")
	create.Flags().BoolVarP(&yesCreate, "yes", "y", false, "confirm --expires never (needed in JSON mode)")
	_ = create.RegisterFlagCompletionFunc("expires", expiryCompletion)

	var newExpires string
	var yesExpiry bool
	expiry := &cobra.Command{
		Use:   "expiry <token-id>",
		Short: "Change when an access token expires, counted from now",
		Long: `Change when an access token expires: 30 days, 90, a year from now, or never. Sooner works
any time; later, or never, needs a sign-in from the last 10 minutes (lyrelog login just before).
In JSON mode --expires never needs --yes.`,
		Example: `  lyrelog tokens expiry 45 --expires 30d
  lyrelog tokens expiry 45 --expires 1y`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if newExpires == "" {
				return usagef("When should it expire?").hint("Pass --expires 30d, 90d, 1y or never.")
			}
			days, err := expiryDays(newExpires)
			if err != nil {
				return err
			}
			if err := a.neverNeedsYes(days, yesExpiry); err != nil {
				return err
			}
			sp := a.spin("Changing the expiry")
			var out struct {
				Session      json.RawMessage `json:"session"`
				NeverExpires bool            `json:"never_expires"`
			}
			err = a.client.JSON(cmd.Context(), api.Request{Method: http.MethodPatch, Path: "/api/account/tokens/" + api.PathEscape(args[0]),
				Body: map[string]any{"expires_in_days": days}}, &out)
			sp.Stop()
			if err != nil {
				return err
			}
			if a.mode.JSON {
				return a.printJSON(map[string]any{"session": out.Session, "never_expires": out.NeverExpires})
			}
			var s session
			_ = json.Unmarshal(out.Session, &s)
			if out.NeverExpires {
				a.done(a.theme.Bold(s.Name) + " never expires now")
				a.neverExpires()
				return nil
			}
			a.done(a.theme.Bold(s.Name) + " now expires " + day(s.ExpiresAt))
			return nil
		},
	}
	expiry.Flags().StringVar(&newExpires, "expires", "", "30d, 90d, 1y or never, from now")
	expiry.Flags().BoolVarP(&yesExpiry, "yes", "y", false, "confirm --expires never (needed in JSON mode)")
	_ = expiry.RegisterFlagCompletionFunc("expires", expiryCompletion)

	var yesRevoke bool
	revoke := &cobra.Command{
		Use:   "revoke <token-id>",
		Short: "Revoke an access token; whatever uses it stops working",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			_, list, err := a.sessions(ctx, true)
			if err != nil {
				return err
			}
			var token *session
			for i := range list {
				if strconv.FormatInt(list[i].ID, 10) == args[0] {
					token = &list[i]
				}
			}
			if token == nil {
				return failf(ExitNotFound, "No access token %s", args[0]).hint("`lyrelog tokens` lists yours. To sign out a browser or a computer, use `lyrelog account sign-out`.")
			}
			if err := a.confirm(ctx, "Revoke the access token "+token.Name+"? Whatever uses it stops working.", yesRevoke); err != nil {
				return err
			}
			if err := a.client.JSON(ctx, api.Request{Method: http.MethodDelete, Path: "/api/account/sessions/" + api.PathEscape(args[0])}, nil); err != nil {
				return err
			}
			if a.mode.JSON {
				return a.printJSON(map[string]any{"revoked": true, "id": token.ID})
			}
			a.done("Revoked " + token.Name)
			return nil
		},
	}
	revoke.Flags().BoolVarP(&yesRevoke, "yes", "y", false, "don't ask to confirm")
	cmd.AddCommand(create, expiry, revoke)
	return cmd
}

var expiryCompletion = cobra.FixedCompletions([]string{"30d", "90d", "1y", "never"}, cobra.ShellCompDirectiveNoFileComp)
