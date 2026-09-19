// Command matrix-bot connects Matrix direct messages to Notekeeper through the bot API
// (docs/design/06-matrix-bot.md). Milestone M0 only provides the skeleton.
package main

import (
	"fmt"
	"os"
)

// Version is overridden with -ldflags "-X main.Version=...".
var Version = "dev"

func run(args []string) (string, int) {
	if len(args) > 0 && args[0] == "version" {
		return Version, 0
	}
	return "usage: matrix-bot <version>", 2
}

func main() {
	out, code := run(os.Args[1:])
	if code == 0 {
		fmt.Println(out)
	} else {
		fmt.Fprintln(os.Stderr, out)
	}
	os.Exit(code)
}
