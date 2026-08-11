package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/madnh/scratchpad/internal/appinfo"
)

// newNotificationCmd prints its arguments and does nothing else.
//
// It exists so that a relay nudge is a VALID, harmless command line rather than a string
// that merely happens not to run. `exec` types into whatever is on the other end of the
// pty, and the agent it launched is not always what is still there — it may have
// crashed, exited, or been ^C-ed, leaving the shell it was started from. Anything typed
// then is executed.
//
// Two shapes were rejected before this one:
//
//   - A bare sentence (`[scratchpad] new activity …`). Harmless only by accident: zsh
//     refuses it because `[…]` is a glob with no matches, while bash reports
//     `command not found`. Relying on a shell's parse errors is relying on which shell
//     the operator uses.
//   - The `pad read` command itself. It runs, which is worse than it sounds: it prints a
//     whole pad into a terminal nobody is reading, and it teaches the pattern that a
//     nudge is something to EXECUTE — one small step from delivering content that way.
//
// So the nudge names this command, whose entire behaviour is to echo the message. In an
// agent it is read as text; in a shell it prints the same words and exits 0. The operator
// who finds it on screen learns exactly what an agent would have.
//
// Flag parsing is disabled and any arguments are accepted, so a message containing
// `--since 2` is text rather than an unknown flag. It takes no action, touches no store,
// and cannot fail.
func newNotificationCmd() *cobra.Command {
	name := appinfo.Name()
	return &cobra.Command{
		Use:   "notification [text...]",
		Short: "Print a notification and exit — the harmless form of a relay nudge",
		Long: "Print the words that follow and exit 0.\n\n" +
			"This is what `" + name + " exec` types into an agent when a pad it takes part in\n" +
			"moves. It is a real command so that it stays harmless if the agent is gone and a\n" +
			"shell receives it instead: it prints the message and does nothing.\n\n" +
			"Every argument is text. Flags are not parsed, nothing is validated, and there is\n" +
			"no failure mode — a notification must never turn into an error somebody has to\n" +
			"deal with.",
		DisableFlagParsing: true,
		Args:               cobra.ArbitraryArgs,
		Run: func(cmd *cobra.Command, args []string) {
			msg := strings.TrimSpace(strings.Join(args, " "))
			if msg == "" {
				return
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", name, msg)
		},
	}
}
