package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if cfg.DefaultProject != DefaultProject || cfg.Limits != DefaultLimits || cfg.Wait != DefaultWait {
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
