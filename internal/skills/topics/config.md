---
id: config
title: Configuration
description: Where data lives, how the dir resolves, env vars, and limits
order: 4
---
# Configuration

Everything lives in one self-contained **Scratchpad dir**: the marker config
(`scratchpad.config.json`), a `config.md` guide, the optional `_rules.md`, the
`projects/` pad store, and the runtime socket. Move the dir and everything moves with it.

**Which files are pads:** inside `projects/<p>/`, a file named `<padid>.md` (`a-z0-9`) is
a pad; a file starting with `_` belongs to the tool (that is where `_rules.md` lives);
anything else is ignored and listed by `doctor` as a stray. Pad ids never contain `_`, so
the two namespaces cannot collide.

## Resolution (every command, same order)

1. `--dir <path>` flag
2. `SCRATCHPAD_DIR` env var
3. the `dir` field in the marker at the default location
4. the default `~/.scratchpad`

The **default dir bootstraps itself** on first use — zero setup. An **explicit** dir
(flag/env/config pointer) must already exist: commands error and point at
`init --dir <path>` rather than auto-create, so a typo can never seed a stray store.
There is no working-directory inference at all.

## Environment variables

| Variable | Meaning |
|---|---|
| `SCRATCHPAD_DIR` | the Scratchpad dir |
| `SCRATCHPAD_PROJECT_NAME` | default project when a command/tool omits one (set per repo, e.g. via direnv) |
| `SCRATCHPAD_AUTHOR` | default author for the CLI `--as` |
| `SCRATCHPAD_NONINTERACTIVE` | truthy = never prompt |
| `SCRATCHPAD_UI_PORT` | loopback port for the Web UI (`ui`), default 6711 |

Every env var has a matching flag; conflicts resolve flag > env > marker > default.

Separate projects, not separate stores: pads from different efforts are kept apart by
`project` (set `SCRATCHPAD_PROJECT_NAME` in each repo), all under one store. Use a
separate store (env `SCRATCHPAD_DIR` + `init`) only for genuinely separate storage
needs, e.g. an encrypted volume.

## Limits and wait bounds

Defaults: title 4KB, content 64KB per section, 1000 sections per pad, 1000 pads per
project; `pad_wait` default 60s, capped at 300s. All overridable via the marker's
`limits`/`wait` groups — the `config.md` written into the dir documents every field.

## Rules

`_rules.md` (store) and `projects/<p>/_rules.md` (project) hold the prose rules that
agents must acknowledge before their first post to a pad; a pad's own rules live inside
it as a `kind: rules` section. Read them with `rules` / `project rules <p>` /
`pad rules <ref>`. A missing or blank file means "no rules at this level", so there is
nothing to configure to turn the feature off.

**Writing** them answers two questions. WHO is the marker's `rules` group: by default
`store` and `project` are `"ui"` (the operator's — the Web UI or the file itself; an
agent asking through the CLI or MCP gets `rules_readonly`) and `pad` is `"opener"` (only
the agent that wrote section 1; anyone else gets `not_rules_owner`). Set them to
`"agent"` / `"any"` to widen. ON TOP OF WHAT is `--if-digest`, required on every write:
the version of that level from the `versions (--if-digest)` line, or `none` when it has
none yet. A stale one is `rules_conflict`, which hands back the version that won.

`--set <text>` carries the rules (or `--set -` for stdin); `pad rules --set` also needs
`--as <agent>` — the reserved author `scratchpad` belongs to the Web UI alone.

`doctor` diagnoses resolution and store health without ever creating or writing
anything — including which rule levels exist and any stray files; `doctor --json` is
machine-readable.
