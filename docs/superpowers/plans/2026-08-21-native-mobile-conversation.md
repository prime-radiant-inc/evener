# Native Mobile Sessions and Conversation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the mobile foundation into a complete session client with a streaming phone-first timeline, composer, structured `ask_user` questions, attachments, activity sheets, and all advertised V1 controls.

**Architecture:** Exactly one active profile AppWire connection feeds strict thread reduction and a mobile-only projection. Profile switching closes and invalidates it before another opens. Dedicated React screens render a virtualized timeline and sheets; typed service interfaces isolate protocol and native transport from UI state.

**Tech Stack:** The foundation plan's React/TypeScript/Tauri/Rust stack plus `@tanstack/react-virtual` 3.14.7, Marked 18.0.6, and DOMPurify 3.4.12.

**Spec:** `docs/superpowers/specs/2026-08-21-native-mobile-app-design.md`

## Global Constraints

- Complete `docs/superpowers/plans/2026-08-21-native-mobile-foundation.md` first.
- Import only files listed in `mobile/protocol-imports.json`; never import Hub web components, CSS, panes, shell, stores, or widgets.
- Treat every Hub/user/agent/tool/filename field as untrusted. Plain text is the default; only assistant Markdown enters the strict sanitizer.
- Use one total AppWire socket for the selected profile. Profile, connection, and conversation generations jointly reject stale cross-server frames or hydration.
- `ThreadCapabilities` controls every action. Never invent support from source name or UI state.
- Preserve drafts on conflicts; never retry user mutations automatically.
- At most eight normalized image attachments, 8 MiB each. View images up to 20 MiB and UTF-8 text/Markdown up to 2 MiB.
- Each task ends with focused tests and a named-path commit.

## File Structure

- `mobile/src/conversation/model.ts` — mobile item and conversation view models.
- `mobile/src/conversation/project.ts` — pure AppWire-to-mobile projection.
- `mobile/src/conversation/markdown.ts` — sanitizer and speakable-text conversion.
- `mobile/src/conversation/paging.ts`, `follow.ts` — anchor/follow state machines.
- `mobile/src/services/conversation.ts`, `activity.ts`, `attachments.ts` — protocol/native services.
- `mobile/src/state/conversation.ts`, `activity.ts` — active session stores.
- `mobile/src/screens/ConversationScreen.tsx` — focused session destination.
- `mobile/src/components/timeline/` — virtual list and item families.
- `mobile/src/components/composer/` — composer, asks, queue, attachments.
- `mobile/src/components/activity/` — tasks/work/usage/controls sheet.
- `mobile/src/components/viewer/` — safe image/text viewer.
- `mobile/src/dev/conversationFixtures.ts` — deterministic visual fixtures.

---

### Task 1: Build the Strict Mobile Thread Projection

**Files:**
- Create: `mobile/src/conversation/model.ts`, `project.ts`, `project.test.ts`
- Modify: `mobile/protocol-imports.json` only if a pure protocol file is required

**Interfaces:**
- Consumes: generated `Thread`, `ThreadItem`, `Turn`, status, task, and capability types.
- Produces: `projectThread(thread: Thread): MobileConversation`; `MobileTimelineItem` discriminated union.

- [ ] **Step 1: Define fixture-driven failing tests**

Cover user text, assistant text/deltas, reasoning, shell/tool/MCP calls, steering, system notices, failures, images, ask-user, unknown forward-compatible item, consecutive tool clustering, capability projection, queue state, usage, and profile/session identity.

The union begins:

```ts
export type MobileTimelineItem =
  | { kind: "user"; id: string; text: string }
  | { kind: "assistant"; id: string; markdown: string; streaming: boolean }
  | { kind: "activity"; id: string; label: string; state: ActivityState; detail: ActivityDetail }
  | { kind: "notice"; id: string; tone: NoticeTone; text: string }
  | { kind: "question"; id: string; batch: AskBatch }
  | { kind: "failure"; id: string; title: string; detail: string }
  | { kind: "attachments"; id: string; items: AttachmentRef[] };
```

- [ ] **Step 2: Run and verify failure**

Run: `npm --prefix mobile test -- --run src/conversation/project.test.ts`

Expected: FAIL because projection is absent.

- [ ] **Step 3: Implement the pure projector**

Unknown items become neutral, collapsed activity rows rather than disappearing or exposing raw HTML. Keep protocol DTOs out of React props.

- [ ] **Step 4: Run tests and boundary gate**

```bash
npm --prefix mobile test -- --run src/conversation/project.test.ts
npm --prefix mobile run boundary
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mobile/src/conversation/model.ts mobile/src/conversation/project.ts mobile/src/conversation/project.test.ts mobile/protocol-imports.json
git commit -m "feat(mobile): project AppWire threads for phone UI"
```

---

### Task 2: Implement Safe Markdown and Content Rendering

**Files:**
- Create: `mobile/src/conversation/markdown.ts`, `markdown.test.ts`
- Create: `mobile/src/components/timeline/AssistantMessage.tsx`, `PlainTextBlock.tsx`
- Modify: `mobile/package.json`, `mobile/package-lock.json`

**Interfaces:**
- Produces: `renderSafeMarkdown(source: string): TrustedHTMLString`; `toSpeakableText(source: string): string`; native external-link callback.

- [ ] **Step 1: Write malicious-content tests**

Inputs include `<script>`, `<img onerror>`, `<form>`, inline style, `javascript:`, `data:`, malformed Unicode, nested raw HTML, and an HTTP link. Assert dangerous content is removed, tool text stays escaped, and allowed links display their destination and invoke native external open.

- [ ] **Step 2: Verify failure**

Run: `npm --prefix mobile test -- --run src/conversation/markdown.test.ts`

Expected: FAIL.

- [ ] **Step 3: Implement the sanitizer**

Configure DOMPurify with an explicit tag/attribute allowlist. Disable raw Markdown HTML before sanitization. Reject every URL protocol except `http:` and `https:`. Do not enable Markdown images.

- [ ] **Step 4: Implement assistant/plain components**

Only `AssistantMessage` may consume sanitized HTML. Every tool, filename, notice, error, and user value uses React text nodes.

- [ ] **Step 5: Run tests and checks**

```bash
npm --prefix mobile test -- --run src/conversation/markdown.test.ts src/components/timeline
npm --prefix mobile run check
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add mobile/package.json mobile/package-lock.json mobile/src/conversation/markdown.ts mobile/src/conversation/markdown.test.ts mobile/src/components/timeline/AssistantMessage.tsx mobile/src/components/timeline/PlainTextBlock.tsx
git commit -m "feat(mobile): sanitize conversation content"
```

---

### Task 3: Build Conversation Service, Store, and Generation Rules

**Files:**
- Create: `mobile/src/services/conversation.ts`, `conversation.test.ts`
- Create: `mobile/src/state/conversation.ts`, `conversation.test.ts`
- Modify: shared AppWire service from foundation

**Interfaces:**
- Produces: `ConversationService.open(ref, cursor?)`; `ConversationService.mutate(action)`; `ConversationStore.open(ref)`; selectors for timeline, capability, queue, draft, and connection state.

- [ ] **Step 1: Write failing service tests**

Use fake AppwireClients for two profiles. Assert one connection serves sequential sessions on profile A; switching closes A before opening B; late A frames/hydration cannot overwrite B; switching back rehydrates A; notification refs route with profile identity; paging cursor is passed; and malformed projection retains only that profile's last good state with compatibility error.

- [ ] **Step 2: Write failing mutation tests**

Cover all required V1 actions and exact methods: send/start, steer, queue, interrupt, compact, change model/effort, rename, and shutdown. Assert false capability blocks before request; server action-unavailable refreshes capabilities; conflict restores draft; no automatic retry.

- [ ] **Step 3: Verify failures**

Run: `npm --prefix mobile test -- --run src/services/conversation.test.ts src/state/conversation.test.ts`

Expected: FAIL.

- [ ] **Step 4: Implement service and store**

Use injected client, native HTTP, clock, and ID factory. Limit optimism to pending send identity, draft, attachment preview, and queue chip; notifications remain authoritative.

- [ ] **Step 5: Run focused tests**

```bash
npm --prefix mobile test -- --run src/services/conversation.test.ts src/state/conversation.test.ts
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add mobile/src/services/conversation.ts mobile/src/services/conversation.test.ts mobile/src/state/conversation.ts mobile/src/state/conversation.test.ts mobile/src/services/appwireSocket.ts
git commit -m "feat(mobile): add conversation state and actions"
```

---

### Task 4: Build the Virtualized Timeline, Paging, and Follow Mode

**Files:**
- Create: `mobile/src/conversation/paging.ts`, `paging.test.ts`, `follow.ts`, `follow.test.ts`
- Create: `mobile/src/components/timeline/Timeline.tsx`, item components, CSS modules, tests
- Create: `mobile/src/screens/ConversationScreen.tsx`, test
- Modify: navigation route

**Interfaces:**
- Produces: `Timeline` with `loadOlder`, anchor preservation, and `NewActivityButton`; focused `ConversationScreen`.

- [ ] **Step 1: Write failing state-machine tests**

Assert near-bottom threshold enables follow; upward scroll disables; new items increment unseen; tapping new activity scrolls bottom; prepending older items returns an anchor adjustment; session or profile switch resets; stale measurements do nothing.

- [ ] **Step 2: Write failing component tests**

Render every item family, activity cluster, streaming assistant, failure, attachment, and unknown neutral row. Assert one timeline scroller, accessible expansion, new-activity pill, load-older row, and no raw protocol JSON by default.

- [ ] **Step 3: Verify failures**

Run: `npm --prefix mobile test -- --run src/conversation/paging.test.ts src/conversation/follow.test.ts src/components/timeline src/screens/ConversationScreen.test.tsx`

Expected: FAIL.

- [ ] **Step 4: Implement state machines and timeline**

Use `@tanstack/react-virtual`; key by stable item identity, not index. Preserve prepend anchor within 2 CSS pixels by measuring before and after the awaited virtualizer update.

- [ ] **Step 5: Implement focused conversation screen**

Hide bottom tabs while pushed. Add top back, title, status, activity-sheet action, timeline, and composer slot. One screen scroller is Timeline; headers/composer are fixed flex children.

- [ ] **Step 6: Run tests and fixture build**

```bash
npm --prefix mobile test -- --run src/conversation src/components/timeline src/screens/ConversationScreen.test.tsx
npm --prefix mobile run build
npm --prefix mobile run boundary
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add mobile/package.json mobile/package-lock.json mobile/src/conversation mobile/src/components/timeline mobile/src/screens/ConversationScreen.tsx mobile/src/screens/ConversationScreen.test.tsx mobile/src/navigation
git commit -m "feat(mobile): add streaming conversation timeline"
```

---

### Task 5: Build Composer, Queue, Attachments, and Structured Questions

**Files:**
- Create: `mobile/src/components/composer/*`
- Create: `mobile/src/services/attachments.ts`, tests
- Create: `mobile/src/state/attachments.ts`, tests
- Modify: `ConversationScreen.tsx`, conversation store

**Interfaces:**
- Produces: `Composer`, `AskComposer`, `AttachmentService`, exact `[answers]` composition through existing ask semantics.

- [ ] **Step 1: Write failing primary-action tests**

Assert idle Send, active Steer, explicit Queue, visible Stop, capability removal, model/effort summary, draft restoration after conflicts, and disabled mutation while reconnecting.

- [ ] **Step 2: Write failing structured-question tests**

Cover single/multi select, free text, notes, decide, fallback, skip, several question calls in one batch, exact answer payload, in-flight state, settlement from another client, and conflict draft recovery.

- [ ] **Step 3: Write failing attachment tests**

Cover JPEG/PNG/WebP/HEIC normalization, eight-count cap, 8 MiB post-normalization cap, opaque handle use, preview removal, cancellation, background cleanup, and error copy that never includes a native path.

- [ ] **Step 4: Verify failures**

Run: `npm --prefix mobile test -- --run src/components/composer src/services/attachments.test.ts src/state/attachments.test.ts`

Expected: FAIL.

- [ ] **Step 5: Implement composer and asks**

The dock uses visual viewport/keyboard information and safe-area padding. Ask mode unmounts normal inputs so hidden controls cannot focus or submit.

- [ ] **Step 6: Implement attachment native service**

Native picker returns an opaque handle. Rust validates media and size, normalizes HEIC to JPEG through the iOS plugin, and deletes temporary files on every terminal path and the 15-minute cleanup sweep.

- [ ] **Step 7: Run focused gates**

```bash
npm --prefix mobile test -- --run src/components/composer src/services/attachments.test.ts src/state/attachments.test.ts
cargo test --manifest-path mobile/src-tauri/Cargo.toml attachment
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add mobile/src/components/composer mobile/src/services/attachments.ts mobile/src/services/attachments.test.ts mobile/src/state/attachments.ts mobile/src/state/attachments.test.ts mobile/src/screens/ConversationScreen.tsx mobile/src-tauri/src mobile/tauri-plugin-evener-native
git commit -m "feat(mobile): add composer asks and attachments"
```

---

### Task 6: Build Safe Document and Image Viewing

**Files:**
- Create: `mobile/src/components/viewer/SafeViewer.tsx`, CSS, tests
- Modify: native HTTP transport and conversation link routing

**Interfaces:**
- Produces: viewer for allowed images ≤20 MiB and UTF-8 text/Markdown ≤2 MiB; explicit unsupported-format state.

- [ ] **Step 1: Write failing media-policy tests**

Cover valid JPEG/PNG/WebP/GIF, plain/Markdown, wrong MIME, MIME sniff mismatch, oversized bodies, invalid UTF-8, HTML/PDF/archive/executable rejection, cancel, object URL revocation, and external link handling.

- [ ] **Step 2: Verify failure**

Run: `npm --prefix mobile test -- --run src/components/viewer`

Expected: FAIL.

- [ ] **Step 3: Implement native bounded fetch and viewer**

Return raw IPC bytes only after validating status, content type, signature where applicable, and byte count. Viewer uses safe Markdown rules and never loads remote embedded images.

- [ ] **Step 4: Run tests**

```bash
npm --prefix mobile test -- --run src/components/viewer
cargo test --manifest-path mobile/src-tauri/Cargo.toml media
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mobile/src/components/viewer mobile/src/services/nativeHttp.ts mobile/src-tauri/src/http_transport.rs
git commit -m "feat(mobile): add safe authenticated viewers"
```

---

### Task 7: Build Tasks, Work, Usage, and Controls Sheet

**Files:**
- Create: `mobile/src/services/activity.ts`, tests
- Create: `mobile/src/state/activity.ts`, tests
- Create: `mobile/src/components/activity/*`, tests
- Modify: `ConversationScreen.tsx`

**Interfaces:**
- Produces: `ActivitySheet` with detents and sections Tasks, Work, Usage, Controls.

- [ ] **Step 1: Write failing projection tests**

Cover active/open/done tasks, nested delegates/jobs/watches, unknown status preservation, running/failed/terminal tones, duration/output summary, token/cost/context values, and redacted diagnostics.

- [ ] **Step 2: Write failing sheet/control tests**

Assert medium/large detents, section disclosure, focus trap, compact/interrupt/model/effort/rename/shutdown capability states, destructive shutdown confirmation, and structured unsupported errors.

- [ ] **Step 3: Verify failures**

Run: `npm --prefix mobile test -- --run src/services/activity.test.ts src/state/activity.test.ts src/components/activity`

Expected: FAIL.

- [ ] **Step 4: Implement service/store/components**

Use current thread/task/job projections; raw IDs appear only inside diagnostics disclosure. Diagnostics export uses the spec's metadata allowlist.

- [ ] **Step 5: Run tests**

```bash
npm --prefix mobile test -- --run src/services/activity.test.ts src/state/activity.test.ts src/components/activity
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add mobile/src/services/activity.ts mobile/src/services/activity.test.ts mobile/src/state/activity.ts mobile/src/state/activity.test.ts mobile/src/components/activity mobile/src/screens/ConversationScreen.tsx
git commit -m "feat(mobile): add session activity sheets"
```

---

### Task 8: Add Geometry, Lifecycle, and Mobile Product Gates

**Files:**
- Create: `mobile/src/dev/conversationFixtures.ts`
- Create: `mobile/scripts/mobile-geometry.mjs`, tests
- Create: `mobile/scripts/mobile-lifecycle.test.mjs`
- Modify: `mobile/package.json`, relevant lifecycle stores/services

**Interfaces:**
- Produces: `npm run test:geometry`; complete fixture routes; lifecycle contract.

- [ ] **Step 1: Write failing lifecycle tests**

Assert background closes active-profile AppWire, cancels HTTP/uploads, revokes URLs, deletes temp handles, ends voice placeholder state, preserves profile/session-keyed process-memory drafts, rejects stale events from inactive profiles, and foreground probes/reconnects/refreshes/rehydrates the selected profile before enabling mutation.

- [ ] **Step 2: Write geometry fixtures and failing assertions**

Cover roster, long/streaming conversation, open activity sheet, ask composer, attachment strip, new session, and settings at the exact viewport/type/theme/motion/safe-area/keyboard matrix from the spec. Assert no document horizontal overflow, one scroller, 44-pixel targets, composer visibility, focus reachability, and 2-pixel paging anchor.

- [ ] **Step 3: Verify failures**

Run:

```bash
npm --prefix mobile test -- --run lifecycle
npm --prefix mobile run test:geometry
```

Expected: FAIL on missing lifecycle/geometry implementation.

- [ ] **Step 4: Implement lifecycle coordinator and geometry runner**

Await native lifecycle events; avoid sleeps. Browser runner starts Vite, waits on fixture readiness, measures DOM, captures screenshots, and stops exact child processes.

- [ ] **Step 5: Format and run complete conversation gates**

```bash
npx --yes @biomejs/biome@2.5.5 check --write mobile/src
npm --prefix mobile run check
npm --prefix mobile test
npm --prefix mobile run boundary
npm --prefix mobile run build
npm --prefix mobile run test:geometry
cargo fmt --manifest-path mobile/src-tauri/Cargo.toml --check
cargo clippy --manifest-path mobile/src-tauri/Cargo.toml --all-targets -- -D warnings
cargo test --manifest-path mobile/src-tauri/Cargo.toml
git diff --check
```

Expected: all exit zero.

- [ ] **Step 6: Commit**

```bash
git add mobile/src/dev mobile/src mobile/scripts mobile/package.json mobile/package-lock.json
git commit -m "test(mobile): gate the complete session experience"
```
