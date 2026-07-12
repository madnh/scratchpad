package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/madnh/scratchpad/internal/config"
)

// newInitCmd scaffolds a Scratchpad dir: marker (header + identity), config guide, and
// the projects/ store. The DEFAULT dir (~/.scratchpad) auto-bootstraps on first
// use, so init exists for CUSTOM dirs (--dir / env) and provisioning. It refuses to
// overwrite an initialized dir so a live store is never clobbered.
func newInitCmd() *cobra.Command {
	var (
		dir         dirFlags
		displayName string
		instance    string
		assumeYes   bool
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create a Scratchpad dir (marker + guide + projects store)",
		Long: "Create a Scratchpad dir at --dir / $" + config.EnvDir + ", or at the default\n" +
			"location when neither is set. The default dir auto-bootstraps on first use anyway,\n" +
			"so init is only required for a custom dir. Refuses to overwrite an already-\n" +
			"initialized dir — remove it first to re-initialize.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			target, explicit, err := resolveInitTarget(dir.dir)
			if err != nil {
				return err
			}

			// A dir chosen implicitly (the default) is confirmed with the operator; an
			// explicit --dir/env means the operator already decided — skip the prompt.
			if !explicit && !assumeYes {
				chosen, err := confirmInitDir(cmd, target)
				if err != nil {
					return err
				}
				target = chosen
			}

			if config.IsInitialized(target) {
				return fmt.Errorf("dir %q is already initialized (%s exists) — remove it first to re-initialize",
					target, config.MarkerFilename)
			}

			// Ask for the display name when a human is driving and didn't pass the flag.
			if !cmd.Flags().Changed("display-name") && isInteractive() {
				displayName = promptLine(cmd, fmt.Sprintf("Display name [%s]: ", displayName), displayName)
			}

			if err := config.WriteMarker(target, config.Config{DisplayName: displayName, Instance: instance}); err != nil {
				return err
			}
			if err := config.WriteDoc(target); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Join(target, "projects"), 0o700); err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "initialized Scratchpad dir at %s\n", target)
			fmt.Fprintf(out, "  %-28s marker config (display name %q)\n", config.MarkerFilename, displayName)
			fmt.Fprintf(out, "  %-28s configuration guide\n", config.DocFilename)
			fmt.Fprintf(out, "  %-28s pad store\n", "projects/")
			printInitGuidance(cmd, target)
			return nil
		},
	}
	dir.bind(cmd)
	f := cmd.Flags()
	f.StringVar(&displayName, "display-name", config.DefaultDisplayName, "human-facing name of this deployment")
	f.StringVar(&instance, "instance", config.DefaultInstance, "technical label (names the unix socket)")
	f.BoolVar(&assumeYes, "yes", false, "accept the default dir without prompting (for automation)")
	return cmd
}

// resolveInitTarget picks where to initialize: --dir / env (explicit=true), else the
// fixed default dir (explicit=false, subject to confirmation).
func resolveInitTarget(flagDir string) (target string, explicit bool, err error) {
	if s := strings.TrimSpace(flagDir); s != "" {
		abs, aerr := filepath.Abs(s)
		return abs, true, aerr
	}
	if s := strings.TrimSpace(os.Getenv(config.EnvDir)); s != "" {
		abs, aerr := filepath.Abs(s)
		return abs, true, aerr
	}
	def, err := config.DefaultDir()
	return def, false, err
}

// confirmInitDir asks the operator to accept the default target or type another path.
// A non-terminal stdin with no --yes is an error rather than a silent guess.
func confirmInitDir(cmd *cobra.Command, target string) (string, error) {
	if !isInteractive() {
		return "", fmt.Errorf("refusing to guess the dir non-interactively: pass --dir <path> or --yes (would default to %s)", target)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Initialize a Scratchpad dir at:\n  %s\nPress Enter to accept, or type a different path: ", target)
	line, err := readLine(os.Stdin)
	if err != nil {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return target, nil
	}
	return filepath.Abs(line)
}

// printInitGuidance tells the operator how commands will find this dir next time:
// the default location is found automatically; a custom one needs --dir / the env var
// (or the `dir` pointer in the default marker).
func printInitGuidance(cmd *cobra.Command, target string) {
	out := cmd.OutOrStdout()
	def, err := config.DefaultDir()
	if err == nil && target == def {
		fmt.Fprintln(out, "\nthis is the default location — every command finds it automatically.")
		return
	}
	fmt.Fprintf(out, "\nnote: %q is not the default location (%s).\n", target, def)
	fmt.Fprintf(out, "      to run against it, pass --dir %s\n", target)
	fmt.Fprintf(out, "      or set %s=%s\n", config.EnvDir, target)
	fmt.Fprintf(out, "      or point the default marker at it: set \"dir\": %q in %s\n", target, config.MarkerPath(def))
}
