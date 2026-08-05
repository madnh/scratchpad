package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/madnh/scratchpad/internal/config"
	"github.com/madnh/scratchpad/internal/pad"
)

// v1OnDisk is a pad file in the format shipped before the header carried an opener. It is
// written as a literal rather than produced by this build, so the test measures
// compatibility instead of measuring the current renderer against itself.
const v1OnDisk = "<!-- scratchpad v1; created: 2026-07-11T10:29:00Z -->\n" +
	"\n# 1 - frontend - How does API X work\n" +
	"<!-- ts: 2026-07-11T10:30:00Z -->\n" +
	"\nthe question\n" +
	"\n# 2 - backend - Answer\n" +
	"<!-- ts: 2026-07-11T10:40:00Z; to: frontend; re: 1 -->\n" +
	"\nthe answer\n"

// writeV1Pad drops a v1 file straight into a store's projects dir, the way an existing
// deployment's disk looks the moment its binary is replaced.
func writeV1Pad(t *testing.T, s *Store, project, id, text string) string {
	t.Helper()
	dir := filepath.Join(s.projectsDir, project)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".md"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return project + "-" + id
}

func readPadRaw(t *testing.T, s *Store, project, id string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(s.projectsDir, project, id+".md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestPostUpgradesAV1PadInPlace is the migration contract: nobody runs a command, the file
// comes up to date the first time it is written to, and the pad it describes is unchanged.
func TestPostUpgradesAV1PadInPlace(t *testing.T) {
	s := testStore(t)
	ref := writeV1Pad(t, s, "default", "oldpad", v1OnDisk)

	before := readPadRaw(t, s, "default", "oldpad")
	if !strings.HasPrefix(before, "<!-- scratchpad v1;") {
		t.Fatalf("fixture is not v1: %q", before[:40])
	}

	res, err := s.Post(PostRequest{Ref: ref, Author: "frontend", Title: "next", Content: "more"})
	if err != nil {
		t.Fatalf("posting to a v1 pad must work: %v", err)
	}
	if res.Section != 3 {
		t.Errorf("section = %d, want 3", res.Section)
	}

	after := readPadRaw(t, s, "default", "oldpad")
	if !strings.HasPrefix(after, "<!-- scratchpad v2;") {
		t.Errorf("file was not upgraded: %q", after[:40])
	}
	if !strings.Contains(after, "opener: frontend") {
		t.Errorf("upgraded header does not name the opener: %q", strings.SplitN(after, "\n", 2)[0])
	}
	// Everything below line 1 must be byte-identical to the old file plus the new section.
	_, oldBody, _ := strings.Cut(before, "\n")
	_, newBody, _ := strings.Cut(after, "\n")
	if !strings.HasPrefix(newBody, oldBody) {
		t.Error("the upgrade altered the pad's existing sections")
	}
	if !strings.Contains(newBody[len(oldBody):], "# 3 - frontend - next") {
		t.Errorf("the new section is missing: %q", newBody[len(oldBody):])
	}
}

// TestRefusedPostLeavesAV1PadAlone: migration is the tool's business, but a request that
// is turned away must not be the thing that performs it. Otherwise "it was refused" and
// "the file changed" become true at the same time, which is the state nobody reasons about.
func TestRefusedPostLeavesAV1PadAlone(t *testing.T) {
	s := testStore(t)
	ref := writeV1Pad(t, s, "default", "oldpad", v1OnDisk)
	before := readPadRaw(t, s, "default", "oldpad")

	// backend wrote the last message, so backend posting again is not their turn.
	if _, err := s.Post(PostRequest{Ref: ref, Author: "backend", Title: "again", Content: "x"}); err == nil {
		t.Fatal("expected not_your_turn")
	}
	if got := readPadRaw(t, s, "default", "oldpad"); got != before {
		t.Error("a refused post rewrote the file")
	}
}

// TestUpgradeHappensOnceKeepsAppending guards the cost: the rewrite is O(file), so a
// second post must go back to appending rather than rewriting on every call.
func TestUpgradedPadKeepsItsOpenerOnLaterPosts(t *testing.T) {
	s := testStore(t)
	ref := writeV1Pad(t, s, "default", "oldpad", v1OnDisk)

	if _, err := s.Post(PostRequest{Ref: ref, Author: "frontend", Title: "a", Content: "1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Post(PostRequest{Ref: ref, Author: "backend", Title: "b", Content: "2"}); err != nil {
		t.Fatal(err)
	}
	p, err := s.Get(ref, "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Opener() != "frontend" {
		t.Errorf("opener = %q after two more posts, want frontend", p.Opener())
	}
	if p.Header.Version != pad.FileVersion {
		t.Errorf("version = %d, want %d", p.Header.Version, pad.FileVersion)
	}
	if n := strings.Count(readPadRaw(t, s, "default", "oldpad"), "<!-- scratchpad v"); n != 1 {
		t.Errorf("file carries %d headers — a rewrite went wrong", n)
	}
}

// TestUpgradePreservesAProtectedPad is the case where getting it wrong locks a person out
// of their own pad: the hash sat immediately after the field the old parser cut on.
func TestUpgradePreservesAProtectedPad(t *testing.T) {
	s := testStore(t)
	hash, err := HashPassword("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	text := "<!-- scratchpad v1; created: 2026-07-11T10:29:00Z; password: " + hash + " -->\n" +
		"\n# 1 - frontend - q\n<!-- ts: 2026-07-11T10:30:00Z -->\n\nbody\n"
	ref := writeV1Pad(t, s, "default", "locked", text)

	if _, err := s.Post(PostRequest{Ref: ref, Author: "backend", Title: "a", Content: "1", Password: "hunter2"}); err != nil {
		t.Fatalf("the password must still work through an upgrade: %v", err)
	}
	if _, err := s.Get(ref, "hunter2"); err != nil {
		t.Fatalf("reading with the right password after upgrade: %v", err)
	}
	if _, err := s.Get(ref, ""); err == nil {
		t.Error("the upgraded pad lost its password entirely")
	}
}

// TestPadRulesOwnershipSurvivesTheUpgrade ties the migration to the reason it exists. Under
// the `opener` policy the answer must be the same before and after — and it is now read from
// the header rather than from section 1.
func TestPadRulesOwnershipSurvivesTheUpgrade(t *testing.T) {
	s := testStorePolicy(t, config.DefaultLimits, config.DefaultRulesPolicy)
	ref := writeV1Pad(t, s, "default", "oldpad", v1OnDisk)

	// The opener may set the pad's rules.
	if _, err := s.Post(PostRequest{
		Ref: ref, Author: "frontend", Title: "rules", Content: "- be brief",
		Meta: Meta{Kind: pad.KindRules}, RulesDigest: "none",
	}); err != nil {
		t.Fatalf("the opener must be able to write the pad's rules: %v", err)
	}
	// Anyone else may not, and the refusal must name the opener. The ack is quoted because
	// the line above just changed what binds this pad: the read gate runs first, and a
	// refusal about ownership is only reachable once there is nothing left unread.
	rules, err := s.PadRules(ref, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Post(PostRequest{
		Ref: ref, Author: "backend", Title: "rules", Content: "- be long",
		Meta: Meta{Kind: pad.KindRules}, RulesDigest: "none", AckRules: rules.Digest,
	})
	if err == nil {
		t.Fatal("a non-opener wrote the pad's rules")
	}
	if !strings.Contains(err.Error(), "frontend") {
		t.Errorf("the refusal should name the opener; got: %v", err)
	}
}
