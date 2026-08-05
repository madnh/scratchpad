package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/madnh/scratchpad/internal/config"
	"github.com/madnh/scratchpad/internal/store"
)

// runSearch executes the real command against a throwaway store and hands back the two
// streams separately — which is the whole point of the test below.
func runSearch(t *testing.T, dir string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newPadSearchCmd()
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(append(args, "--dir", dir))
	err = cmd.Execute()
	return out.String(), errb.String(), err
}

// A search that matched nothing prints NOTHING on stdout — not even the header row.
//
// The header alone reads as one result to anything counting lines
// (`search … 2>/dev/null | wc -l`), which is exactly how a script concludes "found it"
// about a store where the word does not appear. grep's silence is the convention, and
// here it is a contract: the summary a person needs is on stderr, where it is not output.
func TestSearchPrintsNothingWhenNothingMatched(t *testing.T) {
	dir := t.TempDir()
	if err := config.WriteMarker(dir, config.Config{}); err != nil {
		t.Fatal(err)
	}
	cfg, _, _, err := config.Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(config.NewLive(cfg))
	if _, _, err := st.CreatePad(store.CreateRequest{
		Project: "projectx", Author: "frontend", Title: "Retry budget",
		Content: "the budget resets per pad\n",
	}); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runSearch(t, dir, "a word that is nowhere")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "" {
		t.Errorf("a search with no matches must print nothing on stdout, got %q", stdout)
	}
	// Silence on stdout is only safe because the reason is still reported.
	if !strings.Contains(stderr, "0 match(es)") {
		t.Errorf("stderr must still explain the silence, got %q", stderr)
	}

	// And when there IS a match, the table comes back — header included.
	stdout, _, err = runSearch(t, dir, "budget")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "REF") || !strings.Contains(stdout, "projectx-") {
		t.Errorf("a matching search must print the table, got %q", stdout)
	}
	if n := strings.Count(strings.TrimRight(stdout, "\n"), "\n") + 1; n < 2 {
		t.Errorf("want a header plus at least one row, got %d line(s)", n)
	}
}
