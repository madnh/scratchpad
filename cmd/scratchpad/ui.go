package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/madnh/scratchpad/internal/appinfo"
	"github.com/madnh/scratchpad/internal/config"
	"github.com/madnh/scratchpad/internal/webui"
)

func newUICmd() *cobra.Command {
	var (
		dir    dirFlags
		port   int
		noAuth bool
		open   bool
	)

	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Run the Web UI to browse and watch pads in a browser",
		Long: "Run the Web UI: browse projects and pads, read a pad, and watch for new\n" +
			"sections with live updates and browser notifications.\n\n" +
			"This is the HUMAN counterpart to `pad wait` — a command whose exit code wakes an\n" +
			"agent gives a person nothing to look at. It is a separate loopback listener from\n" +
			"`serve`, not another MCP transport: a browser cannot speak to the Unix socket, and\n" +
			"the auth model is browser-shaped (a one-time link, then a session cookie).\n\n" +
			"The surface is read-only for pad content — a person watching a conversation is not\n" +
			"a participant in it. Pads can still be deleted, as with `pad delete`.\n\n" +
			"Binds 127.0.0.1 only. The Scratchpad dir resolves like every command (--dir /\n" +
			"$" + config.EnvDir + " / default, see `" + appinfo.Name() + " skills docs config`).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, cfg, err := dir.open()
			if err != nil {
				return err
			}

			// flag > env > marker > default, the same precedence as everywhere else.
			resolvedPort := cfg.UI.Port
			if v := os.Getenv(config.EnvUIPort); v != "" {
				n, convErr := strconv.Atoi(v)
				if convErr != nil || n <= 0 || n > 65535 {
					return fmt.Errorf("%s=%q is not a valid port", config.EnvUIPort, v)
				}
				resolvedPort = n
			}
			if cmd.Flags().Changed("port") {
				resolvedPort = port
			}
			resolvedNoAuth := cfg.UI.NoAuth
			if cmd.Flags().Changed("no-auth") {
				resolvedNoAuth = noAuth
			}

			srv, err := webui.New(st, cfg, webui.Options{Port: resolvedPort, NoAuth: resolvedNoAuth})
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			// The URL carries a one-time token, so it belongs on stdout: a person
			// pipes it, copies it, or feeds it to a browser. Everything else about
			// starting up is chatter for stderr.
			url := srv.URL()
			fmt.Fprintln(cmd.OutOrStdout(), url)
			fmt.Fprintf(cmd.ErrOrStderr(), "Scratchpad UI on %s (dir %s)\n", cfg.DisplayName, cfg.ProjectsDir)
			if resolvedNoAuth {
				fmt.Fprintln(cmd.ErrOrStderr(),
					"warning: --no-auth — every local process that can reach this port can read every pad")
			} else {
				fmt.Fprintln(cmd.ErrOrStderr(),
					"open the URL above; the token in it starts a session, then it is dropped from the address bar")
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "press Ctrl-C to stop")

			if open {
				openBrowser(cmd, url)
			}
			return srv.Run(ctx)
		},
	}

	dir.bind(cmd)
	f := cmd.Flags()
	f.IntVar(&port, "port", config.DefaultUIPort,
		"loopback port to listen on (overrides the marker's ui group; env "+config.EnvUIPort+")")
	f.BoolVar(&noAuth, "no-auth", false,
		"skip the one-time link and the session cookie (trusted single-user machine only)")
	f.BoolVar(&open, "open", false, "open the URL in the default browser")
	return cmd
}

// openBrowser launches the platform's URL opener. It is opt-in (--open) because
// spawning another program is a side effect a person should ask for; a failure is
// reported and ignored, since the URL is already printed.
func openBrowser(cmd *cobra.Command, url string) {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", url)
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		c = exec.Command("xdg-open", url)
	}
	if err := c.Start(); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "could not open a browser (%v); open the URL above yourself\n", err)
		return
	}
	go func() { _ = c.Wait() }() // reap the child; we never block on it
}
