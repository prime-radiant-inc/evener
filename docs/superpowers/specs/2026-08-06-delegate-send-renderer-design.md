# delegate_send renderer: prettier card with delegate link and chat body

Date: 2026-08-06
Status: approved (user picked Approach A and all three recommended presentation options)

## Problem

The `delegate_send` tool row renders its collapsed summary as a raw metadata
dump — `Messaged dlg_… · delegate_id dlg_… · started · started_job_id job_… ·
running · running in background` — and its expanded body as a labeled
`CodeBlock`. The row tells the reader nothing useful at a glance and gives no
way to jump to the delegate it messaged.

## Design

Three changes, all in the hub frontend. No backend work: the
`delegateSendResult` wire shape (`agent/session_tools_jobs.go`) already ships
`transcript_ref` in the tool call's raw state.

### 1. Collapsed summary plus open-in-pane link

The summary reads:

```
Sent a message to delegate <clipped-id> · <status>
```

- `<clipped-id>` is the target delegate id clipped with the existing
  `ID_CLIP` helper.
- `<status>` is one word (`running`, `completed`, `failed`, …) recovered from
  the existing footer parse via `statusWordFromText`. No footer, no status
  segment.
- The bracketed footer dump leaves the summary entirely.

The link reuses the existing idiom and component: `OpenTranscriptButton`
(`transcript/openTranscript.tsx`), the same "open ⤢" control the subagent
module rows use, rendered in `ToolRow`'s `trailing` slot.

To get it there, `ToolRendererDescriptor` gains an optional field:

```ts
openTranscriptRef?(item: ItemModel): string | undefined;
```

This mirrors `openBesidePath` exactly: the descriptor declares data,
`ToolCallItem` renders the control. `ToolCallItem` composes the button into
`trailing` alongside any `FileOpenBesideButton`, passing `sessionRef` as
`parentRef` so the transcript opens beside the parent session with back
navigation intact.

The `delegate_send` descriptor implements `openTranscriptRef` by validating
`item.raw.transcript_ref`. A missing ref (runtime-message results, transcripts
written before this field existed) renders no button — never a dead link.

Rejected alternatives: widening `summary()` to `ReactNode` (the descriptor
contract keeps summary a plain string for truncation, `summarySuffix`
concatenation, and cluster headers); a `summaryLink`-style URL (panes are not
URLs); putting the link only in the expanded body (fails the ask).

### 2. Expanded body: an outgoing chat message

The body renders the sent message as a slack-lean chat message, reusing the
`UserMessageView` structure (`messages/UserMessageItem.tsx`):

- `SpeakerAvatar` tile (agent speaker).
- Header: `Agent → <clipped-id>` at body size, the call's clock time at
  caption.
- Bubble: the message text.
- A copy affordance in the header-actions slot replaces `CodeBlock`'s copy
  button.

`UserMessageView` gains optional `speaker` and `name` props; their defaults
preserve today's exact rendering for real user messages.

### 3. Delegate reply: an incoming bubble

When the call waited and the delegate replied, a second bubble renders below
the outgoing one, again with the agent avatar: header `<clipped-id>
(delegate)`, reply text in the bubble.
The response comes from the existing `delegateSendResponse` extraction
(validated raw `output`, else the formatted output minus its footer). No
reply, no second bubble.

### Unchanged

- The `useCorrelateSubagentRow` side effect that patches the subagent module
  row.
- The legacy `job_send_message` alias match.
- The `HeadClippedOutputBody` fallbacks for the other job-family tools.

## Error handling

Every new read degrades to today's behavior: malformed or absent raw state
yields no link and no status word; an absent message arg with no response
still renders nothing (the body's current early return).

## Testing

Extend `cmd/serf-hub/frontend/src/panes/session/transcript/tools/jobTools.test.tsx`:

- Summary wording with and without a footer status.
- `openTranscriptRef` returns the ref for valid raw state, `undefined` for
  absent or malformed raw state.
- Expanded body renders the outgoing bubble with the message text and the
  `Agent → <id>` header.
- A response renders the incoming `<id> (delegate)` bubble; no response
  renders no second bubble.

Add a `ToolCallItem`-level test proving the descriptor's ref is threaded to a
working `OpenTranscriptButton` (click opens the transcript pane), mirroring
the existing `summaryLink` threading test in `toolRowGrammar.test.tsx`.

Gates: `npx biome check --write` on touched files, then `make test-web`; on
this Chrome-capable host also `make test-web-browser`.
