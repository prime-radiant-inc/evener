# Restore the last mobile location

Jesse delegated routine decisions and approved iterative native development. Persist a small navigation bookmark in local SQLite: selected saved-hub ID, and optionally the conversation reference/title. Restore only after saved profiles load, and only for a still-saved hub. Reconstruct a fresh native stack (Hubs, Sessions, optional Conversation); never persist live clients, navigator internals, credentials, transcript data, or pending actions. New-session forms reopen at Sessions until form-draft persistence is implemented. Returning to Hubs clears the bookmark.

Use the existing conversation open/reconnect path so restoration fetches current server state and never sends or replays input. Keep SQLite draft recovery independent. Malformed/obsolete bookmarks fall back to Hubs. Storage failures show a concise local-restoration notice without blocking ordinary navigation. Persist after navigation and selected-profile state agree.

- [ ] Behavioral tests for restoring valid locations, removed hubs, malformed state, safe stack construction and storage round trips.
- [ ] Integrate startup profile selection and navigation persistence.
- [ ] Rebuild both platforms, exercise Android font-change recreation and both-platform cold starts, verify back navigation and hub isolation.
- [ ] Independent review, exact evidence and remaining limitations.
