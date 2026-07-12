// Package config holds the Scratchpad directory layout and its resolution rules.
// Everything lives in one self-contained directory (marker config, config guide, the
// projects/ pad store, and the runtime socket); the marker file is a versioned JSON
// header plus settings. On conflict FLAGS WIN over env, env over the marker file, and
// the file over built-in defaults.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/madnh/scratchpad/internal/appinfo"
)

// MarkerFilename is the config file whose presence marks a directory as an initialized
// Scratchpad dir. It is a FORMAT identifier: it never changes with the binary's name.
const MarkerFilename = "scratchpad.config.json"

// ConfigType is the fixed value of a marker's `type` field. Loading rejects a file
// whose type differs — that file belongs to some other tool, not Scratchpad.
const ConfigType = "scratchpad"

// ConfigVersion is the current marker SCHEMA version. Bump it when the marker format
// changes; the loader refuses a file from a newer schema than it understands.
const ConfigVersion = 1

// DefaultDirName is the product-named directory under the user's home that serves as
// the default Scratchpad dir (`~/.scratchpad`). Unlike an explicit dir it is
// AUTO-BOOTSTRAPPED on first use: the path is fixed relative to the home directory —
// never inferred from the working directory — so a mis-run command cannot seed a stray
// store somewhere surprising.
const DefaultDirName = ".scratchpad"

// Environment variables. Every env var this tool reads shares the SCRATCHPAD_
// prefix so deployments configure it generically — no host is ever hardcoded.
const (
	// EnvDir points at the Scratchpad dir, mirroring --dir.
	EnvDir = "SCRATCHPAD_DIR"
	// EnvProject is the default project used when a command/tool omits `project`,
	// mirroring --project. Precedence: flag > env > marker default_project > "default".
	EnvProject = "SCRATCHPAD_PROJECT_NAME"
	// EnvAuthor is the default author for CLI --as. Author identity is per-agent
	// session state, so it belongs in the environment, never in the marker file.
	EnvAuthor = "SCRATCHPAD_AUTHOR"
	// EnvNonInteractive, when truthy, forces non-interactive mode (never prompt; fail
	// fast instead), mirroring the --non-interactive flag.
	EnvNonInteractive = "SCRATCHPAD_NONINTERACTIVE"
)

// Neutral defaults for the marker's identity fields.
const (
	// DefaultDisplayName is the human-facing name of a deployment.
	DefaultDisplayName = "Scratchpad"
	// DefaultInstance is the technical label; it names the socket file.
	DefaultInstance = "scratchpad"
	// DefaultProject is the project used when nothing configures one.
	DefaultProject = "default"
	// DefaultTCPPort is the opt-in loopback TCP port (67xx range). TCP is never the
	// default transport.
	DefaultTCPPort = 6710
)

// Limits bounds every resource so a runaway agent cannot grow a pad or a project
// without bound. Zero values mean "use the default".
type Limits struct {
	MaxTitleKB        int `json:"max_title_kb,omitempty"`
	MaxContentKB      int `json:"max_content_kb,omitempty"`
	MaxSectionsPerPad int `json:"max_sections_per_pad,omitempty"`
	MaxPadsPerProject int `json:"max_pads_per_project,omitempty"`
}

// DefaultLimits are the built-in bounds used when the marker sets none.
var DefaultLimits = Limits{
	MaxTitleKB:        4,
	MaxContentKB:      64,
	MaxSectionsPerPad: 1000,
	MaxPadsPerProject: 1000,
}

// Wait bounds the MCP pad_wait long-poll. The cap exists because an MCP tool call
// must return within the host's per-turn timeout; the CLI `pad wait` is uncapped.
type Wait struct {
	DefaultS int `json:"default_s,omitempty"`
	MaxS     int `json:"max_s,omitempty"`
}

// DefaultWait is the built-in pad_wait timing used when the marker sets none.
var DefaultWait = Wait{DefaultS: 60, MaxS: 300}

// TCP configures the opt-in loopback TCP transport. It is off unless `serve --tcp`
// is passed; a marker `tcp` group only supplies its settings. Tokens are stored as
// SHA-256 digests ("sha256:<hex>"), never plaintext.
type TCP struct {
	Port           int      `json:"port,omitempty"`
	TokenDigests   []string `json:"token_digests,omitempty"`
	AllowedOrigins []string `json:"allowed_origins,omitempty"`
}

// Config is the marker file: a schema header (type/version), identity, and optional
// setting groups. `init` writes only the header + identity; the optional groups are
// added by an operator when needed (config.md documents the defaults).
//
// ProjectsDir and SocketPath are DERIVED from the dir at load time (self-contained
// layout) and never persisted — move the dir and they move with it.
type Config struct {
	// Type identifies the file kind; always ConfigType in a valid marker.
	Type string `json:"type"`
	// Version is the marker schema version (see ConfigVersion).
	Version int `json:"version"`

	// DisplayName is the human-facing name of this deployment.
	DisplayName string `json:"display_name"`
	// Instance is the technical label; the socket file is named <instance>.sock.
	Instance string `json:"instance"`

	// Dir optionally relocates the store: it is only meaningful in the marker at the
	// DEFAULT location (~/.scratchpad) and points at another initialized dir. This
	// is the deliberate exception to "never persist paths" — a user-facing pointer so
	// the whole machine can be re-homed via the config file alone.
	Dir string `json:"dir,omitempty"`
	// DefaultProject is the project used when a command/tool omits `project`
	// (overridden by SCRATCHPAD_PROJECT_NAME / --project).
	DefaultProject string `json:"default_project,omitempty"`

	Limits Limits `json:"limits,omitzero"`
	Wait   Wait   `json:"wait,omitzero"`
	TCP    TCP    `json:"tcp,omitzero"`

	// ProjectsDir is <dir>/projects — derived, never persisted.
	ProjectsDir string `json:"projects_dir,omitempty"`
	// SocketPath is <dir>/<instance>.sock — derived, never persisted.
	SocketPath string `json:"socket_path,omitempty"`
}

// MarkerPath returns the marker file path inside dir.
func MarkerPath(dir string) string { return filepath.Join(dir, MarkerFilename) }

// IsInitialized reports whether dir looks like an initialized Scratchpad dir — i.e.
// the marker file exists. It does not validate the marker's contents; LoadDir does.
func IsInitialized(dir string) bool {
	fi, err := os.Stat(MarkerPath(dir))
	return err == nil && !fi.IsDir()
}

// LoadDir loads the marker from an initialized dir, validates the schema header, applies
// defaults, and derives the self-contained paths (projects dir, socket).
func LoadDir(dir string) (Config, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Config{}, err
	}
	b, err := os.ReadFile(MarkerPath(abs))
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", MarkerPath(abs), err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", MarkerPath(abs), err)
	}
	if err := c.validateSchema(); err != nil {
		return Config{}, fmt.Errorf("%s: %w", MarkerPath(abs), err)
	}
	c.applyDefaults()
	c.ProjectsDir = filepath.Join(abs, "projects")
	c.SocketPath = filepath.Join(abs, c.Instance+".sock")
	return c, nil
}

// validateSchema rejects a marker that is not ours or is newer than this binary knows.
func (c *Config) validateSchema() error {
	if c.Type != ConfigType {
		return fmt.Errorf("not a Scratchpad config (type = %q, want %q)", c.Type, ConfigType)
	}
	if c.Version > ConfigVersion {
		return fmt.Errorf("config schema version %d is newer than this binary supports (max %d); upgrade %s",
			c.Version, ConfigVersion, appinfo.Name())
	}
	return nil
}

// applyDefaults fills neutral defaults for fields the marker leaves empty.
func (c *Config) applyDefaults() {
	if strings.TrimSpace(c.DisplayName) == "" {
		c.DisplayName = DefaultDisplayName
	}
	if strings.TrimSpace(c.Instance) == "" {
		c.Instance = DefaultInstance
	}
	if strings.TrimSpace(c.DefaultProject) == "" {
		c.DefaultProject = DefaultProject
	}
	if c.Limits.MaxTitleKB <= 0 {
		c.Limits.MaxTitleKB = DefaultLimits.MaxTitleKB
	}
	if c.Limits.MaxContentKB <= 0 {
		c.Limits.MaxContentKB = DefaultLimits.MaxContentKB
	}
	if c.Limits.MaxSectionsPerPad <= 0 {
		c.Limits.MaxSectionsPerPad = DefaultLimits.MaxSectionsPerPad
	}
	if c.Limits.MaxPadsPerProject <= 0 {
		c.Limits.MaxPadsPerProject = DefaultLimits.MaxPadsPerProject
	}
	if c.Wait.DefaultS <= 0 {
		c.Wait.DefaultS = DefaultWait.DefaultS
	}
	if c.Wait.MaxS <= 0 {
		c.Wait.MaxS = DefaultWait.MaxS
	}
	if c.TCP.Port <= 0 {
		c.TCP.Port = DefaultTCPPort
	}
}

// WriteMarker creates dir (0700) and writes the marker file (0600). It refuses to
// overwrite an existing marker so `init` never clobbers a live deployment. Only the
// schema header + identity groups are persisted; derived paths are cleared first, and
// optional groups are persisted only when they differ from the zero value.
func WriteMarker(dir string, c Config) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if IsInitialized(abs) {
		return fmt.Errorf("dir %q is already initialized (%s exists)", abs, MarkerFilename)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return err
	}
	c.Type = ConfigType
	c.Version = ConfigVersion
	if strings.TrimSpace(c.DisplayName) == "" {
		c.DisplayName = DefaultDisplayName
	}
	if strings.TrimSpace(c.Instance) == "" {
		c.Instance = DefaultInstance
	}
	c.ProjectsDir = "" // derived at load time, never persisted
	c.SocketPath = ""  // derived at load time, never persisted
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(MarkerPath(abs), append(b, '\n'), 0o600)
}

// ResolveProject picks the effective project name with the precedence
// flag > env SCRATCHPAD_PROJECT_NAME > marker default_project (already defaulted
// to "default" by LoadDir). Blank values are skipped at each level.
func ResolveProject(c Config, flagVal string) string {
	if v := strings.TrimSpace(flagVal); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv(EnvProject)); v != "" {
		return v
	}
	return c.DefaultProject
}
