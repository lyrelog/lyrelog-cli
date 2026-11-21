package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"go.lyrelog.com/cli/internal/api"
	"go.lyrelog.com/cli/internal/ui"
)

// organization finds one of yours by id or name; "" means the default project's, or your only one.
func (a *App) organization(ctx context.Context, ref string) (api.Organization, error) {
	var me api.Me
	if err := a.client.Get(ctx, "/api/me", nil, &me); err != nil {
		return api.Organization{}, err
	}
	names := func() string {
		var n []string
		for _, o := range me.Organizations {
			n = append(n, fmt.Sprintf("%s (%d)", o.Name, o.ID))
		}
		return strings.Join(n, ", ")
	}
	if ref == "" {
		if len(me.Organizations) == 1 {
			return me.Organizations[0], nil
		}
		if pref := a.projectRef(nil); pref != "" {
			if p, err := a.project(ctx, pref); err == nil {
				for _, o := range me.Organizations {
					if o.ID == p.OrganizationID {
						return o, nil
					}
				}
			}
		}
		if len(me.Organizations) == 0 {
			return api.Organization{}, failf(ExitNotFound, "You're not in any organization")
		}
		return api.Organization{}, usagef("Which organization?").hint("Pass --org with one of: " + names() + ".")
	}
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		for _, o := range me.Organizations {
			if o.ID == id {
				return o, nil
			}
		}
	}
	for _, o := range me.Organizations {
		if strings.EqualFold(o.Name, ref) {
			return o, nil
		}
	}
	return api.Organization{}, failf(ExitNotFound, "No organization %q among yours", ref).hint("Yours: " + names() + ".")
}

func orgPath(o api.Organization, rest string) string {
	p := "/api/organizations/" + strconv.FormatInt(o.ID, 10)
	if rest != "" {
		p += "/" + rest
	}
	return p
}

func (a *App) orgsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "orgs",
		Aliases: []string{"org", "organizations"},
		Short:   "Your organizations, their people and invitations",
		Example: `  lyrelog orgs
  lyrelog orgs show
  lyrelog orgs invite --role member --email ana@example.com
  lyrelog orgs role ana@example.com owner`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			sp := a.spin("Loading organizations")
			raw, err := a.getMap(cmd.Context(), "/api/me", nil)
			sp.Stop()
			if err != nil {
				return err
			}
			if a.mode.JSON {
				return a.printRaw(rawList(raw, "organizations"))
			}
			orgs := decodeInto[api.Organization](raw["organizations"])
			t := ui.Table{Columns: []ui.Column{{Header: "ID", Right: true}, {Header: "Organization", Flex: true}, {Header: "Your role"}, {Header: "Plan"}}}
			for _, o := range orgs {
				plan := planName(o.Plan)
				if o.PlanUntil != nil {
					plan += a.theme.Dim(" until " + o.PlanUntil.Local().Format("Jan 2, 2006"))
				}
				t.Add(strconv.FormatInt(o.ID, 10), a.theme.Bold(o.Name), o.Role, plan)
			}
			t.Render(a.out, a.theme, a.Width)
			return nil
		},
	}
	var org string
	withOrg := func(c *cobra.Command) *cobra.Command {
		c.Flags().StringVar(&org, "org", "", "organization (id or name; default: your only one, or the default project's)")
		return c
	}

	show := &cobra.Command{
		Use:   "show [org]",
		Short: "List an organization's people and open invitations",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := org
			if len(args) == 1 {
				ref = args[0]
			}
			sp := a.spin("Loading people")
			o, err := a.organization(cmd.Context(), ref)
			var raw map[string]json.RawMessage
			if err == nil {
				raw, err = a.getMap(cmd.Context(), orgPath(o, ""), nil)
			}
			sp.Stop()
			if err != nil {
				return err
			}
			if a.mode.JSON {
				return a.printJSON(raw)
			}
			var people api.OrganizationPeople
			_ = remarshal(raw, &people)
			a.renderOrg(o, people)
			return nil
		},
	}

	rename := withOrg(&cobra.Command{
		Use:     "rename <name>",
		Short:   "Rename an organization (owners)",
		Example: `  lyrelog orgs rename "Ana's apps"`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := a.organization(cmd.Context(), org)
			if err != nil {
				return err
			}
			var out map[string]json.RawMessage
			if err := a.client.JSON(cmd.Context(), api.Request{Method: http.MethodPatch, Path: orgPath(o, ""), Body: map[string]string{"name": args[0]}}, &out); err != nil {
				return err
			}
			if a.mode.JSON {
				return a.printJSON(out)
			}
			a.done("Renamed to " + a.theme.Bold(args[0]))
			return nil
		},
	})

	var role, email string
	invite := withOrg(&cobra.Command{
		Use:   "invite",
		Short: "Invite someone to an organization (owners)",
		Long:  "Make an invitation link (7 days, works once), or email it with --email.",
		Example: `  lyrelog orgs invite
  lyrelog orgs invite --role owner --email ana@example.com`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if role != "owner" && role != "member" {
				return usagef("--role is owner or member")
			}
			o, err := a.organization(cmd.Context(), org)
			if err != nil {
				return err
			}
			body := map[string]string{"role": role}
			if email != "" {
				body["email"] = email
			}
			var out struct {
				Invite  api.Invitation `json:"invite"`
				Path    string         `json:"path"`
				Emailed bool           `json:"emailed"`
			}
			if err := a.client.JSON(cmd.Context(), api.Request{Method: http.MethodPost, Path: orgPath(o, "invites"), Body: body}, &out); err != nil {
				return err
			}
			link := a.base + out.Path
			if a.mode.JSON {
				return a.printJSON(map[string]any{"invite": out.Invite, "link": link, "emailed": out.Emailed})
			}
			if out.Emailed {
				a.done("Invitation emailed to " + a.theme.Bold(email))
			} else {
				a.done("Invitation made")
			}
			a.showSecret("Link (works once, for 7 days)", link, "Anyone holding the link can join "+o.Name+" as "+role+".")
			return nil
		},
	})
	invite.Flags().StringVar(&role, "role", "member", "owner or member")
	invite.Flags().StringVar(&email, "email", "", "email the invitation to this address")

	revokeInvite := withOrg(&cobra.Command{
		Use:   "revoke-invite <invite-id>",
		Short: "Cancel an open invitation (owners)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := a.organization(cmd.Context(), org)
			if err != nil {
				return err
			}
			if err := a.client.JSON(cmd.Context(), api.Request{Method: http.MethodDelete, Path: orgPath(o, "invites/"+api.PathEscape(args[0]))}, nil); err != nil {
				return err
			}
			if a.mode.JSON {
				return a.printJSON(map[string]any{"revoked": true, "invite": args[0]})
			}
			a.done("Invitation cancelled")
			return nil
		},
	})

	setRole := withOrg(&cobra.Command{
		Use:     "role <member> <owner|member>",
		Short:   "Change someone's role (owners)",
		Long:    "Change someone's role. The member is their email or user id. An organization always keeps an owner.",
		Example: `  lyrelog orgs role ana@example.com owner`,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[1] != "owner" && args[1] != "member" {
				return usagef("The role is owner or member")
			}
			o, m, err := a.member(cmd.Context(), org, args[0])
			if err != nil {
				return err
			}
			if err := a.client.JSON(cmd.Context(), api.Request{Method: http.MethodPatch, Path: orgPath(o, "members/"+strconv.FormatInt(m.UserID, 10)), Body: map[string]string{"role": args[1]}}, nil); err != nil {
				return err
			}
			if a.mode.JSON {
				return a.printJSON(map[string]any{"user_id": m.UserID, "email": m.Email, "role": args[1]})
			}
			a.done(m.Email + " is now " + a.theme.Bold(args[1]) + " of " + o.Name)
			return nil
		},
	})

	var yes bool
	remove := withOrg(&cobra.Command{
		Use:   "remove <member>",
		Short: "Remove someone from an organization (owners; or yourself, to leave)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			o, m, err := a.member(ctx, org, args[0])
			if err != nil {
				return err
			}
			if err := a.confirm(ctx, fmt.Sprintf("Remove %s from %s?", m.Email, o.Name), yes); err != nil {
				return err
			}
			if err := a.client.JSON(ctx, api.Request{Method: http.MethodDelete, Path: orgPath(o, "members/"+strconv.FormatInt(m.UserID, 10))}, nil); err != nil {
				return err
			}
			if a.mode.JSON {
				return a.printJSON(map[string]any{"removed": true, "user_id": m.UserID, "email": m.Email})
			}
			a.done("Removed " + m.Email + " from " + o.Name)
			return nil
		},
	})
	remove.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask to confirm")

	cmd.AddCommand(show, rename, invite, revokeInvite, setRole, remove)
	return cmd
}

// member finds someone in an organization by email or user id.
func (a *App) member(ctx context.Context, orgRef, ref string) (api.Organization, api.Member, error) {
	o, err := a.organization(ctx, orgRef)
	if err != nil {
		return o, api.Member{}, err
	}
	var people api.OrganizationPeople
	if err := a.client.Get(ctx, orgPath(o, ""), nil, &people); err != nil {
		return o, api.Member{}, err
	}
	for _, m := range people.Members {
		if strings.EqualFold(m.Email, ref) || strconv.FormatInt(m.UserID, 10) == ref {
			return o, m, nil
		}
	}
	return o, api.Member{}, failf(ExitNotFound, "Nobody called %q in %s", ref, o.Name).hint("`lyrelog orgs show` lists its people.")
}

func (a *App) renderOrg(o api.Organization, people api.OrganizationPeople) {
	th, now := a.theme, a.Now()
	fmt.Fprintf(a.out, "%s %s\n\n", th.Bold(people.Organization), th.Dim(fmt.Sprintf("%d · %s plan · you're %s", o.ID, planName(o.Plan), people.Role)))
	t := ui.Table{Columns: []ui.Column{{Header: "Name", Flex: true}, {Header: "Email", Flex: true}, {Header: "Role"}, {Header: "Since"}, {Header: "Last sign-in"}}}
	for _, m := range people.Members {
		last := th.Dim("never")
		if m.LastLogin != nil {
			last = ui.Ago(*m.LastLogin, now)
		}
		role := m.Role
		if role == "owner" {
			role = th.Primary(role)
		}
		t.Add(th.Bold(m.Name), m.Email, role, m.Since.Local().Format("Jan 2, 2006"), last)
	}
	t.Render(a.out, th, a.Width)
	if len(people.Invites) > 0 {
		fmt.Fprintf(a.out, "\n%s\n", th.Bold("Open invitations"))
		it := ui.Table{Columns: []ui.Column{{Header: "ID", Right: true}, {Header: "Role"}, {Header: "Sent to", Flex: true}, {Header: "By"}, {Header: "Expires"}}}
		for _, i := range people.Invites {
			to := i.Email
			if to == "" {
				to = th.Dim("a link")
			}
			it.Add(strconv.FormatInt(i.ID, 10), i.Role, to, i.CreatedBy, ui.Ago(i.ExpiresAt, now))
		}
		it.Render(a.out, th, a.Width)
	}
}

func (a *App) usageCommand() *cobra.Command {
	var org string
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "This month's events against your plan, per organization",
		Example: `  lyrelog usage
  lyrelog usage --org "Ana's apps"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			sp := a.spin("Counting this month's events")
			defer sp.Stop()
			var orgs []api.Organization
			if org != "" {
				o, err := a.organization(ctx, org)
				if err != nil {
					return err
				}
				orgs = []api.Organization{o}
			} else {
				var me api.Me
				if err := a.client.Get(ctx, "/api/me", nil, &me); err != nil {
					return err
				}
				orgs = me.Organizations
			}
			var raws []map[string]json.RawMessage
			var usages []api.Usage
			for _, o := range orgs {
				raw, err := a.getMap(ctx, orgPath(o, "usage"), nil)
				if err != nil {
					return err
				}
				raw["organization_id"], _ = json.Marshal(o.ID)
				raws = append(raws, raw)
				var u api.Usage
				_ = remarshal(raw, &u)
				usages = append(usages, u)
			}
			sp.Stop()
			if a.mode.JSON {
				if raws == nil {
					raws = []map[string]json.RawMessage{}
				}
				return a.printJSON(raws)
			}
			for i, u := range usages {
				if i > 0 {
					a.println()
				}
				a.renderUsage(u)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&org, "org", "", "one organization (id or name; default: all of yours)")
	return cmd
}

func (a *App) renderUsage(u api.Usage) {
	th := a.theme
	plan := planName(u.Plan)
	if u.PlanUntil != nil {
		plan += " until " + u.PlanUntil.Local().Format("Jan 2, 2006")
	}
	fmt.Fprintf(a.out, "%s %s\n", th.Bold(u.Organization), th.Dim("· "+plan+" plan"))
	limit := u.Limits.IncludedEvents
	resets := u.NextMonth.Local().Format("Jan 2")
	if limit > 0 {
		share := float64(u.Events) / float64(limit)
		width := 30
		if a.Width > 0 {
			width = max(min(width, a.Width-8), 10) // "  " + the bar + " 100%"
		}
		filled, empty := ui.Bar(share, width)
		switch {
		case share >= 1:
			filled = th.Error(filled)
		case share >= 0.8:
			filled = th.Warning(filled)
		default:
			filled = th.Primary(filled)
		}
		fmt.Fprintf(a.out, "  %s%s %s\n", filled, th.Dim(empty), ui.Percent(share))
		what := "included"
		if u.Limits.HardEvents > 0 {
			what = "a month"
		}
		a.aside(fmt.Sprintf("%s of %s events %s", th.Bold(ui.Number(u.Events)), ui.Number(limit), what), "· counts start again on "+resets)
		if u.Limits.HardEvents > 0 && u.Events >= u.Limits.HardEvents {
			fmt.Fprintf(a.out, "  %s\n", th.Error("New events are refused until "+resets+"."))
		}
	} else {
		a.aside(th.Bold(ui.Number(u.Events))+" events this month", "· no limit · counts start again on "+resets)
	}
	if len(u.Days) > 0 {
		// The month's days so far (UTC, as the server counts them), not the ones still to come:
		// they'd be blank and push the label away from the bars.
		end := u.NextMonth
		if today := a.Now().UTC().Truncate(24 * time.Hour).Add(24 * time.Hour); today.After(u.MonthStart) && today.Before(end) {
			end = today
		}
		counts := make([]int64, 0, u.DaysInMonth)
		byDay := map[string]int64{}
		for _, d := range u.Days {
			byDay[d.Day] = d.Events
		}
		for d := u.MonthStart; d.Before(end) && len(counts) < 31; d = d.Add(24 * time.Hour) {
			counts = append(counts, byDay[d.UTC().Format("2006-01-02")])
		}
		first, last := u.MonthStart.UTC(), u.MonthStart.UTC().AddDate(0, 0, len(counts)-1)
		span := first.Format("Jan 2")
		if len(counts) > 1 {
			span += " to " + last.Format("2")
		}
		a.aside(a.sparkline(counts), "per day, "+span)
	}
	if len(u.Projects) > 0 {
		a.println()
		t := ui.Table{Columns: []ui.Column{{Header: "  Project", Flex: true}, {Header: "Events", Right: true}, {Header: "Share", Right: true}}}
		for _, p := range u.Projects {
			share := "–"
			if u.Events > 0 {
				share = ui.Percent(float64(p.Events) / float64(u.Events))
			}
			t.Add("  "+p.Name, ui.Number(p.Events), share)
		}
		t.Render(a.out, th, a.Width)
	}
}

// aside writes "  text aside" (the aside dim) on one line, or the aside on the next, without its
// leading "· ", when the line would be wider than the terminal.
func (a *App) aside(text, aside string) {
	if a.Width > 0 && 2+ui.Width(text)+1+ui.Width(aside) > a.Width {
		fmt.Fprintf(a.out, "  %s\n  %s\n", text, a.theme.Dim(strings.TrimPrefix(aside, "· ")))
		return
	}
	fmt.Fprintf(a.out, "  %s %s\n", text, a.theme.Dim(aside))
}

// sparkline draws a day's events as a bar in cobalt, and a day without any as a dim dot, so the
// month keeps its shape.
func (a *App) sparkline(counts []int64) string {
	var b strings.Builder
	line := []rune(ui.Sparkline(counts))
	for i := 0; i < len(line); {
		j := i
		for j < len(line) && (line[j] == ' ') == (line[i] == ' ') {
			j++
		}
		if line[i] == ' ' {
			b.WriteString(a.theme.Dim(strings.Repeat("·", j-i)))
		} else {
			b.WriteString(a.theme.Primary(string(line[i:j])))
		}
		i = j
	}
	return b.String()
}
