package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/madnh/scratchpad/internal/pad"
)

// writePadAt puts a one-section pad on disk with a chosen timestamp. Posting through the
// store would stamp everything with the same second, and the questions these tests ask —
// which mention came FIRST, what falls inside a window — only exist because a store has
// a past.
func writePadAt(t *testing.T, s *Store, project, id, author, title, body string, ts time.Time) string {
	t.Helper()
	dir := filepath.Join(s.ProjectsDir(), project)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := pad.RenderHeader(pad.Header{Created: ts, Opener: author}) + "\n" +
		pad.RenderSection(1, author, title, ts, pad.Meta{Kind: pad.KindMessage}, body)
	if err := os.WriteFile(filepath.Join(dir, id+".md"), []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	return project + "-" + id
}

// dated builds the store both ordering tests read: the same word decided long ago in one
// pad, and restated last week in another.
func dated(t *testing.T) (*Store, string, string) {
	t.Helper()
	s := testStore(t)
	old := writePadAt(t, s, "projectx", "oldone", "erp", "Decision",
		"the retry budget is per pad\n", time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC))
	recent := writePadAt(t, s, "projectx", "newone", "ios", "Today",
		"remind me how the retry budget works\n", time.Date(2026, 6, 20, 9, 0, 0, 0, time.UTC))
	return s, old, recent
}

// searchStore builds a store holding the pads every search test below reads. The prose is
// deliberately mixed-case and partly Vietnamese: folding and word boundaries are the two
// decisions this command makes that a pure-ASCII fixture would never exercise.
func searchStore(t *testing.T) (*Store, string) {
	t.Helper()
	s := testStore(t)
	p, _, err := create(s, "projectx", "frontend", "Retry budget for uploads",
		"The retry budget resets per pad.\nUnrelated line.\n", false)
	if err != nil {
		t.Fatal(err)
	}
	ref := p.Ref()
	if _, err := s.Post(PostRequest{
		Ref: ref, Author: "backend", Title: "Answer",
		Content: "A Retry BUDGET is per pad, not per agent.\nngữ nghĩa của danh từ này.\n",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Post(PostRequest{
		Ref: ref, Author: "frontend", Title: "Track the budget change",
		Content: "budgeting is a different word.\n",
		Meta:    Meta{Kind: pad.KindTask, To: []string{"backend"}}, OpenTask: true,
	}); err != nil {
		t.Fatal(err)
	}
	return s, ref
}

func TestSearchFindsLinesWithCoordinates(t *testing.T) {
	s, ref := searchStore(t)
	res, err := s.Search(SearchRequest{Query: "retry budget"})
	if err != nil {
		t.Fatal(err)
	}
	// Two bodies say it, and section 1's TITLE says it — a search that missed the title
	// would miss the most deliberate statement of what a section is about.
	if len(res.Hits) != 3 {
		t.Fatalf("want 3 hits, got %d: %+v", len(res.Hits), res.Hits)
	}
	var titleHits int
	for _, h := range res.Hits {
		if h.Ref != ref {
			t.Errorf("hit in the wrong pad: %+v", h)
		}
		if h.InTitle {
			titleHits++
			if h.Line != 0 {
				t.Errorf("a title hit has no file line, got %d", h.Line)
			}
			continue
		}
		if h.Line <= 1 {
			t.Errorf("body hit must name a line below the header, got %d", h.Line)
		}
		if !strings.Contains(strings.ToLower(h.Text), "retry budget") {
			t.Errorf("hit text does not contain the match: %q", h.Text)
		}
	}
	if titleHits != 1 {
		t.Errorf("want 1 title hit, got %d", titleHits)
	}
	if res.Scanned != 1 {
		t.Errorf("want 1 pad scanned, got %d", res.Scanned)
	}
}

// The line number is what locates a match inside a long section, so it has to be the
// file's own numbering — the one an editor or `sed -n` shows.
func TestSearchLineNumberIsTheFileLine(t *testing.T) {
	s, ref := searchStore(t)
	res, err := s.Search(SearchRequest{Query: "Unrelated"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("want 1 hit, got %d", len(res.Hits))
	}
	if res.Hits[0].Ref != ref {
		t.Fatalf("hit in the wrong pad: %+v", res.Hits[0])
	}
	got := res.Hits[0]
	// header(1) + blank(2) + "# 1 - …"(3) + meta(4) + blank(5) + first body line(6) + this(7)
	if got.Line != 7 {
		t.Errorf("want the match on file line 7, got %d (%q)", got.Line, got.Text)
	}
}

func TestSearchFoldsCaseByDefault(t *testing.T) {
	s, _ := searchStore(t)
	res, err := s.Search(SearchRequest{Query: "RETRY BUDGET"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("the default search must ignore case")
	}
	res, err = s.Search(SearchRequest{Query: "RETRY BUDGET", CaseSensitive: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 0 {
		t.Fatalf("--case-sensitive must match case exactly, got %+v", res.Hits)
	}
}

// --word exists for exactly this: "budget" must not be answered by "budgeting". The
// boundary is spelled with Unicode classes, so it has to hold for Vietnamese too.
func TestSearchWordBoundaries(t *testing.T) {
	s, _ := searchStore(t)
	loose, err := s.Search(SearchRequest{Query: "budget"})
	if err != nil {
		t.Fatal(err)
	}
	strict, err := s.Search(SearchRequest{Query: "budget", Word: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(strict.Hits) >= len(loose.Hits) {
		t.Fatalf("--word must drop the 'budgeting' line: loose=%d strict=%d",
			len(loose.Hits), len(strict.Hits))
	}
	for _, h := range strict.Hits {
		if strings.Contains(h.Text, "budgeting") {
			t.Errorf("--word matched inside a longer word: %q", h.Text)
		}
	}
	// A diacritic is not an ASCII word character. An \b-based boundary would treat "ĩ" as
	// a non-word character and so accept "ngh" as a whole word inside "nghĩa"; the Unicode
	// classes this uses do not.
	viet, err := s.Search(SearchRequest{Query: "ngh", Word: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(viet.Hits) != 0 {
		t.Errorf("--word matched an ASCII prefix inside a Vietnamese word: %+v", viet.Hits)
	}
	viet, err = s.Search(SearchRequest{Query: "nghĩa", Word: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(viet.Hits) != 1 {
		t.Errorf("want the Vietnamese word matched as a whole word, got %d hits", len(viet.Hits))
	}
}

func TestSearchFiltersByAuthorAndKind(t *testing.T) {
	s, _ := searchStore(t)
	res, err := s.Search(SearchRequest{Query: "budget", Author: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("want backend's line")
	}
	for _, h := range res.Hits {
		if h.Author != "backend" {
			t.Errorf("author filter leaked %+v", h)
		}
	}
	res, err = s.Search(SearchRequest{Query: "budget", Kind: string(pad.KindTask)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("want the task section's line")
	}
	for _, h := range res.Hits {
		if h.Kind != pad.KindTask {
			t.Errorf("kind filter leaked %+v", h)
		}
	}
}

func TestSearchRegexpAndBadPattern(t *testing.T) {
	s, _ := searchStore(t)
	res, err := s.Search(SearchRequest{Query: "budget(ing)? is", Regexp: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("regexp search found nothing")
	}
	if _, err := s.Search(SearchRequest{Query: "budget(", Regexp: true}); err == nil {
		t.Fatal("a broken pattern must be an error, not an empty result")
	}
	// Without --regexp the same text is a literal, and matches nothing rather than failing.
	res, err = s.Search(SearchRequest{Query: "budget("})
	if err != nil {
		t.Fatalf("a literal search must not care about regexp syntax: %v", err)
	}
	if len(res.Hits) != 0 {
		t.Fatalf("literal search matched something it should not: %+v", res.Hits)
	}
	if _, err := s.Search(SearchRequest{Query: ""}); err == nil {
		t.Fatal("an empty pattern must be refused")
	}
}

// A protected pad is not searched, and the fact that it was left out is REPORTED: an
// empty result must never be readable as "the word is nowhere in the store".
func TestSearchSkipsProtectedPadsButSaysSo(t *testing.T) {
	s := testStore(t)
	secret, pw, err := create(s, "projectx", "frontend", "Secret", "the budget is 12\n", true)
	if err != nil {
		t.Fatal(err)
	}
	if pw == "" {
		t.Fatal("a protected pad must return its password")
	}
	res, err := s.Search(SearchRequest{Query: "budget"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 0 {
		t.Fatalf("a protected pad's content leaked into a store-wide search: %+v", res.Hits)
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != secret.Ref() {
		t.Fatalf("want the protected pad reported as skipped, got %+v", res.Skipped)
	}
	if res.Scanned != 0 {
		t.Errorf("a skipped pad must not count as scanned, got %d", res.Scanned)
	}

	// Named with its password, it IS searchable — the password gates the content, and
	// this is the only form that can carry one.
	res, err = s.Search(SearchRequest{Query: "budget", Ref: secret.Ref(), Password: pw})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("the right password must open the pad to a search")
	}
	if _, err := s.Search(SearchRequest{Query: "budget", Ref: secret.Ref()}); err == nil {
		t.Fatal("searching a protected pad without its password must fail")
	}
	if _, err := s.Search(SearchRequest{Query: "budget", Ref: secret.Ref(), Password: "wrong"}); err == nil {
		t.Fatal("a wrong password must fail")
	}
}

func TestSearchLimitTruncates(t *testing.T) {
	s, _ := searchStore(t)
	res, err := s.Search(SearchRequest{Query: "budget", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || !res.Truncated {
		t.Fatalf("want 1 hit marked truncated, got %d hits truncated=%t", len(res.Hits), res.Truncated)
	}
	res, err = s.Search(SearchRequest{Query: "budget"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Truncated {
		t.Error("an uncapped search must not report truncation")
	}
}

// The store's own files are not pads, and a search walks by the same naming law as every
// other walk rather than by the .md suffix.
func TestSearchIgnoresToolFiles(t *testing.T) {
	s, _ := searchStore(t)
	if err := s.SetStoreRules(RulesWrite{
		Text: "the retry budget applies to rules files too\n", By: ByAgent, IfDigest: pad.NoRules,
	}); err != nil {
		t.Fatal(err)
	}
	res, err := s.Search(SearchRequest{Query: "rules files too"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 0 {
		t.Fatalf("_rules.md is not a pad and must not be searched: %+v", res.Hits)
	}
}

func TestSearchProjectFilter(t *testing.T) {
	s, _ := searchStore(t)
	if _, _, err := create(s, "other", "frontend", "Elsewhere", "the budget again\n", false); err != nil {
		t.Fatal(err)
	}
	all, err := s.Search(SearchRequest{Query: "budget"})
	if err != nil {
		t.Fatal(err)
	}
	one, err := s.Search(SearchRequest{Query: "budget", Project: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if one.Scanned != 1 || all.Scanned != 2 {
		t.Fatalf("scanned all=%d one=%d", all.Scanned, one.Scanned)
	}
	for _, h := range one.Hits {
		if !strings.HasPrefix(h.Ref, "other-") {
			t.Errorf("project filter leaked %+v", h)
		}
	}
	if _, err := s.Search(SearchRequest{Query: "budget", Project: "NOT VALID"}); err == nil {
		t.Fatal("an invalid project name must be refused")
	}
}

// The default answers "what is being said about this", and --oldest answers "where was
// this settled". They are different questions, and a search that could only ask the first
// buries the origin of a word under every later restatement of it.
func TestSearchOldestOrdersByWhenItWasWritten(t *testing.T) {
	s, old, recent := dated(t)

	def, err := s.Search(SearchRequest{Query: "retry budget"})
	if err != nil {
		t.Fatal(err)
	}
	if len(def.Hits) != 2 || def.Hits[0].Ref != recent {
		t.Fatalf("default order must lead with the most recently active pad, got %+v", def.Hits)
	}
	first, err := s.Search(SearchRequest{Query: "retry budget", Oldest: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Hits) != 2 || first.Hits[0].Ref != old {
		t.Fatalf("--oldest must lead with the earliest mention, got %+v", first.Hits)
	}
	// The pairing that makes it useful: the earliest mention survives a limit, which is
	// the whole reason to ask for that order.
	one, err := s.Search(SearchRequest{Query: "retry budget", Oldest: true, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(one.Hits) != 1 || one.Hits[0].Ref != old || !one.Truncated {
		t.Fatalf("--oldest --limit 1 must keep the FIRST mention, got %+v", one.Hits)
	}
}

func TestSearchTimeWindowFiltersSections(t *testing.T) {
	s, old, recent := dated(t)
	cut := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC).Unix()

	before, err := s.Search(SearchRequest{Query: "retry budget", Before: cut})
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Hits) != 1 || before.Hits[0].Ref != old {
		t.Fatalf("Before must keep only what predates the cut, got %+v", before.Hits)
	}
	after, err := s.Search(SearchRequest{Query: "retry budget", After: cut})
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Hits) != 1 || after.Hits[0].Ref != recent {
		t.Fatalf("After must keep only what follows the cut, got %+v", after.Hits)
	}
	// Both pads were still READ — the window filters sections, not pads, so an old
	// decision stays findable inside a pad that is busy today.
	if before.Scanned != 2 || after.Scanned != 2 {
		t.Errorf("the window must not stop pads being scanned: %d / %d",
			before.Scanned, after.Scanned)
	}
	// A title is filtered by the same window as the body beneath it.
	titles, err := s.Search(SearchRequest{Query: "Decision", After: cut})
	if err != nil {
		t.Fatal(err)
	}
	if len(titles.Hits) != 0 {
		t.Errorf("a title escaped the time window: %+v", titles.Hits)
	}
}

func TestSearchExcludePad(t *testing.T) {
	s, old, recent := dated(t)
	res, err := s.Search(SearchRequest{Query: "retry budget", ExcludePads: []string{recent}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || res.Hits[0].Ref != old {
		t.Fatalf("--exclude-pad must drop the named pad, got %+v", res.Hits)
	}
	if res.Scanned != 1 {
		t.Errorf("an excluded pad must not be read at all, scanned=%d", res.Scanned)
	}
	// A typo must not silently exclude nothing: the pad the caller wanted rid of would
	// come back, and they would read that as "the word is only here".
	if _, err := s.Search(SearchRequest{Query: "budget", ExcludePads: []string{"not a ref"}}); err == nil {
		t.Fatal("an unparsable --exclude-pad must be refused")
	}
}

// A match is REPORTED WHERE IT IS, and the cut is taken around it. Agent prose arrives
// as one long line, so a window applied to the front of it returned a row that did not
// contain the word being reported — a wrong answer wearing the shape of a truncated one.
func TestSearchExcerptIsCutAroundTheMatch(t *testing.T) {
	s := testStore(t)
	line := strings.Repeat("a", 500) + " needle " + strings.Repeat("b", 500)
	ref := writePadAt(t, s, "projectx", "longline", "erp", "Long",
		line+"\n", time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC))

	res, err := s.Search(SearchRequest{Query: "needle", TextWidth: 60})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("want one hit in %s, got %+v", ref, res.Hits)
	}
	h := res.Hits[0]
	if !strings.Contains(h.Text, "needle") {
		t.Fatalf("the excerpt does not contain the word it reports: %q", h.Text)
	}
	if n := len([]rune(h.Text)); n > 62 {
		t.Errorf("excerpt is %d runes, want the 60-rune window plus at most two ellipses", n)
	}
	// The offsets must point at the word inside the text that is actually returned.
	r := []rune(h.Text)
	if h.MatchStart < 0 || h.MatchEnd > len(r) || string(r[h.MatchStart:h.MatchEnd]) != "needle" {
		t.Fatalf("offsets %d..%d do not select the match in %q", h.MatchStart, h.MatchEnd, h.Text)
	}
	// Cut on both sides, so both ellipses are there.
	if !strings.HasPrefix(h.Text, "…") || !strings.HasSuffix(h.Text, "…") {
		t.Errorf("a line cut at both ends must say so: %q", h.Text)
	}
}

// The offsets survive a multi-byte line: they are RUNE indices, and a Vietnamese pad is
// the ordinary case here rather than the exotic one.
func TestSearchOffsetsAreRunesNotBytes(t *testing.T) {
	s := testStore(t)
	writePadAt(t, s, "projectx", "nonascii", "erp", "Tiêu đề",
		"phần điều tra về địa chỉ hai cấp\n", time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC))

	res, err := s.Search(SearchRequest{Query: "địa chỉ"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("want one hit, got %+v", res.Hits)
	}
	h := res.Hits[0]
	r := []rune(h.Text)
	if got := string(r[h.MatchStart:h.MatchEnd]); got != "địa chỉ" {
		t.Fatalf("offsets %d..%d gave %q, want %q", h.MatchStart, h.MatchEnd, got, "địa chỉ")
	}
}

// --word's boundaries are consuming character classes (RE2 has no lookaround), so the
// reported range must come from the submatch. Reporting the whole match would hand every
// caller a range covering the space either side of the word.
func TestSearchWordOffsetsExcludeTheBoundaries(t *testing.T) {
	s := testStore(t)
	writePadAt(t, s, "projectx", "wordy", "erp", "Word",
		"the cache is warm\n", time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC))

	res, err := s.Search(SearchRequest{Query: "cache", Word: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("want one hit, got %+v", res.Hits)
	}
	h := res.Hits[0]
	r := []rune(h.Text)
	if got := string(r[h.MatchStart:h.MatchEnd]); got != "cache" {
		t.Fatalf("--word reported %q as the match; the boundaries must not be part of it", got)
	}
}

// A title is never cut: it is one short line by rule and it is what a person scans the
// result list by.
func TestSearchTitleHitCarriesOffsetsAndIsNotCut(t *testing.T) {
	s := testStore(t)
	title := "Decision about the retry budget that runs on and on and on and on and on and on"
	writePadAt(t, s, "projectx", "titled", "erp", title,
		"body says nothing\n", time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC))

	res, err := s.Search(SearchRequest{Query: "budget", TextWidth: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || !res.Hits[0].InTitle {
		t.Fatalf("want one title hit, got %+v", res.Hits)
	}
	h := res.Hits[0]
	if h.Text != title {
		t.Errorf("a title must not be cut by TextWidth, got %q", h.Text)
	}
	r := []rune(h.Text)
	if string(r[h.MatchStart:h.MatchEnd]) != "budget" {
		t.Errorf("offsets %d..%d do not select the match in the title", h.MatchStart, h.MatchEnd)
	}
}

// TextWidth 0 is the whole line, which is what a caller with room asks for.
func TestSearchTextWidthZeroKeepsTheWholeLine(t *testing.T) {
	s := testStore(t)
	line := strings.Repeat("a", 300) + " needle"
	writePadAt(t, s, "projectx", "whole", "erp", "Long",
		line+"\n", time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC))

	res, err := s.Search(SearchRequest{Query: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || res.Hits[0].Text != line {
		t.Fatalf("TextWidth 0 must keep the line intact, got %+v", res.Hits)
	}
}
