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
	"github.com/madnh/scratchpad/internal/pad"
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
		newPadSearchCmd(), newPadWaitCmd(), newPadTasksCmd(), newPadWhoCmd(),
		newPadListCmd(), newPadRulesCmd(), newPadDeleteCmd(), newPadPurgeCmd())
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
	cmd.AddCommand(list, newProjectRulesCmd())
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
// (header line, metadata comment, blank line, content) — stable, documented, pipeable.
// Rendering goes through the shared renderer so the printed form can never drift from
// what is actually stored.
func printSections(w io.Writer, sections []store.Section) {
	for i, sec := range sections {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprint(w, strings.TrimPrefix(
			pad.RenderSection(sec.N, sec.Author, sec.Title, time.Unix(sec.TS, 0), sec.Meta, sec.Content), "\n"))
	}
}

// printTOC writes a table of contents with the routing columns that make a long pad
// navigable: who a section was for, what it answers, and which task it belongs to.
func printTOC(w io.Writer, sections []store.Section) {
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "N\tAUTHOR\tTS\tTO\tRE\tTASK\tTITLE")
	for _, sec := range sections {
		re, task := "", ""
		if sec.Re > 0 {
			re = "§" + strconv.Itoa(sec.Re)
		}
		if sec.Task > 0 {
			task = "T" + strconv.Itoa(sec.Task)
			if sec.Status != "" {
				task += " " + string(sec.Status)
			}
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", sec.N, sec.Author,
			time.Unix(sec.TS, 0).UTC().Format(time.RFC3339),
			strings.Join(sec.To, ","), re, task, sec.Title)
	}
	_ = tw.Flush()
}

// printOwed writes outstanding acknowledgements, oldest first — the ones most likely to
// need a human.
func printOwed(w io.Writer, owed []store.Owed, now time.Time) {
	for _, o := range owed {
		fmt.Fprintf(w, "  %s  %s -> %s  (%s ago)  %s\n", o.What, o.From, o.To,
			humanAge(now.Sub(time.Unix(o.TS, 0))), o.Title)
	}
}

// humanAge renders a duration the way a person reads one.
func humanAge(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
}

func newPadCreateCmd() *cobra.Command {
	var (
		dir      dirFlags
		project  string
		author   string
		title    string
		protect  bool
		ackRules string
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
			created, pw, err := st.CreatePad(store.CreateRequest{
				Project: config.ResolveProject(cfg.Get(), project), Author: a,
				Title: title, Content: content, Protect: protect, AckRules: ackRules,
			})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "ref: %s\n", created.Ref())
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
	ackRulesFlag(cmd, &ackRules)
	_ = cmd.MarkFlagRequired("title")
	return cmd
}

// ackRulesFlag binds --ack-rules. It is spelled the same on create and post because it
// answers the same question in both: have you read how work is done here?
func ackRulesFlag(cmd *cobra.Command, ack *string) {
	cmd.Flags().StringVar(ack, "ack-rules", "",
		"the digest of the rules you have read (required on your first post to a pad that has rules, and again after they change)")
}

func newPadPostCmd() *cobra.Command {
	var (
		dir      dirFlags
		author   string
		title    string
		password string
		to       []string
		re       int
		taskOpen bool
		task     int
		status   string
		ackRules string
	)
	cmd := &cobra.Command{
		Use:   "post <ref> [content|-]",
		Short: "Post the next section to a pad (turn-based)",
		Long: "Append a section. The author of the pad's last MESSAGE may not post again — a\n" +
			"not_your_turn error means wait for another agent (`pad wait`). Task events are\n" +
			"exempt, so a coordinator can open several tasks in a row.\n\n" +
			"--to addresses the section: everyone can still READ it, but only those named are\n" +
			"woken by `pad wait --wake-for me`. --re anchors it to the section it answers, and\n" +
			"also addresses that section's author.\n\n" +
			"--task-open opens a task (requires --to: a task must have an owner) and prints the\n" +
			"number it was given. --task with --status moves an existing one; only its owners\n" +
			"(their own slice) or its opener (reassign, drop, force-close) may do that.",
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
			if taskOpen && task > 0 {
				return fmt.Errorf("pass either --task-open (a new task) or --task <n> (an existing one), not both")
			}
			meta := pad.Meta{To: to, Re: re, Task: task, Status: pad.Status(status)}
			// Opening a task or reporting a status puts the section in the task's
			// RECORD. `--task N` on its own is the other layer — a message that merely
			// cross-references the work — so it keeps the turn rule and does not stand
			// in for the owner's answer.
			if taskOpen || status != "" {
				meta.Kind = pad.KindTask
			}
			res, err := st.Post(store.PostRequest{
				Ref: args[0], Author: a, Title: title, Content: content,
				Password: password, Meta: meta, OpenTask: taskOpen, AckRules: ackRules,
			})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			// When a full pad continued, `ref:` is the SUCCESSOR — the pad this post
			// actually landed in. Saying so on its own line keeps the machine-readable
			// shape ("ref:" is still where you posted) while making the move impossible to
			// miss for a person reading the same output.
			if res.ContinuedFrom != "" {
				fmt.Fprintf(out, "continued-from: %s\n", res.ContinuedFrom)
			}
			fmt.Fprintf(out, "ref: %s\n", res.Pad.Ref())
			fmt.Fprintf(out, "section: %d\nnext: %d\n", res.Section, res.Section+1)
			if res.Task > 0 {
				verb := "task"
				if taskOpen {
					verb = "opened"
				}
				fmt.Fprintf(out, "%s: T%d\n", verb, res.Task)
			}
			// Warnings go to stderr: the post SUCCEEDED, and stdout must stay clean for
			// whatever is parsing it.
			for _, w := range res.Warnings {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning:", w)
			}
			return nil
		},
	}
	dir.bind(cmd)
	authorFlag(cmd, &author)
	f := cmd.Flags()
	f.StringVar(&title, "title", "", "one-line title of this section (required)")
	f.StringVar(&password, "password", "", "the pad's password (when protected)")
	f.StringSliceVar(&to, "to", nil,
		"authors this section is addressed to (comma-separated); omit to broadcast."+
			" On a TASK EVENT it is not addressing — it sets the task's owners, and only the opener may")
	f.IntVar(&re, "re", 0,
		"the section number this one answers; it also addresses that section's author,"+
			" except on a task event where `to` means ownership")
	f.BoolVar(&taskOpen, "task-open", false, "open a new task (needs --to) and print its number")
	f.IntVar(&task, "task", 0,
		"the number of an existing task this section concerns; on its own it merely references the task and stays an ordinary message")
	f.StringVar(&status, "status", "",
		"move the task: open, wip, blocked, done or dropped — this is what makes the section a task event")
	ackRulesFlag(cmd, &ackRules)
	_ = cmd.MarkFlagRequired("title")
	return cmd
}

func newPadGetCmd() *cobra.Command {
	var (
		dir      dirFlags
		password string
		author   string
		kind     string
	)
	cmd := &cobra.Command{
		Use:   "get <ref>",
		Short: "Compact pad status: table of contents + whose turn (no content)",
		Long: "Print the pad's table of contents with its routing columns (to, re, task) and the\n" +
			"turn state, without any section bodies. With --as, also print your inbox: what\n" +
			"was addressed to you since your own last post — the cheap way back into a long\n" +
			"pad after being away.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, _, err := dir.open()
			if err != nil {
				return err
			}
			p, err := st.Get(args[0], password)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "ref: %s\n", p.Ref())
			fmt.Fprintf(out, "project: %s\n", p.Project)
			fmt.Fprintf(out, "created: %s\n", time.Unix(p.CreatedTS(), 0).UTC().Format(time.RFC3339))
			fmt.Fprintf(out, "sections: %d\n", len(p.Sections))
			fmt.Fprintf(out, "authors: %s\n", strings.Join(p.Authors(), ", "))
			fmt.Fprintf(out, "protected: %t\n", p.Protected())
			// "last message", not "last section": with task events and rules in the file
			// the last section is often bookkeeping, and naming its author beside the turn
			// reads as if THEY held it.
			fmt.Fprintf(out, "turn: %s (last message: %s)\n", p.TurnState().WaitingFor, p.TurnState().LastAuthor)
			if a := strings.TrimSpace(author); a != "" {
				in := p.Inbox(a)
				fmt.Fprintf(out, "\ninbox for %s (your last post: §%d)\n", a, in.Since)
				if len(in.Unread) == 0 {
					fmt.Fprintln(out, "  nothing addressed to you since then")
				}
				printTOC(out, in.Unread)
				if len(in.Owes) > 0 {
					fmt.Fprintln(out, "you owe:")
					printOwed(out, in.Owes, time.Now())
				}
				if len(in.Awaiting) > 0 {
					fmt.Fprintln(out, "waiting on others:")
					printOwed(out, in.Awaiting, time.Now())
				}
			}
			fmt.Fprintln(out)
			printTOC(out, p.Select(store.Selector{Kind: pad.Kind(kind)}).Sections)
			// An agent asking for a pad's state is usually about to post to it, so this is
			// where it should meet the rules — on stderr, before it has written anything,
			// rather than as a refusal after it has.
			printRulesFor(cmd.ErrOrStderr(), st, p, author)
			return nil
		},
	}
	dir.bind(cmd)
	authorFlag(cmd, &author)
	f := cmd.Flags()
	f.StringVar(&password, "password", "", "the pad's password (when protected)")
	f.StringVar(&kind, "kind", "", "limit the table of contents to one stream: message or task")
	return cmd
}

func newPadReadCmd() *cobra.Command {
	var (
		dir      dirFlags
		section  int
		since    int
		kind     string
		task     int
		password string
	)
	cmd := &cobra.Command{
		Use:   "read <ref>",
		Short: "Print section contents (one, newer-than, a task's thread, or the whole pad)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if section != 0 && since != 0 {
				return fmt.Errorf("pass either --section or --since, not both")
			}
			st, _, err := dir.open()
			if err != nil {
				return err
			}
			p, err := st.Get(args[0], password)
			if err != nil {
				return err
			}
			res := p.Select(store.Selector{
				Section: section, Since: since, Kind: pad.Kind(kind), Task: task,
			})
			if section != 0 && len(res.Sections) == 0 {
				return fmt.Errorf("pad %s has no section %d (last is %d)", p.Ref(), section, p.Last().N)
			}
			printSections(cmd.OutOrStdout(), res.Sections)
			return nil
		},
	}
	dir.bind(cmd)
	f := cmd.Flags()
	f.IntVar(&section, "section", 0, "print exactly this section number")
	f.IntVar(&since, "since", 0, "print every section numbered above this")
	f.StringVar(&kind, "kind", "", "limit to one stream: message or task")
	f.IntVar(&task, "task", 0, "print one task's thread (its opening section and everything referencing it)")
	f.StringVar(&password, "password", "", "the pad's password (when protected)")
	return cmd
}

// searchTextWidth caps how much of a matching line is printed. Agent prose runs to whole
// paragraphs on one line, and a search is a way to FIND a section, not to read it — the
// ellipsis says the line goes on and `pad read --section` is how to see the rest.
const searchTextWidth = 140

func newPadSearchCmd() *cobra.Command {
	var (
		dir        dirFlags
		project    string
		ref        string
		password   string
		excludePad []string
		author     string
		kind       string
		before     string
		after      string
		oldest     bool
		asRegexp   bool
		word       bool
		matchCase  bool
		limit      int
	)
	cmd := &cobra.Command{
		Use:   "search <pattern>",
		Short: "Find a word or phrase in pad CONTENT, across the store",
		Long: "Search the prose inside pads — the one question the other read commands cannot\n" +
			"answer, since they select by section, kind or task rather than by what was said.\n" +
			"Each hit names the pad, the section and the line, so `pad read <ref> --section <n>`\n" +
			"reads on from where a hit was found. Section TITLES are searched too.\n\n" +
			"The pattern is a literal substring and matching ignores case; --regexp reads it as\n" +
			"a pattern instead, --word requires a whole word, --case-sensitive stops the folding.\n\n" +
			"Results are grouped by pad, most recently active first. Looking for where something\n" +
			"was DECIDED is the opposite question — that is almost always the FIRST time the word\n" +
			"appears, and the default buries it under every later restatement — so pass --oldest,\n" +
			"and narrow with --before/--after or --exclude-pad when a live discussion drowns out\n" +
			"the pad the thing was settled in.\n\n" +
			"There is no index: a search reads the pads it looks at, so narrow it with --project\n" +
			"or --pad on a large store. Protected pads are NOT searched — name one with --pad and\n" +
			"its --password to search inside it — and every pad left out is reported on stderr, so\n" +
			"an empty result never quietly means \"not searched\".",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			beforeTS, err := parseWhen(before)
			if err != nil {
				return fmt.Errorf("--before: %w", err)
			}
			afterTS, err := parseWhen(after)
			if err != nil {
				return fmt.Errorf("--after: %w", err)
			}
			st, _, err := dir.open()
			if err != nil {
				return err
			}
			res, err := st.Search(store.SearchRequest{
				Query: args[0], Project: project, Ref: ref, Password: password,
				ExcludePads: excludePad, Regexp: asRegexp, Word: word, CaseSensitive: matchCase,
				Author: author, Kind: kind, Before: beforeTS, After: afterTS,
				Oldest: oldest, Limit: limit,
			})
			if err != nil {
				return err
			}
			errOut := cmd.ErrOrStderr()
			for _, warn := range res.Warnings {
				fmt.Fprintln(errOut, "warning:", warn)
			}
			// No matches means NOTHING on stdout, header row included — grep's convention,
			// and here it is a contract rather than a nicety: a caller doing
			// `search … 2>/dev/null | wc -l` reads a lone header as one result. Everything
			// a person needs to interpret the silence is on stderr, where it does not
			// count as output.
			if len(res.Hits) > 0 {
				w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
				fmt.Fprintln(w, "REF\tSECTION\tWHERE\tAUTHOR\tMATCH")
				for _, h := range res.Hits {
					where := "L" + strconv.Itoa(h.Line)
					if h.InTitle {
						where = "title"
					}
					fmt.Fprintf(w, "%s\t§%d\t%s\t%s\t%s\n", h.Ref, h.Section, where, h.Author,
						truncateRunes(h.Text, searchTextWidth))
				}
				if err := w.Flush(); err != nil {
					return err
				}
			}
			// The summary goes to stderr so the table stays pipeable, and it is printed even
			// when nothing matched: "0 hits in 12 pads" and "0 hits, 12 pads skipped" are
			// different answers to the same search.
			//
			// What was NOT searched belongs on that same line rather than only on the one
			// below it. A reader who takes in one line takes in the wrong answer otherwise,
			// and this is precisely the shape of silent miss the command exists to avoid.
			summary := fmt.Sprintf("%d match(es) in %d pad(s) searched", len(res.Hits), res.Scanned)
			if len(res.Skipped) > 0 {
				summary += fmt.Sprintf(", %d pad(s) NOT searched (protected)", len(res.Skipped))
			}
			fmt.Fprintln(errOut, summary)
			if res.Truncated {
				fmt.Fprintf(errOut, "stopped at --limit %d; there may be more\n", limit)
			}
			if len(res.Skipped) > 0 {
				fmt.Fprintf(errOut, "not searched: %s\n", strings.Join(res.Skipped, ", "))
			}
			return nil
		},
	}
	dir.bind(cmd)
	f := cmd.Flags()
	f.StringVar(&project, "project", "", "only search pads of this project")
	f.StringVar(&ref, "pad", "", "only search this pad (the only way to search a protected one)")
	f.StringVar(&password, "password", "", "the pad's password (with --pad, when protected)")
	f.StringSliceVar(&excludePad, "exclude-pad", nil, "skip these pads (repeatable) — e.g. the one being argued in today")
	// --author, not --as: everywhere else --as says who YOU are, and this names whose
	// sections to look at. One flag cannot mean both without being read as the wrong one.
	f.StringVar(&author, "author", "", "only sections written by this author")
	f.StringVar(&kind, "kind", "", "only sections of one stream: message, task or rules")
	f.StringVar(&before, "before", "", "only sections written before this: a date (2026-07-01) or an age (30d)")
	f.StringVar(&after, "after", "", "only sections written after this: a date (2026-07-01) or an age (30d)")
	f.BoolVar(&oldest, "oldest", false, "earliest first — where something was first said, not last repeated")
	f.BoolVar(&asRegexp, "regexp", false, "read the pattern as a regular expression")
	f.BoolVar(&word, "word", false, "match whole words only")
	f.BoolVar(&matchCase, "case-sensitive", false, "match case exactly (default: ignore case)")
	f.IntVar(&limit, "limit", 0, "stop after this many hits (0 = no cap)")
	return cmd
}

// parseWhen reads a point in time written either way people naturally reach for one: an
// absolute date ("2026-07-01", or a full RFC3339 stamp) or an AGE ("30d", "12h"), which
// is `purge --older-than`'s vocabulary and means "that long ago". Empty is no bound.
//
// It returns unix seconds because that is what a section's timestamp is; 0 means unset,
// which is safe here for the same reason it is safe everywhere else in this file — a pad
// written at the epoch is not a case the store can produce.
func parseWhen(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if d, err := parseDuration(s); err == nil {
		return time.Now().Add(-d).Unix(), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.Unix(), nil
		}
	}
	return 0, fmt.Errorf("invalid time %q (want a date like 2026-07-01 or an age like 30d)", s)
}

// truncateRunes shortens a line for the table without cutting a multi-byte character in
// half — the searches this exists for are frequently not ASCII.
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

func newPadWaitCmd() *cobra.Command {
	var (
		dir      dirFlags
		since    int
		timeout  string
		password string
		author   string
		wakeFor  []string
		unacked  string
	)
	cmd := &cobra.Command{
		Use:   "wait <ref> --since <n>",
		Short: "Block until a section matching your selectors arrives",
		Long: "Wait for a new section, print it, and exit 0 — designed to run in the background\n" +
			"and wake an agent when the reply arrives. With --timeout it exits 3 when nothing\n" +
			"arrived in time (1 is reserved for real errors). No --timeout waits until\n" +
			"interrupted. Unlike the MCP pad_wait tool this command has no server-side cap.\n\n" +
			"By default ANY new section wakes you. In a pad with several agents, pass --as and\n" +
			"--wake-for me so exchanges between two OTHER agents stop interrupting you. You can\n" +
			"still read everything — the selectors only decide what is worth waking for, and\n" +
			"whatever wakes you, the sections you missed are still listed (on stderr) so\n" +
			"filtering never leaves a silent gap.\n\n" +
			"--unacked also returns when something YOU addressed has gone unanswered that long,\n" +
			"so an agent that was never listening cannot hang this wait forever.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var d, un time.Duration
			var err error
			if timeout != "" {
				if d, err = parseDuration(timeout); err != nil {
					return err
				}
			}
			if unacked != "" {
				if un, err = parseDuration(unacked); err != nil {
					return err
				}
			}
			wake, err := pad.ParseWake(wakeFor)
			if err != nil {
				return err
			}
			a := strings.TrimSpace(author)
			if a == "" {
				a = strings.TrimSpace(os.Getenv(config.EnvAuthor))
			}
			st, _, err := dir.open()
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			res, err := st.Wait(ctx, store.WaitRequest{
				Ref: args[0], Password: password, Since: since, Author: a,
				Wake: wake, Timeout: d, Unacked: un,
			})
			if err != nil {
				return err
			}
			if !res.Changed {
				return errWaitTimeout
			}
			errOut := cmd.ErrOrStderr()
			if res.Reason == "unacked" {
				fmt.Fprintln(errOut, "unanswered — nobody may be listening:")
				printOwed(errOut, res.Unacked, time.Now())
				return nil
			}
			// The catch-up list goes to stderr so stdout stays exactly what it has always
			// been: the sections themselves, pipeable.
			if len(res.Skipped) > 0 {
				fmt.Fprintf(errOut, "also missed %d section(s) that did not match your selectors:\n", len(res.Skipped))
				printTOC(errOut, res.Skipped)
			}
			// Being woken by a pad you have never posted to is exactly the "joining"
			// moment, so the rules come with the wake-up rather than with the rejection
			// of the reply it is about to write.
			printRulesFor(errOut, st, res.Pad, a)
			printSections(cmd.OutOrStdout(), res.Matched)
			return nil
		},
	}
	dir.bind(cmd)
	authorFlag(cmd, &author)
	f := cmd.Flags()
	f.IntVar(&since, "since", 0, "the last section number you have seen (required)")
	f.StringVar(&timeout, "timeout", "", "give up after this long, e.g. 90s, 10m, 2h (empty = wait until interrupted)")
	f.StringSliceVar(&wakeFor, "wake-for", nil,
		"what should WAKE you: any (default), me, mine, tasks, task:<n> — comma-separated, they union")
	f.StringVar(&unacked, "unacked", "", "also return when something you addressed has gone unanswered this long, e.g. 15m")
	f.StringVar(&password, "password", "", "the pad's password (when protected)")
	_ = cmd.MarkFlagRequired("since")
	return cmd
}

func newPadTasksCmd() *cobra.Command {
	var (
		dir      dirFlags
		task     int
		openOnly bool
		password string
	)
	cmd := &cobra.Command{
		Use:   "tasks <ref>",
		Short: "The pad's task board (derived, read-only)",
		Long: "Show where the work stands without reading the pad. A task's status is folded from\n" +
			"the events that moved it, PER OWNER: a task shared by two agents is done only when\n" +
			"both are, so one agent finishing cannot make the other's work disappear.\n\n" +
			"Read-only by design — tasks are opened, moved and closed with `pad post`.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, _, err := dir.open()
			if err != nil {
				return err
			}
			p, err := st.Get(args[0], password)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if task > 0 {
				t, ok := p.Task(task)
				if !ok {
					return fmt.Errorf("pad %s has no task T%d", p.Ref(), task)
				}
				fmt.Fprintf(out, "T%d  %s  %s\n", t.Task, t.Status, t.Title)
				fmt.Fprintf(out, "opened by %s in §%d\n", t.Opener, t.OpenedSection)
				for _, o := range t.Owners {
					fmt.Fprintf(out, "  %-16s %s\n", o.Author, o.Status)
				}
				fmt.Fprintln(out)
				printSections(out, p.Thread(task))
				return nil
			}
			w := tabwriter.NewWriter(out, 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "TASK\tSTATUS\tOWNERS\tSECTIONS\tTITLE")
			for _, t := range p.Tasks() {
				if openOnly && !t.Open() {
					continue
				}
				owners := make([]string, 0, len(t.Owners))
				for _, o := range t.Owners {
					mark := "..."
					if o.Status == pad.StatusDone {
						mark = "done"
					}
					owners = append(owners, o.Author+":"+mark)
				}
				span := fmt.Sprintf("§%d->§%d", t.OpenedSection, t.LastSection)
				fmt.Fprintf(w, "T%d\t%s\t%s\t%s\t%s\n", t.Task, t.Status,
					strings.Join(owners, " "), span, t.Title)
			}
			return w.Flush()
		},
	}
	dir.bind(cmd)
	f := cmd.Flags()
	f.IntVar(&task, "task", 0, "show one task and its whole thread")
	f.BoolVar(&openOnly, "open", false, "list only tasks that still need attention")
	f.StringVar(&password, "password", "", "the pad's password (when protected)")
	return cmd
}

func newPadWhoCmd() *cobra.Command {
	var (
		dir      dirFlags
		password string
	)
	cmd := &cobra.Command{
		Use:   "who <ref>",
		Short: "Per-author last activity, and what each one owes",
		Long: "Who has fallen behind. This deliberately reports LAST ACTIVITY and outstanding\n" +
			"acknowledgements rather than \"who is currently waiting\": presence cannot be\n" +
			"derived from an append-only transcript, and it would lie in both directions —\n" +
			"an agent busy working is not inside a wait, and an agent parked in a wait with\n" +
			"the wrong selectors is not listening for you.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, _, err := dir.open()
			if err != nil {
				return err
			}
			p, err := st.Get(args[0], password)
			if err != nil {
				return err
			}
			now := time.Now()
			out := cmd.OutOrStdout()
			w := tabwriter.NewWriter(out, 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "AUTHOR\tLAST\tAGE\tOWES")
			for _, part := range p.Participants() {
				owes := make([]string, 0, len(part.Owes))
				for _, o := range part.Owes {
					owes = append(owes, fmt.Sprintf("%s (%s)", o.What, humanAge(now.Sub(time.Unix(o.TS, 0)))))
				}
				// An author who has only ever been ADDRESSED has no last section, and an
				// age counted from the epoch would print "20668d" — the exact row this
				// command exists to make legible, rendered as noise. Say "never".
				last, age := fmt.Sprintf("§%d", part.LastSection), humanAge(now.Sub(time.Unix(part.LastTS, 0)))
				if part.LastSection == 0 {
					last, age = "—", "never"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", part.Author, last, age, strings.Join(owes, " · "))
			}
			return w.Flush()
		},
	}
	dir.bind(cmd)
	cmd.Flags().StringVar(&password, "password", "", "the pad's password (when protected)")
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
			// AUTHORS says who is on the pad, LAST AUTHOR who is blocked by the turn
			// rule — the second is not implied by the first, so both earn a column.
			fmt.Fprintln(w, "REF\tSECTIONS\tAUTHORS\tLAST AUTHOR\tLAST TS\tPROT\tTITLE")
			for _, p := range pads {
				// A pad this process cannot read still gets its row. Dropping it made a pad
				// that exists look deleted; the columns it has no values for stay blank and
				// the reason goes where the title would be.
				if p.Unreadable != "" {
					fmt.Fprintf(w, "%s\t-\t-\t-\t-\t-\t(unreadable: %s)\n", p.Ref, p.Unreadable)
					continue
				}
				prot := ""
				if p.Protected {
					prot = "yes"
				}
				fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\t%s\n", p.Ref, p.SectionCount,
					strings.Join(p.Authors, ", "), p.LastAuthor,
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
