# attention-needs-you-end-to-end: awaiting state drives sidebar, NeedsYou tier, and tab title live

**What this covers**: the attention & status model
(`docs/superpowers/specs/2026-07-03-attention-status-model-design.md`).
Before this work, `agent.SessionState` collapsed "waiting for your
reply" into plain `idle`, so the wire `awaiting` state was
unreachable, the NeedsYou tier never lit up, and notifications keyed
on impossible transitions behind a 5s poll with channels off by
default. This scenario is the single-tab happy path named in the
spec's Testing section, plus the interrupt guard-rail. (The goal-loop
variant is intentionally NOT re-proven here — see the note at the
end.)

Driver: superpowers-chrome:browsing + curl REST shim (spawn/interrupt),
per `docs/agentic-testing.md`.

## Pre-state

- Fresh binaries built from this branch:
  ```bash
  go build -o /tmp/serf-hub-attn ./cmd/serf-hub
  go build -o /tmp/serf-attn ./cmd/serf
  ```
- Hub started against a **scratch HOME** so it mints its own
  auth-token/state dir and does not touch the operator's real
  `~/.serf`:
  ```bash
  export HOME=$(mktemp -d -t serf-e2e-attn-home-XXXXX)
  mkdir -p "$HOME/.serf"
  # Provider auth is env-based since a scratch HOME has no
  # credentials.toml / stored OAuth — inherit the real key:
  /tmp/serf-hub-attn -addr 127.0.0.1:9181 -serf /tmp/serf-attn &
  sleep 2
  TOKEN=$(cat "$HOME/.serf/auth-token")
  HUB=http://127.0.0.1:9181
  ```
  (Port `9181` to avoid colliding with a dev hub on the default
  `9180`.)
- `ANTHROPIC_API_KEY` set in the environment (cheap model is
  `anthropic/claude-haiku-4-5-20251001`, this repo's standard
  cheap-model convention).
- Chrome available via `superpowers-chrome:browsing`. **Clear
  `localStorage` for the hub origin first** (see Sharp edges — a
  browser profile that visited an older build may already have the
  notifications-prefs migration applied with title/favicon off,
  which would falsify assertion 3 for reasons unrelated to this
  branch).

## Steps

### A. Happy path — reply lands, thread needs you, thread clears

1. **Spawn** via the browser `/new` form (not the REST shim — the
   form is part of what's under test):
   ```
   navigate http://127.0.0.1:9181/auth?token=<TOKEN>&next=/new
   ```
   ```javascript
   document.querySelector("textarea[name=prompt]").value =
     "Reply with exactly the word PONG.";
   document.querySelector("textarea[name=prompt]")
     .dispatchEvent(new Event("input", {bubbles:true}));
   document.querySelector('button[data-chip="model"]').click();
   ```
   Pick the cheap model the same way `spawn-picker-enter-noop.md`
   does — type a unique substring into the picker's search input,
   then Enter selects it (does not submit the form):
   ```
   type haiku
   ```
   ```javascript
   // press Enter in the focused .chip-picker-search input, then confirm:
   document.querySelector('input[name=model]').value
   // expect "anthropic/claude-haiku-4-5-20251001"
   ```
   Submit (click the spawn button, or `⌘↵`). Confirm the URL becomes
   `/s/<SID>`; record `SID` from the URL.

2. **Wait for the turn to settle to `awaiting`**, polling the daemon
   state directly (bounds the "within 10s of the reply landing"
   requirement without depending on DOM timing):
   ```bash
   SID=<from step 1>
   for i in $(seq 1 12); do
     state=$(curl -s -H "Authorization: Bearer $TOKEN" \
       "$HUB/api/sessions/local:$SID" | jq -r '.state // ""')
     [ "$state" = "awaiting" ] && break
     sleep 1
   done
   echo "state=$state"   # expect: awaiting, within ~10s
   ```

3. **Without opening another tab or refreshing**, assert all three
   surfaces in the still-open `/s/<SID>` tab:
   ```javascript
   ({
     rows: [...document.querySelectorAll(`a.sb-row[href="/s/${SID}"]`)]
       .map(el => el.getAttribute("data-state")),
     needsYouTier: !!document.querySelector('section[data-tier="needs-you"] a[href="/s/' + SID + '"]'),
     title: document.title,
   })
   ```
   Expected:
   - `rows` — every matching row (the plain per-project row and, if
     present, the NeedsYou-tier row — see Sharp edges on why there
     can be two) reports `"awaiting"`.
   - `needsYouTier` — `true`.
   - `title` — starts with `"(1) "` (notifications.js's title-count
     format is a prefix, not a suffix: `"(" + count + ") " + base`).

4. **Reply** in the open thread (composer, not REST — the assertion
   is about the UI's own-tab-instant path):
   ```javascript
   const ta = document.querySelector("textarea.message-input");
   ta.focus(); ta.value = "thanks — nothing else.";
   ta.dispatchEvent(new Event("input", {bubbles:true}));
   document.querySelector(".send-btn").click();
   ```
   Immediately after the next status event for this thread (own-tab
   path — should be near-instant, well under a second), re-check:
   ```javascript
   !!document.querySelector('section[data-tier="needs-you"] a[href="/s/' + SID + '"]')
   // expect false
   document.title
   // expect no leading "(N) " — back to the bare title
   ```
   If the title/tier haven't cleared yet, retry for up to **6s**
   (the attention watcher's broadcast tick is every 5s; this is
   headroom above that for the non-owning-tab reconcile path — see
   Sharp edges on why the own-tab path should be much faster than
   this ceiling).

### B. Interrupt variant — never shows awaiting

5. **Spawn** a second session with a long-running prompt (REST shim
   is fine here — this variant isn't testing the spawn form). Fresh
   hermetic workdir, per `docs/agentic-testing.md`:
   ```bash
   tmpdir=$(mktemp -d -t serf-e2e-attn-interrupt-XXXXX)
   resp=$(curl -s -X POST -H "Content-Type: application/json" \
     -H "Authorization: Bearer $TOKEN" \
     -d "{
       \"prompt\":\"Run \\\"sleep 60\\\" via the shell tool, then reply DONE.\",
       \"model\":\"anthropic/claude-haiku-4-5-20251001\",
       \"working_dir\":\"$tmpdir\",
       \"harness\":\"serf\",
       \"branch\":\"\",
       \"access_mode\":\"full\",
       \"agent\":\"default\",
       \"launch_overrides\":{}
     }" \
     "$HUB/api/spawn")
   SID2=$(echo "$resp" | jq -r '.session_id')
   # wait for state=active (turn actually started, sleep in flight)
   for i in $(seq 1 15); do
     state=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID2" | jq -r '.state // ""')
     [ "$state" = "active" ] && break
     sleep 1
   done
   ```
6. **Interrupt mid-run**:
   ```bash
   turn=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID2" | jq -r '.active_turn_id // ""')
   curl -s -X POST -H "Content-Type: application/json" \
     -H "Authorization: Bearer $TOKEN" \
     -d "{\"turn_id\":\"$turn\"}" "$HUB/s/$SID2/interrupt"
   ```
7. **Poll for settle** and assert the state is never `awaiting` at
   any point during or after the interrupt:
   ```bash
   seen_awaiting=0
   for i in $(seq 1 15); do
     state=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID2" | jq -r '.state // ""')
     [ "$state" = "awaiting" ] && seen_awaiting=1
     [ "$state" = "idle" ] && break
     sleep 1
   done
   echo "final=$state seen_awaiting=$seen_awaiting"   # expect: idle, 0
   ```
   Cross-check the sidebar DOM agrees (no reload needed — the
   project rebuild/attention watcher should have already pushed the
   idle state by the time step 7's polling loop exits):
   ```javascript
   document.querySelector(`a.sb-row[href="/s/${SID2}"]`)?.getAttribute("data-state")
   // expect "idle" (or the row may be gone if SID2's tab was never opened —
   // then check the project listing instead of asserting on a specific tab)
   ```

### C. Goal variant — not re-proven here

The spec's Testing section places the goal-loop case in integration,
not e2e, because a real goal-loop run adds LLM-turn-count flakiness
without adding coverage the unit tests don't already have. That
coverage lives in `agent/session_awaiting_test.go`
(`TestSettleTerminalState`) and `agent/session_goal_fix_test.go`
(`TestSettleGoalOnIdleKicksWindowGoal`,
`TestSettleGoalOnIdleNoKickWhenTerminal`) — assert those pass instead
of driving a live goal session here.

## Expected

- Step 3: all three channels (sidebar row(s), NeedsYou tier, tab
  title) agree the session needs you, within 10s of the reply
  landing, with no second tab and no refresh.
- Step 4: the owning tab's tier membership and title both clear;
  worst case within 6s if something delays the own-tab path down to
  the broadcast cadence.
- Step 7: `seen_awaiting` is always `0` — an interrupted turn must
  never pass through `awaiting` on its way to `idle`.

Falsification:

- Step 3 shows `awaiting` in only one of the three channels (e.g.
  tier lit but title didn't change) — a channel is wired to a
  different/stale source of truth.
- Step 4's tier/title only clear after the full ~6s window every
  time — the own-tab instant path (`thread/status/changed`) regressed
  to the broadcast-only path.
- Step 7's `seen_awaiting=1` — the drain-settle upgrade fired on an
  interrupted turn (the exact regression the design's "boundary
  function stays untouched" guarantee exists to prevent).

## Cleanup

```bash
curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" -d '{}' "$HUB/s/$SID/shutdown" >/dev/null 2>&1
curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" -d '{}' "$HUB/s/$SID2/shutdown" >/dev/null 2>&1
pkill -f "serf-hub-attn -addr 127.0.0.1:9181"
rm -rf "$HOME" "$tmpdir"
```

## Sharp edges

- **Two DOM rows, one session.** A session in the NeedsYou tier
  still renders in its normal per-project group too (`sidebar.html`
  renders the tier as a separate `{{range .NeedsYou}}` block, not a
  substitute for the project listing) — both carry `data-state`, and
  they can never disagree within one render since both come from the
  same tree build, but a selector that assumes exactly one
  `a.sb-row[href=...]` will only see the first (the tier row, since
  it's emitted earlier in the template) unless you use
  `querySelectorAll`.
- **Stale localStorage defeats assertion 3, not the feature.** The
  title/favicon-on-by-default migration
  (`cmd/serf-hub/assets/notifications.js`) is versioned and
  one-shot: a browser profile that already has a pre-migration
  `serf-hub.notifications` blob gets its missing keys backfilled to
  `false`, not the new `true` default (round-4 A4's explicit
  intent — never silently flip a preference a returning user didn't
  choose). A profile that has never opened this hub build gets the
  new defaults outright. Clear `localStorage` (or use a throwaway
  Chrome profile) before running, or the title assertion will fail
  for a reason that has nothing to do with a regression.
- **5s is the broadcast floor, not the own-tab path.** The attention
  watcher ticks every 5s (`cmd/serf-hub/main.go`, `NewAttentionWatcher`);
  the 6s figure in steps 4 and the Expected section is that plus
  slack, and it is the ceiling for *other* tabs/observers reconciling
  from `serf/attention/changed`. The tab with the thread open should
  clear on the very next `thread/status/changed` for its own thread —
  if you find yourself needing the full 6s in the owning tab, that's
  itself worth a kata, not just a slower retry loop.
- **Model picker options carry no stable per-option selector** —
  `.chip-picker-option` rows are plain `textContent`, matched by
  typing into `.chip-picker-search`, not clicked by attribute.
  `spawn-picker-enter-noop.md` is the canonical reference for this
  interaction (and documents that Enter can jump active providers if
  your substring matches more than one — pick a substring, like
  `haiku`, that is unique across configured providers).
- The interrupt variant's daemon-side contract (interrupted turns
  hard-code `idle`, never upgrade to `awaiting`) is also covered by
  Go tests; this scenario's REST-shim variant is the cross-surface
  confirmation (daemon state feeds the same sidebar row the happy
  path checks), not the only proof.
