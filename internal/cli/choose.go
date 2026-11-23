package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// choice is one line of a question with options.
type choice struct {
	label, hint string
	checked     bool
}

// choose asks a person to pick from options: one (multi false) or several (multi true, the
// checked ones ticked to start with). At a real terminal it's a list moved with the arrows;
// otherwise (stdin from a pipe, tests) a numbered list answered with numbers. It returns the
// chosen indexes, in order. Only for people: callers check a.mode.Interactive first.
func (a *App) choose(ctx context.Context, title string, items []choice, multi bool) ([]int, error) {
	if f, ok := a.Stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) && a.lineCh == nil {
		return a.chooseRaw(f, title, items, multi)
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

// chooseRaw draws the list on stderr and reads keys: up and down (or k and j) move, space ticks
// (several) or Enter chooses (one), a ticks or unticks them all, Esc, q or Ctrl-C give up. The
// list is redrawn in place and replaced by one line with the answer when done.
func (a *App) chooseRaw(f *os.File, title string, items []choice, multi bool) ([]int, error) {
	old, err := term.MakeRaw(int(f.Fd()))
	if err != nil {
		return a.chooseLines(context.Background(), title, items, multi)
	}
	defer term.Restore(int(f.Fd()), old)

	th := a.theme
	checked := make([]bool, len(items))
	for i, it := range items {
		checked[i] = it.checked
	}
	cursor, drawn, warn := 0, 0, ""
	help := "↑↓ move · enter choose"
	if multi {
		help = "↑↓ move · space tick · a all · enter done"
	}
	draw := func() {
		if drawn > 0 {
			fmt.Fprintf(a.errOut, "\x1b[%dA\r\x1b[J", drawn)
		}
		var b strings.Builder
		b.WriteString(th.PrimaryBold("? ") + title + "\r\n")
		for i, it := range items {
			pointer := "  "
			if i == cursor {
				pointer = th.PrimaryBold("› ")
			}
			box := ""
			if multi {
				box = "[ ] "
				if checked[i] {
					box = th.PrimaryBold("[x]") + " "
				}
			}
			label := it.label
			if i == cursor {
				label = th.Bold(label)
			}
			b.WriteString(pointer + box + label)
			if it.hint != "" {
				b.WriteString(" " + th.Dim(it.hint))
			}
			b.WriteString("\r\n")
		}
		if warn != "" {
			b.WriteString("  " + th.Warning(warn) + "\r\n")
		} else {
			b.WriteString("  " + th.Dim(help) + "\r\n")
		}
		fmt.Fprint(a.errOut, b.String())
		drawn = len(items) + 2
	}
	finish := func(picked []int) []int {
		fmt.Fprintf(a.errOut, "\x1b[%dA\r\x1b[J", drawn)
		names := make([]string, len(picked))
		for i, p := range picked {
			names[i] = items[p].label
		}
		fmt.Fprintf(a.errOut, "%s %s %s\r\n", th.Mark("ok"), title, th.Bold(strings.Join(names, ", ")))
		return picked
	}

	draw()
	buf := make([]byte, 64)
	for {
		n, err := f.Read(buf)
		if err != nil {
			return nil, err
		}
		for _, key := range splitKeys(buf[:n]) {
			warn = ""
			switch key {
			case "\x1b[A", "k":
				cursor = (cursor - 1 + len(items)) % len(items)
			case "\x1b[B", "j", "\t":
				cursor = (cursor + 1) % len(items)
			case " ":
				if multi {
					checked[cursor] = !checked[cursor]
				}
			case "a":
				if multi {
					all := true
					for _, c := range checked {
						all = all && c
					}
					for i := range checked {
						checked[i] = !all
					}
				}
			case "\r", "\n":
				if !multi {
					return finish([]int{cursor}), nil
				}
				var picked []int
				for i, c := range checked {
					if c {
						picked = append(picked, i)
					}
				}
				if len(picked) > 0 {
					return finish(picked), nil
				}
				warn = "Tick at least one with space"
			case "\x1b", "q", "\x03":
				fmt.Fprintf(a.errOut, "\x1b[%dA\r\x1b[J", drawn)
				return nil, failf(ExitInterrupted, "Nothing was installed")
			}
		}
		draw()
	}
}

// splitKeys cuts what one read gave into keys: an arrow is three bytes (Esc [ A), anything
// else one; keys typed fast arrive in the same read.
func splitKeys(b []byte) []string {
	var keys []string
	for i := 0; i < len(b); {
		if b[i] == 0x1b && i+2 < len(b) && b[i+1] == '[' {
			keys = append(keys, string(b[i:i+3]))
			i += 3
			continue
		}
		keys = append(keys, string(b[i:i+1]))
		i++
	}
	return keys
}
