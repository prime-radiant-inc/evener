# Memory refresh notifications

## Outcome and scope

Automatic memory context in the web transcript appears as a compact,
steering-style notification headed **Refreshed my memory**. Opening the
notification reveals readable memory content rather than the model-facing
quoted envelope.

Jesse approved keeping the disclosure collapsed by default at **every**
verbosity level, including Full, on 2026-10-06. This spec records the
web-only change. Dedicated memory-tool renderers, native app presentation,
CLI/TUI presentation, memory storage and model context remain unchanged.

## Current cause

`agent/session_memory.go` appends a `MEMORY_CONTEXT` turn containing a
scope/state envelope and a Go-quoted index. The transcript projection in
`internal/apptranscript/apptranscript.go` emits a `systemMessage` with no
event kind. The shared transcript projector deliberately retains unknown
system messages at every detail level. `SystemNoticeItem.tsx` renders the
unclassified body as plain text.

## Presentation contract

- Keep each memory refresh inspectable in its original transcript position.
  Render a steering-style glyph, the exact summary **Refreshed my memory**,
  and a disclosure chevron instead of the raw text wall.
- Begin collapsed at Conversation, Intent, Tools, Activity and Full.
  Changing verbosity or applying a general expansion baseline must not open
  a memory refresh. Explicit user expansion remains authoritative.
- Opening reveals the scope and index state, then the decoded index through
  the existing safe Markdown renderer. Headings, paragraphs, lists and code
  remain readable. Do not render escaped newlines or enclosing quote syntax
  as the memory body.
- Preserve all available content. A truncated source stays visibly marked
  truncated; the renderer must not imply it has the missing remainder.
  Empty, missing, revoked and unavailable states remain distinguishable.
  Unavailable or revoked observations carry that state on the collapsed
  row as well, so the summary does not imply a successful read.
- If a recorded body cannot be decoded, retain the compact notification and
  show its complete original text when opened. Do not discard an unfamiliar
  record, manufacture index data, or interpret it as trusted instructions.
- Keep disclosure state scoped to the session and item. An explicit choice
  survives transcript remounts without opening a same-ID item in another
  session. Keyboard and pointer activation use the existing disclosure
  interaction. Narrow layouts wrap content without horizontal page overflow.
- Memory refreshes remain standalone notifications rather than disappearing
  into generic system-event groups. Ordinary steering, shared notes, system
  notices and memory-tool calls retain their existing behavior.

## Ownership and preservation

Give projected memory context an explicit identity derived from its
`MEMORY_CONTEXT` turn kind. Keep it a context observation rather than
reclassifying it as a user steer. Projection owns extracting display data
from the recorded observation; the web renderer owns presentation. Reuse
the existing steering disclosure structure with a formatted body rather
than duplicating its interaction and styling.

Live and reloaded history must reach the same presentation. The typed
classification and any shared-projector changes must preserve native
visibility and presentation. No transcript rewrite, memory-file mutation,
new memory store, RPC, model-facing prompt change, or recall timing change
belongs to this work. Current history projection is the supported path;
no old-daemon compatibility layer is included.

Decode failures affect only presentation of the affected item. The raw
record remains inspectable, and surrounding transcript items remain usable.
Later successful observations display normally without clearing history.

## Verification

Use deterministic fixture memory and the real production paths below the
provider boundary. Inspect test runners before invoking them.

1. Add a failing regression proving that a real recorded memory-context
   turn projects with explicit identity and preserved source data. Cover
   personal, project and session scopes, state-only observations, truncated
   indexes, Unicode and quoted content, and undecodable bodies.
2. Feed producer-backed wire fixtures through the shared adapter/projector
   and mounted web transcript. At every preset, assert the compact heading,
   closed disclosure and absence of visible raw payload before interaction.
3. Open the disclosure using pointer and keyboard. Assert formatted content,
   truthful state/truncation labels and complete fallback evidence. Test
   explicit open/close, verbosity changes, general expansion baselines,
   remount, session separation and saved-history reload.
4. Cover surrounding ordinary steering, shared notes, system grouping and
   memory-tool calls. Pin unchanged native projection for the new identity.
5. Exercise the production renderer in a real browser at desktop and narrow
   widths. Check closed/open geometry, keyboard activation and no overflow.
   Run focused Go/frontend/shared-client checks, then the canonical web gate;
   leave the repository's full-suite gates to CI.

Update `docs/product/memory.md` and the affected responsibilities in
`docs/product/subsystems.md` alongside the implementation. Complete the
required independent reviews, simplification and PR checks before merging.
Deployment or hub restart requires separate authorization.
