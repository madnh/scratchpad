package pad

import "fmt"

// CapacityWarning describes how full a pad is once a post lands, or reports nothing when
// the pad is still comfortably short of its limit.
//
// It is LEVEL-triggered, not edge-triggered: every post from the first threshold onwards
// carries the warning, not just the one that crossed it. Edge-triggering is cheaper and
// wrong here — the pad is shared, so the agent that happens to cross 80% is usually not
// the agent that will hit the wall, and a warning delivered once to somebody else is a
// warning nobody acts on. The cost is one extra line per post over a pad's last fifth.
//
// sections is the count AFTER the post being reported on, so the number an agent reads is
// the number it just caused.
func CapacityWarning(sections, max int, thresholds []int) string {
	if max <= 0 || sections <= 0 || len(thresholds) == 0 {
		return ""
	}
	// Integer percentage, rounded DOWN: a pad that is 79.9% full must not be reported as
	// having crossed 80. The threshold is a promise about when the warning starts.
	percent := sections * 100 / max
	crossed := 0
	for _, t := range thresholds {
		if t >= 1 && t <= 100 && percent >= t && t > crossed {
			crossed = t
		}
	}
	if crossed == 0 {
		return ""
	}

	left := max - sections
	switch {
	case left <= 0:
		// Reachable when the limit was LOWERED under a pad that was already past it: the
		// posts happened, the policy moved afterwards. Saying "0 left" and nothing else
		// would read as a bug in the counter.
		return fmt.Sprintf(
			"this pad is FULL (%d of %d sections) — the next post will be refused; wrap up here",
			sections, max)
	case percent >= 99 || left <= 5:
		return fmt.Sprintf(
			"this pad is %d%% full (%d of %d sections): %s left before posts are refused — "+
				"say what is unfinished and where it continues, before you run out",
			percent, sections, max, plural(left, "post"))
	case percent >= 90:
		return fmt.Sprintf(
			"this pad is %d%% full (%d of %d sections): %s left — start closing threads rather than opening them",
			percent, sections, max, plural(left, "post"))
	default:
		return fmt.Sprintf(
			"this pad is %d%% full (%d of %d sections): %s left",
			percent, sections, max, plural(left, "post"))
	}
}

// plural writes "1 post" and "2 posts", because a warning that says "1 posts" reads as
// generated text and gets skimmed past — which is the one thing this message cannot afford.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
