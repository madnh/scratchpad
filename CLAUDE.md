# CLAUDE.md — scratchpad

Shared pads for AI agents to exchange messages turn by turn (Go). Independent
codebase: it runs standalone, with no dependency on any particular host/mesh. Any host
integration must stay **generic and opt-in** — configured by the operator, never
hardcoded (no host names in code, docs, or skills content).

Design source of truth: `DESIGN.md` (spec), `IDEA.md` (concept), `USECASES.md`.

## Configuration model

Everything lives in one **self-contained Scratchpad dir**: the marker
(`scratchpad.config.json`), the guide (`config.md`), the pad store (`projects/`),
and the runtime socket.

- Resolution: `--dir` flag → `SCRATCHPAD_DIR` → the `dir` pointer in the DEFAULT
  dir's marker → the default `~/.scratchpad`. **No working-directory inference.**
- The DEFAULT dir auto-bootstraps on first use; an EXPLICIT dir must be `init`-ed
  first (commands error instead of creating it). `doctor` never bootstraps anything.
- All env vars share the `SCRATCHPAD_` prefix; every env var has a matching flag
  (flag > env > marker > default).
- `projects/` and the socket are **derived** from the dir, never configured.

## Storage model

One pad = one markdown file `projects/<project>/<padid>.md`, append-only. The first
line is the pad header (created ts + optional bcrypt password hash); sections are
headed `# <n> - <author> - <title>`. Turn state is DERIVED from the last section —
there is no state outside the pad files. Appends take an exclusive `flock` on the pad
file; reads a shared one. The CLI and the MCP server share `internal/store` — never
write a second disk path.

The MCP surface is **append-only by design**: no delete/update tools. Deletion/purge
exist only in the CLI.

## HARD RULE — keep the config guide in sync

`internal/config/config.md` is embedded (`go:embed`) and written into every
Scratchpad dir. **Whenever you change the config schema** (marker fields, defaults,
resolution order, env vars, the pad file format), **update `internal/config/config.md`
in the same change**, and bump `config.ConfigVersion` when the marker format changes
incompatibly. The skills topics (`internal/skills/topics/`) document the same
contracts — check them too.

## Build / test

```
make check        # gofmt + vet + test
go build ./...
```

Under `serve --stdio`, stdout belongs to JSON-RPC — all logging must go to stderr.
`scratchpad serve --stdio >/tmp/out 2>/tmp/err` must leave `/tmp/out` empty.
