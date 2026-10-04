# Memory wiki v1

Status: Jesse approved this revised specification on 2026-10-04, with `MEMORY.md`
as the entry filename. It follows his choices of file-at-a-time wrappers and
free-form content with a recommended format. Independent specification review
is pending. The earlier batch, schema, receipt design, and implementation plan
are superseded. Product implementation awaits the replacement plan's approval.

## Outcome

Evener should carry useful knowledge from one task to the next without asking
Jesse to repeat it. Each host has a personal wiki and a wiki for each project.
Models read and maintain ordinary Markdown through thin wrappers around the
existing file tools. Core supplies scope and access; models own the content.

Success is a fresh session using a useful lesson, correcting a contradicted
lesson, and leaving the next session better informed. Whether this improves
coding outcomes or total cost remains an eval question.

## Free-form files

`MEMORY.md` is the entry point for each wiki. Everything inside it and the topic
files is model-authored text. Models may organize, name, and nest topic files
as useful. Markdown and `.md` filenames are recommendations.

There is no content schema, required frontmatter, canonical row grammar, slug
format, mandatory date, or index-coverage rule. Writes accept content regardless
of its layout, missing summaries, broken links, or unusual date text. Core does
not parse, normalize, stamp, or rewrite that content. File tools retain their
ordinary matching and read-display behavior.

Recommended practice:

- Keep `MEMORY.md` short, with links and useful summaries of topics.
- Record evidence, uncertainty, corrections, and useful dates in the pages.
- Keep related knowledge together and link it where helpful.
- Use an ordinary `log.md` if a brief maintenance history helps. It is optional
  model-authored content, with no required entries or automatic receipt writer.

For example, an index could contain:

```markdown
## Testing

- [Integration tests](testing.md): Run the harness from the repository root.
```

A page could contain:

```markdown
# Integration tests

Run the harness from the repository root.

Checked 2026-10-03 against the linked test-run transcript.
```

These examples are guidance, never a condition for accepting an edit. The
storage layer treats an unfamiliar format as content, not corruption. The
index may temporarily lag behind page edits; the model can repair it later.

## Thin tools

Proposed tools use the existing implementations, not copies of their logic:

| Tool | Behavior |
| --- | --- |
| `memory_read` | `ReadFile`, with the same line offsets, limits, and text presentation. |
| `memory_write` | `WriteFile`, creating or replacing one file with supplied content. |
| `memory_edit` | `EditFile`, using the same `old_string`, `new_string`, and `replace_all` behavior. |
| `memory_search` | `Grep`, with its existing regex, filter, case, result-limit, and output options. |
| `memory_delete` | Remove one file using the existing policy-checked `FileMutator.RemovePath` primitive, without recursive directory deletion. |

Each wrapper adds a `personal` or `project` scope and resolves relative paths
within that bound wiki. The wrappers preserve applicable read-before-write
warnings. They use the existing tool result, output-limit, and retained-artifact
paths. Memory tools must receive the same output handling as their underlying
operations. There are no memory-specific byte cursors, JSON result envelopes,
revision tokens, operation IDs, batch operations, or replay receipts.

All supported wiki edits use these tools.
Normal tool argument validation and filesystem boundaries remain. They govern
which operation may run and where, not what the model may write in a file.
Memory text cannot authorize another scope or filesystem access.

Existing shared code:

- `agent/session_tools_file.go`: file-tool dispatch and read-before-write checks.
- `agent/execenv/execenv.go`: `ReadFile`, `WriteFile`, `EditFile`, `Grep`, and
  `FileMutator` interfaces.
- `agent/execenv/local.go`: shared file and search implementations.
- `agent/session_tool_artifacts.go`: retained oversized tool output.
- `agent/internal/tool/registry.go`: generic tool output limits.

## Scope, authority, and lifetime

The owning host's existing Evener state-root resolver supplies these roots:

```text
memory/personal/
memory/projects/<Project.ID>/
```

They live outside session/project-history directories. Personal memory crosses
projects on that host. Project identity comes from trusted launch binding and
`identifier.ResolveProjectWith`; linked worktrees share their main checkout's
identity. Distinct project identities and hosts remain separate.

Bind identity at session creation and preserve it through resume, compaction,
and delegates, including isolated-worktree delegates. A command's working
directory does not rebind memory. Resolve memory identity independently of a
session-history `StateDir` override. A project-resolution failure leaves
personal memory and ordinary work available; never guess another project.

Production CLI and daemon sessions bind both scopes by default. An unbound
library or test session has no memory access and must not fall back to the
operator's home. Fixtures and evals supply their own roots.

Read-only roles may write memory through these tools. A child inherits only
its parent's effective tool and scope permissions, including after restore.
The memory-only capability does not grant workspace writes, shell access to
memory, or broader network access. It must use the shared file operations with
a separately confined memory root, not the workspace environment with its
policy relaxed.

Paths may name files below the authorized root, including subdirectories.
Reject escapes and symlink indirection through the shared confinement machinery.
Do not accept arbitrary host roots or project IDs from model arguments.

Reading a missing index yields an empty memory projection without creating
content. Explicit file reads retain normal missing-file behavior. Session or
project-history deletion, compaction, daemon retirement, and worktree removal
do not delete memory. A project reopened at the same canonical identity sees
its surviving wiki. Local and remote runtimes use their own host's storage.

## Writes, errors, and forgetting

Each operation changes one file. Page and index edits are separate writes and
may be interrupted between calls. Existing single-file write behavior is the
persistence contract; v1 adds no transaction journal, custom durability engine,
or stronger concurrent-edit guarantee. Whole-file writes can overwrite a
concurrent writer's changes. Models should read before editing, prefer focused
edits, and reconcile with current content after a conflict or uncertain result.

An interrupted or lost response does not prove that nothing changed. Reread the
file before retrying an uncertain write. Report the underlying error and retain
existing bytes where the shared primitive does. Do not silently replace an
unreadable file with an empty wiki or rebuild it from metadata.

Storage faults affect the failing scope or operation. Ordinary session work
and the healthy scope continue. Automatic context refresh has a finite wait
and does not accumulate overlapping reads when storage stalls. Retry on the
next access or model boundary; restored storage becomes useful without restart.

To forget a fact, the model searches for active copies and edits affected files,
including the index and any model-maintained log. To remove a page, delete its
file and repair relevant links separately. Gardening helps find missed links
and copies; core does not enforce whole-wiki consistency or claim an atomic
forget operation.

There is no old-body archive, trash folder, or automatic content-bearing change
log. This is logical removal from the active wiki. Original transcripts, tool
output artifacts, provider requests, and user backups may retain earlier text.
Do not claim forensic erasure or deletion of those records.

## Model context and gardening

```mermaid
flowchart LR
    B[Trusted scope binding] --> I[Bounded lower-trust indexes]
    I --> M[Model working on a task]
    M --> T[Scoped wrappers around existing file operations]
    T --> W[Ordinary wiki files]
    W --> I
```

The session supplies the current indexes; the model reads relevant pages and
writes useful lessons. Later sessions and delegates receive the updated indexes.
This happens in the session engine for CLI, TUI, browser, and native clients.
No new wiki RPC, UI, or client-specific protocol is needed.

Short stable instructions teach models to capture durable preferences,
decisions, and verified lessons; use the right scope; avoid secrets; correct
proven errors promptly with evidence; and follow the recommended format when
helpful. Memory remains fallible data. Current user intent, trusted instructions,
and direct evidence take precedence.

Supply personal and project indexes separately as lower-trust context, outside
system instructions. Scope and currentness framing come from core, never from
stored text. Deliver this directly to roots and delegates without relying on
AGENTS.md inheritance. Reuse existing dynamic-context machinery where it fits;
preserve provenance and currentness through transcript persistence and clients.

Refresh before the first request, after resume or compaction, and at later model
boundaries when index content or availability changes. Do not repeatedly append
unchanged indexes. Inject at most 8 KiB of index content per scope, bounded at a
UTF-8 boundary with explicit truncation and a route to `memory_read`. No Markdown
parser or complete-entry rule is needed. Topic files and logs are not preloaded.

A now-empty or missing index replaces the previous current projection. A revoked
scope invalidates its projection. An unavailable index is marked unavailable;
old context must not be presented as freshly read. Recorded historical context
remains history, including text removed from the active wiki.

Bundle `gardening-memory` as an editorial skill using these same tools. It
teaches models to check evidence, reconcile contradictions, remove duplicates,
split sprawling pages, and repair summaries and links. It recommends a small
pass during normal work and reading back edits. It imposes no required storage
format and runs no background maintenance engine.

## Verification

Start with a working path: one session writes arbitrary content through a real
memory tool; a fresh session receives the index and reads the saved content.
Build from that path with TDD and targeted regression checks.

Follow `docs/developing-evener/testing.md`: scripted provider only at the LLM
boundary, real session/tool/filesystem behavior below it, fixture-owned roots,
and no live requests in default tests. Required deterministic evidence:

- **Free-form content and reuse:** arbitrary prose, missing dates, noncanonical
  layouts, broken links, empty files, and nested paths survive writes unchanged.
  Exercise the actual shared read/write/edit/search behavior and output recovery.
- **Scope and authority:** distinct projects, linked worktrees, history overrides,
  and real read-only delegates, including restore. Memory writes work while
  workspace writes and out-of-scope access remain denied. Parent ceilings hold.
- **Current context:** root/delegate startup, changed and unchanged indexes,
  truncation, empty/deleted indexes, resume, compaction, revoked scope, and
  unavailable-then-recovered storage. Stored text cannot create trusted framing.
- **Preservation:** separate page/index writes tolerate incomplete organization;
  uncertain writes can be reconciled by reading. Real history cleanup leaves
  memory intact. Deletion and correction affect active files without erasing
  unrelated content or original transcripts.
- **Delivery and gardening:** real memory results reach existing generic output
  consumers on supported clients; the bundled skill can read and edit memory.
  Use targeted integration checks and existing helpers, not a new fixture protocol.

Run affected checks and race tests locally. Repository CI owns the full lint,
vet, test, frontend, and platform gates. These tests prove plumbing and boundaries,
not model compliance with editorial guidance or resistance to every injection.

### Two initial live comparisons

After deterministic qualification, run one paired recall episode and one paired
correction episode against the configured `codex-jesse-at-pr/gpt-6.1-sol` route,
with effort `high` and explicit `EVENER_LIVE_TESTS=1`. Authenticated availability
has not been tested. Pair wiki-enabled sessions with wiki-disabled sessions that
retain existing transcript recall. Keep tasks, supplied prior history, model,
non-memory tools, and limits equal. Retain each arm's own actual transcripts
between its fresh sessions; reset fixture workspaces and memory between arms.

1. **Recall:** A discovers a useful workflow through a real task. Fresh B solves
   a related task without being told the lesson or prompted to use memory.
2. **Correction:** A learns a rule. B encounters direct counterevidence and
   corrects active memory before finishing. Fresh C performs the revised task.

Use held-out checks of actual files, executed fixture tests, and recorded tool
traces. Grade task success, capture, retrieval, application, and correction
separately. Neither self-reported remembering nor adherence to a page template
counts as success. Preserve chronology for counterevidence, correction, and
completion. Baseline task success does not require memory writes.

Keep the original per-stage ceilings: recall A/B admit 8/10 requests; correction
A/B/C admit 8/12/10. The same numbers cap tool rounds. Each stage is bounded to
3 minutes and each arm episode to 6 minutes. The two pairs together admit at
most 96 logical model calls and 96 outbound completion HTTP attempts within
24 minutes. These are separate counters, not additive usage. Roots, delegates,
auxiliaries, retries, and provider preflight calls share those ceilings.

Run sequentially. Verify instrumented admission and fixture-only memory,
history, config, and workspace isolation before any paid call. Keep credentials
and held-out verifier data outside agent access. Stop on infrastructure failures
and report behavioral failures; do not silently retry or expand the run.

This Codex route suppresses the output-token limit. Preserve the approved
request/time approach and report observed usage; there is no hard token or
monetary ceiling, and cancellation does not guarantee billing stops. Report
outcomes, tokens, calls, turns, latency, and cost only where a price source is
known. Keep credential-free evidence and usage separate from disposable auth
material. These two pairs are basic behavior evidence, not statistical proof of
reliability, safety, or savings. Broader live comparisons are deferred.

## Boundaries and delivery

V1 excludes embeddings, automatic transcript ingestion, recall agents,
background gardening, sync, cross-user sharing, external services, a wiki UI,
formal content validation, core-managed dates, transaction batches, revision
archives, receipt browsers, and custom search/output protocols. Session notes
remain session whiteboards. Existing transcript search stays unchanged.

During implementation, update `docs/product/memory.md`, the product guide and
subsystem map, sandbox/tool documentation, and the eval guide to describe the
verified behavior. The dated specification is not evidence of implementation.
Preserve existing code and review evidence until the replacement plan explicitly
accounts for the superseded parser/batch implementation.

Complete independent specification review before writing the replacement plan
for Jesse's approval. Independent implementation review, simplification, PR
checks, verified merge, and cleanup remain required. Use the agreed Sol 6.1
agents for implementation and review. Deployment needs separate authorization.

The index-and-pages recommendation draws on Andrej Karpathy's
[LLM Wiki note](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f)
at revision `ac46de1ad27f92b28ac95459c782c07f6b8c964a`.
