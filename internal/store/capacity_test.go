package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/madnh/scratchpad/internal/config"
)

// warningsFrom posts alternating authors (the turn rule) and returns each post's warnings.
func warningsFrom(t *testing.T, s *Store, ref string, posts int) [][]string {
	t.Helper()
	authors := []string{"b", "a"}
	var out [][]string
	for i := 0; i < posts; i++ {
		res, err := s.Post(PostRequest{
			Ref: ref, Author: authors[i%2], Title: "t", Content: "c",
		})
		if err != nil {
			t.Fatalf("post %d: %v", i+1, err)
		}
		out = append(out, res.Warnings)
	}
	return out
}

func hasCapacityWarning(ws []string) string {
	for _, w := range ws {
		if strings.Contains(w, "full") || strings.Contains(w, "FULL") {
			return w
		}
	}
	return ""
}

// TestPostWarnsAsThePadFills is the feature in one test: a pad of 5 sections warns from
// section 4 (80%) onwards, and the numbers describe the post that just landed.
func TestPostWarnsAsThePadFills(t *testing.T) {
	s := testStore(t) // MaxSectionsPerPad = 5
	p, _, err := create(s, "default", "a", "opening", "content", false)
	if err != nil {
		t.Fatal(err)
	}
	// Sections 2 and 3 are below 80% of 5; sections 4 and 5 are at and above it.
	all := warningsFrom(t, s, p.Ref(), 4)

	if w := hasCapacityWarning(all[0]); w != "" {
		t.Errorf("section 2 of 5 must not warn: %q", w)
	}
	if w := hasCapacityWarning(all[1]); w != "" {
		t.Errorf("section 3 of 5 must not warn: %q", w)
	}
	w4 := hasCapacityWarning(all[2])
	if w4 == "" {
		t.Fatal("section 4 of 5 is 80% and must warn")
	}
	if !strings.Contains(w4, "4 of 5") || !strings.Contains(w4, "1 post left") {
		t.Errorf("the warning must count what just happened and what is left: %q", w4)
	}
	w5 := hasCapacityWarning(all[3])
	if !strings.Contains(w5, "FULL") {
		t.Errorf("the last accepted post must say the pad is full: %q", w5)
	}
}

// TestTheWarningArrivesBeforeTheRefusal is the point of the whole feature: an agent must
// have been told before the pad stops accepting posts. Run against a REJECTING store,
// where "stops accepting" is a refusal; under the default the same last warning is what
// precedes the move to a successor.
func TestTheWarningArrivesBeforeTheRefusal(t *testing.T) {
	s := testRejectingStore(t)
	p, _, err := create(s, "default", "a", "opening", "content", false)
	if err != nil {
		t.Fatal(err)
	}
	all := warningsFrom(t, s, p.Ref(), 4) // fills the pad to 5 of 5

	if _, err := s.Post(PostRequest{Ref: p.Ref(), Author: "b", Title: "t", Content: "c"}); err == nil {
		t.Fatal("expected the pad to be full")
	}
	if hasCapacityWarning(all[len(all)-1]) == "" {
		t.Error("the pad filled up without the last accepted post warning about it")
	}
}

// TestWarningsCanBeTurnedOff: [0] is the explicit off switch, and it must not read as
// "unset" — which would restore the defaults and make the setting impossible to express.
func TestWarningsCanBeTurnedOff(t *testing.T) {
	limits := config.DefaultLimits
	limits.MaxSectionsPerPad = 5
	limits.MaxPadsPerProject = 3
	limits.WarnAtPercent = []int{0}
	s := testStorePolicy(t, limits, config.DefaultRulesPolicy)

	p, _, err := create(s, "default", "a", "opening", "content", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, ws := range warningsFrom(t, s, p.Ref(), 4) {
		if w := hasCapacityWarning(ws); w != "" {
			t.Errorf("warnings are off but one arrived: %q", w)
		}
	}
}

// TestRaisingTheLimitStopsTheWarning ties the feature to the reason limits reload: an
// operator who makes room should see the pad stop complaining, with nothing restarted.
func TestRaisingTheLimitStopsTheWarning(t *testing.T) {
	limits := config.DefaultLimits
	limits.MaxSectionsPerPad = 5
	limits.MaxPadsPerProject = 3
	dir := t.TempDir()
	projects := filepath.Join(dir, "projects")
	if err := os.MkdirAll(projects, 0o700); err != nil {
		t.Fatal(err)
	}
	live := config.NewLive(config.Config{
		RootDir: dir, ProjectsDir: projects, Limits: limits, Rules: config.DefaultRulesPolicy,
	})
	s := New(live)

	p, _, err := create(s, "default", "a", "opening", "content", false)
	if err != nil {
		t.Fatal(err)
	}
	all := warningsFrom(t, s, p.Ref(), 3) // 4 of 5 — warning
	if hasCapacityWarning(all[len(all)-1]) == "" {
		t.Fatal("expected a warning at 4 of 5")
	}

	raised := live.Get()
	raised.Limits.MaxSectionsPerPad = 100
	live.Set(raised)

	// warningsFrom left "b" holding the last message, so "a" takes the next turn.
	res, err := s.Post(PostRequest{Ref: p.Ref(), Author: "a", Title: "t", Content: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if w := hasCapacityWarning(res.Warnings); w != "" {
		t.Errorf("the limit was raised; the warning should have stopped: %q", w)
	}
}
