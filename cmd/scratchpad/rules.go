package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/madnh/scratchpad/internal/pad"
	"github.com/madnh/scratchpad/internal/store"
)

// The rules commands, one per level: `rules` (whole store), `project rules` (one
// project), `pad rules` (one pad). Each one reads without arguments and writes with
// --set, which is the shape a person expects from a config command and the shape an agent
// can discover from `--help` without a round trip.

// rulesLong is shared by all three so the model is explained wherever a person lands.
const rulesLong = "Rules are how a pad is expected to be worked: message length, when to\n" +
	"open a task instead of narrating, whether to address or broadcast. They apply in\n" +
	"three levels — store, project, pad — each EXTENDING the ones above it, with the\n" +
	"pad having the final word.\n\n" +
	"An agent posting to a pad for the first time must quote the digest of the rules in\n" +
	"force (`--ack-rules`), so nobody joins a long-running pad without having seen how\n" +
	"it works. Later changes are not gated: a pad's rules section is a broadcast, so it\n" +
	"wakes everyone already there."

func newRulesCmd() *cobra.Command {
	var (
		dir     dirFlags
		set     string
		replace bool
	)
	cmd := &cobra.Command{
		Use:   "rules [--set <text|->]",
		Short: "Read or write the rules that apply to every pad in this store",
		Long:  "Read or write the store-wide rules.\n\n" + rulesLong,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, _, err := dir.open()
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("set") {
				text, err := ruleText(cmd, set)
				if err != nil {
					return err
				}
				if err := st.SetStoreRules(text, replace); err != nil {
					return err
				}
			}
			rules, err := st.ProjectRuleSet("")
			if err != nil {
				return err
			}
			return printRules(cmd, rules)
		},
	}
	dir.bind(cmd)
	bindRuleWriteFlags(cmd, &set, &replace)
	return cmd
}

func newProjectRulesCmd() *cobra.Command {
	var (
		dir     dirFlags
		set     string
		replace bool
	)
	cmd := &cobra.Command{
		Use:   "rules <project> [--set <text|->]",
		Short: "Read or write the rules of one project",
		Long:  "Read or write a project's rules — they extend the store's.\n\n" + rulesLong,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, _, err := dir.open()
			if err != nil {
				return err
			}
			project := args[0]
			if cmd.Flags().Changed("set") {
				text, err := ruleText(cmd, set)
				if err != nil {
					return err
				}
				if err := st.SetProjectRules(project, text, replace); err != nil {
					return err
				}
			}
			rules, err := st.ProjectRuleSet(project)
			if err != nil {
				return err
			}
			return printRules(cmd, rules)
		},
	}
	dir.bind(cmd)
	bindRuleWriteFlags(cmd, &set, &replace)
	return cmd
}

func newPadRulesCmd() *cobra.Command {
	var (
		dir      dirFlags
		set      string
		replace  bool
		author   string
		title    string
		password string
	)
	cmd := &cobra.Command{
		Use:   "rules <ref> [--set <text|->]",
		Short: "Read the rules in force on a pad, or write the pad's own",
		Long: "Read the three levels of rules in force on a pad, or append a new rules section\n" +
			"to it with --set.\n\n" +
			"Writing rules is an APPEND like everything else in a pad: the previous rules stay\n" +
			"readable as history and the newest section is what is in force. It does not take\n" +
			"the turn, so stating the rules never blocks the conversation.\n\n" +
			"Without --as, the section is written as \"" + pad.SystemAuthor + "\" — the identity for a change a\n" +
			"PERSON made. An agent setting the rules of a pad it works on passes its own --as.\n\n" +
			rulesLong,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, _, err := dir.open()
			if err != nil {
				return err
			}
			ref := args[0]
			if cmd.Flags().Changed("set") {
				text, err := ruleText(cmd, set)
				if err != nil {
					return err
				}
				// The operator identity is the DEFAULT here, unlike every other writing
				// command: `pad rules --set` at a terminal is a person deciding how the
				// pad works, and making them invent an agent name to say so would put a
				// fictional teammate in the transcript.
				a := strings.TrimSpace(author)
				if a == "" {
					a = pad.SystemAuthor
				}
				res, err := st.SetPadRules(ref, a, title, text, password, replace)
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "section: %d\n", res.Section)
			}
			rules, err := st.PadRules(ref, password)
			if err != nil {
				return err
			}
			return printRules(cmd, rules)
		},
	}
	dir.bind(cmd)
	authorFlag(cmd, &author)
	bindRuleWriteFlags(cmd, &set, &replace)
	f := cmd.Flags()
	f.StringVar(&title, "title", "", "title of the rules section (default \"Pad rules\")")
	f.StringVar(&password, "password", "", "the pad's password (when protected)")
	return cmd
}

// bindRuleWriteFlags binds the two flags every level shares. Writing is behind an
// explicit --set rather than "an argument means write", so a mistyped read can never
// overwrite the rules with the word the person meant as a filter.
//
// --set CARRIES the text rather than being a boolean beside a positional argument,
// because rules are a bullet list: `--set "- keep it short"` as a positional would be
// parsed as a flag by any getopt-style parser, and every rule set anyone writes starts
// with a dash. As a flag value it is simply the value.
func bindRuleWriteFlags(cmd *cobra.Command, set *string, replace *bool) {
	f := cmd.Flags()
	f.StringVar(set, "set", "", "write these rules, replacing what was there (\"-\" reads them from stdin)")
	f.BoolVar(replace, "replace", false,
		"with --set: these rules REPLACE the levels above instead of extending them")
}

// ruleText resolves --set: the flag's own value, or stdin when it is "-".
func ruleText(cmd *cobra.Command, set string) (string, error) {
	if set == "-" {
		b, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", fmt.Errorf("read rules from stdin: %w", err)
		}
		return string(b), nil
	}
	return set, nil
}

// printRules renders the effective rules with one block per level, each labelled with
// where it came from.
//
// The levels are never flattened into one blob: a person reading rules needs to know
// which line is the store's law and which is this pad's local habit, because that is what
// tells them where to go to change it.
func printRules(cmd *cobra.Command, rules pad.Rules) error {
	out := cmd.OutOrStdout()
	if rules.Empty() && len(rules.Layers) == 0 {
		fmt.Fprintln(out, "no rules here — write some with --set (see --help)")
		return nil
	}
	fmt.Fprintf(out, "digest: %s\n", rules.Digest)
	for _, l := range rules.Layers {
		fmt.Fprintln(out)
		fmt.Fprintf(out, "# %s — %s%s%s\n", l.Level, l.Source,
			ruleAuthorSuffix(l), map[bool]string{true: "  [superseded]"}[l.Superseded])
		fmt.Fprintln(out, l.Text)
	}
	if len(rules.History) > 0 {
		fmt.Fprintf(out, "\nearlier versions: %s\n", joinSections(rules.History))
	}
	return nil
}

// ruleAuthorSuffix names who wrote a pad-level rule set and when — the file levels carry
// their path instead, which is all the provenance a file has.
func ruleAuthorSuffix(l pad.Layer) string {
	if l.Author == "" {
		return ""
	}
	return fmt.Sprintf(" (%s, %s ago)", l.Author, humanAge(time.Since(time.Unix(l.TS, 0))))
}

// joinSections renders a list of section numbers the way everything else prints them.
func joinSections(ns []int) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = fmt.Sprintf("§%d", n)
	}
	return strings.Join(parts, ", ")
}

// printRulesFor writes the rules to stderr for an author who has not posted in this pad
// yet — the same moment the post gate would fire, but as a heads-up rather than a
// refusal. `pad get` and `pad wait` are how an agent arrives at a pad, so this is where
// it learns the rules BEFORE it has written anything.
func printRulesFor(w io.Writer, st *store.Store, p *store.Pad, author string) {
	if strings.TrimSpace(author) == "" || p.HasPosted(author) {
		return
	}
	// From the pad already loaded: its password was checked when it was read, and
	// re-reading it here would rebuild every section body a second time.
	rules, err := st.RulesOf(p)
	if err != nil || rules.Empty() {
		return
	}
	fmt.Fprintf(w, "\nthis pad has rules and you have not posted here yet — your first post must carry --ack-rules %s\n\n%s\n",
		rules.Digest, rules.Text)
}
