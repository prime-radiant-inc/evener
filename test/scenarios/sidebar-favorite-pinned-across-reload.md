# sidebar-favorite-pinned-across-reload: named pinned session sections survive reloads, reuse hidden empties, and leave project favorites unchanged

**What this covers**: the named-section replacement for session favorites: `POST /api/session-pin`, `DELETE /api/session-pin`, `GET /api/pin-sections`, `PATCH /api/pin-sections/<id>`, `DELETE /api/pin-sections/<id>`, and `/api/tree`'s `pin_sections[]` plus per-row `pin_section_id`. It proves the sidebar is driven by durable server state, not optimistic client state, across raw API mutations and hard reloads.

**Surface**: see `docs/agentic-testing.md`, especially the setup checklist and the reminder that every `eval` must assert the scenario hub's own `location.port`. Use a **fresh Hub state directory** and a **dedicated Chrome profile** for this scenario only. Never reuse Jesse's real hub, real `$HOME`, or any fixed host port from another run.

A session row is addressed by `[data-session-ref="local:<SID>"]`. Section headings are top-level disclosure buttons named by the section title. The row menu exposes **Pin this session…** or **Move pinned session…** depending on `pin_section_id`. The picker lists durable sections from `GET /api/pin-sections`, including hidden empties.

## Pre-state

1. Follow `docs/agentic-testing.md`'s isolated setup exactly:
   - `run=$(mktemp -d -t serf-e2e-XXXXXX)`
   - `make build-web`
   - build fresh `serf-hub` and `serf` binaries into `$run`
   - `export HOME="$run/home"; unset XDG_STATE_HOME`
   - start `"$run/serf-hub" -addr 127.0.0.1:0 -serf "$run/serf" 2>"$run/hub.log" &`
   - derive `PORT` from the hub log, set `HUB=http://127.0.0.1:$PORT`, read `TOKEN`
2. Create a **dedicated Chrome profile** under the same run dir, for example `PROFILE="$run/chrome-profile"`.
3. Prepare at least three top-level local sessions plus one project row:
   - `local:<SID_A>` and `local:<SID_B>`: ordinary ended or idle top-level sessions that can be pinned.
   - `local:<SID_C>`: another top-level session used to reopen the picker after a section becomes hidden.
   - one visible project row whose favorite star/menu behavior can still be toggled.
4. Browser-auth to the test hub with a fresh top-level navigation to `/auth?token=$TOKEN&next=/` using that dedicated profile.
5. Keep this shell helper for authenticated raw API calls:

```bash
api() {
  curl -sS -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' "$@"
}
```

6. Record the expected hub port once for later `eval` checks:

```bash
EXPECTED_PORT="$PORT"
```

## Scenario

Every browser `eval` below must return `location.port` and the check must assert it equals `$EXPECTED_PORT`.

### 1. Create **Client work** via **Pin this session… → New section…**

1. In the browser, open the row menu for `local:<SID_A>`.
2. Choose **Pin this session…**.
3. In the picker, choose **New section…**, enter `Client work`, and confirm.
4. Assert in the browser that:

```javascript
(() => ({
  port: location.port,
  clientHeading: !!document.querySelector('[aria-label="Client work"], button[aria-label="Client work"], button[aria-expanded][name="Client work"]'),
  rowPresent: !!document.querySelector('[data-session-ref="local:<SID_A>"]'),
}))()
```

5. Raw API cross-check:
   - `GET /api/pin-sections` contains one `Client work` section with `member_count: 1`.
   - `GET /api/tree` contains one `pin_sections[]` entry named `Client work`, and the pinned row carries its `pin_section_id`.

### 2. Pin a second session into existing **Client work**

1. In the browser, open the row menu for `local:<SID_B>`.
2. Choose **Pin this session…**.
3. In the picker, choose the existing **Client work** section, not **New section…**.
4. Assert in the browser that both sessions render under **Client work** and return the correct port.
5. Raw API cross-check: `GET /api/pin-sections` now reports `Client work` with `member_count: 2`.

### 3. Create **Research**, hard reload, and verify alphabetical top-level headings plus durable assignments

1. Pin `local:<SID_C>` into a new section named **Research** through **Pin this session… → New section…**.
2. Hard reload with a fresh top-level navigation to `/auth?token=$TOKEN&next=/`.
3. Browser `eval`:

```javascript
(() => {
  const headings = Array.from(document.querySelectorAll('h3,[role="heading"]'), (el) => el.getAttribute('aria-label') || el.textContent || '');
  return {
    port: location.port,
    headings,
    clientRows: Array.from(document.querySelectorAll('[data-session-ref]')).filter((el) =>
      ['local:<SID_A>', 'local:<SID_B>'].includes(el.getAttribute('data-session-ref') || ''),
    ).length,
    researchRow: !!document.querySelector('[data-session-ref="local:<SID_C>"]'),
  };
})()
```

4. Assert the visible top-level order is **Live**, **Client work**, **Research**, **Projects** (with any other standard headings preserving their normal positions around them) and that the hard reload still shows the assignments.
5. Raw API cross-check after reload: `/api/tree` still reports `pin_sections` for **Client work** and **Research**.

### 4. Collapse **Research**, reload, and verify disclosure state stays collapsed

1. In the browser collapse **Research**.
2. Confirm `aria-expanded="false"` on the **Research** disclosure.
3. Hard reload again via `/auth?token=$TOKEN&next=/`.
4. Browser `eval` returns `port`, the **Research** disclosure's `aria-expanded`, and whether `local:<SID_C>` is visible.
5. Assert the port matches, **Research** remains collapsed, and `local:<SID_C>` is hidden until re-expanded.

### 5. Move one session from **Client work** to **Research** via **Move pinned session…**

1. Re-expand **Research** if needed.
2. Open the row menu for `local:<SID_B>`.
3. Choose **Move pinned session…** and select **Research**.
4. Browser assertion: `local:<SID_B>` disappears from **Client work** and appears under **Research**.
5. Raw API cross-check: `/api/pin-sections` shows **Client work** `member_count: 1`, **Research** `member_count: 2`.

### 6. Unpin the last **Client work** member and verify the empty section disappears

1. Open the row menu for `local:<SID_A>`.
2. Choose **Move pinned session… → Unpin**.
3. Assert in the browser that the **Client work** heading disappears entirely.
4. Raw API cross-check:
   - `GET /api/pin-sections` still includes **Client work** with `member_count: 0`.
   - `GET /api/tree` no longer renders **Client work** in `pin_sections[]`.

### 7. Open another session’s picker and verify hidden **Client work** remains selectable

1. Open the row menu for an unpinned top-level session.
2. Choose **Pin this session…**.
3. Assert the picker lists **Client work** even though it is hidden from the sidebar, and return `location.port` from the same `eval`.
4. Cancel out without changing assignments.

### 8. Reuse **Client work**, rename it, and verify disclosure identity/state survives

1. Reopen the picker for that unpinned session and choose the hidden existing **Client work** entry, not **New section…**.
2. Assert the **Client work** section reappears.
3. Collapse or expand it to a known state.
4. Use the section heading overflow menu to rename **Client work** to a new visible name, for example **Client renamed**.
5. Assert after rename that:
   - the section heading shows **Client renamed**;
   - the same session membership remains;
   - the disclosure state survives the rename instead of resetting.
6. Raw API cross-check: `GET /api/pin-sections` shows the same section ID with the new display name.

### 9. Create a dormant remote assignment through the API fixture, verify delete confirmation counts it, then cancel

1. Seed a dormant durable assignment for the renamed client section through a raw API/database fixture appropriate to this scenario's harness, so the section has one durable member that is not currently renderable in `/api/tree`.
2. Open the heading overflow menu for **Client renamed** and choose **Delete**.
3. Assert the confirmation text counts **all durable members**, including the dormant hidden one.
   - Example: if only one visible row is under the section but the dormant seeded assignment makes two durable members, the dialog must say it will unpin `2 sessions`.
4. Cancel the dialog.
5. Assert no delete request was sent and the section remains visible.

### 10. Delete the visible section, confirm all members unpin, hard reload, and verify it stays gone

1. Reopen the delete dialog for **Client renamed**.
2. Confirm deletion.
3. Raw API cross-check immediately after success:
   - `GET /api/pin-sections` no longer lists that section.
   - affected sessions no longer carry its `pin_section_id` in `/api/tree`.
4. Hard reload via `/auth?token=$TOKEN&next=/`.
5. Browser `eval` must confirm the same port and that the deleted section heading is still absent.

### 11. Favorite and unfavorite a project; verify project behavior is unchanged

1. Pick a visible project row.
2. Use the ordinary project favorite control/menu to favorite it.
3. Raw API cross-check that project favorite behavior still uses `/api/favorite` project semantics, not `/api/session-pin`.
4. Browser assertion: the project's favorite state changes as before.
5. Unfavorite it and verify the project row returns to its original state.

### 12. Assert every `eval` targeted this scenario hub's expected port

For every browser `eval` above, assert `location.port === "$EXPECTED_PORT"`. A passing visual assertion from the wrong port is invalid and must fail the scenario.

## Expected

- Session pins are section-based and durable across hard reloads.
- Non-empty named sections render alphabetically between **Live** and **Projects**.
- Empty sections become hidden in the sidebar but remain selectable through the picker.
- Re-entering an existing section name or choosing an existing hidden section reuses the durable section instead of creating a duplicate.
- Renaming preserves disclosure identity/state because disclosure storage is keyed by section ID, not the mutable display name.
- Delete confirmation uses durable `member_count`, not visible rows.
- Deleting a section atomically unpins all of its members and the section stays gone after hard reload.
- Project favorites continue to behave exactly as before.
- Every browser assertion is tied to the correct test hub by the explicit `location.port` check.

## Cleanup

- Delete any seeded dormant assignment fixture if the section delete step did not already remove it.
- Kill the hub by `$(cat "$run/hub.pid")`.
- Remove the whole run dir, including the dedicated Chrome profile.

## Sharp edges

- Use raw API calls plus hard reloads for durability checks; an optimistic overlay is not sufficient evidence.
- Empty sections are intentionally absent from `/api/tree`; only `GET /api/pin-sections` proves they still exist.
- Hidden or dormant durable members must count toward delete confirmation even when the sidebar cannot currently render them.
- The auth cookie is not port-scoped. A dedicated Chrome profile plus explicit `location.port` assertions are mandatory.
- Do not introduce or look for a standalone **Add pinned section** sidebar control; creation is only through **Pin this session…** or **Move pinned session…**.
