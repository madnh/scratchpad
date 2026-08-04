package config

import (
	"slices"
	"sync/atomic"
)

// Live is the deployment config as the RUNNING process sees it — a holder that can be
// re-pointed while requests are in flight.
//
// It exists because a long-lived process used to freeze the marker at startup: the store
// kept its limits, the MCP server its wait bounds, the UI its display name. Editing
// `scratchpad.config.json` then changed nothing until a restart, while the CLI — a new
// process per command — picked the same file up immediately. One store, two answers to
// "what is the limit", depending on which surface you asked.
//
// Reads are a snapshot: take one at the top of an operation and use it throughout, so a
// single Post cannot see two different limits halfway down. The snapshot is shallow —
// the slices inside TCP are shared with the stored value and must never be mutated in
// place; replace the whole Config instead, which is what Set does.
type Live struct {
	cur atomic.Pointer[Config]
}

// NewLive wraps an already-loaded config. Every surface takes a *Live rather than a
// Config, including the short-lived CLI where it never changes: a surface that can be
// handed a frozen copy is a surface that WILL be handed one, and the bug that produces
// is silent — it enforces yesterday's limits without complaining.
func NewLive(cfg Config) *Live {
	l := &Live{}
	l.cur.Store(&cfg)
	return l
}

// Get returns the current config. Safe from any goroutine.
func (l *Live) Get() Config { return *l.cur.Load() }

// Set replaces the whole config. Callers reloading from disk should prefer Apply, which
// keeps the groups that cannot change under a running process.
func (l *Live) Set(cfg Config) { l.cur.Store(&cfg) }

// Apply installs a freshly loaded marker and reports which COLD groups differ from what
// this process is actually running (see MergeHot). The caller is expected to say so out
// loud: an operator who edited `ui.port` and saw nothing happen deserves a line telling
// them a restart is what applies it, not silence that reads like success.
func (l *Live) Apply(fresh Config) []string {
	running := l.Get()
	l.Set(MergeHot(running, fresh))
	return ColdChanges(running, fresh)
}

// MergeHot returns the running config with only the HOT groups taken from fresh.
//
// The split is not about importance, it is about what a process can honestly change
// underneath itself. Limits and rules are consulted per operation, so a new value simply
// applies to the next one. A listener is already bound and a socket is already named, so
// swapping `ui.port`, `tcp`, `instance` or `dir` in memory would leave the process
// running on the old one while reporting the new — a config that lies is worse than a
// config that is stale.
//
// HOT:  display_name, default_project, limits, wait, rules
// COLD: dir, instance, tcp, ui, and every derived path (root_dir, projects_dir,
// socket_path — derived from the two cold identity fields, so they move only with them).
//
// rules is HOT even though the Web UI may not write it: "where is it edited" and "does it
// need a restart" are separate questions, and an operator who widens a policy by editing
// the file means it to take effect now.
func MergeHot(running, fresh Config) Config {
	out := running
	out.DisplayName = fresh.DisplayName
	out.DefaultProject = fresh.DefaultProject
	out.Limits = fresh.Limits
	out.Wait = fresh.Wait
	out.Rules = fresh.Rules
	return out
}

// ColdChanges names the cold groups that differ between what is running and what the
// marker now says. The names are the marker's own field names, so the message points at
// the thing the operator just edited.
func ColdChanges(running, fresh Config) []string {
	var out []string
	if running.Dir != fresh.Dir {
		out = append(out, "dir")
	}
	if running.Instance != fresh.Instance {
		out = append(out, "instance")
	}
	if running.TCP.Port != fresh.TCP.Port ||
		!slices.Equal(running.TCP.TokenDigests, fresh.TCP.TokenDigests) ||
		!slices.Equal(running.TCP.AllowedOrigins, fresh.TCP.AllowedOrigins) {
		out = append(out, "tcp")
	}
	if running.UI != fresh.UI {
		out = append(out, "ui")
	}
	return out
}
