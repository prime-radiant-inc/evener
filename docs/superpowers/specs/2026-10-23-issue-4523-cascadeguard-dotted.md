# Issue #4523: real-browser coverage of a dotted ancestor spine

Status: frozen for independent PAR review. Lane `issue-4523-cascadeguard-dotted`, based on `a261c22565` plus the frontend production build.

## Missing invariant

`assertSpineStatus` in `cmd/evener-hub/frontend/scripts/cascadeguard/run.mjs` already knows three runtime states and their glyphs:

| Runtime state | Hover and screen-reader text | Glyph `data-state` |
| --- | --- | --- |
| `active` | `Active` | `working` |
| `awaiting` | `Awaiting` | `needs-you` |
| `idle` | `Idle` | none |

Every call site passes `"idle"`. In an idle runtime state `ScopeRuntimeState`'s compact branch renders no `StatusDot`, so the guard never executes, in a real browser:

- the glyph family assertion (`data-state` equals the shared session-list signal),
- the decorative assertion (`aria-hidden="true"`, no `role="img"`, no second accessible name),
- the reduced-motion stillness assertion for the working spinner,
- the real accessibility-tree read (`Accessibility.queryAXTree`) over a subtree that actually contains a glyph.

The accessibility-tree assertion is the strongest evidence for the #3856 contract ("assistive tech hears exactly one label while the glyph is present"), and today it only proves the glyph-free case. The redesign in #4283 removed the origin column from the rendered track, so the fixture's only busy conversation (`CASCADE_BUSY_INPUT` at role 0) can never become a rendered spine. The remaining chain delegates are all settled idle.

Invariant to restore: a real browser drills a rendered, non-origin ancestor into a spine while that ancestor is `active`, asserts its status indicator and exactly one accessible name from the real accessibility tree, then releases the provider call and asserts the resting `idle` state. The existing idle coverage stays.

## Fixture change (`cmd/evener-hub/cascade_browser_test.go`)

Scope: this file's provider script and control-record consumer only. No product code changes.

The browser cannot start the ancestor's turn. A cascade column mounts the live
composer, but the delegate session is retained by its owner; a `turn/start` for
refs[1] is refused with "session is retained by \<owner\>; open the owning
session or refresh after it stops" (`mutationOutcomeUnknown`, blocked). The
first red run against a composer-driven design proved this refusal. The
ancestor is therefore busy from fixture creation: hold the delegate's own final
provider round.

1. The provider script holds role 1's final round: its phase-1 round, which
   arrives after the delegate tool returned (delegation is asynchronous). It
   writes `provider-ancestor-held.json` (`{"role":1,"phase":1}`) and answers
   the held round from a goroutine after a new `releaseAncestor` channel
   closes, so the serial provider loop stays free to finish refs[2..6]:

   ```go
   phase := phases[role]
   phases[role]++
   if role == 1 && phase == 1 {
       write provider-ancestor-held.json: {"role":1,"phase":1}
       toolOwners[call.ToolCallID()] = role
       ancestorHeld = true
       go func(call *fakellm.Call) {
           select {
           case <-releaseAncestor:
               call.RespondToolCall("communicate", communicateArgs("CASCADE_ANCESTOR_BUSY_REPLY"))
           case <-ctx.Done():
           }
       }(call)
       continue
   }
   ```

   The held goroutine only answers the call, so it never touches the loop's
   `toolOwners`, `phases` or `completed` maps. Readiness moves to one
   `sync.Once` signal (`signalReady`) and accounts for the held role: when
   `ancestorHeld` is set, the completed-count target drops by one (57 instead
   of 58). The fixture therefore builds the chain and starts the driver after
   every other role has finished, while refs[1] is still active. The root's
   own turn may already have ended (it never renders); that is fine.

   Policy decision: hold the original round rather than inventing a new input
   token or a second turn. This is the only way the production cascade can
   expose a dotted non-origin ancestor without product changes.

   The existing `CASCADE_BUSY_INPUT` role-0 branch and token accounting are
   unchanged.
3. `cascadeAwaitProviderRelease` keeps reading complete newline-terminated records from `control.jsonl`, but accepts both `release-provider` and `release-ancestor-provider`. Keep a handled set across reads so a re-read of the same complete line never closes a channel twice. Each command closes its own channel exactly once; the loop returns only after every known command has been handled. An unknown command is still an error. A record batch may carry both commands. The function signature passes both channels (a command-to-channel map is acceptable).
4. Delivery accounting is unchanged: no new token, so every existing expected
   count stays the same.
5. Milestone enforcement: add `dotted-ancestor-status` to the required milestone list (the loop at `cascade_browser_test.go` that reads `milestones.jsonl`). Without this entry a future driver could skip the active leg and the test would still pass.

## Why the hold uses the original round

The scripted provider serves one call at a time: the goroutine loops on
`provider.Next` and blocks inside a held branch until its release channel
closes. Holding the origin's `CASCADE_BUSY_INPUT` call and then trying to
observe a delegate's call cannot work; the delegate's call is queued unread.
Role 1's final round is the last chain call, so holding it blocks nothing the
guard needs. It is released early in the main drill sequence, long before
`sourceMutationJourney` arms the origin hold, so the two holds never overlap.
The two control records still exercise the multi-command consumer:
`release-ancestor-provider` arrives first, the watcher stays alive, and
`release-provider` arrives later.

## Guard sequence (`cmd/evener-hub/frontend/scripts/cascadeguard/run.mjs`)

`sourceMutationJourney` and every existing `assertSpineStatus` call stay
unchanged. A new `dottedAncestorJourney` runs immediately after the source
pane's identity is captured and before the source-reading setup, so the live
delegate row cannot remount the source reader during its reading checks.

1. `dottedAncestorJourney` clicks the source Agents control and drills
   `edges[0]`, `edges[1]`, `edges[2]` to level 3. The rendered track is
   `refs[1..3]`: `refs[1]` is a 52px spine, `refs[2]` and `refs[3]` are
   readable columns.
2. `dottedAncestorStatus(refs[1])` runs:
3. `assertSpineStatus(refs[1], "active")`:
   - runtime `data-runtime-state="active"`, hover `title="Active"`, label text `Active`;
   - compact box at most 8.5x8.5, label paint at most 1x1;
   - glyph exists with `data-state="working"`, `aria-hidden="true"`, no `role="img"`;
   - real accessibility tree for the status subtree yields only `["Active"]` (non-ignored nodes with names), at least one name.
4. Set `prefers-reduced-motion: reduce`, assert `matchMedia("(prefers-reduced-motion: reduce)").matches === true`, then assert `active` again; the helper additionally requires the working glyph's `animation-name` to be `none`. The explicit media assertion is what stops a skipped override from silently skipping the stillness check.
5. Clear the media override and capture `dotted-ancestor-status` while the
   spine is active. The existing idle `compact-spine-status` capture in
   `sourceMutationJourney` is untouched and keeps its name.
6. Install `observeGeometryAndFocus(refs[1])`, then append
   `release-ancestor-provider` to `control.jsonl`.
7. Wait for a real `thread/status/changed` frame for `refs[1]` with `status.type === "idle"`, then `assertSpineStatus(refs[1], "idle")`: text `Idle`, no glyph, real tree only `["Idle"]`. Assert focus and geometry are unchanged, then disconnect the observer.
8. Emit the required milestone `dotted-ancestor-status` with the ancestor ref, the
   states returned by the helper (`active`, `reducedMotionActive`, `idle`), the
   observed glyph and the number of allowed diagnostics. The Go test parses
   that detail and requires `active == "active"`,
   `reducedMotionActive == "active"` and `idle == "idle"`, so the milestone
   cannot be satisfied by a literal or a skipped leg.
9. `dottedAncestorJourney` pins the source DOM, runs the full Return proof and
   returns to the source. A plain close and re-entry is not enough: the
   ancestor's shared thread subscription can still carry the released round's
   stale `active` metadata, and the re-acquired store reuses it, so the
   six-edge idle assertion would fail even though the live status is idle.
   This product defect is filed as #4878. The driver reloads the page after
   Return (waiting for the reload's own load event before re-opening, so
   `openSession` cannot return against the outgoing document), which makes
   every subscription re-read the hub's current status. It then re-opens the
   source and asserts the reload restored the saved source pane identity and
   tab count (it does not silently accept replacements) before recapturing
   them. The baseline source-reading setup and main journey then run unchanged
   with refs[1] idle.

## Exact accessibility assertions

For both states the guard reads Chrome's real tree, not DOM attributes:

```js
const axNodes = await queryAccessibilityTree(driver.send, `${scope(ref)} [data-runtime-state]`);
const axNames = (axNodes ?? []).filter((node) => !node.ignored && node.name?.value).map((node) => node.name.value);
assert.ok(axNames.length > 0, "the compact status exposes an accessible label in the real tree");
assert.ok(axNames.every((name) => name === text), ...);
```

Chrome's accessibility tree can trail a status transition by a frame, so the
guard polls this read until the names agree with the rendered label, bounded
at 100 attempts x 50 ms. The bound is a tripwire; a genuinely competing name
never converges and still fails the `every` assertion.

With `active`, `text` is `Active` and the glyph must be decorative; a glyph that
keeps its standalone name contributes `Working`, making `every` fail.

The mutation proof must reach the accessibility assertion, not an earlier DOM
check: with `decorative` removed, `exposedImage` and `glyphHidden` fail first.
So the temporary mutation run does both of these, then reverts both:

1. Remove `decorative` from the spine's `StatusDot` in
   `ActivityPeek.tsx` and rebuild the frontend.
2. Temporarily move the accessibility-tree block in `assertSpineStatus` ahead
   of the DOM `exposedImage`/`glyphHidden` assertions, so the real tree is the
   oracle under test.

The observed failure must be the accessibility message with a competing name
in `got`, not the decorative DOM message; the actual mutation run produced
`got ["Working","Active","Active"]`. Revert both, rebuild, and rerun green.
The final diff changes no product code and keeps the DOM checks in their
existing order.

Existing assertions are neither removed nor relaxed. The prior idle checks at the six-edge geometry leg remain; the idle check in this leg remains after release.

One diagnostic is allowed by exact identity and window. While refs[1] is active
as a readable column, its live composer provokes kata 5gdv's warning
("composer is showing a working session with no Stop") for that exact ref. The
allowance is armed only for the early dotted journey and disarmed after the
release; it requires the marker and refs[1] and records the sighting in
`knownDiagnostics`, written to `known-diagnostics.json` and counted in the
milestone. Every other warning and every error still fails the run. The
unrelated product finding is filed as #4877, and this allowance is removed
once it is settled.

## Non-goals

- No product, widget, CSS, or accessibility behavior change; no new label, prop, or widget.
- No new provider hold for `awaiting` or `failed`; `active` is the dotted state this issue names.
- No change to the origin hold's role-0 contract, the `CASCADE_BUSY_INPUT` delivery count, or the source transcript checks.
- No timeout widening, no full browser suite, no `make test-web-browser` locally; CI owns the full lane.
- Unrelated findings become separate GitHub issues, not this PR.

## Verification

1. Red-first: with only the driver leg added, run

   ```sh
   BROWSER_GUARD_VITE_CACHE_DIR="$EVENER_SCRATCH_DIR/vite-cache" \
     go test -tags browserguard ./cmd/evener-hub -run '^TestAgentCascadeBrowser$' -count=1 -v
   ```

   and record the failure against the unchanged fixture: the level-3
   `assertSpineStatus(refs[1], "active")` times out because the fixture leaves
   refs[1] idle. The failure DOM is also kept: it contains one
   `[data-testid="cascade-spine"][data-scope-ref=refs[1]]` plus the refs[2] and
   refs[3] columns, all three with `data-runtime-state="idle"`. This proves the
   guard reaches a rendered non-origin spine and demands a dotted state; the
   green run supplies it and exercises the glyph and accessibility assertions,
   and the mutation run proves the accessibility-tree oracle.
2. After the fixture change, the same command exits zero and the run emits `dotted-ancestor-status`; run it twice to show stability.
3. Temporary competing-name mutation proves the AX assertion fails when the glyph is not decorative; revert it and rerun green.
4. `make build` for the Go fixture change; `npm run typecheck` from `cmd/evener-hub/frontend` if any TypeScript changes (none expected).
5. Record the exact commands, outputs, evidence paths, and any unverified limits in the PR and final report.
