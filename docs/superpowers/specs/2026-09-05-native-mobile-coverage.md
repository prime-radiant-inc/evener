# Native mobile coverage

Jesse's objective is an intuitive, reliable iOS and Android app with shared code,
multiple Evener hubs, and mobile access to Evener functionality. A working shell
is an iteration milestone. It is not feature completeness. Voice with barge-in
and control of other sessions is outside v1.

## Current evidence and remaining workflows

Initial inventory at `fabdb86db`, updated through the queue and session-control increments on 5 September 2026. The current
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
| Browse conversations | First 50 distinct sessions, server-side search, paged project catalogs and current/recent/archived project sessions | Full navigation invalidation, real subagent lifecycle acceptance, favorites, pin sections and remaining paging acceptance |
| Read conversation | Shared canonical projection, older history, expandable activity and grouped internal details; approved first visual slice; native Markdown/code installed with initial copy and dark-mode evidence | Complete rich-text interaction/accessibility validation, usable attachment viewing, long-transcript performance, reading-position restoration and accessible disclosure |
| Compose | Text send/steer/queue/stop, durable per-hub/session drafts, queue inspection/cancel/promotion/drain and idle recovery; real-daemon delivery evidence | Attachments, command selection, queue-specific uncertain/reconnect/stale-view acceptance and final-head running-turn regression |
| Create session | Native project/harness/model/reasoning/prompt flow; real isolated Evener creation manually exercised on both standalone platforms | Large catalogs, directory assistance, actionable validation, physical keyboard/accessibility coverage and final-head regression checks |
| Manage session | Native rename, context compaction and confirmed runtime stop; both-platform isolated real-daemon checks | Fork, clear, explicit lifecycle controls, disconnected/uncertain action acceptance, accessibility and physical-device verification |
| Model and launch settings | Model/reasoning selection during creation and in the composer for existing sessions | Vision selection, reasoning fault/a11y acceptance, launch layers, schema validation and repository trust |
| Goals and work | Not exposed | Goals, tasks, background jobs/output, subagent previews and navigation |
| Approvals and questions | Question text/options rendered with instruction to reply through composer; no dedicated decision controls | Sandbox escalation resolution, structured question interaction, stale/resolved decisions and any other user decision surfaces |
| Hub navigation | Project pin/favorite and project/session archive/unarchive in row actions; both-platform isolated-hub checks | Project/session removal, session pin assign/unpin, section rename/delete and pinned browsing |
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

## Last-location update — 5 September 2026

Source `dc2dd032f` restores a validated saved-hub/conversation bookmark after native restart. Both-platform cold starts, Android font-change recreation, toolbar back navigation and Android cross-hub identity have [manual evidence](../../design/mobile/location-restoration-evidence.md). Native tests are 53/53. Android system Back exited the restored conversation instead of popping; this remains an explicit navigation defect. Reading offsets and form-draft restoration are not implemented.

Android API 35 system Back is subsequently corrected by disabling the unsupported predictive opt-in for the current navigator. Final release checks verify restored-stack popping, keyboard-first dismissal, root exit and edge-swipe navigation; see the location evidence. Predictive transition previews and Android 16+ verification remain outstanding.

### Queue-management increment

The native composer queue count opens a platform modal with full queued text (or labeled preview), identity-guarded cancellation, and entry/revision-guarded individual/bulk steering. Both simulators exercised these actions against the scripted WebSocket fixture. Shared tests 2,233 and native tests 54 passed; both Release builds succeeded. See `docs/design/mobile/queue-evidence.md`. Real-daemon steering consumption, native conflict/uncertain-response scenarios, idle run-now and durable queue mutation recovery remain unverified or unimplemented; this is not full queue workflow completion.


### Real queue delivery and idle resume

The isolated real daemon accepted Android cancellation/promotion and iOS bulk steering. The next scripted provider request contained the promoted and drained markers, and excluded the cancelled marker. Idle resume is now exposed and manually exercised on iOS (selected steering) and Android (all combined); UI copy explains that resuming releases remaining queued work. Shared 2,234/native54 tests and both Release builds pass. See the real-daemon section of `docs/design/mobile/queue-evidence.md`; queue-specific uncertain/reconnect/native-conflict acceptance is still open.


### Native session controls

The Session sheet adds rename, compaction and confirmed runtime stop. Native 59 tests and TypeScript pass; both Release builds passed and were installed. Both simulators exercised real isolated-daemon rename/stop, with persisted names and process-exit evidence; compaction persisted CHECKPOINT/SUMMARY records. Binding identity prevents stale confirmation dispatch after reconnect, and compaction refreshes cold-runtime projections. See `docs/design/mobile/session-controls-evidence.md` for exact coverage and remaining acceptance.


### Existing-session reasoning

The initial Session sheet exposed server-advertised effort levels, with current-binding validation, serialized actions and authoritative refresh. That placement is superseded by composer controls below. Native61 tests/TypeScript and both Release builds passed; both simulators changed effort through the real isolated daemon. See `docs/design/mobile/reasoning-controls-evidence.md` for historical evidence.

### Composer model and reasoning

Model and reasoning now live directly inside the composer. Model selection uses a scoped searchable catalog and explicit application; reasoning uses a short choice sheet. Both preserve drafts and prevent overlap with compose submission. Native65/shared2,237 tests, TypeScript, targeted Biome and both Release builds pass. Both simulators switched models and effort against the isolated real daemon; Android keyboard overlap and iOS live large-text wrapping were corrected and visually verified. See `docs/design/mobile/composer-settings-evidence.md` for exact evidence and remaining acceptance.

### Roster search

Server-side search, clear and query-preserving navigation are implemented. Native68/shared2,238 tests and both Release builds pass, with real isolated-hub manual checks on both platforms. Live iOS large-text roster clipping is corrected. Hub aggregate pagination is demonstrably absent despite cursor fields in the protocol; this remains required work. See `docs/design/mobile/roster-search-evidence.md`.

### Project browsing

Native project catalogs and session tiers now use the existing revisioned navigation API. Raw offsets, remaining counts and generation/revision checks govern Load more. Both Release builds and Native72 tests pass. Real isolated-hub checks cover both-platform navigation, iOS tier switching/large text and Android continuation/refresh through a page-size proxy. See `docs/design/mobile/project-browsing-evidence.md`; this does not complete nested navigation, pins, live invalidation or physical-device acceptance.

### Project invalidation

Project browsing now observes live navigation invalidations, preserves visible rows, and requires refresh before continuing a stale snapshot. Both simulators manually exercised real isolated-hub rename notifications and refresh recovery. Native78 tests, TypeScript and both Release builds pass. Generation-restart and sequence-gap races have deterministic boundary coverage; full native fault acceptance remains open.

### Related sessions

Project browsing now exposes nested related sessions, omitted counts and partial-tree notices. Both simulators exercised two expansion levels and preserved them on return from a real session opened through a scripted hierarchy. Native81 tests and both Release builds pass. The fixture does not establish real daemon-created subagent lifecycle acceptance; see project-browsing-evidence.md for the boundary.

### Organization actions

Native row actions now expose project pin/favorite and project/session archive/unarchive with receipt-confirmed refresh and stale-owner protection. Native87 tests, TypeScript, targeted Biome and both Release builds pass. Both simulators manually archived and restored the owned project and session against the isolated real hub; project pinning and removal were also checked. See `docs/design/mobile/organization-evidence.md` for exact verification and remaining acceptance.
