# W6 close (T6, pre-merge) — task status report

**Status:** DONE_WITH_CONCERNS (concerns are non-blocking parity gaps + environment-limited
live-proof items + one operational finding; nothing broken, nothing needing a decision to proceed to
the controller merge).

**Worktree:** `webui-w6-surfaces`, branch `w6-surfaces`. **Commit range this task:**
`5c3d8d66e` (pre-task HEAD) → `a6debb8f0` (micro-items) → this report/artifacts commit.

## Item 0 — four micro-items (`a6debb8f0`)

All four comment/demo-level, gated (tsc/vitest/lint/build all green) and committed first:
- `prefs.test.ts` — dropped the stale `COLLAPSED_STORAGE_KEY` citation; the "1"/"0" encoding is now
  stated as this store's own uniform boolean encoding.
- `prefs.ts:44-49` — the landmark comment no longer claims "no CSS keys off them yet" (tokens.css
  now does); landmark span preserved exactly at 44-49 (cited by `display-gates.test.ts:12` +
  `tokens.css:135`).
- `theme.tsx:92` — phone-density help reworded from "Type-scale variant" to "Scales line spacing on
  phones (≤900px)" (the lever scales `--density-scale`/line-height, not type scale). Keeps the
  `/900px/` the copy-pin test asserts.
- `gallery-sections/sheet.tsx` — added the left-side Sheet demo (parity with right/bottom).

## Item 1 — parity sweep (all 250 floor items, code-verified)

159 MET / 78 DIVERGED / **13 GAP (all minor/low, none blocker)**.

| Section | MET | DIVERGED | GAP |
|---|---|---|---|
| §1 Spawn | 59 | 42 | 8 |
| §2 Palette | 57 | 11 | 3 |
| §3 Notifications | 22 | 19 | 0 |
| §4 Display | 21 | 6 | 2 |

Full gap punch list + the consciously-diverged ledger (each with its ruling citation) are in
`docs/superpowers/plans/wave6-report.md`. Headline gaps: §1.14 re-sendable images (no post-spawn form
reset; most consequential), §4.2 no pre-paint FOUC successor (low-med), §4.8 inert `showCost` (low).
The htmx→React/Zustand re-architecture is the root of most divergences; no capability is lost.

## Item 2 — live proof (real hub, real browser, real `openai/gpt-5-nano`, no mocks)

Isolated fake `$HOME` (own `hub.lock`; real host hub untouched), `SERF_HUB_WEB=new`, port 19286.
All six journeys **Pass**, including **THE wave-5-blocked headline**: queue/steer/edit/promote
against a **hub-spawned** session under an active turn (Send→Queue flip, FIFO drain, promote pulls an
entry ahead, edit restores-to-composer-and-dequeues, steer injects mid-turn with a "Steering
injected" marker). Also proven live: full spawn (image thumbnail reached the turn; branch
auto-resolved to `main`), `/theme` immediate flip (hazard #1), search→open by qualified ref
(`/s/local:…`), blocked sentinel, font-size + density CSS cascade, ⌘B cycle + left drawer, all-OFF
notification defaults + title-count/favicon-dot (#e0af68) on a real ask_user needs_you transition +
one-leader Web-Lock election. Evidence: `.superpowers/sdd/w6-close-t6-evidence/` (8 shots).

Not verified live (environment-limited, findings not failures): OS-notification popup (headless
didn't grant permission), sound audibility, reconnect-no-realert (all verified in code + units).

## Item 3 — gates (all green)

`npx tsc --noEmit` EXIT 0 → `npx vitest run` EXIT 0 (**217 files / 3189 tests**, unchanged) →
`npm run lint` EXIT 0 (611 files) → `npm run build` EXIT 0 (`dist/PLACEHOLDER` restored, tree clean).
Go: `go build ./...` EXIT 0; `go test ./cmd/serf-hub/...` ok (all packages).

## Item 4 — wave6-report

`docs/superpowers/plans/wave6-report.md` written (wave5-report shape: full trail, parity sweep,
divergence ledger, live-proof table, decisions-for-Jesse, next-steps, verification).

## Concerns (surfaced, non-blocking)

1. **Operational (merge/M9):** the new SPA serves only under `SERF_HUB_WEB=new`
   (`webnext.go:16`, "legacy UI until the M9 cutover flips it"). Any live demo of the new UI must set
   it. (I initially drove the legacy UI before catching this; re-ran the whole live proof against the
   new UI.)
2. **Parity:** the 13 minor/low gaps above — chiefly §1.14 re-sendable images, §4.2 FOUC, §4.8
   inert `showCost`. W8 fold-in candidates.
3. **Out-of-scope (Wave-5 chrome, not W6):** the work-time clock rendered "495269h" (a likely
   zero-start-time bug in the frozen `StatusRow`); a stale dockview tab was restored from the shared
   browser profile's layout localStorage.

## Not this task (controller-owned)

The serial merge (W6 → integration, then integration re-absorbs main). No merge, no push performed.
