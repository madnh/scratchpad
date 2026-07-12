package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/madnh/scratchpad/internal/config"
)

// flagNonInteractive is bound to the root's persistent --non-interactive flag, so every
// subcommand shares one switch. It forces non-interactive mode regardless of the TTY.
var flagNonInteractive bool

// isInteractive reports whether a human is (probably) driving this command, so commands
// can prompt/confirm for people while failing fast for processes. There is no reliable
// OS "is a human" bit, so this is a heuristic with an explicit override:
//
//   - --non-interactive or a truthy SCRATCHPAD_NONINTERACTIVE forces false, so
//     automation is never at the mercy of whether a PTY happened to be allocated.
//   - otherwise it falls back to "is stdin a terminal".
//
// It deliberately offers no force-TRUE: nothing should prompt when it cannot read a
// human's answer. Callers that must run unattended pass their own flag (e.g. --yes).
func isInteractive() bool {
	if flagNonInteractive {
		return false
	}
	if envTruthy(os.Getenv(config.EnvNonInteractive)) {
		return false
	}
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// confirmYesNo asks a yes/no question on stderr and returns the answer. defaultYes
// picks what a bare Enter means. Callers gate it behind isInteractive().
func confirmYesNo(cmd *cobra.Command, prompt string, defaultYes bool) (bool, error) {
	hint := "[y/N]"
	if defaultYes {
		hint = "[Y/n]"
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "%s %s: ", prompt, hint)
	line, err := readLine(os.Stdin)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return defaultYes, nil
	}
}

// promptLine asks for a single line on stderr, returning def on a bare Enter.
func promptLine(cmd *cobra.Command, prompt, def string) string {
	fmt.Fprint(cmd.ErrOrStderr(), prompt)
	line, err := readLine(os.Stdin)
	if err != nil {
		return def
	}
	if s := strings.TrimSpace(line); s != "" {
		return s
	}
	return def
}

// readLine reads one line (without the trailing newline) from r.
func readLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// envTruthy treats an env var as set-and-on unless it is empty or an explicit off value.
func envTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}
