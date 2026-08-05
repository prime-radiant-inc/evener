# serf-doctor

Read-only forensic inspector for serf sessions, jobs, watches, and the session
tree. It reads settled on-disk state (`transcript.jsonl`, `api.jsonl`, `meta.json`,
`jobs.jsonl`, `mutations/<sid>.json`) through the same folds and types the serf
runtime uses, so a schema change either flows through automatically or fails to
compile — the numbers it reports are the runtime's numbers, never hand-parsed.
The client-mutation store is the one exception, and it is a deliberate one: the
runtime's snapshot types are unexported, so `mutations` mirrors the persisted
shape and refuses anything it does not recognize rather than reporting a journal
it could not fully decode.

This is the home of serf's session/transcript diagnostics. The terminal-bench eval
scripts that used to live in `tools/` were removed (they only served the benchmark);
the one capability worth keeping — API-call analysis — became the `apilog`
subcommand here.

Build: `make build-doctor` (or `go build ./cmd/serf-doctor`). Agent-facing usage
and the diagnose→findings workflow live in the bundled `doctoring-serf` skill;
design rationale is in `docs/superpowers/specs/2026-06-19-serf-doctor-unified-design.md`
and `…/2026-06-20-serf-doctor-apilog-design.md`.

## Subcommands (the tools)

Every session-scoped subcommand takes a session selector first: `local:<id>`,
`proj:<project-id>:<id>`, or a bare `<id>` (searched across buckets). The
state-root sweeps (`turnids`) take no selector. Common flags: `--state-dir <path>`
(state root; default `SERF_STATE_DIR` → `XDG_STATE_HOME` → `~/.local/state`) and
`--json`. Run `serf-doctor <subcommand> -h` for the full flag list.

| Tool | What it does | Key flags |
|---|---|---|
| `locate <sel>` | Resolve a selector to its on-disk transcript / canonical API log / meta / jobs / client-mutation paths and bucket. A bare `<id>` found in more than one bucket is reported as ambiguous, naming every bucket it appears in. | — |
| `transcript <sel>` | Render a session's logical turns; answer "how many real `X` calls?" structurally (calls vs. textual mentions). | `--count <tool>`, `--format outline\|markdown`, `--range last:N\|start:N\|A-B` |
| `apilog <sel>` | Canonical provider-attempt diagnostics: identity, grouping, finality, settlement state, tokens/latency, empty responses, errors, cache spikes, and whole-session token spend. Reads `sessions/<sid>.api.jsonl` through the shared API-log codec. `--validate` instead runs a whole-history structural-integrity scan: strictly decode every record from offset zero through clean EOF and report every corrupt/malformed/oversized/unsupported record with its offset (explicit diagnostics, proportional to file size — not run at logger open). | `--empty`, `--errors`, `--cache-spikes [--threshold N]`, `--summary`, `--validate` |
| `jobs <sel>` | Every job the session ran, folded from `jobs.jsonl`: status, reason, exit code, output bytes, start/end times, and the delegate/transcript/parent links to pivot on. | `--job <id>` |
| `mutations <sel>` | Did the user's input reach the daemon? Renders `mutations/<sid>.json`: the journal of every client mutation the daemon accepted **and** every one it rejected (method / operation state / execution state / stable turn / rejection), the durable input queue, pending executions, accepted turns, and queue revision. Absence from the journal means the request never arrived. | — |
| `watches <sel>` | Watch/delivery inspector: distinct deliveries (collapsing coalescing), provenance, lifecycle, the self-loop verdict, and the **target job's state** joined from the same `jobs.jsonl` (a target that died with no output could never match its condition). | `--watch <id>`, `--self-loops` |
| `tree <sel>` | Parent ↔ delegate/observer session tree across buckets. | `--depth N`, `--observers` |
| `turnids` | State-root sweep (no selector): which sessions persisted a reserved turn id inside the transcript's entry-index namespace (`turn_<digits>` rather than `turn_m<digits>`), so a reseed publishes one id for two turns. Names each session, the offending ids, the entries under them, and the turn kinds; a transcript it cannot decode is listed separately as unanswered rather than counted clean. | — |

## Examples

```sh
serf-doctor locate local:01KV8MVQ7BZHX0EN8D7ZH5QDE4
serf-doctor transcript <id> --count delegate_send       # real invocations, not mentions
serf-doctor apilog <id> --summary                       # token spend + empties + errors at a glance
serf-doctor apilog <id> --cache-spikes --threshold 40000
serf-doctor apilog <id> --validate                      # whole-history integrity scan; nonzero exit if any record is bad
serf-doctor jobs <id>                                   # what jobs has this session run, and how did each end
serf-doctor jobs <id> --job job_01KV8MVQ7BZHX0EN8D7ZH5   # what state is this one job in
serf-doctor mutations <id>                              # did the user's message reach the daemon at all?
serf-doctor watches <id> --self-loops
serf-doctor turnids                                     # which sessions carry a reserved turn id that collides with an entry's position
serf-doctor tree <id> --observers
```
