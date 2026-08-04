package pad

import (
	"strings"
	"testing"
)

func TestCapacityWarningThresholds(t *testing.T) {
	thresholds := []int{80, 90, 99}
	for _, tc := range []struct {
		name     string
		sections int
		want     bool
	}{
		{"far below", 500, false},
		{"just below the first threshold", 799, false},
		{"exactly the first threshold", 800, true},
		{"between thresholds", 850, true},
		{"at the last", 990, true},
		{"one before full", 999, true},
		{"full", 1000, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := CapacityWarning(tc.sections, 1000, thresholds, false)
			if (got != "") != tc.want {
				t.Fatalf("%d/1000 -> %q, warning expected: %v", tc.sections, got, tc.want)
			}
		})
	}
}

// TestCapacityWarningRoundsDown pins the promise a threshold makes. 799/1000 is 79.9%, and
// rounding that to 80 would fire the warning a post early — small here, and the same bug
// fires it a hundred posts early on a limit of 100000.
func TestCapacityWarningRoundsDown(t *testing.T) {
	if got := CapacityWarning(799, 1000, []int{80}, false); got != "" {
		t.Errorf("79.9%% must not count as 80%%: %q", got)
	}
	if got := CapacityWarning(8, 10, []int{80}, false); got == "" {
		t.Error("80%% exactly must warn")
	}
}

// TestCapacityWarningCountsWhatIsLeft: the number of posts remaining is the actionable
// part. An off-by-one here tells an agent it has one more post than it does.
func TestCapacityWarningCountsWhatIsLeft(t *testing.T) {
	for _, tc := range []struct{ sections, want int }{{990, 10}, {999, 1}} {
		got := CapacityWarning(tc.sections, 1000, []int{80}, false)
		if !strings.Contains(got, plural(tc.want, "post")+" left") {
			t.Errorf("%d/1000 should report %d left: %q", tc.sections, tc.want, got)
		}
	}
}

func TestCapacityWarningSaysWhenFull(t *testing.T) {
	got := CapacityWarning(1000, 1000, []int{80}, false)
	if !strings.Contains(got, "FULL") || !strings.Contains(got, "refused") {
		t.Errorf("a full pad must say so plainly: %q", got)
	}
	// A lowered limit puts an existing pad PAST its maximum. The message must still make
	// sense rather than reporting a negative remainder.
	over := CapacityWarning(1200, 1000, []int{80}, false)
	if !strings.Contains(over, "FULL") || strings.Contains(over, "-") {
		t.Errorf("a pad past its (lowered) limit reads wrong: %q", over)
	}
}

// TestCapacityWarningEscalates: three thresholds are only worth having if the message
// changes with them. If every level said the same sentence, the later ones would be noise.
func TestCapacityWarningEscalates(t *testing.T) {
	low := CapacityWarning(800, 1000, []int{80, 90, 99}, false)
	mid := CapacityWarning(900, 1000, []int{80, 90, 99}, false)
	high := CapacityWarning(995, 1000, []int{80, 90, 99}, false)
	if low == mid || mid == high || low == high {
		t.Errorf("levels do not escalate:\n80: %q\n90: %q\n99: %q", low, mid, high)
	}
	if !strings.Contains(high, "continues") && !strings.Contains(high, "unfinished") {
		t.Errorf("the last warning should say what to do with the conversation: %q", high)
	}
}

func TestCapacityWarningOffAndDegenerate(t *testing.T) {
	for name, got := range map[string]string{
		"no thresholds":  CapacityWarning(999, 1000, nil, false),
		"empty list":     CapacityWarning(999, 1000, []int{}, false),
		"no limit":       CapacityWarning(999, 0, []int{80}, false),
		"empty pad":      CapacityWarning(0, 1000, []int{80}, false),
		"junk threshold": CapacityWarning(999, 1000, []int{0, -5, 900}, false),
	} {
		if got != "" {
			t.Errorf("%s should produce no warning, got %q", name, got)
		}
	}
}

// TestCapacityWarningTellsTheTruthAboutTheEnding pins the message against the deployment's
// on-full policy. Under the DEFAULT policy a post arriving at a full pad is not refused — it
// opens a successor — and this warning said "refused" to everyone regardless. An agent that
// believes the next post will be rejected wraps up against a wall that is not there, which is
// the opposite of the behaviour the warning exists to produce.
func TestCapacityWarningTellsTheTruthAboutTheEnding(t *testing.T) {
	for _, tc := range []struct {
		name      string
		continues bool
		want      string
		notWant   string
	}{
		{"reject policy says refused", false, "refused", "successor"},
		{"continue policy must not say refused", true, "new pad", "refused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, sections := range []int{995, 1000} {
				got := CapacityWarning(sections, 1000, []int{80, 90, 99}, tc.continues)
				if !strings.Contains(got, tc.want) {
					t.Errorf("%d/1000 continues=%v should mention %q: %q",
						sections, tc.continues, tc.want, got)
				}
				if strings.Contains(got, tc.notWant) {
					t.Errorf("%d/1000 continues=%v must not mention %q: %q",
						sections, tc.continues, tc.notWant, got)
				}
			}
		})
	}
	// The advice is the same under both policies — a successor keeps the tasks but leaves the
	// transcript one hop behind, so "wrap up here" is right either way. Only the consequence
	// changes, and a policy that also changed the advice would be a second message to keep in
	// step with this one.
	for _, sections := range []int{995, 1000} {
		refuse := CapacityWarning(sections, 1000, []int{80, 90, 99}, false)
		cont := CapacityWarning(sections, 1000, []int{80, 90, 99}, true)
		if strings.Contains(refuse, "wrap up") != strings.Contains(cont, "wrap up") {
			t.Errorf("the two policies give different advice at %d:\n%q\n%q", sections, refuse, cont)
		}
	}
}

// TestCapacityWarningPicksTheHighestCrossed keeps an unsorted or partly-crossed list from
// reporting the mildest message when a stronger one applies.
func TestCapacityWarningPicksTheHighestCrossed(t *testing.T) {
	unsorted := CapacityWarning(995, 1000, []int{99, 80, 90}, false)
	sorted := CapacityWarning(995, 1000, []int{80, 90, 99}, false)
	if unsorted != sorted {
		t.Errorf("threshold order changed the message:\n%q\n%q", unsorted, sorted)
	}
}
