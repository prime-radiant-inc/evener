# serf-doctor

Read-only forensic inspector for serf sessions, jobs, watches, and the session
tree. It reads settled on-disk state (`transcript.jsonl`, `meta.json`,
`jobs.jsonl`) through the same folds and types the serf runtime uses, so a schema
change either flows through automatically or fails to compile — the numbers it
reports are the runtime's numbers, never hand-parsed.

This is the home of serf's session/transcript diagnostics. The terminal-bench eval
scripts that used to live in `tools/` were removed (they only served the benchmark);
the one capability worth keeping — API-call analysis — became the `apilog`
subcommand here.

Build: `make build-doctor` (or `go build ./cmd/serf-doctor`). Agent-facing usage
and the diagnose→findings workflow live in the bundled `doctoring-serf` skill;
design rationale is in `docs/superpowers/specs/2026-06-19-serf-doctor-unified-design.md`
and `…/2026-06-20-serf-doctor-apilog-design.md`.

## Subcommands (the tools)

Every subcommand takes a session selector first: `local:<id>`, `proj:<hash>:<id>`,
or a bare `<id>` (searched across buckets). Common flags: `--state-dir <path>`
(state root; default `SERF_STATE_DIR` → `XDG_STATE_HOME` → `~/.local/state`) and
`--json`. Run `serf-doctor <subcommand> -h` for the full flag list.

| Tool | What it does | Key flags |
|---|---|---|
| `locate <sel>` | Resolve a selector to its on-disk transcript / meta / jobs paths and bucket. | `--all-buckets` |
| `transcript <sel>` | Render a session's logical turns; answer "how many real `X` calls?" structurally (calls vs. textual mentions). | `--count <tool>`, `--format outline\|markdown`, `--range last:N\|start:N\|A-B` |
| `apilog <sel>` | API-call diagnostics: per-call tokens/latency, empty responses, errors, cache spikes, and whole-session token spend. Reads the in-transcript `api_call` records via serf's `transcript.APICall` type. | `--empty`, `--errors`, `--cache-spikes [--threshold N]`, `--summary` |
| `watches <sel>` | Watch/delivery inspector: distinct deliveries (collapsing coalescing), provenance, lifecycle, and the self-loop verdict. | `--watch <id>`, `--self-loops` |
| `tree <sel>` | Parent ↔ delegate/observer session tree across buckets. | `--depth N`, `--observers` |

## Examples

```sh
serf-doctor locate local:01KV8MVQ7BZHX0EN8D7ZH5QDE4
serf-doctor transcript <id> --count delegate_send       # real invocations, not mentions
serf-doctor apilog <id> --summary                       # token spend + empties + errors at a glance
serf-doctor apilog <id> --cache-spikes --threshold 40000
serf-doctor watches <id> --self-loops
serf-doctor tree <id> --observers
```
