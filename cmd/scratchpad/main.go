// Command scratchpad runs the shared-pad MCP server and its operator subcommands
// (init, serve, doctor, skills, version, pad, project).
package main

import (
	"fmt"
	"os"
)

// exitTimeout is the exit code `pad wait` uses when the timeout elapsed with no new
// section — distinct from 1 (error) so a caller can branch on "nothing yet".
const exitTimeout = 3

func main() {
	if err := newRootCmd().Execute(); err != nil {
		if err == errWaitTimeout {
			fmt.Fprintln(os.Stderr, "timeout: no new section")
			os.Exit(exitTimeout)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
