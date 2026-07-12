package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/madnh/scratchpad/internal/appinfo"
	"github.com/madnh/scratchpad/internal/skills"
)

func newSkillsCmd() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Self-documenting help for AI agents and operators",
		Long: "Print an overview of the embedded documentation topics. Read one with\n" +
			"`" + appinfo.Name() + " skills docs <topic>`, or everything with `skills docs --all`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			all := skills.All()
			if output == "json" {
				list := make([]skills.Topic, len(all))
				for i, t := range all {
					t.Body = "" // listing only; bodies come from `skills docs`
					list[i] = t
				}
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(list)
			}
			name := appinfo.Name()
			fmt.Fprintf(out, "%s — shared pads for AI agents to exchange messages turn by turn.\n\n", name)
			fmt.Fprintf(out, "Topics (read with `%s skills docs <topic>`):\n", name)
			for _, t := range all {
				fmt.Fprintf(out, "  %-10s %s\n", t.ID, t.Description)
			}
			fmt.Fprintf(out, "\nAll at once: `%s skills docs --all` (add `-o json` for machine-readable output).\n", name)
			return nil
		},
	}
	cmd.PersistentFlags().StringVarP(&output, "output", "o", "text", "output format: text or json")
	cmd.AddCommand(newSkillsDocsCmd(&output))
	return cmd
}

func newSkillsDocsCmd(output *string) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "docs [topic]",
		Short: "Print one documentation topic (or --all)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			var topics []skills.Topic
			switch {
			case all:
				topics = skills.All()
			case len(args) == 1:
				t, err := skills.Get(args[0])
				if err != nil {
					return err
				}
				topics = []skills.Topic{t}
			default:
				return fmt.Errorf("pass a topic name or --all (see `%s skills` for the list)", appinfo.Name())
			}
			if *output == "json" {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(topics)
			}
			for i, t := range topics {
				if i > 0 {
					fmt.Fprintln(out)
				}
				fmt.Fprint(out, t.Body)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "print every topic")
	return cmd
}
