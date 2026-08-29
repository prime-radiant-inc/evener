# Mobile Live Conversation Frame Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the three concept-owned conversation pages with one bounded, keyboard-safe, virtualized production frame that preserves every production intent while keeping unsafe system/evidence text out of the primary DOM.

**Architecture:** Keep `MobileConversation.items` authoritative for stores and behavior, and project one immutable bounded feed/evidence snapshot in `LiveConceptHost`. `ConversationFrame` owns chrome, status, the sole transcript scroller, composer, evidence portal, accessibility, focus, and anchors; each concept contributes only visual render callbacks. Generalize the existing `@tanstack/react-virtual` timeline mechanics once, migrate concepts serially, delete all old conversation structures, and prove the actual host in unit, AppWire, real-browser, simulator, and finally physical-device lanes.

**Tech Stack:** React 19.2.7, TypeScript 6.0.3, Zustand 5.0.14, `@tanstack/react-virtual` 3.14.7, Vitest 4.1.10, Vite 8.1.5, Biome 2.5.5, Node built-ins plus Chrome DevTools Protocol, Tauri 2.11.4, Rust, Xcode/iOS Simulator, IDB, and Apple `devicectl`/`simctl`.

**Spec:** `docs/superpowers/specs/2026-08-28-mobile-live-conversation-frame-redesign.md`

## Global Constraints

- Baseline is approved spec commit `a06ba218c`; do not implement from the superseded conversation-presentation sections of the 2026-08-26 design.
- `RootShell` remains the only owner of profiles, credentials, AppWire, lifecycle, root navigation, New, Settings, Voice, and production store identity.
- `MobileConversation.items` remains canonical for stores, notifications, paging, mutations, voice, and activity correlation; bounded display objects are never mutation inputs except through opaque private-map lookups.
- Preserve the current pairing probe bounds/serialization/AppWire validation, AppWire initialize optional-field compatibility, profile lease/generation fencing, exact draft restoration, and capability-before-error publication. Do not modify `mobile/src-tauri/gen/apple/**`.
- Only `--viewport-height` and `--keyboard-inset` from `mobile/src/ui/platformPresentation.ts` control live conversation geometry; do not create another viewport/keyboard coordinator or use `--visual-viewport-height`.
- Hidden instructions and system preludes never enter DOM text, attributes, accessible descriptions, logs, screenshots, or serialized display snapshots; redaction happens before truncation.
- UTF-8 maxima are exact: title/project 256 B; status/updated label 128 B; feed label 128 B; detail label 256 B; user/assistant 64 KiB; question header 128 B, prompt 8 KiB, option label 1 KiB, option detail/`why`/fallback 4 KiB; failure title 256 B, feed body 8 KiB, safe detail 64 KiB; notice preview/detail 512 B/16 KiB; hidden/prelude 0 B/0 B; allowlisted lifecycle detail 16 KiB; reasoning preview/detail 512 B/64 KiB; tool preview and each argument/output detail 512 B/64 KiB; attachment label/metadata 512 B/4 KiB total; diagnostic preview/detail 256 B/16 KiB; unknown preview/detail 0 B/16 KiB after redaction; evidence heading 128 B; connection/read/mutation error 1 KiB after redaction.
- User-visible truncation emits exactly one `… truncated`, stays within its byte cap, and cuts only at a valid Unicode code-point boundary.
- Exactly one page/content scroller is active: transcript normally, modal sheet while open. Native textarea scrolling after six lines is the sole permitted exception.
- Virtual transcript overscan is six, family estimates are marker 56 px, user 96 px, assistant 128 px, question/failure 160 px, and mounted transcript/evidence row containers never exceed 48.
- Follow threshold is 48 CSS px; prepend, replacement, resize, Dynamic Type, and eviction restore the surviving stable anchor within 2 CSS px.
- Portrait widths are 375, 393, and 430 CSS px; landscape is 852×393; semantic type is standard, XXL, and AX-XXXL; targets are at least 44×44 CSS px; composer is at most six text lines.
- Route motion is opacity plus at most 16 px translation for at most 180 ms; concept switching is a crossfade of at most 120 ms; reduced motion disables both; row/stream/measurement updates never replay route motion.
- Every task uses RED/GREEN TDD, named-path staging, an independent focused gate/review, and at least one scoped commit. A timeout, missing browser/runtime, sandbox denial, or launch failure is incomplete, never green.
- Keep screenshots, accessibility trees, geometry JSON, apps, and logs under the executor's scratch directory; never stage those artifacts or sensitive profile/thread values.

## File and Responsibility Map

| Path | Change | Singular responsibility |
|---|---|---|
| `mobile/src/conversation/model.ts` | Modify lines 18–52, 80–101 | Preserve authoritative activity/system family metadata needed for safe display classification. |
| `mobile/src/conversation/project.ts` | Modify lines 36–74, 271–382 | Derive notice/activity family from wire type plus authoritative event metadata, never labels. |
| `mobile/src/live-concepts/display-text.ts` | Create | Redaction-first UTF-8 bounding and the one-marker contract. |
| `mobile/src/live-concepts/model.ts` | Modify lines 46–76 | Immutable narrative, activity-marker, evidence, and snapshot display models. |
| `mobile/src/live-concepts/project-conversation.ts` | Modify lines 50–152, 345–388, 404–779 | Transactional source-to-display/evidence projection and private operational lookup maps. |
| `mobile/src/live-concepts/accessibility-semantics.ts` | Modify lines 58–85 | Exact shared narrative/marker labels and settled streaming announcements. |
| `mobile/src/live-concepts/conversation/contract.ts` | Create | Frame, skin, row-render, anchor, and composer interfaces; no source/store/transport types. |
| `mobile/src/live-concepts/conversation/ConversationFrame.tsx` | Create | Stable production-owned chrome/transcript/status/composer/sheet composition. |
| `mobile/src/live-concepts/conversation/DockedComposer.tsx` | Create | Shared send/steer/queue/interrupt and complete question answer dock. |
| `mobile/src/live-concepts/conversation/VirtualTranscript.tsx` | Create | Feed semantics around the shared variable-height virtual list. |
| `mobile/src/live-concepts/conversation/ActivityEvidenceSheet.tsx` | Create | Portal, safe detail rendering, independent evidence virtualization, scroll lock, and focus lifecycle. |
| `mobile/src/live-concepts/conversation/conversation-frame.css` | Create | Shared viewport, safe-area, one-scroll-owner, textarea, responsive, and motion mechanics. |
| `mobile/src/components/timeline/VariableHeightVirtualList.tsx` | Create | One generic measured `@tanstack/react-virtual` implementation used by both timelines. |
| `mobile/src/conversation/paging.ts` | Replace lines 16–92 | Stable-key measured anchor capture/reconciliation; remove item-count/average-height inference. |
| `mobile/src/components/timeline/Timeline.tsx` | Modify lines 1–156 | Consume the shared virtual list and measured anchor algorithm rather than a private approximation. |
| `mobile/src/ui/Sheet.tsx` | Reuse unchanged, especially lines 634–820 | Existing portal, modal, Escape, focus trap, and restoration primitive for Activity/Evidence. |
| `mobile/src/screens/ConversationScreen.tsx` | Reuse structural precedent unchanged, especially lines 144–193 | Existing production sibling composition for top bar, sole timeline scroller, composer, and activity sheet. |
| `mobile/src/live-concepts/live-ui-store.ts` | Modify lines 22–112 | `{concept,threadKey}` anchors, follow/unseen, evidence, disclosure, and focus intent. |
| `mobile/src/live-concepts/contract.ts` | Modify lines 32–113 | Add bounded frame state and require one `ConversationSkin` per concept module. |
| `mobile/src/live-concepts/LiveConceptHost.tsx` | Modify lines 158–544 | Project once, preserve last good snapshot, compose frame, wire stable local UI, and remove absolute `scrollTop` restoration. |
| `mobile/src/live-concepts/stillwater/ConversationSkin.tsx` | Create | Stillwater chrome, narrative rows, and markers only. |
| `mobile/src/live-concepts/constellation/ConversationSkin.tsx` | Create | Constellation visual rows/decorations only. |
| `mobile/src/live-concepts/field-notes/ConversationSkin.tsx` | Create | Field Notes visual rows with row-local chronology only. |
| `mobile/src/live-concepts/{stillwater,constellation,field-notes}/ConversationView.tsx` | Delete after each migration | Remove concept-owned transcript loops, questions, disclosures, statuses, and composers. |
| `mobile/src/live-concepts/{stillwater,constellation,field-notes}/*.tsx` | Modify renderer/shell line regions named per task | Keep Sessions/Work shells while conversation routes through the shared frame. |
| `mobile/src/live-concepts/{stillwater,constellation,field-notes}/*.css` | Modify exact line regions named per task | Keep theme tokens/skin rules; delete conversation geometry/scrollers/composers. |
| `mobile/scripts/check-live-concepts-boundary.mjs` | Modify | Reject structural drift: source DTOs, concept virtualizers/scrollers/composers, raw maps, and old viewport token. |
| `mobile/src/test/live-conversation-pathological.fixture.ts` | Create | Exact 39-item/44,700-byte and 500-item variable-height fixtures with safe sentinels. |
| `mobile/src/test/LiveConceptBrowserHarness.tsx` | Create | Actual production `LiveConceptHost` with injected production store/service fakes. |
| `mobile/src/test/live-conversation-browser-entry.tsx` | Create | Test-only Vite entry selecting scenario/concept/matrix inputs. |
| `mobile/live-conversation-harness.html` | Create | Browser-harness HTML entry, never used by production navigation. |
| `mobile/scripts/browserguard.vite.config.mjs` | Create | Private loopback Vite config consumed by the repository browser-process helper. |
| `mobile/scripts/live-conversation-geometry.mjs` | Create | CDP real-browser matrix, interactions, screenshots, AX/DOM/geometry/anchor evidence. |
| `mobile/src/test/live-concepts-real-bridge.test.tsx` | Modify current AppWire vertical slice | Prove bounded display/evidence updates and unchanged production action/request ownership. |
| `mobile/scripts/smoke-live-concepts.mjs` and `.test.mjs` | Modify | Add geometry/system-suppression/simulator evidence without weakening existing identity and receipt checks. |
| `mobile/package.json` | Modify scripts lines 6–17 | Canonical focused, browser, and smoke commands. |
| `make/testing.mk`, `.github/workflows/ci.yml`, `docs/developing-evener/testing.md` | Modify | Required deterministic mobile and Chrome-capable mobile gates with generated documentation. |

## Dependency Graph and Isolation

```text
Task 1 bounded projection/semantics
  └── Task 2 shared frame/composer contract
       └── Task 3 shared variable-height virtual list
            └── Task 4 Activity/Evidence sheet
                 └── Task 5 Stillwater skin
                      └── Task 6 Constellation skin
                           └── Task 7 Field Notes skin
                                └── Task 8 final host/state/boundary cutover
                                     └── Task 9 browser/AppWire/simulator proof
                                          └── Task 10 canonical gates/review
                                               └── deferred physical-device acceptance
```

Task 1's `display-text.ts` tests and Task 3's pure anchor tests can be developed in isolated worktrees. After Task 4, the three skin files are path-disjoint in principle, and Task 9's fixture/runner can begin in isolation after the frame contract is frozen. Integrate them in the serial order above anyway: Tasks 2–4 share frame interfaces; each skin migration changes the registry/host contract; and serial review prevents three incompatible structural exceptions. Do not parallel-edit `model.ts`, `contract.ts`, `LiveConceptHost.tsx`, `live-ui-store.ts`, registry files, shared frame files, package scripts, or gate wiring.

---

### Task 1: Bound and Classify the Display/Evidence Projection

**Files:**
- Create: `mobile/src/live-concepts/display-text.ts`
- Create: `mobile/src/live-concepts/display-text.test.ts`
- Modify: `mobile/src/conversation/model.ts:18-52,80-101`
- Modify: `mobile/src/conversation/project.ts:36-74,271-382`
- Modify: `mobile/src/conversation/project.test.ts:430-520`
- Modify: `mobile/src/live-concepts/model.ts:46-76`
- Modify: `mobile/src/live-concepts/project-conversation.ts:50-152,345-388,404-779`
- Modify: `mobile/src/live-concepts/project-conversation.test.ts:1-180` and append classification/bounds cases
- Modify: `mobile/src/live-concepts/accessibility-semantics.ts:58-85`
- Modify: `mobile/src/live-concepts/accessibility-semantics.test.ts:1-151` exact transcript-label cases
- Modify temporarily for the new bounded fields: all three `live-concepts/*/ConversationView.tsx` transcript text reads and renderer tests

**Interfaces:**
- Consumes: canonical `MobileTimelineItem`, authoritative `ThreadItem.type`, `eventKind`, `steeringKind`, `ActivityFamily`, and the existing transactional opaque-key registry.
- Produces: `boundDisplayText(text: string, maxUtf8Bytes: number, policy: "plain" | "redacted"): BoundedDisplayText`.
- Produces: `ConversationDisplaySnapshot`, `ConversationDisplayItem`, `EvidenceDisplayItem`, and `ConversationOperationalMap` with opaque evidence lookups.
- Produces: `conversationItemAxLabel(item: ConversationDisplayItem, questionResolution: QuestionDraft["resolution"]): string` and `streamingAnnouncement(previous, next): string | null`.

- [ ] **Step 1: Add the exact 44.7 KB leak, classification, bounds, and semantic failures**

Extend the existing projector fixtures with a byte-exact sentinel and assert absence from every serializable display field:

```ts
const SYSTEM_SENTINEL = "SYSTEM-PRELUDE-MUST-NOT-RENDER:";
const systemPrelude =
  SYSTEM_SENTINEL + "x".repeat(44_700 - utf8Bytes(SYSTEM_SENTINEL));
expect(utf8Bytes(systemPrelude)).toBe(44_700);

const result = projector.project(
  makeConversation({
    items: [
      {
        kind: "notice",
        id: "sys-44k",
        origin: "system",
        family: "system-prelude",
        tone: "system",
        text: systemPrelude,
      },
      { kind: "user", id: "u-1", text: "actual user" },
    ],
  }),
  OPTS,
);
const serialized = JSON.stringify(result.view);
expect(serialized).not.toContain(SYSTEM_SENTINEL);
expect(result.view.items[0]).toMatchObject({
  sourceKind: "system",
  semanticKind: "system-context",
  preview: null,
  evidenceKey: null,
});
expect(
  result.view.items.map((item) => conversationItemAxLabel(item, null)),
).toEqual([
  "System context; details hidden",
  "Your message; completed",
]);
```

Add table-driven cases for every bound in Global Constraints, split 4-byte emoji at each edge, embedded/repeated truncation markers, empty strings, warning/lifecycle/diagnostic/unknown system families, hostile labels claiming `user`, and redaction sentinels for bearer tokens, authorization URLs, profile IDs, operational IDs, and absolute paths. Assert redaction precedes truncation by placing the secret across the truncation boundary. Assert projection failure commits no feed key, evidence key, sequence, or private mapping.

- [ ] **Step 2: Run the focused RED and retain the observed root-cause output**

Run:

```bash
cd mobile
npx vitest run \
  src/conversation/project.test.ts \
  src/live-concepts/display-text.test.ts \
  src/live-concepts/project-conversation.test.ts \
  src/live-concepts/accessibility-semantics.test.ts
```

Expected: FAIL because `display-text.ts` and the new display contracts do not exist; the current notice case also serializes the 44,700-byte sentinel and labels it `Your message; completed`.

- [ ] **Step 3: Preserve authoritative source family metadata**

Add this closed canonical metadata without changing store retention or mutation behavior:

```ts
export type NoticeOrigin = "steering" | "system";
export type NoticeFamily =
  | "informational"
  | "warning"
  | "hidden-instruction"
  | "system-prelude"
  | "lifecycle"
  | "diagnostic"
  | "unknown-system";

// MobileTimelineItem notice member
{
  kind: "notice";
  id: string;
  origin: NoticeOrigin;
  family: NoticeFamily;
  tone: NoticeTone;
  text: string;
}
```

Classify in `conversation/project.ts` from wire metadata, never display text:

```ts
const HIDDEN_EVENT_KINDS = new Set(["system_prompt", "prompt_loaded"]);
const PRELUDE_EVENT_KINDS = new Set(["environment"]);
const DIAGNOSTIC_EVENT_KINDS = new Set(["round_timings"]);
const LIFECYCLE_EVENT_KINDS = new Set([
  "plugin_loaded", "skill_activated", "hook_completed",
  "context_compaction", "compaction", "goal_ended", "fork_summary",
  "tool_repair", "model_switch",
]);

function systemFamily(eventKind: string | undefined): NoticeFamily {
  if (eventKind && WARNING_EVENT_KINDS.has(eventKind)) return "warning";
  if (eventKind && HIDDEN_EVENT_KINDS.has(eventKind)) return "hidden-instruction";
  if (eventKind && PRELUDE_EVENT_KINDS.has(eventKind)) return "system-prelude";
  if (eventKind && DIAGNOSTIC_EVENT_KINDS.has(eventKind)) return "diagnostic";
  if (eventKind && LIFECYCLE_EVENT_KINDS.has(eventKind)) return "lifecycle";
  return "unknown-system";
}
```

Steering projects as origin `steering`, family `warning` for the existing warning set and `informational` otherwise. Update source-projection tests to prove a `toolName: "Your message"` remains family `tool` and unknown event values take `unknown-system` with suppressed body.

- [ ] **Step 4: Implement redaction-first UTF-8 bounds**

Use one exported primitive for every family:

```ts
export const TRUNCATION_MARKER = "… truncated";
const encoder = new TextEncoder();

export function boundDisplayText(
  source: string,
  maxUtf8Bytes: number,
  policy: "plain" | "redacted",
): BoundedDisplayText {
  const originalUtf8Bytes = encoder.encode(source).length;
  const safe = policy === "redacted" ? redactSensitiveText(source) : source;
  const encoded = encoder.encode(safe);
  if (encoded.length <= maxUtf8Bytes) {
    return { text: safe, truncated: false, originalUtf8Bytes };
  }
  const withoutMarkers = stripAllMarkersLinear(safe, TRUNCATION_MARKER);
  const budget = maxUtf8Bytes - encoder.encode(TRUNCATION_MARKER).length;
  return {
    text: decodeValidPrefix(encoder.encode(withoutMarkers), budget) +
      TRUNCATION_MARKER,
    truncated: true,
    originalUtf8Bytes,
  };
}
```

`redactSensitiveText` replaces recognized bearer/basic credentials, token/password/secret key-value fields, authorization URL query values, opaque profile/thread/job/delegate/call identifiers, and absolute home/filesystem paths with fixed typed markers. It must not log its input. Keep title/project plain-but-bounded; all error, diagnostic, unknown, and evidence-detail paths use redacted policy. Hidden/prelude uses no call to `boundDisplayText` for its body at all.

- [ ] **Step 5: Replace `LiveTranscriptItem` with one transactional display/evidence snapshot**

Define the immutable model exactly once:

```ts
export interface BoundedDisplayText {
  readonly text: string;
  readonly truncated: boolean;
  readonly originalUtf8Bytes: number;
}

export type ConversationDisplayItem =
  | NarrativeDisplayItem
  | ActivityMarkerDisplayItem;

export interface NarrativeDisplayItem {
  readonly key: string;
  readonly sourceKind: "user" | "assistant" | "question" | "failure";
  readonly body: BoundedDisplayText;
  readonly label: BoundedDisplayText | null;
  readonly tone: DisplayTone;
  readonly streaming: boolean;
  readonly questionKey: string | null;
  readonly sequence: string;
}

export interface ActivityMarkerDisplayItem {
  readonly key: string;
  readonly sourceKind:
    | "notice" | "system" | "reasoning" | "tool"
    | "attachment" | "diagnostic" | "unknown";
  readonly semanticKind:
    | "notice" | "warning-notice" | "system-context" | "system-activity"
    | "reasoning" | "tool" | "attachment" | "activity";
  readonly label: BoundedDisplayText;
  readonly preview: BoundedDisplayText | null;
  readonly tone: DisplayTone;
  readonly state: "running" | "completed" | "failed" | "unavailable";
  readonly evidenceKey: string | null;
  readonly sequence: string;
}

export interface EvidenceSection {
  readonly heading: BoundedDisplayText;
  readonly body: BoundedDisplayText;
}

export interface EvidenceDisplayItem {
  readonly key: string;
  readonly family: ActivityMarkerDisplayItem["sourceKind"];
  readonly title: BoundedDisplayText;
  readonly sections: readonly EvidenceSection[];
  readonly redacted: boolean;
}

export interface ConversationDisplaySnapshot {
  readonly view: LiveConversationView;
  readonly operational: ConversationOperationalMap;
}

export interface ConversationOperationalMap {
  readonly itemKeys: ReadonlyMap<string, string>;
  readonly questionKeys: ReadonlyMap<string, QuestionLink>;
  readonly optionKeys: ReadonlyMap<string, OptionLink>;
  readonly evidenceKeys: ReadonlyMap<string, string>;
}

export interface LiveQuestionView {
  readonly key: string;
  readonly header: BoundedDisplayText;
  readonly prompt: BoundedDisplayText;
  readonly options: ReadonlyArray<{
    readonly key: string;
    readonly label: BoundedDisplayText;
    readonly detail: BoundedDisplayText;
  }>;
  readonly multiple: boolean;
  readonly why: BoundedDisplayText | null;
  readonly ifUnanswered: BoundedDisplayText | null;
}
```

`LiveConversationView` contains bounded `title`, `project`, `status`, and `updatedLabel`, plus `items: readonly ConversationDisplayItem[]` and `evidence: readonly EvidenceDisplayItem[]`. Define one `DISPLAY_LIMITS` constant whose numeric values are exactly the Global Constraints table and use those names at every call site; tests iterate all entries so no literal cap can drift. Build feed, questions, evidence, and all private links in staging registries, validate every non-null `evidenceKey` resolves exactly once, freeze arrays/maps, and commit registries only after complete success. Never create a temporary display object containing hidden/prelude raw text.

Every source-derived display string, including title, project, user text, assistant Markdown source, question text, attachment metadata, and errors, takes the redacted policy. Redact assistant source before truncation and before passing it to the existing Markdown sanitizer. The `plain` policy is reserved for fixed application-generated labels such as `System context`; canonical source/question values remain unmodified behind private operational maps for mutation composition.

- [ ] **Step 6: Implement exact shared accessibility semantics**

Use an exhaustive switch over narrative source kind and marker `semanticKind`. Exact outputs are the spec table: user `Your message; completed`; assistant complete/streaming; question required/resolved; failure action required; notice informational/attention required; system context hidden; system activity informational; reasoning/tool/attachment/unknown with one of running/completed/failed/unavailable. Marker preview is `aria-describedby` content, never part of the name. `streamingAnnouncement` returns only on a completed phrase boundary (`.`, `!`, `?`, newline) or a streaming-state transition and returns `null` for every raw delta between boundaries.

- [ ] **Step 7: Run GREEN, compatibility gates, and independent review**

Run:

```bash
cd mobile
npx biome check --write \
  src/conversation/model.ts src/conversation/project.ts src/conversation/project.test.ts \
  src/live-concepts/display-text.ts src/live-concepts/display-text.test.ts \
  src/live-concepts/model.ts src/live-concepts/project-conversation.ts \
  src/live-concepts/project-conversation.test.ts \
  src/live-concepts/accessibility-semantics.ts \
  src/live-concepts/accessibility-semantics.test.ts \
  src/live-concepts/stillwater/ConversationView.tsx \
  src/live-concepts/constellation/ConversationView.tsx \
  src/live-concepts/field-notes/ConversationView.tsx
npx vitest run src/conversation/project.test.ts \
  src/live-concepts/display-text.test.ts \
  src/live-concepts/project-conversation.test.ts \
  src/live-concepts/accessibility-semantics.test.ts
npm run check
npm run boundary
git diff --check
```

Expected: all exit 0; the 44,700-byte sentinel appears in canonical input only, not JSON display output; the exact user label count is one. Review the diff specifically for any raw body copied before classification and any operational ID in a display key/value.

- [ ] **Step 8: Commit the bounded projection only**

```bash
git add mobile/src/conversation/model.ts mobile/src/conversation/project.ts \
  mobile/src/conversation/project.test.ts \
  mobile/src/live-concepts/display-text.ts mobile/src/live-concepts/display-text.test.ts \
  mobile/src/live-concepts/model.ts \
  mobile/src/live-concepts/project-conversation.ts \
  mobile/src/live-concepts/project-conversation.test.ts \
  mobile/src/live-concepts/accessibility-semantics.ts \
  mobile/src/live-concepts/accessibility-semantics.test.ts \
  mobile/src/live-concepts/stillwater/ConversationView.tsx \
  mobile/src/live-concepts/constellation/ConversationView.tsx \
  mobile/src/live-concepts/field-notes/ConversationView.tsx
git commit --only -m "feat(mobile): bound live conversation projection" -- \
  mobile/src/conversation/model.ts mobile/src/conversation/project.ts \
  mobile/src/conversation/project.test.ts \
  mobile/src/live-concepts/display-text.ts mobile/src/live-concepts/display-text.test.ts \
  mobile/src/live-concepts/model.ts \
  mobile/src/live-concepts/project-conversation.ts \
  mobile/src/live-concepts/project-conversation.test.ts \
  mobile/src/live-concepts/accessibility-semantics.ts \
  mobile/src/live-concepts/accessibility-semantics.test.ts \
  mobile/src/live-concepts/stillwater/ConversationView.tsx \
  mobile/src/live-concepts/constellation/ConversationView.tsx \
  mobile/src/live-concepts/field-notes/ConversationView.tsx
```

### Task 2: Create the Shared Frame and Docked Composer Contract

**Files:**
- Create: `mobile/src/live-concepts/conversation/contract.ts`
- Create: `mobile/src/live-concepts/conversation/ConversationFrame.tsx`
- Create: `mobile/src/live-concepts/conversation/ConversationFrame.test.tsx`
- Create: `mobile/src/live-concepts/conversation/DockedComposer.tsx`
- Create: `mobile/src/live-concepts/conversation/DockedComposer.test.tsx`
- Create: `mobile/src/live-concepts/conversation/VirtualTranscript.tsx` (initial bounded structural adapter; Task 3 installs shared windowing)
- Create: `mobile/src/live-concepts/conversation/conversation-frame.css`
- Modify: `mobile/src/live-concepts/model.ts:1-6` preserve the exact native content-size category
- Modify: `mobile/src/live-concepts/contract.ts:32-113`
- Modify: `mobile/src/live-concepts/LiveConceptHost.tsx:288-327,441-544`
- Modify: `mobile/src/live-concepts/LiveConceptHost.test.tsx:1-966` frame composition/state cases

**Interfaces:**
- Consumes: Task 1 `LiveConversationView`, `ConversationDisplayItem`, `EvidenceDisplayItem`, `LiveComposerView`, `QuestionDraft`, and existing exhaustive `LiveConceptIntent`.
- Produces: `ConversationSkin`, `NarrativeItemRenderProps`, `ActivityMarkerRenderProps`, `ChromeRenderProps`, `ComposerAppearance`, `ConversationAnchor`, and `ConversationFrameProps`.
- Produces: `ConversationFrame(props): ReactElement` and `DockedComposer(props): ReactElement`; no store, service, source DTO, or transport enters either.

- [ ] **Step 1: Write failing frame ownership and complete composer tests**

Render with a deliberately plain test skin. Assert direct sibling order and behavior:

```ts
const frame = render(<ConversationFrame {...props} skin={testSkin} />);
const root = frame.getByRole("main", { name: "Conversation" });
expect([...root.children].map((node) => node.getAttribute("data-frame-part")))
  .toEqual(["chrome", "transcript", "status", "composer"]);
expect(root.querySelectorAll("[data-page-scroll-owner='true']")).toHaveLength(1);
expect(frame.getByRole("textbox", { name: "Message" })).toBeVisible();
expect(frame.getByRole("button", { name: "Submit message" })).toBeVisible();
```

Cover loading with disabled composer, empty, offline with editable draft, last-good read error plus retry, no-data read error with focused retry, pending/accepted/failed mutation, interrupt independent of text mode, and malformed-projection fallback. Exercise send/steer/queue dispatch, six-line textarea sizing, exact draft restoration, capability-disabled explanations, and every question action: single/multi option, note, fallback, decide, skip, resolved state, and one all-question submit intent.

- [ ] **Step 2: Run RED**

Run:

```bash
cd mobile
npx vitest run \
  src/live-concepts/conversation/ConversationFrame.test.tsx \
  src/live-concepts/conversation/DockedComposer.test.tsx \
  src/live-concepts/LiveConceptHost.test.tsx
```

Expected: FAIL because the conversation frame modules and skin contract do not exist.

- [ ] **Step 3: Define the skin boundary and frame props**

```ts
export interface ConversationSkin {
  readonly id: ConceptId;
  readonly className: string;
  readonly composerAppearance: ComposerAppearance;
  renderNarrativeItem(props: NarrativeItemRenderProps): ReactNode;
  renderActivityMarker(props: ActivityMarkerRenderProps): ReactNode;
  renderConversationChrome(props: ChromeRenderProps): ReactNode;
}

export interface ComposerAppearance {
  readonly density: "compact" | "comfortable";
  readonly accent: "forest" | "luminous" | "rust";
}

export interface NarrativeItemRenderProps {
  readonly item: NarrativeDisplayItem;
  readonly body: ReactNode;
  readonly focused: boolean;
}

export interface ActivityMarkerRenderProps {
  readonly item: ActivityMarkerDisplayItem;
  readonly focused: boolean;
}

export interface ChromeRenderProps {
  readonly title: BoundedDisplayText;
  readonly project: BoundedDisplayText;
  readonly status: BoundedDisplayText;
  readonly updatedLabel: BoundedDisplayText | null;
  readonly backControl: ReactNode;
  readonly workControl: ReactNode;
  readonly conceptSwitchControl: ReactNode;
}

export interface ConversationAnchor {
  readonly threadKey: string;
  readonly itemKey: string;
  readonly offsetPx: number;
  readonly following: boolean;
}

export interface ConversationFrameProps {
  readonly state: LiveConceptState;
  readonly skin: ConversationSkin;
  readonly phase: "loading" | "empty" | "ready" | "read-error";
  readonly dispatch: (intent: LiveConceptIntent) => void;
  readonly onRetry: () => void;
  readonly onAnchorChange: (anchor: ConversationAnchor) => void;
  readonly onUnseenChange: (count: number) => void;
  readonly onFocusIntentChange: (key: string | null) => void;
}
```

Render props expose only bounded display items, semantic state, and frame-owned control nodes. They do not expose `MobileTimelineItem`, store handles, source IDs, scroll/virtualizer handles, viewport services, or transport services.

The frame wraps every skin result in the semantic feed/article structure and owns `aria-label`, `aria-describedby`, `aria-posinset`, `aria-setsize`, streaming status, and the exact evidence action. `ChromeRenderProps` carries frame-built controls rather than raw callbacks, so skins may arrange/decorate Back, Work, and concept switch but cannot rename or reimplement them.

Change `TextScale` to the exact native `ContentSizeCategory` type and set `LiveConceptState.textScale = contentSize` without collapsing every accessibility category to one token. The matrix therefore exercises `large` (standard), `extraExtraLarge` (XXL), and `accessibilityExtraExtraExtraLarge` (AX-XXXL), while measurement caches remain correct for every other supported category.

- [ ] **Step 4: Implement stable frame composition and production viewport CSS**

The host may temporarily select the frame only when a migrated module supplies `conversationSkin`; this is the sole migration-only dual path and Task 8 removes it. The frame root must be:

```tsx
<main className={`live-conversation-frame ${skin.className}`} aria-label="Conversation">
  <header data-frame-part="chrome">{skin.renderConversationChrome(chromeProps)}</header>
  <VirtualTranscript data-frame-part="transcript" {...transcriptProps} />
  <ConversationStatus data-frame-part="status" {...statusProps} />
  <DockedComposer data-frame-part="composer" {...composerProps} />
</main>
```

Use these mechanics, not concept overrides:

```css
.live-conversation-frame {
  block-size: var(--viewport-height, 100dvh);
  min-block-size: 0;
  display: grid;
  grid-template-rows: auto minmax(0, 1fr) auto auto;
  overflow: hidden;
}
.live-conversation-frame [data-frame-part="chrome"] {
  padding-block-start: max(var(--safe-area-top, 0px), 0.75rem);
  flex-shrink: 0;
}
.live-conversation-frame [data-frame-part="transcript"] {
  min-block-size: 0;
  overflow: hidden;
}
.live-conversation-frame [data-frame-part="composer"] {
  min-block-size: 0;
  flex-shrink: 0;
  padding-block-end: max(var(--keyboard-inset, 0px), var(--safe-area-bottom, 0px));
}
.live-conversation-composer textarea {
  max-block-size: calc(6 * 1.5em + 1rem);
  overflow-y: auto;
  resize: none;
}
```

No component reads `window.visualViewport`; `platformPresentation.ts` remains untouched.

- [ ] **Step 5: Implement all composer and question behavior once**

Map the exact existing modes/capabilities to intents. Question buttons first dispatch a full `QuestionDraft`, then `submitQuestion`; no display string becomes a mutation payload. Keep the existing dispatcher/private operational-map path. Status announcements are polite except actionable connection/mutation failure, which is one assertive alert. All Back, Work, concept switch, mode, submit, interrupt, question, and retry controls have stable names and 44×44 hit targets.

- [ ] **Step 6: Run GREEN, regression gates, and review**

Run:

```bash
cd mobile
npx biome check --write src/live-concepts/conversation \
  src/live-concepts/contract.ts src/live-concepts/LiveConceptHost.tsx \
  src/live-concepts/LiveConceptHost.test.tsx
npx vitest run \
  src/live-concepts/conversation/ConversationFrame.test.tsx \
  src/live-concepts/conversation/DockedComposer.test.tsx \
  src/live-concepts/dispatch-live-intent.test.ts \
  src/live-concepts/LiveConceptHost.test.tsx \
  src/state/conversation.test.ts
npm run check
npm run boundary
git diff --check
```

Expected: all exit 0; focused review confirms no second viewport coordinator, no fixture fallback, and no changed dispatcher/store mutation semantics.

- [ ] **Step 7: Commit the shared frame contract**

```bash
git add mobile/src/live-concepts/conversation/contract.ts \
  mobile/src/live-concepts/conversation/ConversationFrame.tsx \
  mobile/src/live-concepts/conversation/ConversationFrame.test.tsx \
  mobile/src/live-concepts/conversation/DockedComposer.tsx \
  mobile/src/live-concepts/conversation/DockedComposer.test.tsx \
  mobile/src/live-concepts/conversation/VirtualTranscript.tsx \
  mobile/src/live-concepts/conversation/conversation-frame.css \
  mobile/src/live-concepts/model.ts \
  mobile/src/live-concepts/contract.ts mobile/src/live-concepts/LiveConceptHost.tsx \
  mobile/src/live-concepts/LiveConceptHost.test.tsx
git commit --only -m "feat(mobile): compose shared conversation frame" -- \
  mobile/src/live-concepts/conversation/contract.ts \
  mobile/src/live-concepts/conversation/ConversationFrame.tsx \
  mobile/src/live-concepts/conversation/ConversationFrame.test.tsx \
  mobile/src/live-concepts/conversation/DockedComposer.tsx \
  mobile/src/live-concepts/conversation/DockedComposer.test.tsx \
  mobile/src/live-concepts/conversation/VirtualTranscript.tsx \
  mobile/src/live-concepts/conversation/conversation-frame.css \
  mobile/src/live-concepts/model.ts \
  mobile/src/live-concepts/contract.ts mobile/src/live-concepts/LiveConceptHost.tsx \
  mobile/src/live-concepts/LiveConceptHost.test.tsx
```

### Task 3: Generalize Timeline into a Variable-Height `VirtualTranscript`

**Files:**
- Create: `mobile/src/components/timeline/VariableHeightVirtualList.tsx`
- Create: `mobile/src/components/timeline/VariableHeightVirtualList.test.tsx`
- Modify: `mobile/src/conversation/paging.ts:16-92`
- Modify: `mobile/src/conversation/paging.test.ts:1-90` all fixed-height/count-growth cases
- Modify: `mobile/src/components/timeline/Timeline.tsx:1-156`
- Modify: `mobile/src/components/timeline/Timeline.test.tsx:1-378`
- Replace: `mobile/src/live-concepts/conversation/VirtualTranscript.tsx` initial adapter
- Create: `mobile/src/live-concepts/conversation/VirtualTranscript.test.tsx`
- Modify after Task 2: `mobile/src/live-concepts/conversation/conversation-frame.css` transcript row/overlay rule block

**Interfaces:**
- Consumes: Task 2 row render callbacks and `ConversationAnchor`.
- Produces: generic `VariableHeightVirtualList<T>` with stable `getItemKey`, family estimate, `measureElement`, overscan six, and clamped range extraction.
- Produces: `captureMeasuredAnchor`, `resolveReconciledAnchor`, and `ANCHOR_TOLERANCE_PX = 2` shared by production `Timeline` and live `VirtualTranscript`.
- Produces: `VirtualTranscriptHandle.captureAnchor()`, `.restoreAnchor(anchor)`, `.scrollToTail()`, and `.focusKey(key)` via `forwardRef`.

The generic boundary is exact:

```ts
export interface VariableHeightVirtualListHandle {
  captureAnchor(): MeasuredAnchor | null;
  restoreAnchor(anchor: MeasuredAnchor): Promise<void>;
  scrollToEnd(behavior: "auto" | "smooth"): void;
  focusKey(key: string): void;
}

export interface VariableHeightVirtualListProps<T> {
  readonly items: readonly T[];
  readonly getItemKey: (item: T) => string;
  readonly estimateSize: (item: T) => number;
  readonly cacheScope: string;
  readonly overscan: 6;
  readonly maxMountedRows: 48;
  readonly onScroll: (metrics: { offset: number; viewport: number; total: number }) => void;
  readonly renderItem: (item: T, index: number) => ReactNode;
}
```

- [ ] **Step 1: Write RED tests for the 500-row ceiling and every anchor transition**

Use deterministic fake `ResizeObserver`, measured row rectangles, and a controllable scroll element. Assert:

```ts
expect(screen.getAllByTestId("virtual-transcript-row").length).toBeLessThanOrEqual(48);
expect(screen.queryAllByRole("article")).not.toHaveLength(500);
expect(capture().itemKey).toBe("item-217");
expect(Math.abs(capture().offsetPx - before.offsetPx)).toBeLessThanOrEqual(2);
```

Cover first measured open at tail, saved anchor precedence, 48 px follow boundary, unseen increments while not following, `New activity`, streaming growth in follow/non-follow modes, marker resize, explicit prepend token rather than count inference, authoritative replacement, surviving/next/previous/tail eviction fallback, Dynamic Type cache invalidation, focus before eviction and restoration, and scrolling to every one of 500 logical keys. Prove `Timeline` and `VirtualTranscript` both invoke the same anchor helper.

- [ ] **Step 2: Run RED**

```bash
cd mobile
npx vitest run \
  src/conversation/paging.test.ts \
  src/components/timeline/VariableHeightVirtualList.test.tsx \
  src/components/timeline/Timeline.test.tsx \
  src/live-concepts/conversation/VirtualTranscript.test.tsx
```

Expected: FAIL because the generic variable-height list is missing and current `Timeline` infers prepend from item-count growth using one estimated height.

- [ ] **Step 3: Replace fixed-height paging with stable measured anchors**

```ts
export interface MeasuredAnchor {
  readonly key: string;
  readonly offsetPx: number;
  readonly priorIndex: number;
}
export const ANCHOR_TOLERANCE_PX = 2;

export function resolveReconciledAnchor(
  previousKeys: readonly string[],
  nextKeys: readonly string[],
  anchor: MeasuredAnchor,
): string | null {
  if (nextKeys.includes(anchor.key)) return anchor.key;
  for (let i = anchor.priorIndex; i < previousKeys.length; i += 1) {
    const key = previousKeys[i];
    if (key && nextKeys.includes(key)) return key;
  }
  for (let i = anchor.priorIndex - 1; i >= 0; i -= 1) {
    const key = previousKeys[i];
    if (key && nextKeys.includes(key)) return key;
  }
  return nextKeys.at(-1) ?? null;
}
```

Capture the first intersecting virtual row's stable key and `row.start - scrollOffset`. Restoration calls virtualizer `scrollToIndex`, waits for its measured change notification rather than a fixed flush, then adjusts by measured offset. Prepend is keyed by an explicit pending load generation from `loadOlder`, never array length.

- [ ] **Step 4: Build one measured virtual list**

Configure `useVirtualizer` with stable keys, `measureElement`, and a `ResizeObserver`. Use `defaultRangeExtractor` plus a deterministic clamp around the visible range so overscan is six but returned mounted indexes never exceed 48. Cache measurements by `{threadKey,textScale,itemKey}`. The family estimate function is exact:

```ts
export function estimateDisplayRow(item: ConversationDisplayItem): number {
  if ("semanticKind" in item) return 56;
  if (item.sourceKind === "user") return 96;
  if (item.sourceKind === "assistant") return 128;
  return 160;
}
```

Expose logical `aria-posinset=index+1` and `aria-setsize=items.length` on mounted rows. Never use array index as React or virtualizer identity.

- [ ] **Step 5: Implement follow, unseen, streaming, replacement, focus, and Dynamic Type**

Initial tail happens after the first measurement notification. Follow is `totalSize - (scrollOffset + viewportHeight) <= 48`. When not following, capture/restore around every size or item snapshot change and increment unseen by new logical keys. Before a focused row can leave the mounted range, move DOM focus to the feed, retain the logical key, and restore only when `focusKey` intentionally revisits it. On text-scale change capture, clear that thread/scale cache, remeasure, and restore.

- [ ] **Step 6: Migrate production Timeline to the same mechanics**

`Timeline.tsx` supplies `item.id`, existing row renderer, load-older callback, follow/unseen callbacks, and its estimate through `VariableHeightVirtualList`. Delete the old `itemCountRef`, render-phase `scrollTop` writes, queue-microtask tail write, and `pendingPrependCount * estimateItemHeight` logic.

- [ ] **Step 7: Run GREEN and review**

```bash
cd mobile
npx biome check --write src/conversation/paging.ts src/conversation/paging.test.ts \
  src/components/timeline/VariableHeightVirtualList.tsx \
  src/components/timeline/VariableHeightVirtualList.test.tsx \
  src/components/timeline/Timeline.tsx src/components/timeline/Timeline.test.tsx \
  src/live-concepts/conversation/VirtualTranscript.tsx \
  src/live-concepts/conversation/VirtualTranscript.test.tsx \
  src/live-concepts/conversation/conversation-frame.css
npx vitest run src/conversation/paging.test.ts \
  src/components/timeline/VariableHeightVirtualList.test.tsx \
  src/components/timeline/Timeline.test.tsx \
  src/live-concepts/conversation/VirtualTranscript.test.tsx
npm run check
git diff --check
```

Expected: all exit 0 and the deterministic 500-item case mounts at most 48 rows. Independent review checks no timing sleeps/fixed flush counts, render-phase DOM writes, index keys, or second prepend algorithm.

- [ ] **Step 8: Commit the shared variable-height mechanics**

```bash
git add mobile/src/conversation/paging.ts mobile/src/conversation/paging.test.ts \
  mobile/src/components/timeline/VariableHeightVirtualList.tsx \
  mobile/src/components/timeline/VariableHeightVirtualList.test.tsx \
  mobile/src/components/timeline/Timeline.tsx \
  mobile/src/components/timeline/Timeline.test.tsx \
  mobile/src/live-concepts/conversation/VirtualTranscript.tsx \
  mobile/src/live-concepts/conversation/VirtualTranscript.test.tsx \
  mobile/src/live-concepts/conversation/conversation-frame.css
git commit --only -m "feat(mobile): virtualize shared conversation transcript" -- \
  mobile/src/conversation/paging.ts mobile/src/conversation/paging.test.ts \
  mobile/src/components/timeline/VariableHeightVirtualList.tsx \
  mobile/src/components/timeline/VariableHeightVirtualList.test.tsx \
  mobile/src/components/timeline/Timeline.tsx \
  mobile/src/components/timeline/Timeline.test.tsx \
  mobile/src/live-concepts/conversation/VirtualTranscript.tsx \
  mobile/src/live-concepts/conversation/VirtualTranscript.test.tsx \
  mobile/src/live-concepts/conversation/conversation-frame.css
```

### Task 4: Add Shared Activity/Evidence Markers and Sheet

**Files:**
- Create: `mobile/src/live-concepts/conversation/ActivityEvidenceSheet.tsx`
- Create: `mobile/src/live-concepts/conversation/ActivityEvidenceSheet.test.tsx`
- Modify after Task 2: `mobile/src/live-concepts/conversation/ConversationFrame.tsx` `ConversationFrame` sheet state/composition
- Modify after Task 2: `mobile/src/live-concepts/conversation/ConversationFrame.test.tsx` `ConversationFrame` marker/detail cases
- Modify after Task 3: `mobile/src/live-concepts/conversation/VirtualTranscript.tsx` `VirtualTranscript` marker trigger/focus handoff
- Modify after Task 2: `mobile/src/live-concepts/conversation/conversation-frame.css` modal/locked-feed/evidence rule block

**Interfaces:**
- Consumes: bounded `EvidenceDisplayItem[]`, marker `evidenceKey`, Task 3 transcript handle, and skin marker rendering.
- Produces: `ActivityEvidenceSheet({evidence, triggerKey, onClose}): ReactElement` and `EvidenceSheetState { evidenceKey: string; triggerKey: string; anchor: ConversationAnchor } | null`.
- Produces: exact `Show activity`, `Show evidence`, and `Close activity and evidence` controls.

```ts
export interface ActivityEvidenceSheetProps {
  readonly open: boolean;
  readonly evidence: EvidenceDisplayItem | null;
  readonly triggerKey: string | null;
  readonly onClose: () => void;
  readonly onTriggerUnavailable: () => void;
}
```

- [ ] **Step 1: Write failing safe-detail, scroll-lock, virtualization, and focus tests**

Open a safe tool marker and assert detail was absent before activation, the sheet is a modal dialog, background feed is locked, focus enters the heading/first close action, and close restores marker key plus offset. Open a hidden-prelude marker and assert it has no disclosure control and the raw sentinel remains absent. Create 80 evidence sections and assert no more than 48 evidence row containers. Evict the trigger while open and assert close focuses the feed rather than a detached node.

- [ ] **Step 2: Run RED**

```bash
cd mobile
npx vitest run \
  src/live-concepts/conversation/ActivityEvidenceSheet.test.tsx \
  src/live-concepts/conversation/ConversationFrame.test.tsx \
  src/live-concepts/conversation/VirtualTranscript.test.tsx
```

Expected: FAIL because the shared sheet does not exist.

- [ ] **Step 3: Compose the shared portal/focus primitive and enforce one active scroller**

Render the existing `mobile/src/ui/Sheet.tsx` for its production portal, modal role, Escape handling, Tab trap, and normal focus restoration; do not build a second dialog/history/focus implementation. Resolve only `evidenceKey` against the current bounded evidence array. Missing/stale keys close without source fallback. Capture transcript anchor before opening; set `aria-hidden`/`inert` only for interaction lock, never as a data-safety mechanism. While open, transcript has `overflow-y: hidden` and sheet detail has `data-page-scroll-owner="true"`; on close, restore transcript ownership and anchor, then let `Sheet` restore the triggering marker when still mounted or invoke `onTriggerUnavailable` to focus the feed.

- [ ] **Step 4: Render bounded sections and shared marker actions**

Use plain escaped text for every evidence section; do not pass evidence through Markdown. Virtualize only when section count exceeds 50, with the shared 48-row ceiling. Render state text in addition to color. The sheet title/sections are already bounded; no component accepts a raw detail string.

- [ ] **Step 5: Run GREEN and review**

```bash
cd mobile
npx biome check --write \
  src/live-concepts/conversation/ActivityEvidenceSheet.tsx \
  src/live-concepts/conversation/ActivityEvidenceSheet.test.tsx \
  src/live-concepts/conversation/ConversationFrame.tsx \
  src/live-concepts/conversation/ConversationFrame.test.tsx \
  src/live-concepts/conversation/VirtualTranscript.tsx \
  src/live-concepts/conversation/conversation-frame.css
npx vitest run src/live-concepts/conversation/ActivityEvidenceSheet.test.tsx \
  src/live-concepts/conversation/ConversationFrame.test.tsx \
  src/live-concepts/conversation/VirtualTranscript.test.tsx
npm run check
git diff --check
```

Expected: all exit 0; reviewer confirms the sheet cannot resolve raw source objects and exactly one content scroller remains active.

- [ ] **Step 6: Commit the shared evidence surface**

```bash
git add mobile/src/live-concepts/conversation/ActivityEvidenceSheet.tsx \
  mobile/src/live-concepts/conversation/ActivityEvidenceSheet.test.tsx \
  mobile/src/live-concepts/conversation/ConversationFrame.tsx \
  mobile/src/live-concepts/conversation/ConversationFrame.test.tsx \
  mobile/src/live-concepts/conversation/VirtualTranscript.tsx \
  mobile/src/live-concepts/conversation/conversation-frame.css
git commit --only -m "feat(mobile): add bounded activity evidence sheet" -- \
  mobile/src/live-concepts/conversation/ActivityEvidenceSheet.tsx \
  mobile/src/live-concepts/conversation/ActivityEvidenceSheet.test.tsx \
  mobile/src/live-concepts/conversation/ConversationFrame.tsx \
  mobile/src/live-concepts/conversation/ConversationFrame.test.tsx \
  mobile/src/live-concepts/conversation/VirtualTranscript.tsx \
  mobile/src/live-concepts/conversation/conversation-frame.css
```

### Task 5: Migrate Stillwater to a Conversation Skin

**Files:**
- Create: `mobile/src/live-concepts/stillwater/ConversationSkin.tsx`
- Create: `mobile/src/live-concepts/stillwater/ConversationSkin.test.tsx`
- Delete: `mobile/src/live-concepts/stillwater/ConversationView.tsx:1-407`
- Modify: `mobile/src/live-concepts/stillwater/StillwaterRenderer.tsx:40-67` (remove conversation ownership)
- Modify: `mobile/src/live-concepts/stillwater/StillwaterRenderer.test.tsx:1-1122` conversation cases
- Modify: `mobile/src/live-concepts/stillwater/index.ts:1-9` module registration
- Modify: `mobile/src/live-concepts/stillwater/stillwater.css:1-29,124-237,279-311,423-748`
- Modify: `mobile/src/live-concepts/registry.test.ts:1-18` migrated-skin assertion
- Modify: `mobile/src/live-concepts/LiveConceptHost.test.tsx:1-966` Stillwater shared-frame route

**Interfaces:**
- Consumes: frozen `ConversationSkin` and bounded row render props only.
- Produces: `stillwaterConversationSkin: ConversationSkin` and a module registration that lets `LiveConceptHost` select the shared frame for Stillwater.

- [ ] **Step 1: Write the Stillwater skin RED**

Assert Stillwater keeps restrained forest tokens, grouped light surfaces, full-width assistant prose, trailing compact user treatment, quiet hairline markers, title/status-first chrome, and no structural ownership:

```ts
expect(stillwaterConversationSkin.id).toBe("stillwater");
expect(rendered.container.querySelector(".live-conversation-frame")).not.toBeNull();
expect(rendered.container.querySelectorAll("[data-page-scroll-owner='true']")).toHaveLength(1);
expect(stillwaterSource).not.toMatch(/items\.map|useVirtualizer|<textarea|overflowY/);
```

Exercise all shared composer/question/evidence actions through the host with `concept="stillwater"`; the skin test asserts only visual class output and decorative nodes are `aria-hidden`.

- [ ] **Step 2: Run RED**

```bash
cd mobile
npx vitest run src/live-concepts/stillwater/ConversationSkin.test.tsx \
  src/live-concepts/stillwater/StillwaterRenderer.test.tsx \
  src/live-concepts/LiveConceptHost.test.tsx src/live-concepts/registry.test.ts
```

Expected: FAIL because `ConversationSkin.tsx` is absent and the renderer still owns the conversation page.

- [ ] **Step 3: Implement the skin and delete the old page**

Export one object:

```ts
export const stillwaterConversationSkin: ConversationSkin = {
  id: "stillwater",
  className: "concept-stillwater sw-conversation-skin",
  composerAppearance: { density: "comfortable", accent: "forest" },
  renderConversationChrome: (props) => <StillwaterConversationChrome {...props} />,
  renderNarrativeItem: (props) => <StillwaterNarrativeItem {...props} />,
  renderActivityMarker: (props) => <StillwaterActivityMarker {...props} />,
};
```

Use existing assistant Markdown sanitizer/link policy for assistant rows only; every other field renders as React text. Delete the old item loop, tool disclosure, question card, mutation/error blocks, relative composer, and conversation summary/actions from `ConversationView.tsx`. Remove conversation-only CSS including `--visual-viewport-height`, `.sw-transcript`, `.sw-composer`, and conversation shell spacing; keep tokens plus Sessions/Work rules. Shared frame owns Back, Work, concept switch, status, and composer placement.

- [ ] **Step 4: Run GREEN, inspect at three type sizes, and review**

```bash
cd mobile
npx biome check --write src/live-concepts/stillwater \
  src/live-concepts/LiveConceptHost.test.tsx src/live-concepts/registry.test.ts
npx vitest run src/live-concepts/stillwater/ConversationSkin.test.tsx \
  src/live-concepts/stillwater/StillwaterRenderer.test.tsx \
  src/live-concepts/conversation/ConversationFrame.test.tsx \
  src/live-concepts/LiveConceptHost.test.tsx src/live-concepts/registry.test.ts
npm run check
npm run boundary
git diff --check
```

Expected: all exit 0 and reviewer finds no Stillwater-owned conversation shell/scroller/composer/virtualizer.

- [ ] **Step 5: Commit the Stillwater migration**

```bash
git add mobile/src/live-concepts/stillwater/ConversationSkin.tsx \
  mobile/src/live-concepts/stillwater/ConversationSkin.test.tsx \
  mobile/src/live-concepts/stillwater/ConversationView.tsx \
  mobile/src/live-concepts/stillwater/StillwaterRenderer.tsx \
  mobile/src/live-concepts/stillwater/StillwaterRenderer.test.tsx \
  mobile/src/live-concepts/stillwater/index.ts \
  mobile/src/live-concepts/stillwater/stillwater.css \
  mobile/src/live-concepts/registry.test.ts \
  mobile/src/live-concepts/LiveConceptHost.test.tsx
git commit --only -m "feat(mobile): migrate Stillwater conversation skin" -- \
  mobile/src/live-concepts/stillwater/ConversationSkin.tsx \
  mobile/src/live-concepts/stillwater/ConversationSkin.test.tsx \
  mobile/src/live-concepts/stillwater/ConversationView.tsx \
  mobile/src/live-concepts/stillwater/StillwaterRenderer.tsx \
  mobile/src/live-concepts/stillwater/StillwaterRenderer.test.tsx \
  mobile/src/live-concepts/stillwater/index.ts \
  mobile/src/live-concepts/stillwater/stillwater.css \
  mobile/src/live-concepts/registry.test.ts \
  mobile/src/live-concepts/LiveConceptHost.test.tsx
```

### Task 6: Migrate Constellation Without Structural or Motion Exceptions

**Files:**
- Create: `mobile/src/live-concepts/constellation/ConversationSkin.tsx`
- Create: `mobile/src/live-concepts/constellation/ConversationSkin.test.tsx`
- Delete: `mobile/src/live-concepts/constellation/ConversationView.tsx:1-412`
- Modify: `mobile/src/live-concepts/constellation/ConstellationRenderer.tsx:7-24`
- Modify: `mobile/src/live-concepts/constellation/ConstellationShell.tsx:16-96`
- Modify: `mobile/src/live-concepts/constellation/ConstellationRenderer.test.tsx:1-1436`
- Modify: `mobile/src/live-concepts/constellation/index.ts:1-9`
- Modify: `mobile/src/live-concepts/constellation/constellation.css:1-170,540-940`
- Modify: `mobile/src/live-concepts/registry.test.ts:1-18`
- Modify: `mobile/src/live-concepts/LiveConceptHost.test.tsx:1-966`

**Interfaces:**
- Consumes: the exact Task 5 skin contract; adds no properties or frame branches.
- Produces: `constellationConversationSkin: ConversationSkin` with decoration-only motifs.

- [ ] **Step 1: Write RED tests for visual identity and motion containment**

Assert dark technical field, luminous state accents, compact telemetry markers, local star/connection motifs behind each row, `pointer-events: none`, and `aria-hidden="true"`. Using a `MutationObserver`, stream three updates and assert the frame's `data-route-motion-generation` remains unchanged. Assert route enter ≤180 ms/16 px, concept crossfade ≤120 ms/no lateral translation, and reduced-motion computed duration is 0.

- [ ] **Step 2: Run RED**

```bash
cd mobile
npx vitest run src/live-concepts/constellation/ConversationSkin.test.tsx \
  src/live-concepts/constellation/ConstellationRenderer.test.tsx \
  src/live-concepts/LiveConceptHost.test.tsx
```

Expected: FAIL because the Constellation renderer still supplies the full page and composer.

- [ ] **Step 3: Implement the same skin interface and delete duplicates**

```ts
export const constellationConversationSkin: ConversationSkin = {
  id: "constellation",
  className: "concept-constellation co-conversation-skin",
  composerAppearance: { density: "compact", accent: "luminous" },
  renderConversationChrome: (props) => <ConstellationConversationChrome {...props} />,
  renderNarrativeItem: (props) => <ConstellationNarrativeItem {...props} />,
  renderActivityMarker: (props) => <ConstellationActivityMarker {...props} />,
};
```

Remove conversation selection from `ConstellationRenderer`, conversation topbar responsibility from `ConstellationShell`, `.co-scroll` ownership for conversation, all old transcript/composer/question/tool rules, and route animations attached below the frame. Keep Sessions/Work shell rules. Decorations stay inside the row render output and never affect measured block size.

- [ ] **Step 4: Run GREEN and motion review**

```bash
cd mobile
npx biome check --write src/live-concepts/constellation \
  src/live-concepts/LiveConceptHost.test.tsx src/live-concepts/registry.test.ts
npx vitest run src/live-concepts/constellation/ConversationSkin.test.tsx \
  src/live-concepts/constellation/ConstellationRenderer.test.tsx \
  src/live-concepts/conversation/VirtualTranscript.test.tsx \
  src/live-concepts/LiveConceptHost.test.tsx src/live-concepts/registry.test.ts
npm run check
npm run boundary
git diff --check
```

Expected: all exit 0; review confirms no decoration participates in geometry/accessibility and no transcript update replays route motion.

- [ ] **Step 5: Commit the Constellation migration**

```bash
git add mobile/src/live-concepts/constellation/ConversationSkin.tsx \
  mobile/src/live-concepts/constellation/ConversationSkin.test.tsx \
  mobile/src/live-concepts/constellation/ConversationView.tsx \
  mobile/src/live-concepts/constellation/ConstellationRenderer.tsx \
  mobile/src/live-concepts/constellation/ConstellationShell.tsx \
  mobile/src/live-concepts/constellation/ConstellationRenderer.test.tsx \
  mobile/src/live-concepts/constellation/index.ts \
  mobile/src/live-concepts/constellation/constellation.css \
  mobile/src/live-concepts/registry.test.ts \
  mobile/src/live-concepts/LiveConceptHost.test.tsx
git commit --only -m "feat(mobile): migrate Constellation conversation skin" -- \
  mobile/src/live-concepts/constellation/ConversationSkin.tsx \
  mobile/src/live-concepts/constellation/ConversationSkin.test.tsx \
  mobile/src/live-concepts/constellation/ConversationView.tsx \
  mobile/src/live-concepts/constellation/ConstellationRenderer.tsx \
  mobile/src/live-concepts/constellation/ConstellationShell.tsx \
  mobile/src/live-concepts/constellation/ConstellationRenderer.test.tsx \
  mobile/src/live-concepts/constellation/index.ts \
  mobile/src/live-concepts/constellation/constellation.css \
  mobile/src/live-concepts/registry.test.ts \
  mobile/src/live-concepts/LiveConceptHost.test.tsx
```

### Task 7: Migrate Field Notes with Row-Local Chronology

**Files:**
- Create: `mobile/src/live-concepts/field-notes/ConversationSkin.tsx`
- Create: `mobile/src/live-concepts/field-notes/ConversationSkin.test.tsx`
- Delete: `mobile/src/live-concepts/field-notes/ConversationView.tsx:1-446`
- Modify: `mobile/src/live-concepts/field-notes/FieldNotesRenderer.tsx:24-60` conversation branch
- Modify: `mobile/src/live-concepts/field-notes/FieldNotesShell.tsx:7-20,42-149`
- Modify: `mobile/src/live-concepts/field-notes/FieldNotesRenderer.test.tsx:1-1264`
- Modify: `mobile/src/live-concepts/field-notes/index.ts:1-9`
- Modify: `mobile/src/live-concepts/field-notes/field-notes.css:1-190,540-1120`
- Modify: `mobile/src/live-concepts/registry.test.ts:1-18`
- Modify: `mobile/src/live-concepts/LiveConceptHost.test.tsx:1-966`

**Interfaces:**
- Consumes: unchanged `ConversationSkin` and `sequence` from bounded items.
- Produces: `fieldNotesConversationSkin: ConversationSkin`; every chronology segment is a descendant of one virtual row.

- [ ] **Step 1: Write RED tests for row-local chronology and anchor accuracy**

For every mounted row, require one local marker/segment and reject any rail whose containing block is the transcript spacer:

```ts
for (const row of screen.getAllByTestId("virtual-transcript-row")) {
  expect(row.querySelector("[data-chronology-segment='row-local']")).not.toBeNull();
}
expect(container.querySelector("[data-chronology-rail='document']")).toBeNull();
```

Measure short/long/editorial rows, prepend, and switch away/back; assert the same item key and ≤2 px offset. Verify warm paper, editorial contrast, margin labels, ruled motifs, and sequence labels remain decorative or bounded descriptions rather than AX names.

- [ ] **Step 2: Run RED**

```bash
cd mobile
npx vitest run src/live-concepts/field-notes/ConversationSkin.test.tsx \
  src/live-concepts/field-notes/FieldNotesRenderer.test.tsx \
  src/live-concepts/conversation/VirtualTranscript.test.tsx \
  src/live-concepts/LiveConceptHost.test.tsx
```

Expected: FAIL because the existing `data-chronology-rail` spans the full mapped transcript and the concept still owns its composer.

- [ ] **Step 3: Implement row-local skin and delete old geometry**

```ts
export const fieldNotesConversationSkin: ConversationSkin = {
  id: "field-notes",
  className: "concept-field-notes fn-conversation-skin",
  composerAppearance: { density: "comfortable", accent: "rust" },
  renderConversationChrome: (props) => <FieldNotesConversationChrome {...props} />,
  renderNarrativeItem: (props) => <FieldNotesNarrativeItem {...props} />,
  renderActivityMarker: (props) => <FieldNotesActivityMarker {...props} />,
};
```

Each narrative/marker root includes its own pseudo-element or child for the chronology segment. Delete the full-transcript rail, old mapped items, question/status/composer markup, `--visual-viewport-height`, keyboard-padding calculation, and conversation scroll owner. Keep Sessions/Work editorial shell behavior.

- [ ] **Step 4: Run GREEN and chronology review**

```bash
cd mobile
npx biome check --write src/live-concepts/field-notes \
  src/live-concepts/LiveConceptHost.test.tsx src/live-concepts/registry.test.ts
npx vitest run src/live-concepts/field-notes/ConversationSkin.test.tsx \
  src/live-concepts/field-notes/FieldNotesRenderer.test.tsx \
  src/live-concepts/conversation/VirtualTranscript.test.tsx \
  src/live-concepts/LiveConceptHost.test.tsx src/live-concepts/registry.test.ts
npm run check
npm run boundary
git diff --check
```

Expected: all exit 0; reviewer confirms there is no document-height chronology element and variable row measurement remains concept-neutral.

- [ ] **Step 5: Commit the Field Notes migration**

```bash
git add mobile/src/live-concepts/field-notes/ConversationSkin.tsx \
  mobile/src/live-concepts/field-notes/ConversationSkin.test.tsx \
  mobile/src/live-concepts/field-notes/ConversationView.tsx \
  mobile/src/live-concepts/field-notes/FieldNotesRenderer.tsx \
  mobile/src/live-concepts/field-notes/FieldNotesShell.tsx \
  mobile/src/live-concepts/field-notes/FieldNotesRenderer.test.tsx \
  mobile/src/live-concepts/field-notes/index.ts \
  mobile/src/live-concepts/field-notes/field-notes.css \
  mobile/src/live-concepts/registry.test.ts \
  mobile/src/live-concepts/LiveConceptHost.test.tsx
git commit --only -m "feat(mobile): migrate Field Notes conversation skin" -- \
  mobile/src/live-concepts/field-notes/ConversationSkin.tsx \
  mobile/src/live-concepts/field-notes/ConversationSkin.test.tsx \
  mobile/src/live-concepts/field-notes/ConversationView.tsx \
  mobile/src/live-concepts/field-notes/FieldNotesRenderer.tsx \
  mobile/src/live-concepts/field-notes/FieldNotesShell.tsx \
  mobile/src/live-concepts/field-notes/FieldNotesRenderer.test.tsx \
  mobile/src/live-concepts/field-notes/index.ts \
  mobile/src/live-concepts/field-notes/field-notes.css \
  mobile/src/live-concepts/registry.test.ts \
  mobile/src/live-concepts/LiveConceptHost.test.tsx
```

### Task 8: Make Stable UI State and Structural Boundaries Final

**Files:**
- Modify: `mobile/src/live-concepts/contract.ts:52-65,82-113`
- Modify: `mobile/src/live-concepts/live-ui-store.ts:22-112`
- Modify: `mobile/src/live-concepts/live-ui-store.test.ts:1-194`
- Modify: `mobile/src/live-concepts/LiveConceptHost.tsx:171-182,288-410,441-544`
- Modify: `mobile/src/live-concepts/LiveConceptHost.test.tsx:1-966`
- Modify: `mobile/src/live-concepts/registry.ts:1-11` and `registry.test.ts:1-18`
- Modify: `mobile/src/live-concepts/ConceptSwitcher.tsx:1-209` and `.test.tsx:1-227` focus/crossfade state
- Modify after Task 2: `mobile/src/live-concepts/conversation/ConversationFrame.tsx` `ConversationFrame` state callback block
- Modify: `mobile/scripts/check-live-concepts-boundary.mjs:19-54,116-223`
- Modify: `mobile/scripts/check-live-concepts-boundary.test.mjs:1-165`
- Modify: `mobile/src/screens/RootShell.test.tsx:1-1850` no reconnect/reread/profile-regression cases

**Interfaces:**
- Consumes: all three required skins and Task 3 `ConversationAnchor`.
- Produces: `ConversationUiMemory` keyed by exact nested `{concept, threadKey}` maps, not delimiter strings.
- Produces: required `LiveConceptModule.conversationSkin: ConversationSkin`; removes the migration-only fallback permanently.

- [ ] **Step 1: Write RED tests for complete switch preservation and boundary rejection**

Populate anchor key+offset/following, production draft, composer mode, pending mutation, all question drafts, open evidence key, triggering marker, focused logical key, unseen count, and disclosure state. Switch Stillwater → Constellation → Field Notes → Stillwater and assert exact equality, no new `thread/read`, no AppWire reconnect/resubscribe, and concept-specific anchors with shared-key fallback. Reset profile scope and assert all thread-bound UI clears while persisted concept remains.

Boundary fixtures must fail for each of these exact imports/patterns in `ConversationSkin.tsx` or conversation-only CSS selectors: `@tanstack/react-virtual`, `MobileTimelineItem`, conversation store/service, `items.map(`, `<textarea`, fixed/sticky composer, active conversation `overflow-y`, `data-live-concept-scroller`, `--visual-viewport-height`, or a module missing `conversationSkin`. Sessions/Work may retain their existing list mapping and shell scrolling; the guard must parse selector/file scope rather than banning those unrelated surfaces.

- [ ] **Step 2: Run RED**

```bash
cd mobile
npx vitest run src/live-concepts/live-ui-store.test.ts \
  src/live-concepts/LiveConceptHost.test.tsx \
  src/live-concepts/ConceptSwitcher.test.tsx \
  src/screens/RootShell.test.tsx
node --test scripts/check-live-concepts-boundary.test.mjs
```

Expected: FAIL because current anchors are absolute `{scrollTop}`, evidence/unseen state is absent, and `conversationSkin` remains optional during migration.

- [ ] **Step 3: Implement nested stable UI memory and exact actions**

```ts
export interface ConversationUiMemory {
  readonly anchor: ConversationAnchor | null;
  readonly unseen: number;
  readonly evidenceKey: string | null;
  readonly evidenceTriggerKey: string | null;
  readonly focusedItemKey: string | null;
  readonly expandedEvidenceKeys: ReadonlySet<string>;
}

interface LiveConceptUiStore extends LiveConceptUiState {
  setConversationAnchor(concept: ConceptId, threadKey: string, anchor: ConversationAnchor): void;
  setConversationUnseen(concept: ConceptId, threadKey: string, count: number): void;
  setEvidenceState(concept: ConceptId, threadKey: string, evidenceKey: string | null, triggerKey: string | null): void;
  setConversationFocus(concept: ConceptId, threadKey: string, itemKey: string | null): void;
}
```

Use nested `Map<ConceptId, Map<string, ConversationUiMemory>>` internally and immutable snapshots externally. On switch, capture outgoing first-visible key+offset; incoming preference order is its saved anchor, outgoing shared item key at the same offset, then tail. Remove the document query and absolute `scrollTop` effect at `LiveConceptHost.tsx:507-531`.

- [ ] **Step 4: Complete the one-path host cutover and static guard**

Make `conversationSkin` required in `LiveConceptModule`. `LiveConceptHost` always renders `ConversationFrame` for `surface === "conversation"`; concept renderers receive only Sessions/Work. Remove all migration fallback. Derive phase from production `ConversationStatus` plus last good snapshot without replacing production store ownership. On projection error retain the previous bounded snapshot for the same `{profileEpoch,ref}` only; reject older generations.

The boundary script parses source files after stripping comments and fails with exact path/rule diagnostics. It also proves `platformPresentation.ts` is the only live source that listens to `visualViewport` or writes the two production variables.

- [ ] **Step 5: Run GREEN and preserved behavior gates**

```bash
cd mobile
npx biome check --write src/live-concepts src/screens/RootShell.test.tsx \
  scripts/check-live-concepts-boundary.mjs \
  scripts/check-live-concepts-boundary.test.mjs
npx vitest run src/live-concepts/live-ui-store.test.ts \
  src/live-concepts/LiveConceptHost.test.tsx \
  src/live-concepts/ConceptSwitcher.test.tsx \
  src/live-concepts/dispatch-live-intent.test.ts \
  src/state/conversation.test.ts \
  src/screens/RootShell.test.tsx \
  src/screens/production-services.test.ts
node --test scripts/check-live-concepts-boundary.test.mjs
npm run boundary
npm run check
git diff --check
```

Expected: all exit 0. Independent review compares profile/AppWire ownership paths against the pre-task diff and confirms only presentation subscriptions changed; no pairing, AppWire transport, profile lease, or generated Apple source changed.

- [ ] **Step 6: Commit final structural cutover**

```bash
git add mobile/src/live-concepts/contract.ts \
  mobile/src/live-concepts/live-ui-store.ts mobile/src/live-concepts/live-ui-store.test.ts \
  mobile/src/live-concepts/LiveConceptHost.tsx mobile/src/live-concepts/LiveConceptHost.test.tsx \
  mobile/src/live-concepts/registry.ts mobile/src/live-concepts/registry.test.ts \
  mobile/src/live-concepts/ConceptSwitcher.tsx mobile/src/live-concepts/ConceptSwitcher.test.tsx \
  mobile/src/live-concepts/conversation/ConversationFrame.tsx \
  mobile/scripts/check-live-concepts-boundary.mjs \
  mobile/scripts/check-live-concepts-boundary.test.mjs \
  mobile/src/screens/RootShell.test.tsx
git commit --only -m "refactor(mobile): enforce one live conversation frame" -- \
  mobile/src/live-concepts/contract.ts \
  mobile/src/live-concepts/live-ui-store.ts mobile/src/live-concepts/live-ui-store.test.ts \
  mobile/src/live-concepts/LiveConceptHost.tsx mobile/src/live-concepts/LiveConceptHost.test.tsx \
  mobile/src/live-concepts/registry.ts mobile/src/live-concepts/registry.test.ts \
  mobile/src/live-concepts/ConceptSwitcher.tsx mobile/src/live-concepts/ConceptSwitcher.test.tsx \
  mobile/src/live-concepts/conversation/ConversationFrame.tsx \
  mobile/scripts/check-live-concepts-boundary.mjs \
  mobile/scripts/check-live-concepts-boundary.test.mjs \
  mobile/src/screens/RootShell.test.tsx
```

### Task 9: Prove the Actual Host in Browser, AppWire, and iOS Simulator

**Files:**
- Create: `mobile/src/test/live-conversation-pathological.fixture.ts`
- Create: `mobile/src/test/live-conversation-pathological.fixture.test.ts`
- Create: `mobile/src/test/LiveConceptBrowserHarness.tsx`
- Create: `mobile/src/test/live-conversation-browser-entry.tsx`
- Create: `mobile/live-conversation-harness.html`
- Create: `mobile/scripts/browserguard.vite.config.mjs`
- Create: `mobile/scripts/live-conversation-geometry.mjs`
- Create: `mobile/scripts/live-conversation-geometry.test.mjs`
- Modify: `mobile/src/test/live-concepts-real-bridge.test.tsx:548-655,1476-1975`
- Modify: `mobile/scripts/smoke-live-concepts.mjs:18-91,487-568,570-603,1483-1726,1929-end`
- Modify: `mobile/scripts/smoke-live-concepts.test.mjs:1-2051` milestone/schema/simulator cases
- Modify: `mobile/package.json:6-17`

**Interfaces:**
- Consumes: actual `LiveConceptHost`, real production Zustand stores, real intent dispatcher/projectors, scripted external service boundary, existing repository `browserGuardProcess.mjs`/`browserGuardCdp.mjs`, and current semantic smoke safety/identity contracts.
- Produces: `makePathological39ItemFixture()`, `makeVariableHeight500ItemFixture()`, and scenario actions for prepend/replacement/eviction/stream growth.
- Produces: `npm run test:live-conversation-browser` and simulator-capable `npm run smoke:live-concepts` evidence.

```ts
export interface PathologicalConversationFixture {
  readonly conversation: MobileConversation;
  readonly rawSystemPrelude: string;
  readonly rawSystemSentinel: string;
}
export function makePathological39ItemFixture(): PathologicalConversationFixture;
export function makeVariableHeight500ItemFixture(): PathologicalConversationFixture;
```

- [ ] **Step 1: Write fixture and runner contract RED tests**

The fixture test asserts exact counts and byte length:

```ts
const pathological = makePathological39ItemFixture();
expect(pathological.conversation.items).toHaveLength(39);
expect(new TextEncoder().encode(pathological.rawSystemPrelude)).toHaveLength(44_700);
expect(makeVariableHeight500ItemFixture().conversation.items).toHaveLength(500);
```

The Node runner contract rejects missing matrix axes, duplicate case IDs, geometry JSON without raw-system absence/AX counts/DOM count/anchor values, screenshots outside its output root, a representative-HTML renderer, and any origin other than its private loopback Vite port.

- [ ] **Step 2: Run RED**

```bash
cd mobile
npx vitest run src/test/live-conversation-pathological.fixture.test.ts
node --test scripts/live-conversation-geometry.test.mjs
```

Expected: FAIL because the pathological fixtures and real-browser runner do not exist.

- [ ] **Step 3: Build the test-only actual-host entry**

`LiveConceptBrowserHarness` constructs the same production store types and `LiveConceptHost` used by `RootShell`; only network/native boundaries are scripted. It must not render concept components directly. Query parameters select concept, fixture, type scale, theme, reduced motion, safe area, and keyboard height. Expose test actions through DOM buttons (`Harness prepend`, `Harness replace`, `Harness evict`, `Harness stream`) outside the captured app subtree and mark the harness controls inaccessible/inert during AX counts.

`browserguard.vite.config.mjs` serves `live-conversation-harness.html` from `mobile/` on the private port selected by `startBrowserGuard`; it never changes production `index.html` or app routing.

- [ ] **Step 4: Implement the CDP matrix over computed browser geometry**

Reuse, do not copy, these repository helpers:

```js
import { startBrowserGuard } from "../../cmd/evener-hub/frontend/scripts/browserGuardProcess.mjs";
import {
  applyViewport, assertGuardOrigin, connectPage, evaluate,
  navigateTo, realizedViewport, waitForFonts, waitForHttp,
} from "../../cmd/evener-hub/frontend/scripts/browserGuardCdp.mjs";
```

Run all three concepts × two fixtures × four viewports × three type scales × two themes × two motion modes × two safe areas × keyboard closed/open: exactly 1,152 matrix points. At every point use `getBoundingClientRect`, computed style, DOM/AX serialization, and scripted interactions to assert all 15 criteria: initial composer/action visible at 393×852; document height constant between 39/500; no horizontal overflow; one active scroller; keyboard bottom bound; safe-area once; ≤48 rows; exact mounted semantic counts; 44×44 controls; six-line textarea; motion limits; raw sentinel absent from text/attributes/HTML; ≤2 px anchors after each mutation; every loading/error/offline/reconnect state; and reachable Back, Work, concept switch, load older, send, steer, queue, interrupt, structured-question, evidence, New, Settings, and Voice intents without fixture fallback. Capture screenshots for one portrait, one AX, one keyboard, one landscape, and every failure per concept/fixture; write all geometry rows to JSON.

- [ ] **Step 5: Extend the production AppWire slice without replacing it with fakes**

Script authoritative read, item lifecycle, reasoning, tools, system prelude, warning, resync, tree refresh, load older, send, steer, queue, interrupt, questions, and concept switches through the existing real `AppwireClient`/stdio harness. Assert the bounded display/evidence snapshot updates atomically; `systemMessage` is never user; safe detail appears only after explicit evidence action; Work still exposes tasks, delegates, jobs, watches, usage, and redacted diagnostics; concept switch creates zero new AppWire connections/reads; load older uses cursor without reopen/resubscribe; capability refresh still publishes before action error.

Run the harness build before the test:

```bash
cd mobile
node scripts/build-appwire-harness.mjs
npx vitest run src/test/live-concepts-real-bridge.test.tsx
```

Expected after implementation: PASS with one client connection and unchanged request ownership.

- [ ] **Step 6: Extend semantic smoke evidence and package scripts**

Add geometry fields to observations: visual viewport, composer rect, safe-area values, mounted row count, exact user-label count, raw-system sentinel absence, concept anchor before/after, keyboard overlap, and generation IDs. Keep current staged-app hash, bundle identity, Hub version/protocol, IDB positive-marker, receipt, lifecycle, and fixture-absence checks intact. Add scripts:

```json
{
  "pretest:live-conversation": "node scripts/build-appwire-harness.mjs",
  "test:live-conversation-browser": "node scripts/live-conversation-geometry.mjs",
  "test:live-conversation": "vitest run src/live-concepts/conversation src/test/live-concepts-real-bridge.test.tsx && node --test scripts/live-conversation-geometry.test.mjs",
  "smoke:live-concepts": "node scripts/smoke-live-concepts.mjs"
}
```

- [ ] **Step 7: Run focused GREEN and the real browser gate**

```bash
cd mobile
npx biome check --write src/test src/live-concepts scripts/live-conversation-geometry.mjs \
  scripts/live-conversation-geometry.test.mjs scripts/smoke-live-concepts.mjs \
  scripts/smoke-live-concepts.test.mjs
npx vitest run src/test/live-conversation-pathological.fixture.test.ts \
  src/test/live-concepts-real-bridge.test.tsx
node --test scripts/live-conversation-geometry.test.mjs \
  scripts/smoke-live-concepts.test.mjs
npm run test:live-conversation-browser
npm run check
npm run boundary
npm run build
git diff --check
```

Expected: every command exits 0; browser output reports exactly 1,152 passed points and an absolute scratch evidence directory.

- [ ] **Step 8: Build and run the standard iOS Simulator lane**

Require the existing generated Apple project but do not edit or regenerate it:

```bash
test -d mobile/src-tauri/gen/apple
cd mobile
source "$HOME/.cargo/env"
npx tauri ios build --debug --target aarch64-sim --no-sign --ci
xcrun simctl list devices available --json
SIM_STATE="$(xcrun simctl list devices available --json | \
  node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const j=JSON.parse(s);const all=Object.values(j.devices).flat();const d=all.find(x=>x.udid===process.argv[1]);if(!d)process.exit(2);process.stdout.write(d.state)})' "$IOS_SIM_UDID")"
case "$SIM_STATE" in
  Shutdown) xcrun simctl boot "$IOS_SIM_UDID" ;;
  Booted) : ;;
  *) printf 'unsupported simulator state: %s\n' "$SIM_STATE" >&2; exit 1 ;;
esac
xcrun simctl bootstatus "$IOS_SIM_UDID" -b
```

Select an available standard iPhone 16 Pro simulator and require exactly one fresh `.app` under `src-tauri/gen/apple/build`. Upgrade-install without clearing the configured production app container, then run:

```bash
EVENER_SMOKE_APP_PATH="$SIM_APP_PATH" \
EVENER_SMOKE_THREAD_REF="$PATHOLOGICAL_THREAD_REF" \
EVENER_SMOKE_THREAD_TITLE="$PATHOLOGICAL_THREAD_TITLE" \
node scripts/smoke-live-concepts.mjs \
  --udid "$IOS_SIM_UDID" \
  --bundle-id com.primeradiant.evener \
  --output-dir "$EVENER_SCRATCH_DIR/live-conversation-simulator" \
  --hub-version "$HUB_VERSION"
```

Expected: exit 0 with screenshots, complete AX trees, geometry, DOM-count bridge evidence, all three concepts, load older, send/steer/queue/interrupt, questions, evidence, Work, Back, New, Settings, Voice, rotation, keyboard, Dynamic Type, reduced motion, background/foreground, and reconnect. Simulator success is complete for this lane only; it is not physical-device acceptance.

- [ ] **Step 9: Review browser/simulator artifacts independently**

Confirm `git status --short mobile/src-tauri/gen/apple` is empty and no scratch artifact is tracked. Review geometry JSON independently from fixture construction: inspect browser-produced body/scroll/rect values, not expected constants copied from fixture code.

- [ ] **Step 10: Commit only harness/source files**

```bash
git add mobile/src/test/live-conversation-pathological.fixture.ts \
  mobile/src/test/live-conversation-pathological.fixture.test.ts \
  mobile/src/test/LiveConceptBrowserHarness.tsx \
  mobile/src/test/live-conversation-browser-entry.tsx \
  mobile/live-conversation-harness.html \
  mobile/scripts/browserguard.vite.config.mjs \
  mobile/scripts/live-conversation-geometry.mjs \
  mobile/scripts/live-conversation-geometry.test.mjs \
  mobile/src/test/live-concepts-real-bridge.test.tsx \
  mobile/scripts/smoke-live-concepts.mjs \
  mobile/scripts/smoke-live-concepts.test.mjs mobile/package.json
git commit --only -m "test(mobile): prove live conversation frame geometry" -- \
  mobile/src/test/live-conversation-pathological.fixture.ts \
  mobile/src/test/live-conversation-pathological.fixture.test.ts \
  mobile/src/test/LiveConceptBrowserHarness.tsx \
  mobile/src/test/live-conversation-browser-entry.tsx \
  mobile/live-conversation-harness.html \
  mobile/scripts/browserguard.vite.config.mjs \
  mobile/scripts/live-conversation-geometry.mjs \
  mobile/scripts/live-conversation-geometry.test.mjs \
  mobile/src/test/live-concepts-real-bridge.test.tsx \
  mobile/scripts/smoke-live-concepts.mjs \
  mobile/scripts/smoke-live-concepts.test.mjs mobile/package.json
```

### Task 10: Add Canonical Gates, Run Integration Review, and Close Simulator-Complete Work

**Files:**
- Modify: `make/testing.mk:1,186-end` add the two mobile test targets to the existing testing family
- Modify: `.github/workflows/ci.yml:16-30` add an independent mobile job
- Modify generated target table: `docs/developing-evener/testing.md:996-1017`

**Interfaces:**
- Consumes: `npm run test:live-conversation`, `npm run test:live-conversation-browser`, all current deterministic mobile commands, and root canonical gates documented by `make help`/`docs/developing-evener/testing.md`.
- Produces: `make test-mobile` and `make test-mobile-browser`; CI installs `mobile/package-lock.json` dependencies and runs both in a Chrome-capable job.

- [ ] **Step 1: Run an independent whole-branch review from a clean task boundary**

Give the reviewer the approved spec, this plan, `a06ba218c..HEAD`, the first nine task commits, and browser/simulator evidence. Require Critical/Important/Minor findings with exact lines and explicit judgments on all 15 acceptance criteria, test load-bearingness, redaction before truncation, semantic counts, anchor math, focus eviction, one-scroll ownership, route motion, stale generations, and preservation of pairing/AppWire/profile fixes. A Critical/Important finding sends execution back to the owning Task 1–9 file list and focused RED/GREEN command; commit only that task's named paths with `fix(mobile): address live conversation review`, obtain one scoped re-review, and restart Task 10 from its clean boundary. Do not improvise a broad final-fix file set inside this gate task.

- [ ] **Step 2: Write RED checks for gate discovery and CI ownership**

Run the desired contract directly before adding it:

```bash
make help | grep -F "test-mobile "
make help | grep -F "test-mobile-browser "
rg -n '`make test-mobile`|`make test-mobile-browser`' docs/developing-evener/testing.md
rg -n 'mobile/package-lock.json|make test-mobile test-mobile-browser' .github/workflows/ci.yml
```

Expected: FAIL on the first command because no canonical mobile target exists; the docs and CI searches also have no matches when run separately.

- [ ] **Step 3: Add annotated mobile targets, CI job, and generated docs**

`make/testing.mk` extends its `.PHONY` declaration and uses the repository annotation format with these exact recipes at the end of the testing family:

```make
.PHONY: test-mobile test-mobile-browser

## The deterministic production-mobile gate: type, unit, boundary, build,
## geometry/smoke contracts, and Rust checks.
## proves: Production mobile display/behavior, AppWire harness, config, and Rust source pass without a browser or device.
## trigger: Local pre-merge and required CI mobile job.
## requires: Node/npm dependencies, Rust toolchain, and offline scripted external boundaries; no provider, browser, simulator, or device.
## fails-when: Any npm, Vitest, boundary, build, Node contract, cargo test/check/fmt, or clippy command is nonzero.
test-mobile:
	cd mobile && npm run check && npm test && npm run boundary && npm run build && npm run test:geometry && npm run test:live-conversation && node --test scripts/smoke-live-concepts.test.mjs && cargo test --manifest-path src-tauri/Cargo.toml && cargo check --manifest-path src-tauri/Cargo.toml && cargo fmt --manifest-path src-tauri/Cargo.toml --check && cargo clippy --manifest-path src-tauri/Cargo.toml --all-targets -- -D warnings

## The real-browser production-mobile gate over the actual LiveConceptHost and pathological fixtures.
## proves: Computed CSS geometry, DOM/AX bounds, anchors, keyboard emulation, and motion across the required matrix.
## trigger: Local pre-merge on a Chrome-capable host and required CI mobile job.
## requires: Chrome/Chromium, installed mobile npm dependencies, loopback bind, and a writable private browser profile.
## fails-when: Browser/Vite launch, cleanup, any matrix assertion, screenshot/evidence write, or private-origin check fails.
test-mobile-browser:
	cd mobile && npm run test:live-conversation-browser
```

CI uses a separate Chrome-capable Ubuntu job with Node 26, Rust from the repository toolchain action, `npm ci --prefix mobile`, then `make test-mobile test-mobile-browser`. Do not add browser requirements to default `make test` or `make merge-approval-gate`; the documented browser lane remains separate, as required by testing policy.

```bash
make generate
make lint-generated
make help | grep -F "test-mobile "
make help | grep -F "test-mobile-browser "
rg -n '`make test-mobile`|`make test-mobile-browser`' docs/developing-evener/testing.md
rg -n 'mobile/package-lock.json|make test-mobile test-mobile-browser' .github/workflows/ci.yml
```

Expected: every command exits 0 and the generated testing table carries both targets' full proves/trigger/requires/fails-when contracts.

- [ ] **Step 4: Run all deterministic and browser gates**

```bash
make test-mobile
make test-mobile-browser
cd mobile
npm run check
npm test
npm run boundary
npm run build
npm run test:geometry
node --test scripts/smoke-live-concepts.test.mjs
cargo test --manifest-path src-tauri/Cargo.toml
cargo check --manifest-path src-tauri/Cargo.toml
cargo fmt --manifest-path src-tauri/Cargo.toml --check
cargo clippy --manifest-path src-tauri/Cargo.toml --all-targets -- -D warnings
cd ..
git diff --check
make merge-approval-gate
```

Expected: every command exits 0. `make merge-approval-gate` additionally proves `make lint`, `make build`, `ROOT_FULL=1 make test`, and `make test-dev-tooling` serially. Report simulator and physical lanes separately; only the simulator lane may be marked complete here.

- [ ] **Step 5: Audit all 15 acceptance criteria and repository cleanliness**

Record criterion-to-evidence mapping in the task review report (not source):

1. Task 1 serialization plus Task 9 DOM/AX/screenshot absence.
2. Task 1 exact labels plus Task 9 mounted-window counts.
3. Task 1 warning classification plus all skin rows.
4. Task 4 disclosure lifecycle and hidden/prelude non-disclosure.
5. Task 2 sibling frame plus Task 9 initial 393×852 geometry/document height.
6. Task 2 tokens plus Task 9 keyboard/safe-area measurements.
7. Tasks 2/4 one active scroller plus browser computed styles.
8. Task 3 500-item/48-row proof plus browser reachability.
9. Task 3 follow/stream/prepend/replacement/eviction/Dynamic Type anchors.
10. Task 8 all-concept state and no-network preservation.
11. Tasks 5–7 skins plus Task 9 type/target/overflow matrix.
12. Tasks 6/8 motion generation and browser timing/reduced motion.
13. Task 1 complete bounds/redaction/Unicode suite.
14. Tasks 2/8 dispatcher/store regressions plus AppWire/smoke actions.
15. Tasks 2/8 stable error/reconnect generation states plus AppWire/browser cases.

Then run a source scan proving deleted structures did not return and inspect staged content:

```bash
if rg -n -- "--visual-viewport-height" mobile/src/live-concepts; then exit 1; fi
if rg -n "data-live-concept-scroller|items\.map\(|<textarea|useVirtualizer" \
  mobile/src/live-concepts/stillwater/ConversationSkin.tsx \
  mobile/src/live-concepts/constellation/ConversationSkin.tsx \
  mobile/src/live-concepts/field-notes/ConversationSkin.tsx; then exit 1; fi
if rg -n "@tanstack/react-virtual|MobileTimelineItem|ConversationService" \
  mobile/src/live-concepts/stillwater/ConversationSkin.tsx \
  mobile/src/live-concepts/constellation/ConversationSkin.tsx \
  mobile/src/live-concepts/field-notes/ConversationSkin.tsx; then exit 1; fi
git status --short
git diff --check
git diff --cached --name-only
```

Expected: all three guarded scans produce no matches and the block continues; status contains only Task 10's three named documentation/gate files; the cached list is empty before named staging.

- [ ] **Step 6: Commit canonical gate wiring by exact path**

Stage only the gate files:

```bash
git add make/testing.mk .github/workflows/ci.yml docs/developing-evener/testing.md
git diff --cached --name-only
git commit --only -m "test(mobile): gate shared live conversation frame" -- \
  make/testing.mk .github/workflows/ci.yml docs/developing-evener/testing.md
```

Expected staged paths before commit are exactly `.github/workflows/ci.yml`, `docs/developing-evener/testing.md`, and `make/testing.mk`; any additional path stops the commit.

Finish with `git status --short` and `git log --oneline a06ba218c..HEAD`; expected: clean worktree and ten scoped task commits. Any owning-task remediation commit created before Task 10 is separately visible with the fixed message and its re-review evidence.

## Deferred Physical Development-Device Acceptance Gate (Not an Implementation Task)

This gate is intentionally blocked until a dedicated development iPhone, signing identity, configured production profile, real Hub, and local-network access are available. It does not change the ten-task implementation count and must not be represented as passed by browser or simulator evidence.

- [ ] **Physical setup: preserve the real source/profile configuration**

Use the normal user environment that owns the configured provider and mobile profiles. **Do not set or override `XDG_STATE_HOME` for this final smoke**: doing so hides the provider profiles and tests a different source configuration. Do not clear app data, uninstall the production app, restart a running Hub without authorization, print credentials/auth URLs, or weaken host security.

- [ ] **Capture build, Hub, app, device, and signing identity**

Record under `$EVENER_SCRATCH_DIR/live-conversation-physical/`: app commit, bundle ID/version/hash, Hub version/protocol, redacted profile origin, device model/OS, signing identity summary, and generated artifact hash. Require the existing generated Apple project and leave it unmodified.

- [ ] **Build and upgrade-install the signed app**

```bash
cd mobile
source "$HOME/.cargo/env"
APPLE_DEVELOPMENT_TEAM="$TEAM_ID" \
  npx tauri ios build --debug --target aarch64 --export-method debugging --ci
xcrun devicectl device install app --device "$DEVICE_UDID" "$APP_PATH" \
  --json-output "$EVENER_SCRATCH_DIR/live-conversation-physical/install.json" \
  --log-output "$EVENER_SCRATCH_DIR/live-conversation-physical/install.log"
```

Expected: signed `com.primeradiant.evener` upgrade succeeds without clearing its container. If IDB is healthy it may perform install/semantic interaction; `devicectl` remains install/launch fallback, not a substitute for semantic evidence.

- [ ] **Run the complete semantic/geometry smoke on the physical phone**

```bash
EVENER_SMOKE_APP_PATH="$APP_PATH" \
EVENER_SMOKE_THREAD_REF="$PATHOLOGICAL_THREAD_REF" \
EVENER_SMOKE_THREAD_TITLE="$PATHOLOGICAL_THREAD_TITLE" \
node scripts/smoke-live-concepts.mjs \
  --udid "$DEVICE_UDID" \
  --bundle-id com.primeradiant.evener \
  --output-dir "$EVENER_SCRATCH_DIR/live-conversation-physical" \
  --hub-version "$HUB_VERSION"
```

Require before any swipe: composer/visual-viewport coordinates, exact user-message AX count, raw-system absence, and mounted DOM count. Then verify keyboard clearance/safe area once, portrait/landscape, standard/XXL/AX-XXXL, all three concept anchor key+≤2 px offsets, draft/mode/pending/questions/disclosure/focus/unseen preservation, send/steer/queue/interrupt, load older, evidence and Work, reconnect generations, local-network permission, and background/foreground lifecycle. Retain redacted screenshots and complete accessibility trees.

- [ ] **Close Task 55 only on a real pass**

Independently inspect the evidence manifest/hashes and require the smoke command plus all device assertions to exit zero. Only then append the physical evidence paths and pass result to SDD ledger Task 55. A missing device, signing failure, IDB semantic failure, Hub/profile mismatch, timeout, or any failed assertion leaves Task 55 blocked/incomplete; do not update it to done and do not substitute simulator results.
