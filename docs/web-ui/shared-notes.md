# Shared notes

The Notes panel contains your note, the agent's note, and session links.

Editing your note changes a shared draft for that session. Multiple open panels show the same draft. Leaving the editor schedules a save **10 seconds later**; focusing any editor for the same session cancels that delay. Saving also notifies the agent and may wake an idle agent.

Closing a panel without leaving the field does not save. The draft remains available when you reopen it in the same browser session. If you already left the field, its scheduled save survives closing the panel.

A saved response only acknowledges the draft that was submitted. Typing while an earlier save is pending cannot be overwritten by that response. Notes preserve whitespace and Unicode exactly as returned by the server.

If a save is refused, the panel keeps your draft and shows the error. Edit or refocus and leave the field to schedule another attempt. An uncertain transport outcome stays in the durable outbox and reconnects using the same request identity, rather than sending a second independent notification. A blocked outcome waits for session recovery. Submitted drafts survive reload through that outbox; edits not yet submitted are browser-memory drafts and do not survive a full reload.

A save will not proceed if the session ends, loses notes capability, or changes instance before its deadline. Read-only sessions continue to show saved notes and links.

## Transcript updates

Web and native render internal `notes-context` snapshots as **Shared notes updated**.
Conversation hides them even with System events enabled. At other levels, the
System events setting controls visibility. Failed or interrupted turns never
bring a hidden snapshot back.

Snapshots start folded at every level, including Activity and Full. Opening one
shows its complete literal text, preserving the shared-notes framing, whitespace
and links. Each client remembers explicit expansion by session and item through
remounts. Native keeps list-row text bounded and reads the complete snapshot from
retained canonical turns only when expanded. If the canonical lookup misses,
native preserves the available row text. Web keeps snapshots separate from
grouped lifecycle notices. Your saved note messages remain visible at every level,
and the Notes panel stays available
independently of transcript display settings.
