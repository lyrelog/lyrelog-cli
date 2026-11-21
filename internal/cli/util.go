package cli

import (
	"regexp"

	"github.com/charmbracelet/x/ansi"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func ansiStrip(s string) string { return ansi.Strip(s) }
