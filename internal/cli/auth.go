package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"go.lyrelog.com/cli/internal/api"
	"go.lyrelog.com/cli/internal/login"
	"go.lyrelog.com/cli/internal/ui"
)

// loginTimeout: how long the loopback server waits for the browser.
var loginTimeout = 5 * time.Minute

func (a *App) loginCommand() *cobra.Command {
	var noBrowser bool
	var keep string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in, approving this computer in the dashboard",
		Long: `Sign in to Lyrelog. Your browser opens the dashboard, you approve this computer, and the
browser hands the sign-in back to the CLI. Over SSH, without a display, or with --no-browser,
the page shows a code to paste here instead.

The session ends after 14 days unused (30 at most). With --keep 30d, 90d, 1y or never it lasts
that long whether used or not, until you sign it out from your account page (the page asks you
to have signed in to the dashboard recently, and lets you change the choice before approving).
Some actions, like creating a server, read or admin key, need a sign-in from the last 10
minutes: run lyrelog login again for those.

A coding agent can't sign in by itself: a person runs lyrelog login, and the agent uses
that sign-in. In CI, set LYRELOG_TOKEN to an access token: make one with lyrelog tokens
create right after signing in, or on your account page
(https://docs.lyrelog.com/cli/login#scripts-and-ci).`,
		Example: `  lyrelog login
  lyrelog login --keep 90d                    # this computer stays signed in 90 days
  lyrelog login --keep never                  # until you sign it out from the dashboard
  lyrelog login --no-browser
  lyrelog login --url http://127.0.0.1:8090   # the local stack`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch keep {
			case "", "30d", "90d", "1y", "never":
			default:
				return failf(ExitUsage, "--keep is 30d, 90d, 1y or never").hint("Leave it out for a sign-in that ends like a browser's: 14 days unused, 30 at most.")
			}
			a.keep = keep
			return a.login(cmd.Context(), noBrowser)
		},
	}
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "don't open a browser: show a page to visit and ask for the code it gives")
	cmd.Flags().StringVar(&keep, "keep", "", "keep this sign-in 30d, 90d, 1y or never, used or not (default: like a browser, 14 days unused, 30 at most)")
	return cmd
}

func (a *App) login(ctx context.Context, noBrowser bool) error {
	if a.mode.Agent != "" || !a.StdinTTY {
		who := "This session"
		if a.mode.Agent != "" {
			who = "An agent (" + a.mode.Agent + ")"
		}
		return failf(ExitAuth, "%s can't sign in by itself: signing in needs a person", who).
			hint("Ask a person to run `lyrelog login` in a terminal on this computer; the CLI then uses that sign-in. In CI, set LYRELOG_TOKEN to an access token from your account page: https://docs.lyrelog.com/cli/login#scripts-and-ci")
	}
	proof, err := login.NewPKCE()
	if err != nil {
		return err
	}
	name := "a computer"
	if a.Hostname != nil {
		if h, err := a.Hostname(); err == nil && h != "" {
			name = strings.TrimSuffix(h, ".local")
		}
	}
	th := a.theme
	open := a.OpenBrowser
	if open == nil {
		open = func(u string) error { return login.OpenBrowser(a.Getenv, u) }
	}
	var code string
	if !noBrowser && (a.OpenBrowser != nil || login.CanOpenBrowser(a.Getenv)) {
		code, err = a.loginWithBrowser(ctx, proof, name, open)
		if err != nil {
			return err
		}
	}
	if code == "" {
		target := login.AuthorizeURL(a.base, proof.Challenge, "", name, a.keep)
		fmt.Fprintf(a.errOut, "Open this page, approve %s, and paste the code it shows:\n\n  %s\n\n", th.Bold(name), th.Primary(target))
		for code == "" {
			if code, err = a.ask(ctx, "Paste the code:"); err != nil {
				return err
			}
		}
	}
	sp := a.spin("Signing in")
	var grant api.TokenGrant
	err = a.client.JSON(ctx, api.Request{Method: http.MethodPost, Path: "/api/cli/token", NoAuth: true,
		Body: map[string]string{"code": strings.TrimSpace(code), "verifier": proof.Verifier, "name": name}}, &grant)
	sp.Stop()
	var ae *api.Error
	if errors.As(err, &ae) {
		switch {
		case ae.Status == 400:
			return failf(ExitAuth, "That code didn't work").hint("A code works once, within 5 minutes, and only for the login that asked for it. Run `lyrelog login` again.")
		case ae.Status == 404:
			return failf(ExitError, "%s doesn't let the CLI sign in yet", a.base).hint("The server needs updating, or the address is wrong (--url).")
		}
	}
	if err != nil {
		return err
	}
	if grant.Token == "" {
		return failf(ExitError, "The server didn't send a session").hint("Run `lyrelog login` again.")
	}
	h := a.host()
	h.Token, h.ExpiresAt, h.UserEmail, h.UserName = grant.Token, grant.ExpiresAt, grant.User.Email, grant.User.Name
	a.cfg.URL = a.base
	if err := a.save(); err != nil {
		return err
	}
	a.client.Token, a.token = grant.Token, grant.Token

	who := grant.User.Email
	if grant.User.Name != "" {
		who = grant.User.Name + " " + th.Dim("("+grant.User.Email+")")
	}
	if a.mode.JSON {
		return a.printJSON(map[string]any{"user": grant.User, "url": a.base, "expires_at": grant.ExpiresAt})
	}
	a.done("Signed in as " + who + " on " + hostOf(a.base))
	if time.Unix(grant.ExpiresAt, 0).Year() >= 9999 {
		fmt.Fprintf(a.out, "%s %s\n", a.theme.Warning("!"), "This sign-in never expires. Sign it out from your account page (or lyrelog logout) when you're done with this computer.")
	}
	if a.tokenFromEnv {
		fmt.Fprintf(a.out, "%s %s\n", th.Warning("!"), "LYRELOG_TOKEN is set and wins over this sign-in. Unset it to use this one.")
	}
	if h.Project == "" {
		list, _, err := a.projectList(ctx, false)
		if err == nil && len(list) == 1 {
			h.Project, h.ProjectName = list[0].ID, list[0].Name
			_ = a.save()
			a.done("Using " + th.Bold(list[0].Name) + " " + th.Dim("(lyrelog use <project> to change)"))
		} else if err == nil && len(list) > 1 {
			a.tip("Next, pick a project:", "lyrelog use <project>")
		}
	}
	return nil
}

// loginWithBrowser runs the loopback flow; "" with no error means "fall back to a code".
func (a *App) loginWithBrowser(ctx context.Context, proof login.PKCE, name string, open func(string) error) (string, error) {
	th := a.theme
	lb, err := login.Listen(proof.State)
	if err != nil {
		return "", nil
	}
	defer lb.Close()
	target := login.AuthorizeURL(a.base, proof.Challenge, lb.Redirect, name, a.keep)
	if err := open(target); err != nil {
		fmt.Fprintf(a.errOut, "%s Couldn't open a browser.\n", th.Warning("!"))
		return "", nil
	}
	fmt.Fprintf(a.errOut, "Approve %s in the browser window that opened.\n%s\n  %s\n%s\n\n",
		th.Bold(name), th.Dim("If none did, open:"), th.Primary(target), th.Dim("On another computer? Press Enter to get a code to paste instead."))
	sp := a.spin("Waiting for you to approve (Ctrl-C to stop)")
	type got struct {
		code string
		err  error
	}
	done := make(chan got, 1)
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		code, err := lb.Wait(waitCtx, loginTimeout)
		done <- got{code, err}
	}()
	enter := a.lines()
	for {
		select {
		case r := <-done:
			sp.Stop()
			if errors.Is(r.err, login.ErrTimeout) {
				fmt.Fprintf(a.errOut, "%s The browser didn't come back in %s.\n", th.Warning("!"), ui.Duration(loginTimeout))
				return "", nil
			}
			return r.code, r.err
		case _, ok := <-enter:
			if !ok {
				enter = nil // stdin closed: only the browser can answer now
				continue
			}
			sp.Stop()
			return "", nil
		}
	}
}

func hostOf(base string) string {
	return strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
}

func (a *App) logoutCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "End this computer's session and forget it",
		Long: `End the session on the server (so the token stops working everywhere) and remove it from
this computer's settings.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.token == "" {
				if a.mode.JSON {
					return a.printJSON(map[string]any{"signed_out": true, "url": a.base})
				}
				a.done("You weren't signed in to " + hostOf(a.base))
				return nil
			}
			sp := a.spin("Signing out")
			err := a.client.JSON(cmd.Context(), api.Request{Method: http.MethodPost, Path: "/api/logout"}, nil)
			sp.Stop()
			var ae *api.Error
			// A session that already ended is fine; anything else is told, but the token is forgotten anyway.
			if err != nil && !(errors.As(err, &ae) && ae.Status == 401) {
				fmt.Fprintf(a.errOut, "%s Couldn't end the session on the server (%s). It ends by itself after 14 days unused.\n", a.theme.Warning("!"), err)
			}
			if h := a.cfg.Hosts[a.base]; h != nil && h.Token != "" {
				h.Token, h.ExpiresAt = "", 0
				if err := a.save(); err != nil {
					return err
				}
			}
			if a.mode.JSON {
				return a.printJSON(map[string]any{"signed_out": true, "url": a.base})
			}
			a.done("Signed out of " + hostOf(a.base))
			if a.tokenFromEnv {
				fmt.Fprintf(a.out, "%s %s\n", a.theme.Warning("!"), "That was LYRELOG_TOKEN's session: unset the variable too.")
			}
			return nil
		},
	}
}

func (a *App) whoamiCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "whoami",
		Aliases: []string{"status"},
		Short:   "Show who you're signed in as, where, and the default project",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			sp := a.spin("Checking")
			var me api.Me
			raw, err := a.getMap(cmd.Context(), "/api/me", nil)
			if err == nil {
				err = remarshal(raw, &me)
			}
			sp.Stop()
			if err != nil {
				return err
			}
			h := a.host()
			if a.mode.JSON {
				out := map[string]any{"url": a.base, "user": raw["user"], "organizations": raw["organizations"],
					"token_from": map[bool]string{true: "LYRELOG_TOKEN", false: "config"}[a.tokenFromEnv]}
				if h.Project != "" {
					out["project"] = map[string]string{"id": h.Project, "name": h.ProjectName}
				}
				if !a.tokenFromEnv && h.ExpiresAt > 0 {
					out["expires_at"] = h.ExpiresAt
				}
				if me.Admin {
					out["admin"] = true
				}
				return a.printJSON(out)
			}
			th := a.theme
			name := me.User.Email
			if me.User.Name != "" {
				name = me.User.Name + " " + th.Dim("<"+me.User.Email+">")
			}
			a.done("Signed in as " + name)
			row := func(k, v string) { fmt.Fprintf(a.out, "  %s %s\n", th.Dim(fmt.Sprintf("%-14s", k)), v) }
			row("Server", hostOf(a.base))
			if a.tokenFromEnv {
				row("Session", "from LYRELOG_TOKEN")
			} else if h.ExpiresAt > 0 {
				if t := time.Unix(h.ExpiresAt, 0); t.Year() >= 9999 {
					row("Session", th.Warning("never expires")+th.Dim(" (sign it out from your account page)"))
				} else {
					row("Session", "ends by "+t.Local().Format("Jan 2, 15:04")+th.Dim(" at the latest (14 days unused ends it sooner, unless you kept it)"))
				}
			}
			if h.Project != "" {
				row("Project", h.ProjectName+" "+th.Dim(h.Project))
			} else {
				row("Project", th.Dim("none chosen: lyrelog use <project>"))
			}
			for i, o := range me.Organizations {
				label := ""
				if i == 0 {
					label = "Organizations"
				}
				row(label, o.Name+th.Dim(" · "+o.Role+" · "+planName(o.Plan)))
			}
			if me.Admin {
				row("Administrator", "yes")
			}
			return nil
		},
	}
}

func planName(plan string) string {
	switch plan {
	case "free":
		return "Free"
	case "pro":
		return "Pro"
	case "business":
		return "Business"
	case "unlimited":
		return "Unlimited"
	}
	return plan
}
