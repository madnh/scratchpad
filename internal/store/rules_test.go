package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/madnh/scratchpad/internal/config"
	"github.com/madnh/scratchpad/internal/pad"
)

// setRules writes one of the FILE levels quoting whatever version is currently there. The
// tests that are not ABOUT the compare-and-set go through this, so the check stays tested
// in one place instead of being half-tested everywhere.
func setRules(t *testing.T, s *Store, project, text string, replace bool) {
	t.Helper()
	var cur string
	var curReplace bool
	var err error
	if project == "" {
		cur, curReplace, err = s.StoreRules()
	} else {
		cur, curReplace, err = s.ProjectRules(project)
	}
	if err != nil {
		t.Fatal(err)
	}
	w := RulesWrite{Text: text, Replace: replace, IfDigest: pad.LevelDigest(cur, curReplace), By: ByAgent}
	if project == "" {
		err = s.SetStoreRules(w)
	} else {
		err = s.SetProjectRules(project, w)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// setPadRules is the same for a pad's own rules.
func setPadRules(t *testing.T, s *Store, ref, author, text string) *PostResult {
	t.Helper()
	rules, err := s.PadRules(ref, "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.SetPadRules(PadRulesRequest{
		Ref: ref, Author: author, Text: text, IfDigest: rules.Version(pad.LevelPad), By: ByAgent,
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestStoreAndProjectRulesRoundTrip(t *testing.T) {
	s := testStore(t)

	if text, _, err := s.StoreRules(); err != nil || text != "" {
		t.Fatalf("a fresh store must have no rules: %q, %v", text, err)
	}
	setRules(t, s, "", "- be brief", false)
	setRules(t, s, "proj", "- always --to", true)

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
	setRules(t, s, "", "  \n", false)
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
	setRules(t, s, "proj", "- be brief", false)
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
	setRules(t, s, "", "- be brief", false)

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
	setPadRules(t, s, p.Ref(), "pm", "- and address what you write")
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
	res, err := s.SetPadRules(PadRulesRequest{
		Ref: p.Ref(), Text: "- be brief", IfDigest: pad.NoRules, By: ByUI,
	})
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

// The compare-and-set, at the two levels that store it differently: a file rewritten in
// place, and a section appended to a pad. Both answer the same question — is the thing I
// am replacing still the thing I read — so both are tested the same way.
func TestRulesWriteNeedsTheVersionItReplaces(t *testing.T) {
	s := testStore(t)

	// A level with no rules yet is still a version, and the one two agents race to fill.
	if err := s.SetStoreRules(RulesWrite{Text: "- be brief", By: ByAgent}); !HasCode(err, CodeRulesConflict) {
		t.Fatalf("a write with no version quoted: want rules_conflict, got %v", err)
	}
	if err := s.SetStoreRules(RulesWrite{Text: "- be brief", IfDigest: pad.NoRules, By: ByAgent}); err != nil {
		t.Fatalf("quoting the empty version must be accepted: %v", err)
	}

	// Having written them, the version has moved: the token that just worked no longer does.
	if err := s.SetStoreRules(RulesWrite{Text: "- something else", IfDigest: pad.NoRules, By: ByAgent}); !HasCode(err, CodeRulesConflict) {
		t.Fatalf("a stale version: want rules_conflict, got %v", err)
	}
	rules, err := s.ProjectRuleSet("")
	if err != nil {
		t.Fatal(err)
	}
	// The refusal hands back what has to be merged with, so the retry costs no extra read.
	err = s.SetStoreRules(RulesWrite{Text: "- x", IfDigest: pad.NoRules, By: ByAgent})
	if !strings.Contains(err.Error(), "- be brief") {
		t.Fatalf("a conflict must carry the version that won: %v", err)
	}
	if err := s.SetStoreRules(RulesWrite{
		Text: "- something else", IfDigest: rules.Version(pad.LevelStore), By: ByAgent,
	}); err != nil {
		t.Fatalf("the current version must be accepted: %v", err)
	}

	// `replace` is part of the version: flipping it alone is still a change to what the
	// level means, so a writer holding the old token must not land it.
	rules, err = s.ProjectRuleSet("")
	if err != nil {
		t.Fatal(err)
	}
	before := rules.Version(pad.LevelStore)
	if err := s.SetStoreRules(RulesWrite{
		Text: "- something else", Replace: true, IfDigest: before, By: ByAgent,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStoreRules(RulesWrite{Text: "- third", IfDigest: before, By: ByAgent}); !HasCode(err, CodeRulesConflict) {
		t.Fatalf("flipping replace must move the version: got %v", err)
	}

	// And the pad level, where the check runs under the append's own lock. (The store has
	// rules by now, so creating the pad acknowledges them — a different gate, tested above.)
	proj, err := s.ProjectRuleSet("proj")
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := s.CreatePad(CreateRequest{
		Project: "proj", Author: "pm", Title: "kickoff", Content: "starting", AckRules: proj.Digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	padRules, err := s.PadRules(p.Ref(), "")
	if err != nil {
		t.Fatal(err)
	}
	if v := padRules.Version(pad.LevelPad); v != pad.NoRules {
		t.Fatalf("a pad with no rules section is at %q, want %q", v, pad.NoRules)
	}
	if _, err := s.SetPadRules(PadRulesRequest{
		Ref: p.Ref(), Author: "pm", Text: "- say the device", By: ByAgent,
	}); !HasCode(err, CodeRulesConflict) {
		t.Fatalf("pad rules with no version quoted: want rules_conflict, got %v", err)
	}
	setPadRules(t, s, p.Ref(), "pm", "- say the device")
	if _, err := s.SetPadRules(PadRulesRequest{
		Ref: p.Ref(), Author: "pm", Text: "- and the OS", IfDigest: pad.NoRules, By: ByAgent,
	}); !HasCode(err, CodeRulesConflict) {
		t.Fatalf("a stale pad version: want rules_conflict, got %v", err)
	}
}

// The default policy: the two FILE levels are the operator's, and an agent is told where
// to take its proposal rather than simply refused.
func TestFileRulesAreNotWritableByAnAgentByDefault(t *testing.T) {
	s := testStorePolicy(t, config.DefaultLimits, config.DefaultRulesPolicy)

	err := s.SetStoreRules(RulesWrite{Text: "- be brief", IfDigest: pad.NoRules, By: ByAgent})
	if !HasCode(err, CodeRulesReadOnly) {
		t.Fatalf("an agent writing the store rules: want rules_readonly, got %v", err)
	}
	if !strings.Contains(err.Error(), "Web UI") {
		t.Fatalf("the refusal must name where a person can make the change: %v", err)
	}
	if err := s.SetProjectRules("proj", RulesWrite{
		Text: "- always --to", IfDigest: pad.NoRules, By: ByAgent,
	}); !HasCode(err, CodeRulesReadOnly) {
		t.Fatalf("an agent writing a project's rules: want rules_readonly, got %v", err)
	}

	// The surface the policy points AT is not blocked by it.
	if err := s.SetStoreRules(RulesWrite{Text: "- be brief", IfDigest: pad.NoRules, By: ByUI}); err != nil {
		t.Fatalf("the UI must be able to write the store rules: %v", err)
	}
	// …and is still held to the version check, which is a different question.
	if err := s.SetStoreRules(RulesWrite{Text: "- other", IfDigest: pad.NoRules, By: ByUI}); !HasCode(err, CodeRulesConflict) {
		t.Fatalf("the UI is exempt from the policy, not from the version: %v", err)
	}
}

// The default pad policy: the rules belong to whoever opened the pad.
func TestPadRulesBelongToTheOpener(t *testing.T) {
	s := testStorePolicy(t, config.DefaultLimits, config.DefaultRulesPolicy)
	p, _, err := create(s, "proj", "pm", "kickoff", "starting", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Post(PostRequest{Ref: p.Ref(), Author: "ios", Title: "hi", Content: "hello"}); err != nil {
		t.Fatal(err)
	}

	// An agent that is ON the pad, and still not the one whose rules these are.
	_, err = s.SetPadRules(PadRulesRequest{
		Ref: p.Ref(), Author: "ios", Text: "- anything goes", IfDigest: pad.NoRules, By: ByAgent,
	})
	if !HasCode(err, CodeNotRulesOwner) {
		t.Fatalf("a non-opener writing the pad's rules: want not_rules_owner, got %v", err)
	}
	if !strings.Contains(err.Error(), "pm") {
		t.Fatalf("the refusal must name the opener so the agent knows who to ask: %v", err)
	}

	if _, err := s.SetPadRules(PadRulesRequest{
		Ref: p.Ref(), Author: "pm", Text: "- say the device", IfDigest: pad.NoRules, By: ByAgent,
	}); err != nil {
		t.Fatalf("the opener must be able to write the pad's rules: %v", err)
	}

	// The UI is the way a PERSON overrides this — the opener may be long gone.
	rules, err := s.PadRules(p.Ref(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPadRules(PadRulesRequest{
		Ref: p.Ref(), Text: "- decided by a person", IfDigest: rules.Version(pad.LevelPad), By: ByUI,
	}); err != nil {
		t.Fatalf("the UI must be able to write a pad's rules whoever opened it: %v", err)
	}

	// pad_post carries the same gate: set_rules is not a way around it.
	if _, err := s.Post(PostRequest{
		Ref: p.Ref(), Author: "ios", Title: "mine now", Content: "- anything goes",
		Meta: Meta{Kind: pad.KindRules}, RulesDigest: pad.NoRules,
	}); !HasCode(err, CodeNotRulesOwner) {
		t.Fatalf("pad_post(set_rules) must obey the policy too: %v", err)
	}
}

// The pad policy decides which way an UNRECOGNISED value falls, and it must fall closed.
//
// This is not hypothetical bookkeeping: the policy is now read from a live config that a
// filesystem watcher can replace while the process runs, so "the loader always fills it in"
// became one more thing that has to keep being true. Testing the blank case pins the
// behaviour to the store rather than to the loader's good manners — the check must be
// written so that only the ONE value meaning "anybody" relaxes it.
func TestAnUnrecognisedPadPolicyFailsClosed(t *testing.T) {
	for _, policy := range []string{"", "opener", "something-a-future-binary-writes"} {
		t.Run("policy="+policy, func(t *testing.T) {
			s := testStorePolicy(t, config.DefaultLimits, config.RulesPolicy{
				Store: config.RulesWriteAgent, Project: config.RulesWriteAgent, Pad: policy,
			})
			p, _, err := create(s, "proj", "pm", "kickoff", "starting", false)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Post(PostRequest{Ref: p.Ref(), Author: "ios", Title: "hi", Content: "hello"}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.SetPadRules(PadRulesRequest{
				Ref: p.Ref(), Author: "ios", Text: "- mine now", IfDigest: pad.NoRules, By: ByAgent,
			}); !HasCode(err, CodeNotRulesOwner) {
				t.Fatalf("policy %q let a non-opener write the pad's rules: %v", policy, err)
			}
		})
	}

	// …and the one value that DOES mean "anybody" still does, so the check above is not
	// simply refusing everyone.
	s := testStorePolicy(t, config.DefaultLimits, config.RulesPolicy{
		Store: config.RulesWriteAgent, Project: config.RulesWriteAgent, Pad: config.RulesWriteAny,
	})
	p, _, err := create(s, "proj", "pm", "kickoff", "starting", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Post(PostRequest{Ref: p.Ref(), Author: "ios", Title: "hi", Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPadRules(PadRulesRequest{
		Ref: p.Ref(), Author: "ios", Text: "- anything goes", IfDigest: pad.NoRules, By: ByAgent,
	}); err != nil {
		t.Fatalf("rules.pad = any must let any agent on the pad write them: %v", err)
	}
}

// The way round the opener policy that existed before it did: the reserved identity used
// to be INFERRED from the author string (`SystemPost: author == SystemAuthor`), and the
// CLI defaulted that exact string when --as was omitted — so an agent got a person's
// exemption by not naming itself. It bypassed the read gate even then.
//
// The privilege now comes from By, a field the calling code states. This pins the half a
// store can be held to: an agent asking as the reserved name is refused however it asks.
func TestTheReservedIdentityIsNotAnAgentsToClaim(t *testing.T) {
	s := testStorePolicy(t, config.DefaultLimits, config.DefaultRulesPolicy)
	// As the operator, since this store runs the default policy — the point here is the
	// pad level, and an agent could not have written these.
	if err := s.SetStoreRules(RulesWrite{Text: "- be brief", IfDigest: pad.NoRules, By: ByUI}); err != nil {
		t.Fatal(err)
	}
	rules, err := s.ProjectRuleSet("proj")
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := s.CreatePad(CreateRequest{
		Project: "proj", Author: "pm", Title: "kickoff", Content: "starting", AckRules: rules.Digest,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Naming it outright, through the path that once granted it.
	if _, err := s.SetPadRules(PadRulesRequest{
		Ref: p.Ref(), Author: SystemAuthor, Text: "- mine now", IfDigest: pad.NoRules, By: ByAgent,
	}); !HasCode(err, CodeInvalidInput) {
		t.Fatalf("an agent claiming %q must be refused: %v", SystemAuthor, err)
	}
	// And the read gate it used to skip is still in front of an agent that has not posted
	// here — the exemption travelled with the identity, so losing one loses the other.
	if _, err := s.SetPadRules(PadRulesRequest{
		Ref: p.Ref(), Author: "ios", Text: "- mine now", IfDigest: pad.NoRules, By: ByAgent,
	}); !HasCode(err, CodeRulesUnread) {
		t.Fatalf("writing rules is a first post like any other: %v", err)
	}

	// Nothing landed: the pad still has only its opening section.
	fresh, err := s.Get(p.Ref(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh.Sections) != 1 {
		t.Fatalf("no refused write may have been appended: %+v", fresh.Sections)
	}
}
