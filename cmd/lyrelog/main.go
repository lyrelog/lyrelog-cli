// Command lyrelog is Lyrelog's command-line tool: the dashboard from a terminal, for people
// and for coding agents.
package main

import (
	"os"

	"go.lyrelog.com/cli/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
