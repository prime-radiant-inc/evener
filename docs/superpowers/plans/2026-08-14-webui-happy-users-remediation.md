# WebUI "happy users" remediation plan

Goal: close every issue flagged during the Beautiful UI re-theme and the two
UX evaluation rounds that remains open, ending with a live-verified core
loop. Executed subagent-driven; each task lists its owner scope, done
criteria, and dependencies.

Explicitly OUT of scope (flagged, deliberately deferred): Rail.module.css
decomposition (code health, not user-facing), Beautiful UI follow-up
suggestion chips (needs a daemon wire field serf doesn't have), InsightCard/
ContextCard/DiffTable app adoption (no surface wants them yet).

## T1 — fresh hub + live wire (sequential, first)

Build the current binary (`make build-runtime`), restart the local hub on
it, confirm the new frontend connects live (WS established, transcript
hydrates, rail updates). Hub restart is safe for running sessions: daemons
are independent processes the hub re-discovers via rendezvous files
(README §daemons). Done when: a real session's transcript renders live in
the new UI at 9180 and through the dev proxy.

## T2 — live E2E scenario pass (after T1)

Scenario cards with falsifiable assertions, run hands-on in the browser
against the live hub:
1. Spawn a real session on a scratch repo with a prompt that forces an
   ask ("ask me one clarifying question before doing anything").
2. Watch streaming (transcript follow, Cadence, tab dot working-state).
3. Answer the ask entirely by keyboard (Mod+I, radio arrows, Mod+Enter).
4. Steer mid-run (Shift+Enter path), quote a passage (select + Mod+'),
   use inline / completion.
5. Two sessions needing attention → Mod+J cycles; drawer badge at phone
   width; answer an ask at 390px.
Findings triaged into fixes the same day. Done when: every card passes or
its failure is fixed and re-run.

## T3 — actionable daemon-down guidance (frontend)

The first-run worst moment: spawn/model-list failing because no daemon is
reachable now says "Can't reach the hub right now." even when the HUB is
fine and the DAEMON is missing. Distinguish the two in
`friendlyErrorMessage` call sites that know context (spawn, model picker):
when the hub connection is up but the call fails with the daemon-missing
family, say what to do: "No agent daemon is running for this project —
start one with `serf` in the repo, or pick a live project." Done when:
spawn failure with a live hub and no daemon shows actionable copy (unit +
live check in T2).

## T4 — blocking escalations count as needs-you (Go)

`hubcore.DeriveAttention`/tree state derivation doesn't treat a pending
sandbox escalation as "a human is needed", so escalations are invisible to
the rail/badges/Mod+J. Investigate the derivation first; if a session with
a pending escalation can be surfaced as needs-you state with a contained
change (derivation + tests, no wire-format break), do it. If it demands a
protocol/schema change, STOP and write up the design instead. Done when:
either the state flows to `tree.needs_you` (Go tests prove it) or a
written design explains the larger change.

## T5 — title-count notification defaults ON (frontend)

All notification channels default OFF, so the attention system is
invisible to anyone who never opens prefs. Flip the title-count channel's
default to ON (quiet, reversible; favicon/sound stay OFF), update the
prefs tests that pin the all-OFF floor, and note the decision in
docs/web-ui/decisions.md. Done when: a fresh profile shows "(n)" in the
title with needs-you sessions and the pref toggles it off.

## T6 — welcome pane teaches the chords (frontend)

First-run discoverability: the welcome pane's empty state gains a quiet
shortcut hint row (KeyHint): Mod+K palette, Mod+I composer, Mod+J
needs-you, and "? in the palette shows all shortcuts". Done when: rendered
on welcome, gallery/tests updated.

## T7 — phone transcript legibility (frontend)

Measured 13px message body at 390px. At ≤900px, transcript message BODY
text moves to var(--font-size-body) (14px); chrome/captions stay as they
are. Done when: css asserts the phone block and desktop is unchanged.

## T8 — GoalControl popover clipping (after T1, frontend)

Long-flagged suspicion: the goal popover renders inline under an
overflow:hidden ancestor and may clip. Verify in the live app; if real,
fix by portaling like widgets/popover (or anchoring within the unclipped
region); if not reproducible, delete the stale memory note. Done when:
verified either way, fix landed if needed.

## T9 — cleanup: shared --speaker-gap (frontend)

The off-grid `--speaker-gap: 10px` is declared independently in
session.module.css and turnblock.module.css. Hoist to one declaration on a
shared ancestor (keeping 10px — no visual change). Done when: one
declaration, both consumers resolve, tests pass.

## T10 — final verification + persona re-run + push

Full suite/lint/tsc/build; desktop + phone persona re-pass focused on the
remediated areas; opus review over the batch; push; report.

## Execution order

T1 → {T2, T8} live-dependent; {T3, T5, T6, T7, T9} parallel frontend
workers (disjoint files); T4 solo Go worker with investigate-first
mandate; T10 last.
