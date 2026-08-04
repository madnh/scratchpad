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
//
// continues says what happens when the room runs out — the deployment's on-full policy. It
// is a bool rather than the policy value because this package must not know the config
// vocabulary, and because there are only ever two endings to describe. Getting it wrong is
// not cosmetic: under the DEFAULT policy a post that arrives at a full pad is not refused,
// it moves to a successor, and an agent told it will be refused wraps up against a wall
// that is not there.
func CapacityWarning(sections, max int, thresholds []int, continues bool) string {
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

	// What the wall actually is. Both endings want the pad wrapped up — a successor keeps
	// the tasks but leaves the transcript one hop behind — so only the consequence differs,
	// never the advice.
	ending, atTheWall := "before posts are refused", "the next post will be refused"
	if continues {
		ending = "before the conversation continues in a new pad"
		atTheWall = "the next post will continue in a new pad"
	}

	left := max - sections
	switch {
	case left <= 0:
		// Reachable when the limit was LOWERED under a pad that was already past it: the
		// posts happened, the policy moved afterwards. Saying "0 left" and nothing else
		// would read as a bug in the counter.
		return fmt.Sprintf(
			"this pad is FULL (%d of %d sections) — %s; wrap up here",
			sections, max, atTheWall)
	case percent >= 99 || left <= 5:
		return fmt.Sprintf(
			"this pad is %d%% full (%d of %d sections): %s left %s — "+
				"say what is unfinished and where it continues, before you run out",
			percent, sections, max, plural(left, "post"), ending)
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
