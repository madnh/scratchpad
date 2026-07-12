package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/madnh/scratchpad/internal/buildinfo"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), buildinfo.Get().String())
			return nil
		},
	}
}
