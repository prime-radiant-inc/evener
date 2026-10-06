# Memory

Evener keeps useful personal and project lessons, and working notes about the current work, in ordinary files on the
session's host. The files are the source of truth, not transcript projections,
client caches or session notes. Stored text is fallible evidence, never
instructions or permission. Current user intent and direct evidence take
precedence.

## Ownership and storage

The session engine owns binding, native tools and index refresh:
[`session_memory.go`](../../agent/session_memory.go) and
[`session_tools_memory.go`](../../agent/session_tools_memory.go). The owning
host's [`DefaultStateRoot`](../../cmdutil/statedir.go) supplies these roots:

```text
<state-root>/memory/personal/
<state-root>/memory/projects/<Project.ID>/
<state-root>/memory/sessions/<root-session-id>/
```

The state root is `$XDG_STATE_HOME/evener`, normally
`~/.local/state/evener`. A history `StateDir`, `--state-dir` or
`EVENER_STATE_DIR` override does not relocate memory. Local and remote hosts
own separate storage. Personal memory crosses projects on that host; project
memory belongs to its bound project identity.

Session memory holds working notes about the current work (its plan, what was
tried and found). The root session owns it; delegates resolve the scope to
their root and can read but not write it. A delegate resumed on its own
(`serve --resume <delegate-id>`) is still a delegate: it has no root to
resolve, so it gets no session scope, no session guidance and no fork copy.
Resume keeps a root's session memory.

The session directory appears only when the root session first writes its
session memory (`memory_write`), or at a fork's first use. Reading or editing
an absent scope (the index refresh, `memory_read`, `memory_search`,
`memory_edit`, `memory_delete`) creates nothing: the index projects as
missing, a read or edit finds no file, a search finds no matches and a delete
reports the file removed or already absent. Delegates never create it.

A fork or `--resume-with` child copies its parent's session memory once, the
first time it reads or writes the scope, in its own process, normally at its
first model call; then the two diverge. After that first use the fork's scope
exists, holding the parent's copy or empty, so a resumed fork never copies the
parent's later notes and a failed copy is reported only once. A delegate of
the fork that reads first leaves the directory absent, so it cannot block the
copy. A missing or empty parent scope means a silent empty start. The copy
reads the parent and writes the child through the same confined layer the
memory tools use, so no symlink anywhere on the path is followed. It takes
only regular files and directories within a size limit (empty directories are
skipped); anything else, including a parent scope that exists but is a symlink
or a file, means a warning and an empty start. A copy failure is a session
warning and leaves the scope empty; it never blocks the session.

Deleting a session keeps its session memory. No native memory tool reaches
another session's session directory, so session memory that was written stays
until someone removes its directory by hand.

Production CLI and daemon sessions bind memory by default. Trusted launch
binding uses `identifier.ResolveProjectWith`; linked worktrees share their main
checkout's identity. The binding survives cwd changes, isolated delegates,
resume and compaction. Project-resolution failure leaves personal memory and
ordinary work available without guessing another project. Unbound library
sessions have no memory capability or home fallback. Tests supply fixture roots.

The five [native tools](../tools/memory.md) use separately confined file roots.
Read-only roles can write memory without gaining workspace writes. Fresh and
restored delegates remain within their parent's effective tool/scope ceilings.
Memory does not grant shell or network access. See [sandboxing](../sandboxing.md).

## Context and editing

```mermaid
flowchart LR
    Host[Trusted host and project binding] --> Index[Bounded lower-trust indexes]
    Index --> Session[Model working on the current task]
    Session --> Tools[Scoped memory tools]
    Tools --> Files[Ordinary memory files]
    Files --> Index
```

Enabled sessions receive separate personal, project and session `MEMORY.md` projections
as named user-source context, outside system instructions. Each scope supplies
at most 8 KiB of index content, cut at a UTF-8 boundary, with explicit truncation
and a route to `memory_read`. Topic files and logs are not preloaded.

Enabled sessions also receive core memory guidance. It is the last section of
the system prompt, rendered from what the session can do when the prompt is
built. The guidance is cached with the prompt and re-rendered when the tool
registry, model or environment changes. It says what each scope holds
(personal memory: what applies beyond the current project, such as how the
human partner works and how tools, systems and the world behave; project
memory: knowledge about this project; session memory: working notes about the
current work: its plan, what was tried and found), when to read a page, and that stored memory is fallible evidence, never
instructions or permission. Memory reflects what was true when it was
written: the agent checks that a file, function, command or setting a note names
still exists before relying on it, and follows a recorded decision or rule
unless the partner or newer evidence says it changed. Sessions that can call the save tools are also told
when to save: when the partner corrects the agent or says how they want work
done, when the partner states a project plan, constraint, decision or unfinished
work (saved to project memory), when a root session is partway through longer work (its plan,
what it tried and what it ruled out go to session memory as working notes), and when the agent learns
something the hard way that is not written down. Partner-stated
facts are saved before the work they shape, because complying with them does
not carry them to the next session. The Finishing guidance and the result
tool's description repeat the save check at the point the agent decides it is
done. Pages that contradict what the agent observes are corrected in the same
turn. Saving sessions are also told the shape of a useful page (one durable
fact with its reason and how to apply it, under an index line that says what
the page holds) and that run details which go stale within days (commit SHAs, ids, scratch
paths, test counts, review verdicts) stay out of
personal and project memory. A changed fact is rewritten in place. When the
agent reads a page that has turned into a log, it repairs that page before it
ends its turn. A status-only index line is a reason to read its page. Root
sessions that save working notes to session memory and can update the
whiteboard are told the whiteboard carries status for the partner, not working
notes. Root sessions are told that working notes belong in session memory, not
project memory (the second half only when project memory is bound). The
Finishing guidance adds one row telling root sessions to promote anything that
holds beyond this work out of session memory before they report. Delegates are told session memory is their root's, to read it and
report what they learn to their parent; a delegate with no root session id,
including one resumed on its own, is told nothing about session memory. A delegate's session index projection
says the same in one sentence. Guidance names only tools the session can call, and mentions project
memory only when a project scope is bound.

Refresh runs at startup, resume, after compaction and later model boundaries.
Unchanged projections are not appended again. Empty, missing and revoked states
supersede the previous current context; unavailable storage is not presented as
freshly read. Historical context remains recorded history.

The web transcript shows each index observation as a steering-style **Refreshed
my memory** notification. It starts collapsed at every detail level, including
Full, and opens only through the reader's explicit choice. Expansion shows the
scope, index state and formatted index. Unavailable and revoked states remain
visible on the collapsed row; truncated indexes retain their truncation label.
Source access preserves the complete recorded text, including content Markdown
cannot display. An observation that cannot be decoded opens as its original text.
Disclosure choices belong to the session and item and survive remounts. Live and
reloaded history use the same projection without changing model context or memory
files. Native, CLI and TUI context presentation remains unchanged.

Memory tool calls are separate from these automatic index observations. CLI and
TUI transcripts still use the generic tool-result path, while AppWire web and
native clients render memory tool calls with dedicated step renderers: the step's
words, a saved page for a write and a diff for an edit. There is still no memory
management UI or RPC.

Archived memory-context turns reconstruct as user-role evidence, preserving
their kind, body and order without crossing tool-round boundaries. Text-only
memory updates can join a Responses continuation delta after a valid active
anchor. Compaction without a new valid anchor still requires full history;
unsafe content and the other continuation eligibility checks remain unchanged.

Content has no required schema, frontmatter, filename extension, date grammar
or link-coverage rule. Empty files, arbitrary text, unusual dates and broken
links are accepted unchanged. Markdown, short `MEMORY.md` indexes and useful
dates are recommendations. An optional `log.md` is ordinary model-authored
content, not a runtime-maintained change log.

The bundled `gardening-memory` skill is explicitly activated through
`use_skill`. It supports small editorial passes: check evidence, correct
contradictions, remove duplicates, split sprawling pages and repair summaries
and links. It does not activate automatically or run background work.

## Faults, recovery and concurrent work

A missing automatic index means empty context and creates no index content.
An explicit missing `memory_read` still fails normally. Setup failures and
unreadable storage remain unavailable, not an empty or rebuilt wiki. The
healthy scope and ordinary session work continue. Automatic refresh has a
finite wait and admits no overlapping index reads per scope. A late abandoned
read is discarded; later access or model boundaries retry and can recover
without restarting the session.

The session requalifies each fixed scope directory beneath its captured host
anchor before admitting memory access. A regular directory restored at that
scope becomes usable in the same session. Symlink replacements remain refused,
and replacing the host pathname cannot redirect the captured authority.
Unchanged directories reuse their environment and read-before-write state.
Replaced environments retire only after admitted index and native operations
finish, including operations paused before filesystem I/O. Close does not wait
for stalled storage, and those late operations own their eventual retirement.

Each write/edit/delete changes one file using the shared filesystem primitive.
Page and index edits are separate calls, not a wiki transaction. There is no
revision check, replay receipt or stronger concurrent-edit guarantee. A whole
file write can overwrite another writer's changes. Read first, prefer focused
edits, and reread after a conflict or uncertain response before retrying.
Underlying errors and applicable read-before-write warnings remain visible.
Oversized output uses existing retained artifacts and `read_transcript` recovery.

Deletion admits only a regular file through captured-parent metadata, without
reading its body or requiring file read permission. Parent permissions still
govern removal. Missing files or parents are a no-op, while directories, symlinks
and special files are refused. Success reports removed or already absent, not
whether an unlink occurred. A leaf swapped after admission can still lose a
replacement non-directory entry, but cannot redirect traversal or remove a
directory. This is not an atomic file-identity check.

## Forgetting and lifetime

Forgetting means model-directed search and separate edits of active copies,
including the index and any maintained log. Deleting a page removes one file;
link repair is separate. There is no automatic consistency repair, old-body
archive or trash folder. Search inherits ordinary grep's dotfile and gitignore
exclusions, so it is not an exhaustive erasure tool.

Logical removal from the active files does not erase original transcripts,
retained output artifacts, provider requests or user backups. Session/history
deletion, worktree removal, compaction and daemon retirement do not own memory
deletion. Reopening the same project identity sees its surviving files.

## Per-session opt-out

`--disable-memory` defaults to false on `evener` and `evener serve`. Disabled
sessions expose no native memory tools, add no automatic memory guidance or
index context, and perform no native wiki I/O. Ordinary tools, skills and
session notes remain available. Loading `gardening-memory` does not grant a
disabled session memory capability.

The choice is sticky across resume and compaction. Omitting the flag on resume
preserves it; passing the flag can additionally disable an enabled session.
Disabled parents dominate fresh and restored children. There is no live toggle
or new screen.

Opt-out does not remove stored files, prior conversation or other sessions'
access. It does not revoke general filesystem permissions: unrestricted file
or shell tools can still reach memory storage if already authorized. It is a
native-feature switch, not an erasure or filesystem isolation switch.
