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
			got := CapacityWarning(tc.sections, 1000, thresholds)
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
	if got := CapacityWarning(799, 1000, []int{80}); got != "" {
		t.Errorf("79.9%% must not count as 80%%: %q", got)
	}
	if got := CapacityWarning(8, 10, []int{80}); got == "" {
		t.Error("80%% exactly must warn")
	}
}

// TestCapacityWarningCountsWhatIsLeft: the number of posts remaining is the actionable
// part. An off-by-one here tells an agent it has one more post than it does.
func TestCapacityWarningCountsWhatIsLeft(t *testing.T) {
	for _, tc := range []struct{ sections, want int }{{990, 10}, {999, 1}} {
		got := CapacityWarning(tc.sections, 1000, []int{80})
		if !strings.Contains(got, plural(tc.want, "post")+" left") {
			t.Errorf("%d/1000 should report %d left: %q", tc.sections, tc.want, got)
		}
	}
}

func TestCapacityWarningSaysWhenFull(t *testing.T) {
	got := CapacityWarning(1000, 1000, []int{80})
	if !strings.Contains(got, "FULL") || !strings.Contains(got, "refused") {
		t.Errorf("a full pad must say so plainly: %q", got)
	}
	// A lowered limit puts an existing pad PAST its maximum. The message must still make
	// sense rather than reporting a negative remainder.
	over := CapacityWarning(1200, 1000, []int{80})
	if !strings.Contains(over, "FULL") || strings.Contains(over, "-") {
		t.Errorf("a pad past its (lowered) limit reads wrong: %q", over)
	}
}

// TestCapacityWarningEscalates: three thresholds are only worth having if the message
// changes with them. If every level said the same sentence, the later ones would be noise.
func TestCapacityWarningEscalates(t *testing.T) {
	low := CapacityWarning(800, 1000, []int{80, 90, 99})
	mid := CapacityWarning(900, 1000, []int{80, 90, 99})
	high := CapacityWarning(995, 1000, []int{80, 90, 99})
	if low == mid || mid == high || low == high {
		t.Errorf("levels do not escalate:\n80: %q\n90: %q\n99: %q", low, mid, high)
	}
	if !strings.Contains(high, "continues") && !strings.Contains(high, "unfinished") {
		t.Errorf("the last warning should say what to do with the conversation: %q", high)
	}
}

func TestCapacityWarningOffAndDegenerate(t *testing.T) {
	for name, got := range map[string]string{
		"no thresholds":  CapacityWarning(999, 1000, nil),
		"empty list":     CapacityWarning(999, 1000, []int{}),
		"no limit":       CapacityWarning(999, 0, []int{80}),
		"empty pad":      CapacityWarning(0, 1000, []int{80}),
		"junk threshold": CapacityWarning(999, 1000, []int{0, -5, 900}),
	} {
		if got != "" {
			t.Errorf("%s should produce no warning, got %q", name, got)
		}
	}
}

// TestCapacityWarningPicksTheHighestCrossed keeps an unsorted or partly-crossed list from
// reporting the mildest message when a stronger one applies.
func TestCapacityWarningPicksTheHighestCrossed(t *testing.T) {
	unsorted := CapacityWarning(995, 1000, []int{99, 80, 90})
	sorted := CapacityWarning(995, 1000, []int{80, 90, 99})
	if unsorted != sorted {
		t.Errorf("threshold order changed the message:\n%q\n%q", unsorted, sorted)
	}
}
