package config

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/madnh/scratchpad/internal/appinfo"
)

// DocFilename is the human/AI-readable configuration guide written alongside the marker.
// Its content lives in config.md (embedded below), NOT hardcoded here, so it is edited
// as prose and version-controlled independently.
const DocFilename = "config.md"

// docMarkdown is the configuration guide, embedded from config.md. Bootstrap/init write
// it into the Scratchpad dir so anyone (user or AI) opening the dir can learn how to
// configure it.
//
//go:embed config.md
var docMarkdown []byte

// DocMarkdown returns the embedded configuration guide.
func DocMarkdown() []byte { return docMarkdown }

// DefaultDir returns the fixed default Scratchpad dir, `~/.scratchpad`. It is
// anchored to the home directory — NEVER the working directory — which is what makes
// auto-bootstrapping it safe: there is no path inference that a mis-run command could
// get wrong.
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, DefaultDirName), nil
}

// Resolve finds the Scratchpad dir for a normal command and loads its config.
// Precedence (high → low):
//
//  1. --dir flag        — explicit; must already be initialized (see init).
//  2. SCRATCHPAD_DIR — explicit; must already be initialized.
//  3. marker `dir` field in the DEFAULT dir's config — explicit pointer; must be
//     initialized. Only the default-location marker is consulted for this.
//  4. the default dir `~/.scratchpad` itself — auto-bootstrapped on first use.
//
// An explicit dir (1–3) that is not initialized is a hard error pointing at `init`:
// a typo in a flag or env var must never silently seed a stray store. Only the fixed
// default path may be created implicitly.
func Resolve(flagDir string) (cfg Config, dir, source string, err error) {
	if s := strings.TrimSpace(flagDir); s != "" {
		return loadExplicit(s, "flag --dir")
	}
	if s := strings.TrimSpace(os.Getenv(EnvDir)); s != "" {
		return loadExplicit(s, "env "+EnvDir)
	}
	def, err := DefaultDir()
	if err != nil {
		return Config{}, "", "", err
	}
	if !IsInitialized(def) {
		if err := Bootstrap(def); err != nil {
			return Config{}, "", "", fmt.Errorf("bootstrap default dir %s: %w", def, err)
		}
		fmt.Fprintf(os.Stderr, "bootstrapped default dir %s\n", def)
	}
	cfg, err = LoadDir(def)
	if err != nil {
		return Config{}, "", "", err
	}
	// The default-location marker may relocate the store via its `dir` field.
	if s := strings.TrimSpace(cfg.Dir); s != "" {
		return loadExplicit(s, "config `dir` in "+MarkerPath(def))
	}
	return cfg, def, "default " + def, nil
}

// loadExplicit loads an explicitly chosen dir, requiring it to be initialized.
func loadExplicit(dir, via string) (Config, string, string, error) {
	abs, err := filepath.Abs(expandHome(dir))
	if err != nil {
		return Config{}, "", "", err
	}
	if !IsInitialized(abs) {
		return Config{}, "", "", fmt.Errorf(
			"dir %q (%s) is not initialized: no %s found there — run `%s init --dir %s` to create it",
			abs, via, MarkerFilename, appinfo.Name(), abs)
	}
	cfg, err := LoadDir(abs)
	if err != nil {
		return Config{}, "", "", err
	}
	return cfg, abs, via, nil
}

// expandHome resolves a leading "~/" so config values like "~/pads" behave as a shell
// user expects. A bare path is returned unchanged.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	return p
}

// Bootstrap creates a fully-ready Scratchpad dir: the marker (header + identity only),
// the config guide, and the projects/ store, all under a 0700 dir. It is used for the
// default dir's first-touch auto-bootstrap and by `init` for explicit dirs. It refuses
// an already-initialized dir (WriteMarker guards).
func Bootstrap(dir string) error {
	if err := WriteMarker(dir, Config{}); err != nil {
		return err
	}
	if err := WriteDoc(dir); err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(dir, "projects"), 0o700)
}

// WriteDoc writes the embedded configuration guide into dir as config.md. It overwrites
// any existing copy so a re-init refreshes stale docs.
func WriteDoc(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(abs, DocFilename), docMarkdown, 0o644)
}
