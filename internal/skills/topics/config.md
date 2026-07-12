---
id: config
title: Configuration
description: Where data lives, how the dir resolves, env vars, and limits
order: 4
---
# Configuration

Everything lives in one self-contained **Scratchpad dir**: the marker config
(`scratchpad.config.json`), a `config.md` guide, the `projects/` pad store, and
the runtime socket. Move the dir and everything moves with it.

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

Every env var has a matching flag; conflicts resolve flag > env > marker > default.

Separate projects, not separate stores: pads from different efforts are kept apart by
`project` (set `SCRATCHPAD_PROJECT_NAME` in each repo), all under one store. Use a
separate store (env `SCRATCHPAD_DIR` + `init`) only for genuinely separate storage
needs, e.g. an encrypted volume.

## Limits and wait bounds

Defaults: title 4KB, content 64KB per section, 1000 sections per pad, 1000 pads per
project; `pad_wait` default 60s, capped at 300s. All overridable via the marker's
`limits`/`wait` groups — the `config.md` written into the dir documents every field.

`doctor` diagnoses resolution and store health without ever creating or writing
anything; `doctor --json` is machine-readable.
