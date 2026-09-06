# Native mobile coverage

Jesse's objective is an intuitive, reliable iOS and Android app with shared code,
multiple Evener hubs, and mobile access to Evener functionality. A working shell
is an iteration milestone. It is not feature completeness. Voice with barge-in
and control of other sessions is outside v1.

## Current evidence and remaining workflows

Reconciled against source at `fabdb86db` on 5 September 2026. The current
[design roadmap](../../design/mobile/README.md) and
[workflow studies](../../design/mobile/workflow-studies.md) govern visual work.
No workflow below is certified release-complete.

The protocol inventory comes from
cmd/evener-hub/frontend/src/protocol/types.gen.ts (MethodTypes). A method's
existence is not UI coverage. Each workflow needs a usable native surface and
behavioral verification, including errors, authorization, and hub isolation.

| Workflow | Native status | Work still required |
| --- | --- | --- |
| Hubs and connections | Named profiles, secure bearer credentials, switch, reconnect; one active foreground connection | Edit profiles, pairing, physical network and auth recovery checks; deliberate cross-hub navigation and draft isolation |
| Browse conversations | First 50 distinct sessions, refresh, native navigation | Pagination, search, archived sessions, project organization, favorites and pin sections |
| Read conversation | Shared canonical projection, older history, expandable activity and grouped internal details; approved first visual slice; native Markdown/code installed with initial copy and dark-mode evidence | Complete rich-text interaction/accessibility validation, usable attachment viewing, long-transcript performance, reading-position restoration and accessible disclosure |
| Compose | Text send/steer/queue/stop gated by capabilities; durable per-hub/session drafts and uncertain-send recovery | Attachments, command selection, queue inspection/cancel/promotion/drain; complete both-platform running-turn E2E |
| Create session | Native project/harness/model/reasoning/prompt flow; real isolated Evener creation manually exercised on both standalone platforms | Large catalogs, directory assistance, actionable validation, physical keyboard/accessibility coverage and final-head regression checks |
| Manage session | Not exposed | Rename, fork, resume, clear, compact, shutdown; honor capabilities and identity |
| Model and launch settings | Model/reasoning selection during creation only | Model, vision model, reasoning, launch layers, schema validation and repository trust |
| Goals and work | Not exposed | Goals, tasks, background jobs/output, subagent previews and navigation |
| Approvals and questions | Question text/options rendered with instruction to reply through composer; no dedicated decision controls | Sandbox escalation resolution, structured question interaction, stale/resolved decisions and any other user decision surfaces |
| Hub navigation | Not exposed | Favorite/archive, project/session removal, pin assign/unpin, section rename/delete |
| Provider authentication | Not exposed | Status, API keys, login/logout, device authorization, auth tests |
| Instances | Not exposed | List/create/edit/remove/default |
| Plugins and marketplaces | Not exposed | Browse/preview/install/upgrade/enable/disable/remove, source management, auto-upgrade |
| Hub preferences | Not exposed | Overview, transcript display settings, upgrade |
| Native distribution | Standalone simulator/emulator release builds and installation recorded; iOS credential persistence checked | Distribution signing, physical devices, final-head both-platform regression and performance evidence |
| Design quality | Approved research, philosophy, style guide, lookbook and screen studies; first visual slice installed on both platforms | Further native interaction refinement, rich rendering, accessible visual system and measured fluency |

## Verification boundaries

- The first slice was manually exercised in Expo Go on iOS26.5 and Android35.
  Production checks read the local hub. Scripted playground mutations prove the
  native network/UI path, not real Evener daemon or provider execution.
- Fresh verification on source `fabdb86db`: `npm test` in `mobile-native` passed
  24 tests across five files; `npm run check` passed. Tests exercise profile
  storage with a storage double, the shared handshake, draft recovery, creation
  state, timeline grouping, and shared store/service integration over a loopback
  socket. They do not establish SecureStore behavior on devices or native UI E2E.
- Standalone runtime notes in
  `.superpowers/sdd/2026-09-05-native-session-creation/standalone-runtime.md`
  record real isolated hub + scripted provider creation on iOS and Android.
  iOS send/steer/queue/stop manual evidence predates final hydration-race fixes;
  Android basic send/steer/queue/stop were subsequently exercised on the installed
  release APK; see [artifact identity and observations](../../design/mobile/android-runtime-evidence.md).
  Complete final-head and lifecycle coverage remains outstanding. These are
  historical observations, not freshly repeated final-head manual results.
- Current source anchors: `mobile-native/src/screens.tsx` (four-route shell,
  first-page roster, composer), `ConnectionProvider.tsx` (one foreground client),
  `newSession.ts` (creation state), and `TimelineItem.tsx` / `MarkdownResponse.tsx` (native rich-text rendering).
  The shared `mobile/src/services/attachments.ts` explicitly remains a pure
  validator with no native file picker. Its existence is not attachment support.
- Production session listing can take about 28 seconds and sometimes exceed the
  existing 30-second request limit. Reducing page size did not resolve this. A
  responsive app needs the server path corrected before claiming performance
  readiness. A subsequent [read-only investigation](../../design/mobile/roster-performance.md)
  reproduced 12–14 second reads and sampled historical delegate-log loading
  inside the running hub’s roster path. This worktree already avoids that path;
  production deployment and same-dataset verification remain outstanding.
- Release readiness requires standalone builds, deterministic tests through real
  Evener plumbing with a scripted provider boundary, and manual native E2E of the
  completed workflows. Include Android back behavior, iOS gestures, keyboard,
  rotation, font scaling, screen readers, background/foreground, failed requests,
  reconnect and switching hubs with overlapping session identifiers.

Update this inventory with exact evidence as iterations land. Do not mark a
workflow complete merely because an RPC wrapper or generic settings form exists.

## Draft continuity update — 5 September 2026

Source `b4d7060cf` adds SQLite drafts keyed by hub and session, checkpointed uncertain submissions, explicit recovery, save/load errors and hub-removal cleanup. Native 44 tests, TypeScript, targeted Biome and both release builds pass. [Runtime evidence](../../design/mobile/draft-persistence-evidence.md) covers cold restart on both platforms, Android hub isolation, acknowledgement withholding with zero automatic replay, and recovery that preserves newer text. This does not complete composition: attachments, commands, queue management and representative-device performance remain outstanding.

## Reading validation update — 5 September 2026

Source `2d73a9000` has both-platform release Markdown builds and 48 passing native tests. [Reading evidence](../../design/mobile/markdown-evidence.md) records exact code and source-link copying, Android table overflow navigation, dark appearance and large-text observations. Acceptance remains open: live iOS font changes clip action labels until remount, Android font changes recreate the activity without route restoration, and complete native accessibility/streaming checks remain outstanding. These lifecycle defects take precedence over declaring the reading surface polished.

The subsequent shared-text correction makes iOS Action/Copy/ErrorMessage measurement respond to live font-scale changes, retaining Android native nonlinear scaling. Both builds pass; final iOS manual evidence shows untruncated status and composer actions after a live change. Android route restoration and complete app-wide accessibility acceptance remain open.
