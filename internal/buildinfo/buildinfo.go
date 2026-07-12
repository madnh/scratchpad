// Package buildinfo exposes version metadata stamped into the binary at build
// time via -ldflags. Keeping it in one place means version/commit/date have a
// single source of truth for the CLI, logs, and any future health surface.
package buildinfo

import "runtime/debug"

// These are overridden at build time, e.g.:
//
//	go build -ldflags "-X github.com/madnh/scratchpad/internal/buildinfo.Version=1.2.3 \
//	  -X github.com/madnh/scratchpad/internal/buildinfo.Commit=$(git rev-parse --short HEAD) \
//	  -X github.com/madnh/scratchpad/internal/buildinfo.Date=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

// Info is a snapshot of the build metadata.
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
	Date    string `json:"date,omitempty"`
}

// Get returns the current build info, falling back to the module's VCS stamp
// (recorded automatically by `go build`) when ldflags weren't supplied.
func Get() Info {
	i := Info{Version: Version, Commit: Commit, Date: Date}
	if i.Commit == "" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			for _, s := range bi.Settings {
				switch s.Key {
				case "vcs.revision":
					i.Commit = s.Value
				case "vcs.time":
					if i.Date == "" {
						i.Date = s.Value
					}
				}
			}
		}
	}
	return i
}

// String renders the info as "version (commit, date)" for CLI --version output.
func (i Info) String() string {
	s := i.Version
	switch {
	case i.Commit != "" && i.Date != "":
		s += " (" + i.Commit + ", " + i.Date + ")"
	case i.Commit != "":
		s += " (" + i.Commit + ")"
	}
	return s
}
