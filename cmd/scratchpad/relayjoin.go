package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/madnh/scratchpad/internal/relay"
)

// relayWake is what an automatic registration asks to be nudged for.
//
// `me` rather than `any`: the relay follows every pad the agent has touched, and on a
// busy store `any` would nudge it for exchanges between two other agents on a pad it
// merely passed through once. `me` still covers broadcasts, so nothing addressed to the
// room is missed — and the kinds that change what an agent is ALLOWED to do next
// (continued, rules, notice) bypass the selectors entirely in pad.Wakes.
var relayWake = []string{"me"}

// noteJoin tells the relay that launched this process — if one did — that this agent is
// taking part in a pad, and how far it has read, then SAYS SO in the command's output.
//
// Registering is the whole reason an agent never has to arm anything: joining a pad IS
// posting to it, and every scratchpad command an agent runs under `exec` inherits the
// socket. The registration therefore comes from a process the relay itself spawned, not
// from an author string anybody could send — two sessions calling themselves `backend`
// stay separate because the name was never the thing being trusted.
//
// Reporting it is separate, and matters just as much. An agent decides whether to arm a
// wait at exactly this moment — right after a post — and the alternative was for it to
// know from the skill document, which is a copy on disk that can be months old and is not
// loaded by every host at all. A line in the result reaches it whatever it has read. It
// also closes the worse gap: registration used to fail SILENTLY, so a relay that had died
// left the agent believing somebody was listening for it. Now that failure says, in the
// one place the agent is looking, to arm a wait itself.
func noteJoin(cmd *cobra.Command, ref, author, password string, since int) {
	sock := os.Getenv(relay.EnvSocket)
	if sock == "" || ref == "" || author == "" {
		return
	}
	err := relay.Register(sock, relay.Registration{
		Ref: ref, Author: author, Password: password, Since: since, Wake: relayWake,
	})
	if err != nil {
		// Never fatal: the pad file was written before this ran, and a post that
		// SUCCEEDED must not report failure. But it must not pass for success either.
		fmt.Fprintf(cmd.ErrOrStderr(),
			"warning: this session is under a relay but registering %s failed (%v) — nothing will nudge you about this pad, so arm a wait yourself\n",
			ref, err)
		return
	}
	// stdout, in the same `key: value` shape as the rest of the result: this is a fact
	// about what the command did, not commentary.
	fmt.Fprintf(cmd.OutOrStdout(), "relay: watching %s from §%d\n", ref, since)
	fmt.Fprintln(cmd.ErrOrStderr(),
		"note: a relay is listening for you — it will type a pointer into your input when this pad moves. Do not arm a wait.")
}
