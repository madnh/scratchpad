// Command gendemo builds a demo Scratchpad store: pads with days of history, several
// tasks in different states, and assignments old enough to show up as overdue.
//
// It exists because the interesting views — the task board, `pad who`, /api/stuck, the
// UI's "waiting on someone" — only say anything when the pad has a PAST, and the CLI
// can only stamp a section with the time it is posted. So the sections are rendered
// here with timestamps of our choosing and written straight to disk. That is not a back
// door: a pad IS its file, and every derived thing is a fold over the sections.
//
// What keeps this honest, and what keeps it from rotting as the format grows:
//
//   - Sections are rendered by `pad.RenderSection`, the same function the store writes
//     with. A new metadata key appears here for free; a changed one cannot drift.
//   - Every event is checked by the store's OWN rules before it is appended —
//     `ValidateMeta`, `CheckTurn`, `CheckTaskRef`, `CheckTaskOwner`, `NextTaskNo`. If a
//     rule changes, this tool starts failing on scenarios that no longer make sense,
//     which is exactly when you want to hear about it.
//   - The story lives in scenario.go as data, with LABELS instead of section numbers,
//     so inserting a line in the middle renumbers nothing.
//
// Usage:
//
//	go run ./tools/gendemo                  # ~/.scratchpad-demo, refuses to clobber
//	go run ./tools/gendemo --force          # rebuild it from scratch
//	go run ./tools/gendemo --dir /tmp/demo --force
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/madnh/scratchpad/internal/config"
	"github.com/madnh/scratchpad/internal/pad"
)

// stampFile marks a dir as this tool's to destroy. --force wipes a demo store and
// nothing else: a mistyped --dir pointing at a real store must not be recoverable-by-
// backup, it must simply not happen.
const stampFile = ".gendemo"

func main() {
	dir := flag.String("dir", filepath.Join(os.Getenv("HOME"), ".scratchpad-demo"),
		"the Scratchpad dir to build the demo in")
	force := flag.Bool("force", false, "delete and rebuild an existing demo store")
	flag.Parse()

	if err := run(*dir, *force); err != nil {
		fmt.Fprintln(os.Stderr, "gendemo:", err)
		os.Exit(1)
	}
}

func run(dir string, force bool) error {
	if err := prepare(dir, force); err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Second)

	// The two FILE levels of rules, written with the same renderer the store uses so the
	// replace marker cannot drift. A demo store without them would show an empty rules
	// dialog and a gate that never fires — i.e. it would demo nothing.
	if err := os.WriteFile(filepath.Join(dir, pad.RulesFileName),
		[]byte(pad.RenderRulesFile(storeRules, false)), 0o600); err != nil {
		return err
	}

	for _, sc := range scenarios {
		text, err := build(sc, now)
		if err != nil {
			return fmt.Errorf("%s-%s: %w", sc.Project, sc.ID, err)
		}
		dest := filepath.Join(dir, "projects", sc.Project)
		if err := os.MkdirAll(dest, 0o700); err != nil {
			return err
		}
		if rules, ok := projectRules[sc.Project]; ok {
			if err := os.WriteFile(filepath.Join(dest, pad.RulesFileName),
				[]byte(pad.RenderRulesFile(rules, false)), 0o600); err != nil {
				return err
			}
		}
		if err := os.WriteFile(filepath.Join(dest, sc.ID+".md"), []byte(text), 0o600); err != nil {
			return err
		}
		if err := report(sc, text); err != nil {
			return err
		}
	}

	fmt.Printf("\nstore: %s\n", dir)
	fmt.Printf("  scratchpad ui   --dir %s --open\n", dir)
	fmt.Printf("  scratchpad pad list --dir %s\n", dir)
	return nil
}

// prepare gets an empty, initialized Scratchpad dir, refusing to touch anything that is
// not already a demo store.
func prepare(dir string, force bool) error {
	stamp := filepath.Join(dir, stampFile)
	switch _, err := os.Stat(dir); {
	case os.IsNotExist(err):
	case err != nil:
		return err
	default:
		if _, err := os.Stat(stamp); err != nil {
			return fmt.Errorf("%s exists and was not built by gendemo — refusing to touch it", dir)
		}
		if !force {
			return fmt.Errorf("%s already holds a demo store; pass --force to rebuild it", dir)
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	if err := config.Bootstrap(dir); err != nil {
		return err
	}
	return os.WriteFile(stamp, []byte("built by tools/gendemo\n"), 0o600)
}

// build renders one scenario, checking every event against the store's own rules before
// appending it — the same order store.Post checks them in.
func build(sc scenario, now time.Time) (string, error) {
	var b strings.Builder
	// The demo's pads are opened by whoever writes their first event, the same way a real
	// pad's opener is the agent that created it.
	opener := sc.Events[0].Author
	if sc.Opener != "" {
		opener = sc.Opener
	}
	b.WriteString(pad.RenderHeader(pad.Header{
		Created:     now.Add(-sc.Events[0].Ago),
		Opener:      opener,
		Continues:   sc.Continues,
		ContinuedBy: sc.ContinuedBy,
		TasksFrom:   sc.TasksFrom,
	}) + "\n")

	sections := map[string]int{} // label -> section number
	tasks := map[string]int{}    // label -> task number
	prev := time.Duration(1<<63 - 1)

	for i, ev := range sc.Events {
		if ev.Ago > prev {
			return "", fmt.Errorf("event %d (%q) goes back in time", i+1, ev.Title)
		}
		prev = ev.Ago

		// The pad so far, which is what every rule below is checked against. Before the
		// first section there is nothing to parse and nothing to check: no turn is held,
		// no task exists, and the next task number is 1.
		var p *pad.Pad
		if i > 0 {
			var err error
			if p, err = pad.ParseMeta(sc.Project, sc.ID, []byte(b.String())); err != nil {
				return "", fmt.Errorf("event %d: the pad so far does not parse: %w", i+1, err)
			}
		}

		meta := pad.Meta{To: ev.To}
		if ev.Opens != "" || ev.Status != "" {
			meta.Kind = pad.KindTask
		}
		if ev.Rules {
			meta.Kind, meta.Replace = pad.KindRules, ev.Replace
		}
		if ev.Continued {
			meta.Kind = pad.KindContinued
		}
		if ev.Notice {
			meta.Kind = pad.KindNotice
		}
		meta.Acked = ev.Acked
		switch {
		case ev.Opens != "":
			if _, dup := tasks[ev.Opens]; dup {
				return "", fmt.Errorf("event %d: task %q is opened twice", i+1, ev.Opens)
			}
			meta.Task = 1
			if p != nil {
				meta.Task = p.NextTaskNo()
			}
			if ev.TaskNo > 0 {
				meta.Task = ev.TaskNo // carried over from the pad this one continues
			}
			meta.Status = pad.StatusOpen
			tasks[ev.Opens] = meta.Task
		case ev.Task != "":
			n, ok := tasks[ev.Task]
			if !ok {
				return "", fmt.Errorf("event %d: task %q is referenced before it is opened", i+1, ev.Task)
			}
			meta.Task, meta.Status = n, ev.Status
		case ev.Status != "":
			return "", fmt.Errorf("event %d: a status needs a task", i+1)
		}
		if ev.Re != "" {
			n, ok := sections[ev.Re]
			if !ok {
				return "", fmt.Errorf("event %d: re %q names no earlier section", i+1, ev.Re)
			}
			meta.Re = n
			if parent, found := p.Find(n); found && parent.Author != ev.Author &&
				!containsStr(meta.To, parent.Author) {
				meta.To = append(meta.To, parent.Author) // a reply addresses its parent
			}
		}

		if err := pad.ValidateMeta(meta); err != nil {
			return "", fmt.Errorf("event %d (%q): %w", i+1, ev.Title, err)
		}
		if p != nil {
			if err := p.CheckTurn(ev.Author, meta.Kind); err != nil {
				return "", fmt.Errorf("event %d (%q): %w", i+1, ev.Title, err)
			}
			// The DEFAULT rules policy, checked here for the same reason the turn is: a
			// demo whose pads a real store would refuse to build is a demo that teaches
			// the wrong thing. A scenario that wants a non-opener writing the rules has to
			// say so by shipping a marker that allows it, not by slipping past this loop.
			if meta.Kind == pad.KindRules && ev.Author != p.Opener() {
				return "", fmt.Errorf("event %d (%q): rules written by %q, but %q opened the pad"+
					" (rules.pad = %q)", i+1, ev.Title, ev.Author, p.Opener(), config.RulesWriteOpener)
			}
			if meta.Task > 0 && ev.Opens == "" {
				if err := p.CheckTaskRef(meta.Task); err != nil {
					return "", fmt.Errorf("event %d (%q): %w", i+1, ev.Title, err)
				}
				if meta.Kind == pad.KindTask {
					if err := p.CheckTaskOwner(meta.Task, ev.Author); err != nil {
						return "", fmt.Errorf("event %d (%q): %w", i+1, ev.Title, err)
					}
				}
			}
		}

		n := i + 1
		if ev.Label != "" {
			sections[ev.Label] = n
		}
		b.WriteString(pad.RenderSection(n, ev.Author, ev.Title, now.Add(-ev.Ago), meta, ev.Body))
	}
	return b.String(), nil
}

// report prints what the finished pad DERIVES, not what went into it. A demo store is
// only useful if its board and its debts read the way the scenario intended, and this is
// the same fold the CLI and the UI show.
func report(sc scenario, text string) error {
	p, err := pad.Parse(sc.Project, sc.ID, []byte(text))
	if err != nil {
		return err
	}
	turn := p.TurnState()
	fmt.Printf("%s-%s — %s\n", sc.Project, sc.ID, sc.Note)
	fmt.Printf("  %d sections, turn: %s, tasks:\n", p.Last().N, describeTurn(turn.LastAuthor))
	for _, t := range p.Tasks() {
		owners := make([]string, 0, len(t.Owners))
		for _, o := range t.Owners {
			owners = append(owners, fmt.Sprintf("%s:%s", o.Author, o.Status))
		}
		fmt.Printf("    T%d %-8s %-28s %s\n", t.Task, t.Status, t.Title, strings.Join(owners, " "))
	}
	for _, o := range p.Owed() {
		fmt.Printf("    owed: %s → %s (%s, %s)\n", o.From, o.To, o.What,
			time.Since(time.Unix(o.TS, 0)).Truncate(time.Minute))
	}
	return nil
}

func describeTurn(last string) string {
	if last == "" {
		return "nobody has posted"
	}
	return "anyone but " + last
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
