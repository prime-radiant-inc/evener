# History content selection

The existing AppWire `thread/read` and `thread/turns/list` methods let a client
choose which transcript content it receives. Every inclusion flag defaults to
true. An omitted flag and an explicit `true` select the same view.

| Flag | Content |
| --- | --- |
| `includeConversation` | User and assistant messages, including human steering |
| `includeThinking` | Reasoning items |
| `includeToolCalls` | Tool invocations, arguments and results together |
| `includeToolCallIntents` | Explicit tool descriptions |
| `includeSystemMessages` | System messages and non-human steering |
| `includeDiagnostics` | The detailed `thread.evener.diagnostics` object |

With tool calls excluded and intents included, a described tool stays in its
existing `commandExecution` slot with its identity, description and lifecycle
metadata. It has no arguments, output, error, images or raw tool state. Tools
without descriptions produce no intent row. Including calls and intents keeps
one item, with the description on the call. Excluding intents while including
calls omits the description field; arguments remain intact. Selection is a
bandwidth and presentation control, not a security redaction boundary.

For conversation-only reads, send `includeConversation: true` and set the other
five flags to `false`. Send the same options on older-page requests and on
refresh/reconnect reads. Metadata-only reads can use `includeTurns: false` with
`includeDiagnostics: false` to avoid fetching detailed diagnostics before a send.
Metadata-only reads and mutation relay setup preserve any existing subscription's
history selection and live overlay identities. Use an `includeTurns: true` read
to establish or change the history view.

The public `itemLimit` counts selected items. The server fills a selected page
across complete indexed source windows, skipping excluded content before applying
the public count and byte limit. It does not return empty intermediate pages
just because a source window contains only excluded items. An empty older cursor
means no older selected items remain. The usual single oversized-item exception
to the soft byte limit still applies.

Opaque cursors bind the selection, requested Ref/ThreadID target, source snapshot,
boot generation and epoch. Older-page requests retain the read's target fields.
Selected continuations use snapshot-fenced positional paging, so remote cursor
cache eviction does not invalidate an unchanged conversation. Changing any selection
flag requires a fresh latest read; reusing another view's cursor returns
`transcriptItemCursorStale`. Filtered snapshots have distinct opaque incarnation
identities, so clients' ordinary history replacement rules also separate views.
A held snapshot from another selection receives a replacement read, never an
incremental change set. Changed older items and turn status/version/usage remain
in `changes` when a smaller selected window drops them from the source's latest
window.

Selection also applies to the subscription established by a read: history
updates, overlay items and their deltas use the same view. Lifecycle, status,
capabilities, queue state, delivery receipts, mutation authority and recovery
notifications remain available. History updates with no selected items still
advance the snapshot. A resync requests fresh history without retiring active
stream identities, so a failed refresh can continue receiving their deltas.
Overlay reset/end notifications still clear live state; successful subscription
replacement seeds identities from the new snapshot.

Each downstream subscription generation owns its selection and selected overlay
identities. Metadata reads and mutation relay preparation wait for overlapping
history responses to commit or abort, then preserve that resolved view. The wait
runs after preparation releases source ownership locks; mutations dispatch only
after preparation succeeds. A failed refresh preserves the previous view and live
overlay identities while still delivering daemon recovery notifications. Capture
rollback restores the previous owner and its filter; unsubscribe disposes of both.
Hub relay reads and feeds remain complete, so a compact client cannot hide data
from a full client sharing the same source.
The shared adapter in `internal/apphistory` and registration in
`internal/appserver.HandleThreadHistory` serve both Hub and daemon APIs.

Clients retain their own cached windows and drafts. The server owns selected-page
construction and history fencing; after a stale cursor, a client refreshes the
latest selected window without discarding unsent work. Source/index repair and
transport recovery remain owned by their existing subsystems.

## Summary failure diagnostics

A failed conversation summary leaves the existing history or the operation's
deterministic checkpoint available. The authoritative transcript is unchanged.
The next eligible compaction can install a valid summary and continue normal
model work. Model selection, fallback eligibility, retry counts and warning
visibility are unchanged; repeated failed compactions can still warn.

Context management emits `summary_failure` with the selected provider/model,
terminal failure category and available HTTP status in the existing warning
message. Advice distinguishes a failed fast/cheap route from the session model,
since changing the main model need not change an independently configured summary
model. CLI, AppWire, browser, native and Notification hooks carry these
existing message/title/hint fields without a new renderer or visibility policy.
Predictive-checkpoint recovery retains its informational `context_budget` code.

The runtime's typed `summary_failure` record separates the terminal route from
prior failed routes. It includes controlled category, stage and validation reason
values, validated HTTP status and retryability when known. Provider/model names
come from routing, are bounded to 256 bytes each and have control characters
removed from display. A pre-selection failure has no invented model identity.
Provider messages, response bodies, raw error codes, prompts, summaries, headers
and credentials are excluded. The client projection keeps its existing
`raw.warning` fields: `code`, `source`, `title` and `hint`.

The [session warning publisher](../../agent/summary_diagnostics.go) logs the safe
record once with `code` and `session_id`, independently of the opt-in API logger.
The daemon launcher's private stderr file is `run/logs/daemon-<session-id>.log`
under its state root. A delegate sharing the root daemon's log still records its
own session identity. The launcher retains the latest 1 MiB at launch; there is
no continuous size cap during a run. This is best-effort process logging, without
a new fsync-backed audit guarantee. An unavailable log sink does not suppress
the warning or prevent later compaction. Plain CLI execution writes to stderr;
its caller owns persistence.

The [recorded summary failure fixture](../../agent/testdata/summaryfailurewire/README.md)
exercises the real producer, projection, shared decoder and existing client
warning surfaces, followed by successful summary installation.
