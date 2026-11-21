package cli

import (
	"bufio"
	"context"
	"fmt"
	"strings"
)

// lines reads stdin a line at a time in one goroutine, so a wait can listen for Enter and a
// later question can take the next line.
func (a *App) lines() <-chan string {
	if a.lineCh == nil {
		ch := make(chan string)
		a.lineCh = ch
		go func() {
			defer close(ch)
			sc := bufio.NewScanner(a.Stdin)
			for sc.Scan() {
				ch <- sc.Text()
			}
		}()
	}
	return a.lineCh
}

// ask prints a question on stderr and reads the answer; only for people.
func (a *App) ask(ctx context.Context, question string) (string, error) {
	if !a.mode.Interactive {
		return "", usagef("This needs an answer, and nobody can type one here").hint("Run it in a terminal, or pass the flag that answers it (--yes).")
	}
	fmt.Fprint(a.errOut, a.theme.PrimaryBold("? ")+question+" ")
	select {
	case line, ok := <-a.lines():
		if !ok {
			return "", usagef("No answer (stdin closed)")
		}
		return strings.TrimSpace(line), nil
	case <-ctx.Done():
		fmt.Fprintln(a.errOut)
		return "", ctx.Err()
	}
}

// confirm asks a yes/no question, no by default; --yes answers it.
func (a *App) confirm(ctx context.Context, question string, yes bool) error {
	if yes {
		return nil
	}
	if !a.mode.Interactive {
		return usagef("This can't be undone, so it needs --yes here").hint("Add --yes to confirm (no one can answer a question in this session).")
	}
	answer, err := a.ask(ctx, question+" "+a.theme.Dim("[y/N]"))
	if err != nil {
		return err
	}
	if a := strings.ToLower(answer); a == "y" || a == "yes" {
		return nil
	}
	return failf(ExitError, "Nothing was changed")
}

// confirmTyped asks the person to type a word (an id) before something destructive.
func (a *App) confirmTyped(ctx context.Context, question, word string, yes bool) error {
	if yes {
		return nil
	}
	if !a.mode.Interactive {
		return usagef("This can't be undone, so it needs --yes here").hint("Add --yes to confirm (no one can answer a question in this session).")
	}
	answer, err := a.ask(ctx, question)
	if err != nil {
		return err
	}
	if answer != word {
		return failf(ExitError, "That didn't match, so nothing was changed")
	}
	return nil
}
