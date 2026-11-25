package symbols

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Demangle turns the map's Swift and C++ names into what Xcode and atos show
// ("specialized Checkout.run(_:)", "foo::bar()"): Swift through `xcrun swift-demangle
// --simplified`, C++ through `xcrun c++filt`, in one run each. Without Xcode's tools the names
// stay as they are and the error says so; the map still works, only less readable.
func Demangle(ctx context.Context, maps []*Map) error {
	var swift, cpp []string
	seen := map[string]bool{}
	for _, m := range maps {
		for _, s := range m.Strings {
			if seen[s] {
				continue
			}
			seen[s] = true
			switch {
			case isSwift(s):
				swift = append(swift, s)
			case strings.HasPrefix(s, "_Z") || strings.HasPrefix(s, "__Z") || strings.HasPrefix(s, "___Z"):
				// C++, and Objective-C++ blocks ("___ZZ…_block_invoke"), which c++filt reads too.
				cpp = append(cpp, s)
			}
		}
	}
	names := map[string]string{}
	for _, run := range []struct {
		names []string
		args  []string
	}{{swift, []string{"swift-demangle", "--simplified"}}, {cpp, []string{"c++filt"}}} {
		if len(run.names) == 0 {
			continue
		}
		out, err := pipe(ctx, run.args, run.names)
		if err != nil {
			return fmt.Errorf("demangling with xcrun %s: %w", run.args[0], err)
		}
		for i, n := range run.names {
			if out[i] != "" {
				names[n] = out[i]
			}
		}
	}
	for _, m := range maps {
		for i, s := range m.Strings {
			if d, ok := names[s]; ok {
				m.Strings[i] = d
			}
		}
	}
	return nil
}

func isSwift(s string) bool {
	for _, p := range []string{"$s", "_$s", "$S", "_$S", "$e", "_$e", "_T0"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// pipe runs xcrun with one name a line and reads one a line back.
func pipe(ctx context.Context, args, names []string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "xcrun", args...)
	cmd.Stdin = strings.NewReader(strings.Join(names, "\n") + "\n")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != len(names) {
		return nil, fmt.Errorf("%d names in, %d out", len(names), len(lines))
	}
	return lines, nil
}
