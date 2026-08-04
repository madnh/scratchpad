package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/madnh/scratchpad/internal/appinfo"
	"github.com/madnh/scratchpad/internal/config"
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
	cmd.AddCommand(newSkillsInstallCmd())
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

// newSkillsInstallCmd writes the agent skill document into a skills directory.
//
// The destination is ALWAYS the operator's to name — a flag or an env var, never a
// default. This tool integrates with a host generically or not at all: baking in one
// host's conventional path would make every other host a second-class citizen and would
// put a product name this repo does not depend on into its own source.
func newSkillsInstallCmd() *cobra.Command {
	var into string
	var force, print bool

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Write the agent skill document into a skills directory",
		Long: "Write " + skills.SkillFilename + " — the document a host loads to know when to reach for\n" +
			"this tool — into <dir>/" + appinfo.Name() + "/.\n\n" +
			"The destination is yours to choose: pass --into, or set " + config.EnvSkillsDir + ".\n" +
			"There is no default, because where an agent host keeps its skills is a property of\n" +
			"that host, not of this tool. Use --print to send it to stdout instead and place it\n" +
			"yourself.\n\n" +
			"An existing file is left alone unless it is byte-identical or you pass --force, so a\n" +
			"copy you have edited is never overwritten silently.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if print {
				_, err := out.Write(skills.Skill())
				return err
			}
			if strings.TrimSpace(into) == "" {
				into = strings.TrimSpace(os.Getenv(config.EnvSkillsDir))
			}
			if into == "" {
				return fmt.Errorf(
					"no destination: pass --into <skills dir> or set %s (or use --print to write it yourself)",
					config.EnvSkillsDir)
			}

			dir := filepath.Join(expandTilde(into), appinfo.Name())
			path := filepath.Join(dir, skills.SkillFilename)
			if existing, err := os.ReadFile(path); err == nil {
				if bytes.Equal(existing, skills.Skill()) {
					fmt.Fprintf(cmd.ErrOrStderr(), "already current: %s\n", path)
					return nil
				}
				if !force {
					return fmt.Errorf(
						"%s exists and differs from this binary's copy; re-run with --force to replace it"+
							" (diff it first if you have edited it)", path)
				}
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, skills.Skill(), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "installed %s\n", path)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&into, "into", "", "skills directory to install into (env "+config.EnvSkillsDir+")")
	f.BoolVar(&force, "force", false, "replace an existing file that differs from this binary's copy")
	f.BoolVar(&print, "print", false, "write the document to stdout instead of installing it")
	return cmd
}

// expandTilde resolves a leading "~/" the way a shell would, so --into ~/somewhere works
// even when the shell did not expand it (quoted, or read from a config file).
func expandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	return p
}
