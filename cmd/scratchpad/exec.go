package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"

	"github.com/madnh/scratchpad/internal/appinfo"
	"github.com/madnh/scratchpad/internal/config"
	"github.com/madnh/scratchpad/internal/pad"
	"github.com/madnh/scratchpad/internal/ptyrun"
	"github.com/madnh/scratchpad/internal/relay"
	"github.com/madnh/scratchpad/internal/store"
)

// exitError carries a child's exit status out through cobra so `exec` is transparent:
// whatever the agent exited with, this process exits with. main unwraps it.
type exitError int

func (e exitError) Error() string { return "child exited with status " + strconv.Itoa(int(e)) }

func newExecCmd() *cobra.Command {
	var (
		dir     dirFlags
		logPath string
		rejoin  string
		maxIdle time.Duration
	)
	name := appinfo.Name()
	cmd := &cobra.Command{
		Use:   "exec -- <command> [args...]",
		Short: "Run an agent with a relay that nudges it when a pad moves",
		Long: "Run an AI agent as a child process and stay listening on its behalf.\n\n" +
			"Waiting is the thing agents forget. `pad wait` prints one section and exits, so\n" +
			"staying reachable means re-arming it after every turn — and an agent that is\n" +
			"mid-task, or that simply forgot, is one the other side is talking to in vain.\n" +
			"Under `exec` nobody has to remember, because the listener is not the agent.\n\n" +
			"Registration is automatic and needs nothing from the agent: every " + name + "\n" +
			"command it runs inherits " + relay.EnvSocket + ", so posting to a pad IS joining it.\n" +
			"When a pad the agent takes part in moves, one line is typed into its input:\n" +
			"a POINTER (`" + name + " pad read <ref> --since <n>`), never the other agent's\n" +
			"words — content pasted in as if you had typed it is command injection.\n\n" +
			"A relay remembers nothing once its process ends, so an agent RELAUNCHED under a\n" +
			"new `exec` (a host restart, a reboot, a resumed conversation) is deaf on every pad\n" +
			"until it touches each one again. `--rejoin[=<author>]` closes that: before the agent\n" +
			"starts, the relay looks up the pads that identity takes part in — posted to, or owns\n" +
			"or opened an open task in — and watches each from the identity's own last post, so\n" +
			"what arrived while it was down is pointed at once. Protected pads and pads idle past\n" +
			"--rejoin-max-idle are left out, and so is any pad another live relay is already\n" +
			"watching under the same name: two terminals under one name must not both be nudged.\n\n" +
			"This puts " + name + " between your keyboard and the agent for the whole session.\n" +
			"That is why it is opt-in and why nothing else in this tool depends on it.",
		Args:               cobra.MinimumNArgs(1),
		DisableFlagParsing: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			// The agent's command goes after `--`, and nothing may sit before it. This is
			// what `--rejoin ping -- claude` looks like once the optional value is not
			// taken: `ping` becomes the program to run. Refusing is the only safe reading.
			if n := cmd.ArgsLenAtDash(); n > 0 {
				return fmt.Errorf("unexpected %q before `--`: the agent command goes after it (to name a rejoin identity, write --rejoin=<name>)", strings.Join(args[:n], " "))
			}
			_, live, err := dir.open()
			if err != nil {
				return err
			}
			cfg := live.Get()

			// The socket is named after this process, inside the Scratchpad dir like
			// everything else it owns. Two agents launched side by side get two relays and
			// never see each other's registrations — which is what makes two sessions
			// calling themselves the same name harmless.
			sock := relay.SocketPath(cfg.RootDir)

			// The relay needs somewhere to deliver before the session it delivers to
			// exists. The pointer is filled in the moment the child is up; a nudge that
			// arrives before then is reported as undeliverable and stays pending.
			var sess atomic.Pointer[ptyrun.Session]
			srv := relay.NewServer(sock, live, func(line string) error {
				s := sess.Load()
				if s == nil {
					return fmt.Errorf("no session yet")
				}
				return s.Type(line)
			})

			srv.Command = strings.Join(args, " ")

			// Logging goes to a FILE or nowhere. The terminal belongs to the agent's
			// full-screen UI for the whole session, so a line printed there is not a
			// diagnostic, it is damage.
			if logPath != "" {
				if logPath == logAuto {
					logPath = relay.LogPath(cfg.RootDir)
				}
				f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
				if err != nil {
					return fmt.Errorf("relay log: %w", err)
				}
				defer f.Close()
				// O_APPEND plus a pid on every line is what lets several relays share one
				// file: each write lands whole at the end, and the reader can tell which
				// agent wrote it. Lmsgprefix puts the pid after the timestamp, so the file
				// still sorts and reads chronologically.
				lg := log.New(f, fmt.Sprintf("[%d] ", os.Getpid()), log.LstdFlags|log.Lmicroseconds|log.Lmsgprefix)
				srv.Logf = lg.Printf
				lg.Printf("exec: %s (socket %s)", srv.Command, sock)
			}

			// What a relaunched identity was taking part in is worked out BEFORE the agent
			// starts, while the terminal is still ours to print a summary on.
			var rejoins []relay.Registration
			if rejoin != "" {
				author := rejoin
				if author == rejoinFromEnv {
					author = strings.TrimSpace(os.Getenv(config.EnvAuthor))
					if author == "" {
						return fmt.Errorf("--rejoin without a name takes it from %s, which is not set", config.EnvAuthor)
					}
				}
				if err := pad.ValidateAuthor(author); err != nil {
					return fmt.Errorf("--rejoin: %w", err)
				}
				rejoins, err = planRejoin(cmd.ErrOrStderr(), live, sock, author, maxIdle, srv.Logf)
				if err != nil {
					return fmt.Errorf("--rejoin: %w", err)
				}
			}

			// Listen before spawning: an agent quick enough to post in its first second
			// would otherwise register with a socket that does not exist yet.
			if err := srv.Listen(); err != nil {
				return fmt.Errorf("relay socket: %w", err)
			}
			ctx, stop := context.WithCancel(cmd.Context())
			defer stop()
			// Closed when the relay has finished shutting down — including removing its
			// socket. Without waiting on it this process exits first and leaves the file
			// behind on every clean run, so the store slowly fills with sockets nobody is
			// listening on.
			relayDone := make(chan struct{})
			go func() {
				defer close(relayDone)
				if err := srv.Run(ctx); err != nil {
					// Nowhere safe to print: the terminal belongs to the child's UI.
					_ = err
				}
			}()

			env := append(os.Environ(), relay.EnvSocket+"="+sock)
			s, err := ptyrun.Start(args, env)
			if err != nil {
				return err
			}
			sess.Store(s)

			if len(rejoins) > 0 {
				// Not at once: a nudge typed into an agent that is still drawing its first
				// screen can be swallowed by its startup. Nothing is lost by waiting — each
				// pad is watched from the agent's own last post, so a section that lands in
				// the meantime is still after the mark — and a nudge that IS swallowed is
				// repeated by the re-nudge floor.
				go func() {
					select {
					case <-time.After(rejoinSettle):
					case <-ctx.Done():
						return
					}
					if err := srv.Rejoin(rejoins); err != nil && srv.Logf != nil {
						srv.Logf("rejoin: %v", err)
					}
				}()
			}

			code, err := s.Wait()
			stop()
			// Bounded: a relay that will not stop must not hold the terminal hostage after
			// the agent it served has already gone.
			select {
			case <-relayDone:
			case <-time.After(2 * time.Second):
			}
			if err != nil {
				return err
			}
			if code != 0 {
				return exitError(code)
			}
			return nil
		},
	}
	dir.bind(cmd)
	cmd.Flags().StringVar(&logPath, "log", "",
		"append relay activity (registrations, nudges, pads it cannot read) to a file — bare `--log` uses <dir>/relay.log, or name your own. Never the screen: that belongs to the agent")
	// A bare `--log` means the dir's own file. The path cannot be spelled out here because
	// the dir is not resolved until the command runs, hence the sentinel.
	cmd.Flags().Lookup("log").NoOptDefVal = logAuto
	cmd.Flags().StringVar(&rejoin, "rejoin", "",
		"watch, from the start, every pad this author takes part in — for an agent relaunched under the same identity. Bare `--rejoin` takes the name from "+config.EnvAuthor+"; to name one, write `--rejoin=<author>`")
	cmd.Flags().Lookup("rejoin").NoOptDefVal = rejoinFromEnv
	cmd.Flags().DurationVar(&maxIdle, "rejoin-max-idle", 7*24*time.Hour,
		"with --rejoin, leave out pads with no activity for longer than this (0 = no limit)")
	return cmd
}

// rejoinFromEnv is the sentinel a bare `--rejoin` leaves behind: take the identity from
// the environment the agent is launched with, which is where a host already puts it. It
// cannot collide with a real name — `$` is not allowed in one — and it reads correctly
// where the help prints it.
const rejoinFromEnv = "$" + config.EnvAuthor

// rejoinSettle is how long a relaunched agent is given to come up before the relay types
// into it.
const rejoinSettle = 3 * time.Second

// planRejoin works out which pads a relaunched identity should be watching again, and says
// what it found — including what it left out and why, on the same line as the total, so a
// short list cannot be read as "that is everything".
//
// A pad another live relay of this store already watches under the same name is skipped:
// that relay belongs to a session already holding the conversation, and registering here
// too would type the same pointer into two agents.
func planRejoin(w io.Writer, live *config.Live, sock, author string, maxIdle time.Duration, logf func(string, ...any)) ([]relay.Registration, error) {
	plan, err := store.New(live).Rejoinable(author, maxIdle, time.Now())
	if err != nil {
		return nil, err
	}
	claimed := map[string]int{}
	for _, c := range relay.OtherClaims(live.Get().RootDir, sock) {
		if c.Author == author {
			claimed[c.Ref] = c.PID
		}
	}

	var regs []relay.Registration
	var dup []string
	for _, p := range plan.Pads {
		if pid, ok := claimed[p.Ref]; ok {
			dup = append(dup, fmt.Sprintf("%s (relay pid %d)", p.Ref, pid))
			continue
		}
		regs = append(regs, relay.Registration{Ref: p.Ref, Author: author, Since: p.Since, Wake: relayWake})
	}

	var left []string
	if plan.Protected > 0 {
		left = append(left, fmt.Sprintf("%d protected", plan.Protected))
	}
	if plan.Idle > 0 {
		left = append(left, fmt.Sprintf("%d idle over %s", plan.Idle, maxIdle))
	}
	if plan.Unreadable > 0 {
		left = append(left, fmt.Sprintf("%d unreadable", plan.Unreadable))
	}
	if len(dup) > 0 {
		left = append(left, fmt.Sprintf("%d already watched as %s by another relay", len(dup), author))
	}
	line := fmt.Sprintf("relay: rejoining %d pad(s) as %s", len(regs), author)
	if len(left) > 0 {
		line += " (left out: " + strings.Join(left, ", ") + ")"
	}
	fmt.Fprintln(w, line)
	if logf != nil {
		logf("%s", line)
	}
	for _, d := range dup {
		msg := fmt.Sprintf("warning: %s is already watched as %s — another session runs under this name; not rejoining it here, so only that session is nudged", d, author)
		fmt.Fprintln(w, msg)
		if logf != nil {
			logf("%s", msg)
		}
	}
	return regs, nil
}

// logAuto is the sentinel a bare `--log` leaves behind. Someone who genuinely wants a
// file called "auto" can write `--log ./auto`.
const logAuto = "auto"
