package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/madnh/scratchpad/internal/appinfo"
	"github.com/madnh/scratchpad/internal/config"
	"github.com/madnh/scratchpad/internal/server"
	"github.com/madnh/scratchpad/internal/store"
)

func newServeCmd() *cobra.Command {
	var (
		dir             dirFlags
		stdio           bool
		tcpEnabled      bool
		tcpPort         int
		tcpTokenDigests []string
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the MCP server (Unix socket by default)",
		Long: "Run the MCP server. The default transport is Streamable HTTP over a Unix domain\n" +
			"socket in the Scratchpad dir (no open port; peercred-gated). Use --stdio for a host\n" +
			"that spawns this process, or --tcp for an opt-in loopback listener (bearer token\n" +
			"required, configured as SHA-256 digests).\n\n" +
			"The Scratchpad dir resolves like every command (--dir / $" + config.EnvDir + " /\n" +
			"default, see `" + appinfo.Name() + " skills docs config`); the default dir bootstraps itself.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, cfgDir, source, err := dir.resolve()
			if err != nil {
				return err
			}
			// Announce the resolved store so a misconfigured deployment is visible at
			// startup. MUST go to stderr: under --stdio the JSON-RPC stream owns stdout.
			log.Printf("using Scratchpad dir %s (%s)", cfgDir, source)

			// SIGTERM/SIGINT → graceful shutdown.
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			live := config.NewLive(cfg)
			watchConfig(ctx, live, cfgDir)
			st := store.New(live)
			ms := server.BuildMCPServer(st, live)

			if stdio {
				return server.ServeStdio(ctx, ms)
			}

			var tcp *server.TCPOptions
			if tcpEnabled {
				// Flags win over the marker's tcp group when explicitly set.
				port := cfg.TCP.Port
				if cmd.Flags().Changed("tcp-port") {
					port = tcpPort
				}
				// Credentials are left NIL unless the flag names them, so the guard reads
				// the marker's `auth.clients` per request and a revocation applies without
				// a restart. Passing the flag pins them for the life of the process — the
				// same precedence as every other flag, and the reason the override is a
				// separate field rather than a pre-resolved list.
				var override []string
				if cmd.Flags().Changed("tcp-token-digest") {
					override = tcpTokenDigests
				}
				tcp = &server.TCPOptions{
					Port:           port,
					TokenOverride:  override,
					AllowedOrigins: cfg.TCP.AllowedOrigins,
					Realm:          appinfo.Name(),
				}
			}
			return server.ServeHTTP(ctx, ms, live, cfg.SocketPath, tcp)
		},
	}
	dir.bind(cmd)
	f := cmd.Flags()
	f.BoolVar(&stdio, "stdio", false, "serve over stdio for a host that spawns this process")
	f.BoolVar(&tcpEnabled, "tcp", false, "also serve over an opt-in loopback TCP listener (settings from the marker's tcp group)")
	f.IntVar(&tcpPort, "tcp-port", config.DefaultTCPPort, "loopback port for --tcp (overrides the marker)")
	f.StringArrayVar(&tcpTokenDigests, "tcp-token-digest", nil, "bearer-token digest \"sha256:<hex>\" accepted on --tcp (repeatable; overrides the marker)")
	return cmd
}
