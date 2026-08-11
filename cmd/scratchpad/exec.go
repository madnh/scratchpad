package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"

	"github.com/madnh/scratchpad/internal/appinfo"
	"github.com/madnh/scratchpad/internal/ptyrun"
	"github.com/madnh/scratchpad/internal/relay"
)

// exitError carries a child's exit status out through cobra so `exec` is transparent:
// whatever the agent exited with, this process exits with. main unwraps it.
type exitError int

func (e exitError) Error() string { return "child exited with status " + strconv.Itoa(int(e)) }

func newExecCmd() *cobra.Command {
	var (
		dir     dirFlags
		logPath string
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
			"This puts " + name + " between your keyboard and the agent for the whole session.\n" +
			"That is why it is opt-in and why nothing else in this tool depends on it.",
		Args:               cobra.MinimumNArgs(1),
		DisableFlagParsing: false,
		RunE: func(cmd *cobra.Command, args []string) error {
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
	return cmd
}

// logAuto is the sentinel a bare `--log` leaves behind. Someone who genuinely wants a
// file called "auto" can write `--log ./auto`.
const logAuto = "auto"
