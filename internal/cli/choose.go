package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	"golang.org/x/term"
)

// choice is one line of a question with options.
type choice struct {
	label, hint string
	checked     bool
}

// choose asks a person to pick from options: one (multi false) or several (multi true, the
// checked ones ticked to start with). At a real terminal it's huh's list, moved with the arrows;
// otherwise (stdin from a pipe, tests) a numbered list answered with numbers. It returns the
// chosen indexes, in order. Only for people: callers check a.mode.Interactive first.
func (a *App) choose(ctx context.Context, title string, items []choice, multi bool) ([]int, error) {
	if f, ok := a.Stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) && a.lineCh == nil {
		return a.chooseForm(ctx, f, title, items, multi)
	}
	return a.chooseLines(ctx, title, items, multi)
}

// chooseLines: the options numbered, answered with a number (or numbers, comma-separated);
// Enter alone keeps the ticked ones (or the first, for one).
func (a *App) chooseLines(ctx context.Context, title string, items []choice, multi bool) ([]int, error) {
	th := a.theme
	fmt.Fprintln(a.errOut, th.PrimaryBold("? ")+title)
	var keep []int
	for i, it := range items {
		mark := " "
		if it.checked {
			mark = "x"
			keep = append(keep, i)
		}
		box := fmt.Sprintf("%d)", i+1)
		if multi {
			box += " [" + mark + "]"
		}
		line := "  " + box + " " + it.label
		if it.hint != "" {
			line += " " + th.Dim(it.hint)
		}
		fmt.Fprintln(a.errOut, line)
	}
	question := "Number"
	if multi {
		question = "Numbers, separated by commas (Enter keeps the ticked ones)"
	} else {
		keep = []int{0}
		question = "Number (Enter for 1)"
	}
	for {
		answer, err := a.ask(ctx, question+":")
		if err != nil {
			return nil, err
		}
		if answer == "" && len(keep) > 0 {
			return keep, nil
		}
		var picked []int
		bad := false
		for _, part := range strings.FieldsFunc(answer, func(r rune) bool { return r == ',' || r == ' ' }) {
			n, err := strconv.Atoi(part)
			if err != nil || n < 1 || n > len(items) {
				bad = true
				break
			}
			if !containsInt(picked, n-1) {
				picked = append(picked, n-1)
			}
		}
		if !bad && len(picked) > 0 && (multi || len(picked) == 1) {
			return picked, nil
		}
		fmt.Fprintf(a.errOut, "  %s\n", th.Warning(fmt.Sprintf("Type a number from 1 to %d", len(items))))
	}
}

func containsInt(list []int, n int) bool {
	for _, x := range list {
		if x == n {
			return true
		}
	}
	return false
}

// chooseForm asks with huh (charm.land/huh): a list moved with the arrows, ticked with space
// for several, in Lyrelog's cobalt, drawn on stderr. Esc or Ctrl-C give up.
func (a *App) chooseForm(ctx context.Context, f *os.File, title string, items []choice, multi bool) ([]int, error) {
	label := func(it choice) string {
		if it.hint == "" {
			return it.label
		}
		return it.label + "  " + a.theme.Dim(it.hint)
	}
	var field huh.Field
	var one int
	var several []int
	if multi {
		opts := make([]huh.Option[int], len(items))
		for i, it := range items {
			opts[i] = huh.NewOption(label(it), i).Selected(it.checked)
		}
		field = huh.NewMultiSelect[int]().Title(title).Options(opts...).Value(&several).
			Validate(func(v []int) error {
				if len(v) == 0 {
					return errors.New("tick at least one with space")
				}
				return nil
			})
	} else {
		opts := make([]huh.Option[int], len(items))
		for i, it := range items {
			opts[i] = huh.NewOption(label(it), i)
		}
		field = huh.NewSelect[int]().Title(title).Options(opts...).Value(&one)
	}
	form := huh.NewForm(huh.NewGroup(field)).
		WithInput(f).WithOutput(a.errOut).WithTheme(huh.ThemeFunc(a.formTheme)).WithShowHelp(true)
	if err := form.RunWithContext(ctx); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil, failf(ExitInterrupted, "Nothing was installed")
		}
		return nil, err
	}
	picked := []int{one}
	if multi {
		picked = several
		sort.Ints(picked)
	}
	names := make([]string, len(picked))
	for i, p := range picked {
		names[i] = items[p].label
	}
	fmt.Fprintf(a.errOut, "%s %s %s\n", a.theme.Mark("ok"), title, a.theme.Bold(strings.Join(names, ", ")))
	return picked, nil
}

// formTheme is huh's plain theme with Lyrelog's cobalt for what's focused and ticked.
func (a *App) formTheme(isDark bool) *huh.Styles {
	st := huh.ThemeBase(isDark)
	cobalt := lipgloss.Color("#2b59ff")
	if isDark {
		cobalt = lipgloss.Color("#6d8bff")
	}
	st.Focused.Title = st.Focused.Title.Foreground(cobalt).Bold(true)
	st.Focused.SelectSelector = st.Focused.SelectSelector.Foreground(cobalt)
	st.Focused.MultiSelectSelector = st.Focused.MultiSelectSelector.Foreground(cobalt)
	st.Focused.SelectedPrefix = st.Focused.SelectedPrefix.Foreground(cobalt)
	st.Focused.SelectedOption = st.Focused.SelectedOption.Foreground(cobalt)
	return st
}
