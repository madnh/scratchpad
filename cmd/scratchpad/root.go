package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/madnh/scratchpad/internal/appinfo"
	"github.com/madnh/scratchpad/internal/buildinfo"
	"github.com/madnh/scratchpad/internal/config"
)

func newRootCmd() *cobra.Command {
	name := appinfo.Name()
	root := &cobra.Command{
		Use:   name,
		Short: "Shared pads for AI agents to exchange messages turn by turn",
		Long: fmt.Sprintf("%s gives AI agents shared, append-only markdown pads to exchange messages\n"+
			"turn by turn — no human copy-paste between sessions. It is both a CLI (works\n"+
			"directly on the pad files) and an MCP server (`%s serve`).\n\n"+
			"AI agents: start here → `%s skills`", name, name, name),
		Version:       buildinfo.Get().String(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	// A single switch shared by every subcommand: force non-interactive (never prompt).
	root.PersistentFlags().BoolVar(&flagNonInteractive, "non-interactive", false,
		"never prompt; fail instead of asking (also via "+config.EnvNonInteractive+")")
	root.AddCommand(newInitCmd(), newServeCmd(), newDoctorCmd(), newSkillsCmd(), newVersionCmd(),
		newPadCmd(), newProjectCmd())
	return root
}
