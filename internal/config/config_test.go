package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBootstrapAndLoad(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	if err := Bootstrap(dir); err != nil {
		t.Fatal(err)
	}
	if !IsInitialized(dir) {
		t.Fatal("bootstrap did not initialize the dir")
	}
	if _, err := os.Stat(filepath.Join(dir, DocFilename)); err != nil {
		t.Fatal("config guide missing:", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "projects")); err != nil {
		t.Fatal("projects dir missing:", err)
	}

	cfg, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DisplayName != DefaultDisplayName || cfg.Instance != DefaultInstance {
		t.Fatalf("identity defaults wrong: %+v", cfg)
	}
	if cfg.DefaultProject != DefaultProject || !SameLimits(cfg.Limits, DefaultLimits) || cfg.Wait != DefaultWait {
		t.Fatalf("setting defaults wrong: %+v", cfg)
	}
	if cfg.ProjectsDir != filepath.Join(dir, "projects") {
		t.Fatalf("projects dir not derived: %q", cfg.ProjectsDir)
	}
	if cfg.SocketPath != filepath.Join(dir, DefaultInstance+".sock") {
		t.Fatalf("socket not derived: %q", cfg.SocketPath)
	}

	// The persisted marker must hold only header + identity — no derived paths, no
	// optional groups the operator didn't set.
	raw, err := os.ReadFile(MarkerPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"projects_dir", "socket_path", "limits", "wait", "tcp", "dir", "default_project"} {
		if _, ok := m[forbidden]; ok {
			t.Errorf("marker persists %q; init should write only header + identity", forbidden)
		}
	}

	// Re-bootstrap must refuse to clobber.
	if err := Bootstrap(dir); err == nil || !strings.Contains(err.Error(), "already initialized") {
		t.Fatalf("bootstrap over an initialized dir must refuse: %v", err)
	}
}

func TestSchemaGuards(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) {
		if err := os.WriteFile(MarkerPath(dir), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"type":"something-else","version":1}`)
	if _, err := LoadDir(dir); err == nil || !strings.Contains(err.Error(), "not a Scratchpad config") {
		t.Fatalf("foreign type accepted: %v", err)
	}
	write(`{"type":"scratchpad","version":99}`)
	if _, err := LoadDir(dir); err == nil || !strings.Contains(err.Error(), "newer than this binary") {
		t.Fatalf("newer schema accepted: %v", err)
	}
}

func TestResolveExplicitRequiresInit(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")

	if _, _, _, err := Resolve(missing); err == nil || !strings.Contains(err.Error(), "init") {
		t.Fatalf("uninitialized flag dir must error toward init: %v", err)
	}

	t.Setenv(EnvDir, missing)
	if _, _, _, err := Resolve(""); err == nil || !strings.Contains(err.Error(), "init") {
		t.Fatalf("uninitialized env dir must error toward init: %v", err)
	}
}

func TestResolveFlagBeatsEnv(t *testing.T) {
	flagDir := filepath.Join(t.TempDir(), "flag")
	envDir := filepath.Join(t.TempDir(), "env")
	for _, d := range []string{flagDir, envDir} {
		if err := Bootstrap(d); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(EnvDir, envDir)
	_, dir, source, err := Resolve(flagDir)
	if err != nil {
		t.Fatal(err)
	}
	if dir != flagDir || source != "flag --dir" {
		t.Fatalf("flag must win over env: got %q via %q", dir, source)
	}
	_, dir, source, err = Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if dir != envDir || !strings.Contains(source, EnvDir) {
		t.Fatalf("env must win when no flag: got %q via %q", dir, source)
	}
}

func TestResolveProject(t *testing.T) {
	cfg := Config{DefaultProject: "cfgproj"}
	if got := ResolveProject(cfg, "flagproj"); got != "flagproj" {
		t.Fatal(got)
	}
	t.Setenv(EnvProject, "envproj")
	if got := ResolveProject(cfg, ""); got != "envproj" {
		t.Fatal(got)
	}
	t.Setenv(EnvProject, "")
	if got := ResolveProject(cfg, ""); got != "cfgproj" {
		t.Fatal(got)
	}
}

// The rules policy: a marker that says nothing gets the narrow defaults, and one that
// misspells a value fails to LOAD rather than degrading to a permission nobody granted.
func TestRulesPolicy(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) {
		if err := os.WriteFile(MarkerPath(dir), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(`{"type":"scratchpad","version":1}`)
	c, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Rules != DefaultRulesPolicy {
		t.Fatalf("a marker that says nothing about rules must get the defaults: %+v", c.Rules)
	}

	// A group may set one level and leave the others alone.
	write(`{"type":"scratchpad","version":1,"rules":{"pad":"any"}}`)
	if c, err = LoadDir(dir); err != nil {
		t.Fatal(err)
	}
	if c.Rules.Pad != RulesWriteAny || c.Rules.Store != RulesWriteUI {
		t.Fatalf("a partial rules group must default the rest: %+v", c.Rules)
	}

	for _, body := range []string{
		`{"type":"scratchpad","version":1,"rules":{"store":"anyone"}}`,
		`{"type":"scratchpad","version":1,"rules":{"pad":"ui"}}`, // a real value, wrong level
	} {
		write(body)
		if _, err := LoadDir(dir); err == nil || !strings.Contains(err.Error(), "rules.") {
			t.Fatalf("an unknown policy value must refuse to load (%s): %v", body, err)
		}
	}
}

// A store initialized by an older build must not keep handing out that build's guide.
// init refuses an existing dir, so before this there was no way to refresh it at all.
func TestResolveRefreshesAStaleGuide(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	if err := Bootstrap(dir); err != nil {
		t.Fatal(err)
	}
	docPath := filepath.Join(dir, DocFilename)
	if err := os.WriteFile(docPath, []byte("# an older build wrote this\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv(EnvDir, dir)
	if _, _, _, err := Resolve(""); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, DocMarkdown()) {
		t.Fatalf("the stale guide was not refreshed:\n%s", got)
	}
}

// A guide that is already current must not be rewritten — an untouched file keeps its
// mtime, which is what stops every command from looking like it changed the store.
func TestResolveLeavesACurrentGuideAlone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	if err := Bootstrap(dir); err != nil {
		t.Fatal(err)
	}
	docPath := filepath.Join(dir, DocFilename)
	before, err := os.Stat(docPath)
	if err != nil {
		t.Fatal(err)
	}
	// Backdate it so a rewrite is unmistakable rather than a same-instant coincidence.
	old := before.ModTime().Add(-time.Hour)
	if err := os.Chtimes(docPath, old, old); err != nil {
		t.Fatal(err)
	}

	t.Setenv(EnvDir, dir)
	if _, _, _, err := Resolve(""); err != nil {
		t.Fatal(err)
	}

	after, err := os.Stat(docPath)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(old) {
		t.Fatal("a guide that was already current got rewritten anyway")
	}
}

// A missing guide is restored: it belongs to the tool, and a store without it is a store
// nobody can read their way into.
func TestResolveRestoresADeletedGuide(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	if err := Bootstrap(dir); err != nil {
		t.Fatal(err)
	}
	docPath := filepath.Join(dir, DocFilename)
	if err := os.Remove(docPath); err != nil {
		t.Fatal(err)
	}

	t.Setenv(EnvDir, dir)
	if _, _, _, err := Resolve(""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(docPath); err != nil {
		t.Fatalf("the guide was not restored: %v", err)
	}
}

// Refreshing the guide is a convenience, never a precondition: a store on a read-only
// filesystem must still be usable. Failing `pad post` because a doc could not be rewritten
// would be the tool putting its own paperwork above the work.
func TestResolveSurvivesAnUnwritableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the write bit")
	}
	dir := filepath.Join(t.TempDir(), "store")
	if err := Bootstrap(dir); err != nil {
		t.Fatal(err)
	}
	docPath := filepath.Join(dir, DocFilename)
	if err := os.WriteFile(docPath, []byte("# stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil { // r-x: cannot replace a file inside
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	t.Setenv(EnvDir, dir)
	if _, _, _, err := Resolve(""); err != nil {
		t.Fatalf("an unwritable guide broke the command: %v", err)
	}
}

// TestWarnAtPercentNormalisation covers the three spellings that must stay distinct:
// unset means the defaults, [0] means off, and anything else is cleaned up rather than
// trusted — an operator's list can arrive unsorted, duplicated, or out of range.
func TestWarnAtPercentNormalisation(t *testing.T) {
	for name, tc := range map[string]struct {
		in   []int
		want []int
	}{
		"unset":        {nil, DefaultLimits.WarnAtPercent},
		"empty":        {[]int{}, DefaultLimits.WarnAtPercent},
		"off":          {[]int{0}, []int{}},
		"unsorted":     {[]int{95, 50}, []int{50, 95}},
		"duplicates":   {[]int{80, 80, 90}, []int{80, 90}},
		"out of range": {[]int{80, 101, -3, 100}, []int{80, 100}},
	} {
		t.Run(name, func(t *testing.T) {
			c := Config{Limits: Limits{WarnAtPercent: tc.in}}
			c.applyDefaults()
			got := c.Limits.WarnAtPercent
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestNormalisingDoesNotMutateTheCallersSlice: a config snapshot is shared by every
// goroutine holding it, so sorting in place would reorder a slice another request is
// reading.
func TestNormalisingDoesNotMutateTheCallersSlice(t *testing.T) {
	in := []int{95, 50}
	c := Config{Limits: Limits{WarnAtPercent: in}}
	c.applyDefaults()
	if in[0] != 95 || in[1] != 50 {
		t.Errorf("the caller's slice was sorted in place: %v", in)
	}
}
