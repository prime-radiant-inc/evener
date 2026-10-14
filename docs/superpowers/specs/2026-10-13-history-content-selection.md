# History content selection

Clients select transcript content on the existing `thread/read` and
`thread/turns/list` methods. Six independent optional booleans default to true:
`includeConversation`, `includeThinking`, `includeToolCalls`,
`includeToolCallIntents`, `includeSystemMessages`, and `includeDiagnostics`.

Conversation includes user and assistant messages and user-originated steering.
Thinking includes reasoning. Tool calls include arguments and results together.
Tool intent is the explicit description, never inferred: when calls are excluded,
an intent-bearing call retains its identity, description and lifecycle metadata,
without arguments, output, errors, images or raw tool state. When calls are included,
the intent stays on the same item. System messages include non-user steering.
Diagnostics controls the detailed diagnostics object, not status, capabilities,
mutation receipts, queue state or history identity. These are content controls,
not a security redaction boundary.

Selection precedes the public item count and response size limits. The server
fills a selected page across its existing indexed source pages. Excluded items
do not consume the selected page. An opaque cursor binds the normalized selection
and the original source's history identity. Switching selection requires a fresh
latest read. A held snapshot from another selection cannot produce incremental
changes. Default requests preserve the existing representation.

The same selection governs history and overlay notifications for the subscription
created by a read. Each downstream subscription owns its selection. Shared upstream
feeds stay complete so a compact reader cannot affect another client. Subscription
replacement, rollback, unsubscribe and reconnect retain their existing ownership
and ordering guarantees. Essential lifecycle/recovery notifications always flow.

The Playdate requests conversation only, retains metadata-only send preparation,
and uses the same options for backfill and refresh. No endpoint or Hub behavior is
specific to a device. Validate payload size and real device read/send recovery
separately from unit and transport tests.
