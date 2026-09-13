# Shared notes

The Notes panel contains your note, the agent's note, and session links.

Editing your note changes a shared draft for that session. Multiple open panels show the same draft. Leaving the editor schedules a save **10 seconds later**; focusing any editor for the same session cancels that delay. Saving also notifies the agent and may wake an idle agent.

Closing a panel without leaving the field does not save. The draft remains available when you reopen it in the same browser session. If you already left the field, its scheduled save survives closing the panel.

A saved response only acknowledges the draft that was submitted. Typing while an earlier save is pending cannot be overwritten by that response. Notes preserve whitespace and Unicode exactly as returned by the server.

If a save is refused, the panel keeps your draft and shows the error. Edit or refocus and leave the field to schedule another attempt. An uncertain transport outcome stays in the durable outbox and reconnects using the same request identity, rather than sending a second independent notification. A blocked outcome waits for session recovery. Submitted drafts survive reload through that outbox; edits not yet submitted are browser-memory drafts and do not survive a full reload.

A save will not proceed if the session ends, loses notes capability, or changes instance before its deadline. Read-only sessions continue to show saved notes and links.
