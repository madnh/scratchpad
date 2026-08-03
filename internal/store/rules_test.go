package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/madnh/scratchpad/internal/pad"
)

func TestStoreAndProjectRulesRoundTrip(t *testing.T) {
	s := testStore(t)

	if text, _, err := s.StoreRules(); err != nil || text != "" {
		t.Fatalf("a fresh store must have no rules: %q, %v", text, err)
	}
	if err := s.SetStoreRules("- be brief", false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetProjectRules("proj", "- always --to", true); err != nil {
		t.Fatal(err)
	}

	text, replace, err := s.StoreRules()
	if err != nil || text != "- be brief" || replace {
		t.Fatalf("store rules round trip: %q replace=%t err=%v", text, replace, err)
	}
	text, replace, err = s.ProjectRules("proj")
	if err != nil || text != "- always --to" || !replace {
		t.Fatalf("project rules round trip: %q replace=%t err=%v", text, replace, err)
	}

	// The project's rules live INSIDE the project, so deleting the project takes them.
	if _, err := os.Stat(filepath.Join(s.ProjectsDir(), "proj", pad.RulesFileName)); err != nil {
		t.Fatalf("the project's rules must sit in its own directory: %v", err)
	}

	// Writing blank rules REMOVES the file: "no rules" has one representation on disk.
	if err := s.SetStoreRules("  \n", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.dir, pad.RulesFileName)); !os.IsNotExist(err) {
		t.Fatalf("blanking the rules should remove the file, got %v", err)
	}
}

// The naming law, from the store's side: its own files and a person's stray notes are
// not pads, and are not reported as broken ones either.
func TestRulesFilesAreNotPads(t *testing.T) {
	s := testStore(t)
	p, _, err := create(s, "proj", "pm", "kickoff", "starting", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetProjectRules("proj", "- be brief", false); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(s.ProjectsDir(), "proj", "notes.txt")
	if err := os.WriteFile(stray, []byte("just a note"), 0o600); err != nil {
		t.Fatal(err)
	}

	pads, warnings, err := s.List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(pads) != 1 || pads[0].Ref != p.Ref() {
		t.Fatalf("only the pad should be listed: %+v", pads)
	}
	if len(warnings) != 0 {
		t.Fatalf("a rules file must not be reported as a broken pad: %v", warnings)
	}

	projects, err := s.Projects()
	if err != nil || len(projects) != 1 || projects[0].PadCount != 1 {
		t.Fatalf("pad count must ignore non-pads: %+v %v", projects, err)
	}

	// Silent for listings, but doctor still names it — otherwise a pad renamed by hand
	// would simply disappear.
	strays, err := s.StrayFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(strays) != 1 || !strings.HasSuffix(strays[0], "notes.txt") {
		t.Fatalf("strays = %v, want just the stray note", strays)
	}
}

func TestPostGateOnFirstPost(t *testing.T) {
	s := testStore(t)
	if err := s.SetStoreRules("- be brief", false); err != nil {
		t.Fatal(err)
	}

	// Creating a pad is the author's first post too, so the project's rules gate it.
	if _, _, err := create(s, "proj", "pm", "kickoff", "starting", false); !HasCode(err, CodeRulesUnread) {
		t.Fatalf("create without an ack: want rules_unread, got %v", err)
	}
	rules, err := s.ProjectRuleSet("proj")
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := s.CreatePad(CreateRequest{
		Project: "proj", Author: "pm", Title: "kickoff", Content: "starting", AckRules: rules.Digest,
	})
	if err != nil {
		t.Fatalf("create with the right digest: %v", err)
	}

	// A second author is gated on their first post…
	if _, err := s.Post(PostRequest{Ref: p.Ref(), Author: "ios", Title: "hi", Content: "hello"}); !HasCode(err, CodeRulesUnread) {
		t.Fatalf("first post without an ack: want rules_unread, got %v", err)
	}
	if _, err := s.Post(PostRequest{
		Ref: p.Ref(), Author: "ios", Title: "hi", Content: "hello", AckRules: rules.Digest,
	}); err != nil {
		t.Fatalf("first post with the digest: %v", err)
	}
	// …and never again, even after the rules change under them. (pm answers first, since
	// ios holds the turn — the gate is what is under test, not the turn rule.)
	if _, err := s.SetPadRules(p.Ref(), "pm", "", "- and address what you write", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Post(PostRequest{Ref: p.Ref(), Author: "pm", Title: "ok", Content: "fine"}); err != nil {
		t.Fatalf("the pad's creator must not be gated: %v", err)
	}
	if _, err := s.Post(PostRequest{Ref: p.Ref(), Author: "ios", Title: "again", Content: "more"}); err != nil {
		t.Fatalf("an author already on the pad must never be gated, even after a rule change: %v", err)
	}
}

// A store with no rules must behave exactly as it did before this feature existed.
func TestNoRulesNoGate(t *testing.T) {
	s := testStore(t)
	p, _, err := create(s, "proj", "pm", "kickoff", "starting", false)
	if err != nil {
		t.Fatalf("creating in a store with no rules must not be gated: %v", err)
	}
	if _, err := s.Post(PostRequest{Ref: p.Ref(), Author: "ios", Title: "hi", Content: "hello"}); err != nil {
		t.Fatalf("posting in a pad with no rules must not be gated: %v", err)
	}
}

func TestSetPadRulesIsAnAppendThatTakesNoTurn(t *testing.T) {
	s := testStore(t)
	p, _, err := create(s, "proj", "pm", "kickoff", "starting", false)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.SetPadRules(p.Ref(), SystemAuthor, "", "- be brief", "", false)
	if err != nil {
		t.Fatalf("the UI path must be allowed to write as the reserved author: %v", err)
	}
	if res.Section != 2 {
		t.Fatalf("rules should be section 2, got %d", res.Section)
	}
	// pm posted section 1 and the rules section did not hand the turn on, so pm is still
	// the one who may not post.
	if _, err := s.Post(PostRequest{Ref: p.Ref(), Author: "pm", Title: "again", Content: "x"}); !HasCode(err, CodeNotYourTurn) {
		t.Fatalf("rules must not grant a turn: %v", err)
	}

	rules, err := s.PadRules(p.Ref(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rules.Layers) != 1 || rules.Layers[0].Level != pad.LevelPad || rules.Layers[0].Author != SystemAuthor {
		t.Fatalf("layers = %+v", rules.Layers)
	}

	// An ordinary post may not claim the reserved identity.
	if _, err := s.Post(PostRequest{Ref: p.Ref(), Author: SystemAuthor, Title: "x", Content: "y"}); !HasCode(err, CodeInvalidInput) {
		t.Fatalf("an agent must not be able to post as %q: %v", SystemAuthor, err)
	}
}
