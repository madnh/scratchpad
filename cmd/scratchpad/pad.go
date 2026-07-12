package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/madnh/scratchpad/internal/config"
	"github.com/madnh/scratchpad/internal/store"
)

// errWaitTimeout signals that `pad wait` ran out of time with no new section; main
// maps it to the dedicated exit code 3 (0 = new section, 1 = real error).
var errWaitTimeout = errors.New("pad wait timed out")

func newPadCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pad",
		Short: "Work with pads directly on disk (no server needed)",
		Long: "Create, post to, read, and manage pads. These commands operate on the pad files\n" +
			"through the same storage layer (and locking) as the MCP server, so mixing CLI and\n" +
			"MCP agents on one store is safe.",
	}
	cmd.AddCommand(newPadCreateCmd(), newPadPostCmd(), newPadGetCmd(), newPadReadCmd(),
		newPadWaitCmd(), newPadListCmd(), newPadDeleteCmd(), newPadPurgeCmd())
	return cmd
}

func newProjectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Work with projects (pad namespaces)",
	}
	var dir dirFlags
	list := &cobra.Command{
		Use:   "list",
		Short: "List projects with their pad counts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, _, err := dir.open()
			if err != nil {
				return err
			}
			projects, err := st.Projects()
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "PROJECT\tPADS")
			for _, p := range projects {
				fmt.Fprintf(w, "%s\t%d\n", p.Name, p.PadCount)
			}
			return w.Flush()
		},
	}
	dir.bind(list)
	cmd.AddCommand(list)
	return cmd
}

// authorFlag binds --as with its env-var default so an agent sets its identity once
// per session (SCRATCHPAD_AUTHOR) instead of repeating the flag.
func authorFlag(cmd *cobra.Command, author *string) {
	cmd.Flags().StringVar(author, "as", "",
		"author identity for this post (default from "+config.EnvAuthor+")")
}

// resolveAuthor applies the flag > env precedence for the author identity.
func resolveAuthor(flagVal string) (string, error) {
	if s := strings.TrimSpace(flagVal); s != "" {
		return s, nil
	}
	if s := strings.TrimSpace(os.Getenv(config.EnvAuthor)); s != "" {
		return s, nil
	}
	return "", fmt.Errorf("author is required: pass --as <name> or set %s", config.EnvAuthor)
}

// readContent takes the message body from the positional arg, or from stdin when the
// arg is "-" (the way to pass long content without shell-escaping trouble).
func readContent(args []string) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("pass the content as one argument, or \"-\" to read it from stdin")
	}
	if args[0] != "-" {
		return args[0], nil
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("read content from stdin: %w", err)
	}
	return string(b), nil
}

// printSections writes sections to stdout in the pad file's own on-disk format
// (header line, ts comment, blank line, content) — stable, documented, pipeable.
func printSections(w io.Writer, sections []store.Section) {
	for i, sec := range sections {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "# %d - %s - %s\n", sec.N, sec.Author, sec.Title)
		fmt.Fprintf(w, "<!-- ts: %s -->\n\n", time.Unix(sec.TS, 0).UTC().Format(time.RFC3339))
		fmt.Fprint(w, sec.Content)
	}
}

func newPadCreateCmd() *cobra.Command {
	var (
		dir     dirFlags
		project string
		author  string
		title   string
		protect bool
	)
	cmd := &cobra.Command{
		Use:   "create [content|-]",
		Short: "Create a pad and post its first section",
		Long: "Create a new pad and post section 1. Prints the pad's ref — hand it to the other\n" +
			"agent's session. With --protect the server generates a password and prints it\n" +
			"exactly once; every later access to the pad needs it.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, cfg, err := dir.open()
			if err != nil {
				return err
			}
			a, err := resolveAuthor(author)
			if err != nil {
				return err
			}
			content, err := readContent(args)
			if err != nil {
				return err
			}
			pad, pw, err := st.CreatePad(config.ResolveProject(cfg, project), a, title, content, protect)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "ref: %s\n", pad.Ref())
			fmt.Fprintf(out, "section: 1\nnext: 2\n")
			if pw != "" {
				fmt.Fprintf(out, "password: %s\n", pw)
				fmt.Fprintln(cmd.ErrOrStderr(), "note: the password is shown only this once — relay it together with the ref")
			}
			return nil
		},
	}
	dir.bind(cmd)
	authorFlag(cmd, &author)
	f := cmd.Flags()
	f.StringVar(&project, "project", "", "project to file the pad under (default from "+config.EnvProject+", else the configured default)")
	f.StringVar(&title, "title", "", "one-line title of the first section (required)")
	f.BoolVar(&protect, "protect", false, "password-protect the pad (the password is generated and printed once)")
	_ = cmd.MarkFlagRequired("title")
	return cmd
}

func newPadPostCmd() *cobra.Command {
	var (
		dir      dirFlags
		author   string
		title    string
		password string
	)
	cmd := &cobra.Command{
		Use:   "post <ref> [content|-]",
		Short: "Post the next section to a pad (turn-based)",
		Long: "Append a section. The author of the pad's last section may not post again — a\n" +
			"not_your_turn error means wait for the other agent (`pad wait`).",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, _, err := dir.open()
			if err != nil {
				return err
			}
			a, err := resolveAuthor(author)
			if err != nil {
				return err
			}
			content, err := readContent(args[1:])
			if err != nil {
				return err
			}
			pad, err := st.Post(args[0], a, title, content, password)
			if err != nil {
				return err
			}
			n := pad.Last().N
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "ref: %s\n", pad.Ref())
			fmt.Fprintf(out, "section: %d\nnext: %d\n", n, n+1)
			return nil
		},
	}
	dir.bind(cmd)
	authorFlag(cmd, &author)
	f := cmd.Flags()
	f.StringVar(&title, "title", "", "one-line title of this section (required)")
	f.StringVar(&password, "password", "", "the pad's password (when protected)")
	_ = cmd.MarkFlagRequired("title")
	return cmd
}

func newPadGetCmd() *cobra.Command {
	var (
		dir      dirFlags
		password string
	)
	cmd := &cobra.Command{
		Use:   "get <ref>",
		Short: "Compact pad status: table of contents + whose turn (no content)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, _, err := dir.open()
			if err != nil {
				return err
			}
			pad, err := st.Get(args[0], password)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			last := pad.Last()
			fmt.Fprintf(out, "ref: %s\n", pad.Ref())
			fmt.Fprintf(out, "project: %s\n", pad.Project)
			fmt.Fprintf(out, "created: %s\n", time.Unix(pad.CreatedTS, 0).UTC().Format(time.RFC3339))
			fmt.Fprintf(out, "sections: %d\n", len(pad.Sections))
			fmt.Fprintf(out, "protected: %t\n", pad.Protected())
			fmt.Fprintf(out, "turn: %s (last: %s)\n\n", pad.TurnState().WaitingFor, last.Author)
			w := tabwriter.NewWriter(out, 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "N\tAUTHOR\tTS\tTITLE")
			for _, sec := range pad.Sections {
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", sec.N, sec.Author,
					time.Unix(sec.TS, 0).UTC().Format(time.RFC3339), sec.Title)
			}
			return w.Flush()
		},
	}
	dir.bind(cmd)
	cmd.Flags().StringVar(&password, "password", "", "the pad's password (when protected)")
	return cmd
}

func newPadReadCmd() *cobra.Command {
	var (
		dir      dirFlags
		section  int
		since    int
		password string
	)
	cmd := &cobra.Command{
		Use:   "read <ref>",
		Short: "Print section contents (one, newer-than, or the whole pad)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if section != 0 && since != 0 {
				return fmt.Errorf("pass either --section or --since, not both")
			}
			st, _, err := dir.open()
			if err != nil {
				return err
			}
			pad, err := st.Get(args[0], password)
			if err != nil {
				return err
			}
			sections := pad.Sections
			switch {
			case section != 0:
				sections = nil
				for _, sec := range pad.Sections {
					if sec.N == section {
						sections = []store.Section{sec}
						break
					}
				}
				if sections == nil {
					return fmt.Errorf("pad %s has no section %d (last is %d)", pad.Ref(), section, pad.Last().N)
				}
			case since != 0:
				sections = nil
				for _, sec := range pad.Sections {
					if sec.N > since {
						sections = append(sections, sec)
					}
				}
			}
			printSections(cmd.OutOrStdout(), sections)
			return nil
		},
	}
	dir.bind(cmd)
	f := cmd.Flags()
	f.IntVar(&section, "section", 0, "print exactly this section number")
	f.IntVar(&since, "since", 0, "print every section numbered above this")
	f.StringVar(&password, "password", "", "the pad's password (when protected)")
	return cmd
}

func newPadWaitCmd() *cobra.Command {
	var (
		dir      dirFlags
		since    int
		timeout  string
		password string
	)
	cmd := &cobra.Command{
		Use:   "wait <ref> --since <n>",
		Short: "Block until the pad has a section newer than --since",
		Long: "Wait for a new section, print it, and exit 0 — designed to run in the background\n" +
			"and wake an agent when the reply arrives. With --timeout it exits 3 when nothing\n" +
			"arrived in time (1 is reserved for real errors). No --timeout waits until\n" +
			"interrupted. Unlike the MCP pad_wait tool this command has no server-side cap.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var d time.Duration
			if timeout != "" {
				var err error
				if d, err = parseDuration(timeout); err != nil {
					return err
				}
			}
			st, _, err := dir.open()
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			pad, changed, err := st.Wait(ctx, args[0], password, since, d)
			if err != nil {
				return err
			}
			if !changed {
				return errWaitTimeout
			}
			var fresh []store.Section
			for _, sec := range pad.Sections {
				if sec.N > since {
					fresh = append(fresh, sec)
				}
			}
			printSections(cmd.OutOrStdout(), fresh)
			return nil
		},
	}
	dir.bind(cmd)
	f := cmd.Flags()
	f.IntVar(&since, "since", 0, "the last section number you have seen (required)")
	f.StringVar(&timeout, "timeout", "", "give up after this long, e.g. 90s, 10m, 2h (empty = wait until interrupted)")
	f.StringVar(&password, "password", "", "the pad's password (when protected)")
	_ = cmd.MarkFlagRequired("since")
	return cmd
}

func newPadListCmd() *cobra.Command {
	var (
		dir     dirFlags
		project string
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List pads (metadata only), newest activity first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, _, err := dir.open()
			if err != nil {
				return err
			}
			pads, warnings, err := st.List(project)
			if err != nil {
				return err
			}
			for _, warn := range warnings {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning:", warn)
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "REF\tSECTIONS\tLAST AUTHOR\tLAST TS\tPROT\tTITLE")
			for _, p := range pads {
				prot := ""
				if p.Protected {
					prot = "yes"
				}
				fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\n", p.Ref, p.SectionCount, p.LastAuthor,
					time.Unix(p.LastTS, 0).UTC().Format(time.RFC3339), prot, p.Title)
			}
			return w.Flush()
		},
	}
	dir.bind(cmd)
	cmd.Flags().StringVar(&project, "project", "", "only list pads of this project")
	return cmd
}

func newPadDeleteCmd() *cobra.Command {
	var (
		dir       dirFlags
		assumeYes bool
	)
	cmd := &cobra.Command{
		Use:   "delete <ref>",
		Short: "Delete a pad (asks for confirmation)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, _, err := dir.open()
			if err != nil {
				return err
			}
			if !assumeYes {
				if !isInteractive() {
					return fmt.Errorf("refusing to delete without confirmation: pass --yes")
				}
				ok, err := confirmYesNo(cmd, fmt.Sprintf("Delete pad %s?", args[0]), false)
				if err != nil {
					return err
				}
				if !ok {
					fmt.Fprintln(cmd.ErrOrStderr(), "aborted")
					return nil
				}
			}
			if err := st.Delete(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "deleted %s\n", args[0])
			return nil
		},
	}
	dir.bind(cmd)
	cmd.Flags().BoolVar(&assumeYes, "yes", false, "delete without confirmation (for automation)")
	return cmd
}

func newPadPurgeCmd() *cobra.Command {
	var (
		dir       dirFlags
		project   string
		olderThan string
		assumeYes bool
	)
	cmd := &cobra.Command{
		Use:   "purge --older-than <duration>",
		Short: "Bulk-delete pads whose last activity is older than a duration",
		Long: "Delete every pad (optionally limited to one project) whose LAST section is older\n" +
			"than the given duration (e.g. 30d, 12h). Lists what would be deleted and asks for\n" +
			"confirmation; --yes skips the prompt for automation.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, err := parseDuration(olderThan)
			if err != nil {
				return err
			}
			st, _, err := dir.open()
			if err != nil {
				return err
			}
			pads, warnings, err := st.List(project)
			if err != nil {
				return err
			}
			for _, warn := range warnings {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning:", warn)
			}
			cutoff := time.Now().Add(-d)
			var victims []store.PadMeta
			for _, p := range pads {
				if time.Unix(p.LastTS, 0).Before(cutoff) {
					victims = append(victims, p)
				}
			}
			if len(victims) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "nothing to purge")
				return nil
			}
			for _, p := range victims {
				fmt.Fprintf(cmd.ErrOrStderr(), "  %s  (last activity %s)  %s\n", p.Ref,
					time.Unix(p.LastTS, 0).UTC().Format(time.RFC3339), p.Title)
			}
			if !assumeYes {
				if !isInteractive() {
					return fmt.Errorf("refusing to purge without confirmation: pass --yes")
				}
				ok, err := confirmYesNo(cmd, fmt.Sprintf("Delete these %d pads?", len(victims)), false)
				if err != nil {
					return err
				}
				if !ok {
					fmt.Fprintln(cmd.ErrOrStderr(), "aborted")
					return nil
				}
			}
			for _, p := range victims {
				if err := st.Delete(p.Ref); err != nil {
					return err
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "purged %d pads\n", len(victims))
			return nil
		},
	}
	dir.bind(cmd)
	f := cmd.Flags()
	f.StringVar(&project, "project", "", "only purge pads of this project")
	f.StringVar(&olderThan, "older-than", "", "age threshold on last activity, e.g. 30d, 12h (required)")
	f.BoolVar(&assumeYes, "yes", false, "purge without confirmation (for automation)")
	_ = cmd.MarkFlagRequired("older-than")
	return cmd
}

// parseDuration parses Go durations plus a day suffix ("30d" = 720h), which cleanup
// thresholds are naturally expressed in.
func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if n, ok := strings.CutSuffix(s, "d"); ok {
		if days, err := strconv.ParseFloat(n, 64); err == nil {
			return time.Duration(days * 24 * float64(time.Hour)), nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q (want e.g. 90s, 10m, 2h, 30d)", s)
	}
	return d, nil
}
