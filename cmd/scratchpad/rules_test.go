package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/madnh/scratchpad/internal/config"
	"github.com/madnh/scratchpad/internal/pad"
	"github.com/madnh/scratchpad/internal/store"
)

// runRules executes the real store-rules command against a throwaway store.
func runRules(t *testing.T, dir string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newRulesCmd()
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(append(args, "--dir", dir))
	err = cmd.Execute()
	return out.String(), errb.String(), err
}

// rulesTestStore is a store an AGENT may write the rules of — the default policy points
// this command at the Web UI, and this test is about --notify, not about who may write.
func rulesTestStore(t *testing.T) (dir string, st *store.Store) {
	t.Helper()
	dir = t.TempDir()
	if err := config.WriteMarker(dir, config.Config{
		Rules: config.RulesPolicy{Store: config.RulesWriteAgent, Project: config.RulesWriteAgent},
	}); err != nil {
		t.Fatal(err)
	}
	cfg, _, _, err := config.Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir, store.New(config.NewLive(cfg))
}

// TestRulesAnnouncesByDefault pins the choice that makes a rules edit worth making: a
// change reaches the pads it binds unless somebody says otherwise.
//
// The opposite default is what the feature exists to replace — rules that bind whoever
// happens to post next, while every agent already at work carries on under the old ones
// until a person goes round the sessions by hand.
func TestRulesAnnouncesByDefault(t *testing.T) {
	dir, st := rulesTestStore(t)
	p, _, err := st.CreatePad(store.CreateRequest{
		Project: "projectx", Author: "frontend", Title: "Retry budget", Content: "starting\n",
	})
	if err != nil {
		t.Fatal(err)
	}

	// No --notify anywhere on the command line.
	stdout, _, err := runRules(t, dir, "--set", "- keep it short", "--if-digest", pad.NoRules)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "announced in 1 pad(s)") {
		t.Fatalf("a rules write must announce by default, and say so: %q", stdout)
	}
	got, err := st.Get(p.Ref(), "")
	if err != nil {
		t.Fatal(err)
	}
	last := got.Last()
	if last.Kind != pad.KindNotice || last.Author != pad.SystemAuthor {
		t.Fatalf("want a notice from the tool in the pad, got %+v", last.Meta)
	}
}

// …and silence has to be asked for, in a way that says so out loud. A quiet change is the
// one that surprises people later: nobody was interrupted, and the rules still bind
// everyone from their next post.
func TestRulesCanBeChangedQuietly(t *testing.T) {
	dir, st := rulesTestStore(t)
	p, _, err := st.CreatePad(store.CreateRequest{
		Project: "projectx", Author: "frontend", Title: "Retry budget", Content: "starting\n",
	})
	if err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runRules(t, dir, "--set", "- keep it short", "--if-digest", pad.NoRules, "--notify=false")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "quietly") || !strings.Contains(stdout, "Nobody was told") {
		t.Fatalf("a quiet write must say that nobody was told: %q", stdout)
	}
	got, err := st.Get(p.Ref(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sections) != 1 {
		t.Fatalf("--notify=false must leave every pad untouched: %d sections", len(got.Sections))
	}
}
