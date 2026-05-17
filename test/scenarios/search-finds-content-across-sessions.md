# search-finds-content-across-sessions: ⌘K overlay queries transcripts

**What this covers**: regression baseline for the full-text search
overlay (`cmd/serf-hub/assets/search.js` + server-side
`/api/search`). Validates the overlay opens, queries return
recently-matching transcripts, and clicking through navigates to
the session.

## Pre-state

- Hub running.
- Multiple sessions in the past index whose transcripts contain
  some shared keyword. e.g. "haiku" appears in any session that
  used `claude-haiku-4-5-20251001`.

## Steps

1. From `/` (root or any page), press `⌘K` (Cmd+K on macOS / Ctrl+K
   on Linux). The search overlay should open with focus in the
   `#search-input` field.
   - Programmatic equivalent:
     `document.dispatchEvent(new KeyboardEvent('keydown', {key:'k', metaKey:true, bubbles:true}))`.
2. Type a query likely to match: `haiku`, `OK`, or a phrase from
   a known session.
3. Wait ~1 second for the debounced query to return.
4. Read `#search-results > *` for the rendered result rows. Each
   should show a session title (or "Live" group header) plus a
   working-dir tag and an age stamp (`now`, `1h`, etc.).
5. Click one of the result rows. Confirm URL changes to
   `/s/<session_id>`.

## Expected

- Overlay opens within ~300ms of the key combo.
- Query returns 1+ results for a known-matching string.
- Each row is keyboard-navigable (arrow keys move selection).
- Pressing Enter activates the selected row (navigates to the
  session).
- Pressing Escape closes the overlay; focus returns to the page.
- Falsification: overlay doesn't open, query never resolves, or
  click navigates to wrong session.

## Cleanup

- Close the overlay if open (Escape).

## Sharp edges

- The search backend has a "command palette" prefix mode — typing
  `>` or `:` switches the overlay into command mode. The pill at
  the top of the input shows the active mode. Don't accidentally
  type these as the first character unless testing that mode.
- Search is debounced at ~150-300ms; if the test asserts result
  count immediately after type, it may see an empty state.
- Results include both LIVE sessions (active daemons) and PAST
  sessions (historical transcripts). They render in different
  sections.
- A search for a very common word can return more results than
  the overlay renders (pagination). Look for a "show more" affordance
  or a result count.
