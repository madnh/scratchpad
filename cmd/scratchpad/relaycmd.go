package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/madnh/scratchpad/internal/appinfo"
	"github.com/madnh/scratchpad/internal/relay"
)

// newRelayCmd reports what the running `exec` relays are watching.
//
// It exists because a relay that is working and a relay that never heard of your pad are
// both silent, and silence is the whole symptom when a nudge does not arrive. This turns
// that one symptom into distinguishable answers: no relay running, a relay running with
// nothing registered, a pad registered under a name the traffic is not addressed to, or a
// nudge that WAS delivered and simply ignored.
func newRelayCmd() *cobra.Command {
	var (
		dir      dirFlags
		asJSON   bool
		watchDur time.Duration
	)
	name := appinfo.Name()
	cmd := &cobra.Command{
		Use:   "relay",
		Short: "Show what the running exec relays are watching",
		Long: "List every live relay in this dir and what it is watching.\n\n" +
			"A relay is started by `" + name + " exec` and lives as long as the agent it\n" +
			"launched. For each pad it shows the identity it registered under, the section a\n" +
			"nudge would point at, whether something is waiting to be delivered, and when that\n" +
			"pad was last nudged about — while that is set, new traffic on the pad stays quiet\n" +
			"until the agent acts on the nudge it already has.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, _, _, err := dir.resolve()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			for {
				sts := liveRelays(cfg.RootDir)
				if asJSON {
					enc := json.NewEncoder(out)
					enc.SetIndent("", "  ")
					if err := enc.Encode(sts); err != nil {
						return err
					}
				} else {
					printRelays(out, sts, cfg.RootDir)
				}
				if watchDur <= 0 {
					return nil
				}
				time.Sleep(watchDur)
				fmt.Fprintln(out)
			}
		},
	}
	dir.bind(cmd)
	f := cmd.Flags()
	f.BoolVar(&asJSON, "json", false, "machine-readable output")
	f.DurationVar(&watchDur, "watch", 0, "keep printing every interval, e.g. 2s (Ctrl-C to stop)")
	return cmd
}

// liveRelays queries every socket that answers for this dir, sorted by pid.
//
// A relay is matched on the DIR it reports, not on where its socket file sits: a dir with
// a long path pushes its socket into the shared system temp dir, and looking only inside
// the dir would report "no relay running" while one is running perfectly well.
func liveRelays(rootDir string) []*relay.Status {
	var out []*relay.Status
	for _, sock := range relay.CandidateSockets(rootDir) {
		st, err := relay.Query(sock)
		if err != nil {
			continue // a socket nobody is listening on; `doctor` is what reports those
		}
		if st.Dir != "" && st.Dir != rootDir {
			continue // another store's relay, sharing the temp dir
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}

func printRelays(w io.Writer, sts []*relay.Status, rootDir string) {
	if len(sts) == 0 {
		fmt.Fprintf(w, "No relay is running in %s.\n", rootDir)
		fmt.Fprintf(w, "Start one with: %s exec -- <your agent>\n", appinfo.Name())
		return
	}
	now := time.Now()
	for i, st := range sts {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "▸ pid %d  %s\n", st.PID, st.Command)
		fmt.Fprintf(w, "  socket   %s\n", st.Socket)
		if st.Nudges == 0 {
			fmt.Fprintln(w, "  nudges   none delivered yet")
		} else {
			fmt.Fprintf(w, "  nudges   %d, last %s ago: %s\n",
				st.Nudges, shortAge(now.Sub(time.Unix(st.LastTS, 0))), st.LastNudge)
		}
		if len(st.Watching) == 0 {
			// The most common cause of a quiet relay, and the one worth spelling out: the
			// agent has not touched a pad yet, so there is nothing to be quiet ABOUT.
			fmt.Fprintln(w, "  watching nothing — the agent joins a pad by posting to it,")
			fmt.Fprintf(w, "           or with `%s pad get <ref> --as <name>` if it has not posted yet\n", appinfo.Name())
			continue
		}
		fmt.Fprintln(w, "  watching")
		for _, p := range st.Watching {
			state := "quiet"
			switch {
			case p.Pending && p.NudgedTS > 0:
				state = "new traffic, held (already nudged " + shortAge(now.Sub(time.Unix(p.NudgedTS, 0))) + " ago)"
			case p.Pending:
				state = "new traffic, nudge due"
			case p.NudgedTS > 0:
				state = "nudged " + shortAge(now.Sub(time.Unix(p.NudgedTS, 0))) + " ago, waiting for the agent to act"
			}
			fmt.Fprintf(w, "    %-24s as %-12s from §%-3d %s\n", p.Ref, p.Author, p.Since, state)
		}
	}
}

// shortAge renders a duration the way a status line has room for.
func shortAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}
