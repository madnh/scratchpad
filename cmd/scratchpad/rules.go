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
	"An agent posting to a pad must quote the digest of the rules in force (`--ack-rules`),\n" +
	"so nobody writes here without having seen how it works. WHEN it is asked again is the\n" +
	"deployment's `rules.reack`: by default, every time the rules CHANGE, so a new version\n" +
	"binds the agents already on the pad and not only the next one to arrive. Set it to\n" +
	"`once` for the older behaviour, where the rules are read on the way in and never again.\n\n" +
	"A change to the two FILE levels is announced into the pads it binds by default: a\n" +
	"short notice in each one, which wakes whoever is waiting there. That is what a rules\n" +
	"edit is for — a version nobody hears about binds only whoever happens to post next.\n" +
	"Pass --notify=false to change them quietly. A PAD's own rules need no flag: they are\n" +
	"a section in that pad, so writing them already wakes everyone on it.\n\n" +
	"WRITING rules asks two more questions. Each level prints its own digest, and\n" +
	"--set must quote it in --if-digest — a rule set is EDITED, not appended to, so\n" +
	"writing one without saying which version you read is how one agent silently drops\n" +
	"another's. And who may write at all is the deployment's to decide (the marker's\n" +
	"`rules` group): by default the store's and a project's rules are the operator's,\n" +
	"changed in the Web UI or by editing the file, and a pad's belong to the agent that\n" +
	"opened it."

func newRulesCmd() *cobra.Command {
	var (
		dir      dirFlags
		set      string
		replace  bool
		ifDigest string
		notify   bool
	)
	cmd := &cobra.Command{
		Use:   "rules [--set <text|-> --if-digest <digest> --notify]",
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
				if err := st.SetStoreRules(store.RulesWrite{
					Text: text, Replace: replace, IfDigest: ifDigest, By: store.ByAgent,
				}); err != nil {
					return err
				}
				if err := announceRules(cmd, st, store.NotifyScope{Level: pad.LevelStore}, notify); err != nil {
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
	bindRuleWriteFlags(cmd, &set, &replace, &ifDigest, &notify)
	return cmd
}

func newProjectRulesCmd() *cobra.Command {
	var (
		dir      dirFlags
		set      string
		replace  bool
		ifDigest string
		notify   bool
	)
	cmd := &cobra.Command{
		Use:   "rules <project> [--set <text|-> --if-digest <digest> --notify]",
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
				if err := st.SetProjectRules(project, store.RulesWrite{
					Text: text, Replace: replace, IfDigest: ifDigest, By: store.ByAgent,
				}); err != nil {
					return err
				}
				scope := store.NotifyScope{Level: pad.LevelProject, Project: project}
				if err := announceRules(cmd, st, scope, notify); err != nil {
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
	bindRuleWriteFlags(cmd, &set, &replace, &ifDigest, &notify)
	return cmd
}

// announceRules is --notify: tell the pads this level binds that their rules moved.
//
// It runs after the write and never turns a saved edit into a failed command. The rules are
// on disk by the time this is called; exiting non-zero would say the opposite of what
// happened, and the person would write them again. What went wrong is printed instead —
// including which pads were left out and why, because a fan-out that quietly did less than
// it promised is one people go on trusting.
func announceRules(cmd *cobra.Command, st *store.Store, scope store.NotifyScope, notify bool) error {
	out := cmd.OutOrStdout()
	if !notify {
		// Said out loud, because a quiet change is the one that surprises people later:
		// nobody is interrupted, and the rules still bind everyone from their next post.
		fmt.Fprintln(out, "rules written, quietly (--notify=false). Nobody was told;"+
			" agents already on a pad find out when they next try to post.")
		return nil
	}
	res, err := st.NotifyRulesChanged(scope)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "rules written, but the announcement failed: %v\n", err)
		return nil
	}
	fmt.Fprintf(out, "rules written, and announced in %d pad(s)%s\n", len(res.Notified), skipTail(res.Skipped))
	for ref, msg := range res.Failed {
		fmt.Fprintf(cmd.ErrOrStderr(), "  could not reach %s: %s\n", ref, msg)
	}
	return nil
}

// skipTail names the pads an announcement did not reach. On the SAME line as the count,
// never only below it: a reader who takes in one line must not take in the wrong answer.
//
// The reasons and their order come from the store, so this line, the API's JSON and the
// dialog's count say the same words about the same pads.
func skipTail(skipped map[string]int) string {
	if len(skipped) == 0 {
		return ""
	}
	parts := make([]string, 0, len(skipped))
	for _, reason := range store.SkipOrder {
		if n := skipped[reason]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, reason))
		}
	}
	return " (skipped " + strings.Join(parts, ", ") + ")"
}

func newPadRulesCmd() *cobra.Command {
	var (
		dir      dirFlags
		set      string
		replace  bool
		ifDigest string
		author   string
		title    string
		password string
	)
	cmd := &cobra.Command{
		Use:   "rules <ref> [--as <agent> --set <text|-> --if-digest <digest>]",
		Short: "Read the rules in force on a pad, or write the pad's own",
		Long: "Read the three levels of rules in force on a pad, or append a new rules section\n" +
			"to it with --set.\n\n" +
			"Writing rules is an APPEND like everything else in a pad: the previous rules stay\n" +
			"readable as history and the newest section is what is in force. It does not take\n" +
			"the turn, so stating the rules never blocks the conversation.\n\n" +
			"--set needs --as. The reserved identity \"" + pad.SystemAuthor + "\" belongs to the Web UI alone —\n" +
			"it used to be what this command wrote under when --as was omitted, which meant any\n" +
			"agent could write any pad's rules by simply not naming itself. A person deciding\n" +
			"how a pad works does it in the Web UI; an agent names itself.\n\n" +
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
				if strings.TrimSpace(author) == "" {
					return fmt.Errorf("--set needs --as <agent>: a pad's rules are written by an agent" +
						" on the pad, not anonymously (a person edits them in the Web UI)")
				}
				res, err := st.SetPadRules(store.PadRulesRequest{
					Ref: ref, Author: author, Title: title, Text: text, Password: password,
					Replace: replace, IfDigest: ifDigest, By: store.ByAgent,
				})
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
	bindRuleWriteFlags(cmd, &set, &replace, &ifDigest, nil)
	f := cmd.Flags()
	f.StringVar(&title, "title", "", "title of the rules section (default \"Pad rules\")")
	f.StringVar(&password, "password", "", "the pad's password (when protected)")
	return cmd
}

// bindRuleWriteFlags binds the three flags every level shares. Writing is behind an
// explicit --set rather than "an argument means write", so a mistyped read can never
// overwrite the rules with the word the person meant as a filter.
//
// --set CARRIES the text rather than being a boolean beside a positional argument,
// because rules are a bullet list: `--set "- keep it short"` as a positional would be
// parsed as a flag by any getopt-style parser, and every rule set anyone writes starts
// with a dash. As a flag value it is simply the value.
//
// --if-digest is not optional in practice — the store refuses a write without it — but it
// is a plain flag rather than a required one so the refusal comes from the store, in the
// same words on every surface, and carries the current rules along with it.
// notify is nil at the pad level, where the flag would be a no-op: a pad's rules ARE a
// section, so writing them already reaches everyone waiting on that pad. Binding a flag
// that does nothing is how a surface teaches people the wrong model of the tool.
func bindRuleWriteFlags(cmd *cobra.Command, set *string, replace *bool, ifDigest *string, notify *bool) {
	f := cmd.Flags()
	f.StringVar(set, "set", "", "write these rules, replacing what was there (\"-\" reads them from stdin)")
	f.BoolVar(replace, "replace", false,
		"with --set: these rules REPLACE the levels above instead of extending them")
	f.StringVar(ifDigest, "if-digest", "",
		"with --set: the digest of the version you are replacing, as this level printed it (\""+
			pad.NoRules+"\" when it has none)")
	if notify != nil {
		// Default ON, to match the dialog. A rules change that reaches nobody binds only
		// whoever happens to post next, and an agent in the middle of a task has no other
		// way to hear about it — so silence is the exception, and it has to be asked for:
		// `--notify=false`. Spelled as a value rather than as a `--quiet` flag because
		// there is one decision here, and two names for it is how the two drift apart.
		f.BoolVar(notify, "notify", true,
			"with --set: announce the change in the pads this level binds, waking whoever waits there"+
				" (--notify=false to change them quietly)")
	}
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
		printRuleVersions(out, rules)
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
	printRuleVersions(out, rules)
	return nil
}

// printRuleVersions lists what each level is at, which is what --if-digest quotes.
//
// It covers the levels with NO rules too, and that is the point of printing it as its own
// line rather than tucking each digest into its layer's heading: a level nobody has
// written yet has no heading to tuck it into, and filling an empty level is exactly the
// write two agents are most likely to race on.
//
// It is one line, last, and in the levels' own order — a reader after the RULES should
// meet the text first and the machine tokens after it, the same way `digest:` sits above
// the text it summarises for the one caller that needs it before reading anything.
func printRuleVersions(out io.Writer, rules pad.Rules) {
	var parts []string
	for _, level := range []pad.RuleLevel{pad.LevelStore, pad.LevelProject, pad.LevelPad} {
		if v := rules.Version(level); v != "" {
			parts = append(parts, fmt.Sprintf("%s %s", level, v))
		}
	}
	if len(parts) == 0 {
		return
	}
	fmt.Fprintf(out, "\nversions (--if-digest): %s\n", strings.Join(parts, " · "))
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

// printRulesFor writes the rules to stderr for an author that still owes this pad a read —
// the same moment the post gate would fire, but as a heads-up rather than a refusal.
// `pad get` and `pad wait` are how an agent arrives at a pad, so this is where it learns
// the rules BEFORE it has written anything.
//
// Whether anything is owed is the store's question, asked through the gate itself. Deciding
// it here would mean this heads-up and that refusal could disagree — and the case where
// they would is the one that matters: rules that changed while the agent was working.
func printRulesFor(w io.Writer, st *store.Store, p *store.Pad, author string) {
	// From the pad already loaded: its password was checked when it was read, and
	// re-reading it here would rebuild every section body a second time.
	rules, err := st.UnreadRules(p, strings.TrimSpace(author))
	if err != nil || rules == nil {
		return
	}
	why := "you have not posted here yet"
	if p.HasPosted(author) {
		why = "they have CHANGED since you last posted"
	}
	fmt.Fprintf(w, "\nthis pad has rules and %s — your next post must carry --ack-rules %s\n\n%s\n",
		why, rules.Digest, rules.Text)
}
