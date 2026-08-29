# Mobile Live Conversation Frame Redesign

**Date:** 2026-08-28
**Status:** Approved for implementation
**Baseline:** `0dacaeff6`
**Audience:** Evener mobile and Hub maintainers

## Summary

The live prototype proved the production connection and concept-switching path, but it failed as a phone conversation interface. On an iPhone 16 Pro running iOS 27, a real 39-item thread became an approximately 38,000 CSS-pixel document. Its composer was about 38,292 pixels from the top and required 58 fast full-screen swipes to reach. The first item was a roughly 44.7 KB `systemMessage` rendered as if the user had written it. The user described the result as “giant text sliding all over the place.”

This design replaces the three conversation layouts with one production-owned frame. The frame has stable top chrome, one virtualized transcript scroller, and a composer docked above the keyboard and safe area. Stillwater, Constellation, and Field Notes retain distinct visual identities through skins and item renderers; they no longer own viewport geometry, scrolling, virtualization, or composer placement.

The redesign also separates canonical source items from their display projection. User, assistant, question, and failure items form the narrative. Notices, system events, reasoning, tools, and diagnostics become compact typed markers with bounded previews and explicit Activity/Evidence detail. Hidden instructions and system preludes never enter the primary transcript DOM. All display text is bounded, operational behavior remains in production stores, and no live action or evidence family disappears.

This spec supersedes only the conversation presentation, display-projection, and associated test sections of `2026-08-26-live-mobile-concepts-integration-design.md`. That document continues to govern production runtime ownership, AppWire, profiles, roster, mutations, activity, and concept integration outside this redesign.

## Evidence and Root Cause

### Observed physical failure

The physical smoke used the production mobile app, a real Hub path, and a real thread rather than a fixture-only screen. It established:

- 12 turns and 39 projected items were present;
- the first `systemMessage` was approximately 44.7 KB;
- the conversation document extended to approximately 38,000 pixels;
- the relative composer appeared around `y = 38,292`;
- XCTest needed 58 fast upward swipes before the message field became hittable;
- the same structural problem existed in all three concepts;
- Dynamic Type made the oversized rows and motion feel worse, but was not the primary cause.

The iOS Simulator is the primary redesign environment because it provides repeatable screenshots, browser geometry, rotation, keyboard, Dynamic Type, and UI automation without weakening host security. A future dedicated development iPhone remains required for final local-network, safe-area, keyboard, lifecycle, and background validation. `vphone-cli` is not part of the plan: it targets virtual iPhones, requires private virtualization entitlements and host-security changes, and cannot replace a physical-device smoke.

### Current data flow

The failure crosses five existing boundaries:

1. `RootShell` opens the production conversation projection.
2. `ConversationService` performs a bounded `thread/read` with `turnLimit: 50`.
3. `mobile/src/conversation/project.ts` preserves a wire `systemMessage` verbatim as a `MobileTimelineItem` with `kind: "notice"`.
4. `ConversationStore` bounds assistant and activity bodies, but not notice text. Its 500-item cap limits item count, not rendered height.
5. `mobile/src/live-concepts/project-conversation.ts` converts every notice to `LiveTranscriptItem.kind: "user"`, copies `item.text` without a body bound, and sets `truncated: false`.

`mobile/src/live-concepts/accessibility-semantics.ts` consequently labels a system prelude as `Your message; completed`. The physical accessibility tree therefore contains false user-message articles and exposes the giant prelude as ordinary narrative.

The current 64 KiB tool-output cap does not protect notices. Tool disclosure children are absent from the DOM while collapsed; notice bodies are rendered immediately. The current behavior also conflicts with the existing rule that task prompts, hidden instructions, and raw unbounded work output are not display data.

### Current layout flow

Stillwater, Constellation, and Field Notes each:

- map all projected items directly to transcript `<article>` elements;
- append mutation status and the composer after those articles;
- make the concept shell `<main>` the full-screen vertical scroller; and
- style the composer with `position: relative`.

No live concept uses `@tanstack/react-virtual`. A large first row therefore pushes the composer down by the entire row height, and the retained 500-item store cap can still create 500 mounted transcript articles.

Production already contains a better structural precedent: `mobile/src/components/timeline/Timeline.tsx` virtualizes stable item IDs, and `mobile/src/screens/ConversationScreen.tsx` places its timeline and composer as siblings. The new frame generalizes that production-owned pattern for the live display projection. It does not copy three virtualizers.

### Amplifiers and guard gaps

The root mobile design system maps Dynamic Type to root sizes from 15 to 30 pixels. Concept typography uses `rem`, so accessibility categories legitimately enlarge headings, prose, spacing, and line height. This amplified a 44.7 KiB row. It did not create the unbounded row.

The production visual viewport coordinator writes `--viewport-height` and `--keyboard-inset`. Stillwater and Field Notes size their roots with the undefined `--visual-viewport-height`, which falls back to `100dvh`. This mismatch can produce keyboard and viewport movement. The shared frame must consume only the production tokens.

Existing tests did not guard the failing boundary:

- `mobile/scripts/mobile-geometry.mjs` performs structural jsdom checks against representative canonical conversation HTML, not the actual live host;
- its composer assertion checks presence rather than initial on-screen geometry;
- the prototype browser harness accepts a primary action anywhere in scrollable content and checks keyboard occlusion only for fixed or sticky composer-like elements;
- live renderer fixtures contain seven short items;
- the real-bridge fixture expects a short `systemMessage` body to appear but does not assert its kind or accessibility semantics; and
- the physical smoke verifies actions and semantic presence, not initial composer coordinates, document/DOM bounds, raw-system suppression, or swipe distance.

## Design Goals

1. Make the composer immediately usable regardless of transcript length.
2. Give all three concepts one reliable viewport, scroll, keyboard, virtualization, and anchoring implementation.
3. Keep the conversation narrative readable while preserving access to legitimate activity and evidence.
4. Prevent system or diagnostic content from masquerading as user content.
5. Bound every display text family and keep sensitive source material out of the DOM.
6. Preserve send, steer, queue, interrupt, questions, activity, drafts, disclosures, paging, streaming, reconnect, and concept switching.
7. Prove the actual production live host under pathological data in a real browser, simulator, and final development device.

## Decisions

1. **One production-owned conversation frame is mandatory.** The three concepts cannot provide alternate shell, scroll, virtualizer, or composer structures.
2. **Canonical source and display projection are separate contracts.** Source items remain authoritative for store behavior; bounded display items alone enter the concept frame.
3. **The primary feed is narrative-first.** User, assistant, question, and failure content remains inline. Notice, system, reasoning, tool, attachment, and diagnostic activity is represented by typed markers.
4. **System preludes and hidden instructions are not transcript content.** Their raw bodies never enter the primary DOM, accessibility tree, HTML attributes, logs, or test screenshots.
5. **Evidence remains available without behavior loss.** A marker opens the shared Activity/Evidence detail surface when safe detail exists. Suppressed sensitive content exposes only classification and state, never its raw body.
6. **Only the transcript owns conversation scrolling.** Top chrome and composer are non-scrolling siblings. When a modal Activity/Evidence sheet is open, the background transcript is locked and the sheet is the sole active scroller.
7. **The existing production viewport coordinator is authoritative.** The frame uses `--viewport-height` and `--keyboard-inset`; `--visual-viewport-height` is removed from live concept layout.
8. **The standard iOS Simulator drives visual iteration.** The development iPhone validates the final device-only behaviors.
9. **Migration is serial and deletion-complete.** Projection correctness lands first, then the frame, then Stillwater, Constellation, and Field Notes. Old conversation shell/composer code is removed after all three adapters pass; no permanent dual path remains.

## Architecture

### Ownership

The conversation surface becomes:

```text
RootShell
└── LiveConceptHost
    └── ConversationFrame                 production-owned structure
        ├── ConversationChrome            stable, non-scrolling
        ├── VirtualTranscript             only conversation scroller
        │   └── ConceptConversationSkin   visual item rendering only
        ├── ConversationStatus            compact mutation/offline state
        ├── DockedComposer                keyboard/safe-area aware
        └── ActivityEvidenceSheet         portal; mounted detail on request
```

`RootShell` remains the sole owner of profiles, secure credentials, AppWire, lifecycle, root navigation, New, Settings, Voice, and production store identity. `LiveConceptHost` continues to subscribe to production stores, project display state, and dispatch live intents. `ConversationFrame` owns conversation presentation mechanics only.

A concept module supplies a `ConversationSkin`, not a whole conversation page:

```ts
interface ConversationSkin {
  id: ConceptId;
  className: string;
  renderNarrativeItem(props: NarrativeItemRenderProps): ReactNode;
  renderActivityMarker(props: ActivityMarkerRenderProps): ReactNode;
  renderConversationChrome(props: ChromeRenderProps): ReactNode;
  composerAppearance: ComposerAppearance;
}
```

The skin receives bounded display models and callbacks. It cannot receive source DTOs, store handles, scroll elements, virtualizer handles, viewport services, or transport services. Static boundary tests reject concept-owned overflow scrollers, fixed/sticky composers, direct `items.map` transcript roots, and imports of `@tanstack/react-virtual` beneath individual concept directories.

### Frame geometry

`ConversationFrame` is a constrained grid/flex column whose block size is `var(--viewport-height, 100dvh)` and whose overflow is hidden. Its direct children are:

1. top chrome with top safe-area padding applied once;
2. a `min-height: 0` transcript region that consumes remaining space;
3. compact connection/mutation status when present; and
4. a composer with `flex-shrink: 0` and bottom padding of `max(var(--keyboard-inset), var(--safe-area-bottom))`.

The transcript region contains the only page/content `overflow-y: auto` element on the conversation screen. The document, frame, top chrome, status, and composer do not scroll vertically. A bounded multiline text field may use its native internal text scroll after six lines; that form-control behavior is not a second page/content scroll owner. The composer is in normal frame layout rather than viewport-fixed positioning, so visual viewport resizing reduces transcript height without overlaying or duplicating safe-area insets.

The production `visualViewport` coordinator remains single-owner at the app boundary. It updates `--viewport-height` and `--keyboard-inset` on resize, scroll, and window fallback. The frame never computes keyboard geometry independently.

## Narrative and Evidence Hierarchy

### Primary narrative

The feed gives visual and semantic priority to:

- actual user messages;
- assistant prose;
- pending structured questions and their resolution state; and
- failures that affect the conversation or requested action.

These items remain in chronological order and retain stable source identity. Assistant Markdown continues through the existing sanitizer. All other text is escaped plain text.

### Typed markers

The following source families become compact rows rather than narrative bubbles:

- steering and lifecycle notices;
- warning/error system events;
- ordinary system events;
- reasoning summaries;
- tool calls and tool results;
- attachment metadata;
- unknown forward-compatible activity; and
- redacted diagnostics.

A marker shows only type, safe short label, state, duration when available, and a bounded preview when policy allows it. It offers `Show activity` or `Show evidence` only when the shared detail projection contains safe detail. Expanded detail is rendered in the Activity/Evidence sheet, not inline in the virtual transcript. This avoids a disclosure changing the height of hundreds of downstream rows and gives large evidence an independently bounded, focused surface.

Warnings and failures remain visible. Ordinary preludes do not silently disappear: they produce a compact `System context` marker with no raw preview when their presence is useful. Repeated adjacent markers of the same safe family may cluster, but each canonical source identity remains represented in the cluster's evidence list.

### Hidden and system content

The projector classifies system-originated content before it creates DOM-bound fields:

- `hidden-instruction` and `system-prelude`: raw body suppressed; marker metadata only;
- `lifecycle`: safe typed label and state; bounded safe preview if allowlisted;
- `warning`: concise visible warning; bounded detail;
- `diagnostic`: redacted marker and bounded on-demand evidence;
- unknown system content: suppressed body and neutral `System activity` marker.

Classification never relies on user-controlled label text alone. It uses source item type plus authoritative event/family metadata. Unknown values choose the safer suppressed-body path. The projector does not place a sensitive raw value in a temporary display object and later hide it with CSS.

### Exact accessibility semantics

The shared frame, not each skin, creates feed roles, names, set position, expansion state, and live-region behavior. Skins may add decorative content only.

The semantic labels are:

| Display family | Accessible label |
|---|---|
| user | `Your message; completed` |
| assistant complete | `Assistant message; completed` |
| assistant streaming | `Assistant message; streaming` |
| question pending | `Question; response required` |
| question resolved | `Question; resolved` |
| failure | `Failure; action required` |
| informational notice | `Notice; informational` |
| warning notice | `Notice; attention required` |
| suppressed system/prelude marker | `System context; details hidden` |
| allowlisted system lifecycle marker | `System activity; informational` |
| reasoning marker | `Reasoning activity; <state>` |
| tool marker | `Tool activity, <safe label>; <state>` |
| attachment marker | `Attachment; <state>` |
| unknown marker | `Activity; <state>` |

`<state>` is one of `running`, `completed`, `failed`, or `unavailable`. Only a canonical user source item may produce `Your message; completed`. Marker previews do not become part of the accessible name. A separate description may contain the bounded safe preview. Streaming updates use one polite status announcement per settled phrase or state transition, not one announcement per delta. Mutation errors and connection loss use assertive alerts only when user action is required.

Virtualized rows expose stable logical `aria-posinset` and `aria-setsize`. Focus never remains on an evicted DOM node: before eviction the frame moves focus to the stable feed container or the triggering marker, records the logical key, and restores focus when that key is intentionally revisited.

## Data Contracts and Projection

### Canonical source remains authoritative

`MobileConversation.items` remains the canonical production projection used by stores, notifications, mutation behavior, voice filtering, paging, and activity correlation. The redesign does not rewrite wire DTOs into concept-specific data or discard source identities.

The display projector creates a distinct immutable contract:

```ts
type ConversationDisplayItem =
  | NarrativeDisplayItem
  | ActivityMarkerDisplayItem;

interface NarrativeDisplayItem {
  key: string;
  sourceKind: "user" | "assistant" | "question" | "failure";
  body: BoundedDisplayText;
  tone: DisplayTone;
  streaming: boolean;
  questionKey: string | null;
  sequence: string;
}

interface ActivityMarkerDisplayItem {
  key: string;
  sourceKind:
    | "notice"
    | "system"
    | "reasoning"
    | "tool"
    | "attachment"
    | "diagnostic"
    | "unknown";
  label: BoundedDisplayText;
  preview: BoundedDisplayText | null;
  tone: DisplayTone;
  state: "running" | "completed" | "failed" | "unavailable";
  evidenceKey: string | null;
  sequence: string;
}

interface BoundedDisplayText {
  text: string;
  truncated: boolean;
  originalUtf8Bytes: number;
}

interface EvidenceDisplayItem {
  key: string;
  family: ActivityMarkerDisplayItem["sourceKind"];
  title: BoundedDisplayText;
  sections: readonly EvidenceSection[];
  redacted: boolean;
}
```

Display keys and sequences retain the existing opaque stable-key guarantees. Raw refs, call IDs, thread IDs, job/delegate IDs, commands, profile IDs, tokens, authorization URLs, and filesystem paths stay in private operational maps unless a field is already explicitly approved user-visible evidence. Evidence keys are opaque display keys, not encoded operational IDs.

The projector returns the feed and evidence as one transactional snapshot. It cannot emit a marker that points to missing evidence, and a failed projection cannot partially mutate stable-key registries.

### Display bounds

Bounds are measured in UTF-8 bytes, cut on a valid code-point boundary, and represented with exactly one visible `… truncated` marker when truncation is user-visible. Source/store retention limits remain separate. The display contract applies these maxima before React receives the value:

| Text family | Primary feed maximum | On-demand detail maximum |
|---|---:|---:|
| conversation title or project | 256 B each | none |
| conversation status or updated label | 128 B each | none |
| item/marker label | 128 B | 256 B |
| user message | 64 KiB | none |
| assistant prose | 64 KiB | none |
| question header | 128 B | none |
| question prompt | 8 KiB | none |
| question option label | 1 KiB | none |
| question option detail | 4 KiB | none |
| question supporting text (`why`/fallback) | 4 KiB each | none |
| failure title | 256 B | none |
| failure body | 8 KiB | 64 KiB when safe source detail exists |
| safe notice preview | 512 B | 16 KiB |
| system/prelude preview | 0 B | 0 B for hidden/prelude; 16 KiB for allowlisted lifecycle evidence |
| reasoning preview | 512 B | 64 KiB |
| tool preview | 512 B | 64 KiB per arguments/output section |
| attachment name/media label | 512 B | 4 KiB metadata total |
| diagnostic preview | 256 B | 16 KiB after redaction |
| unknown activity preview | 0 B | 16 KiB after redaction |
| evidence section heading | 128 B | none |
| connection/read/mutation error | 1 KiB after redaction | none |

A single feed row can therefore never contain the raw 44.7 KB system prelude. Long legitimate user and assistant narrative remains readable and virtualized, with explicit truncation instead of silent loss. Existing 64 KiB tool argument/output behavior remains available through Evidence rather than the primary feed.

Question answer composition, full composer drafts, mutation snapshots, source DTOs, and store state do not use these display bounds. They retain their existing validated behavioral limits. The display projection never becomes a mutation input except for opaque lookup keys resolved through the private operational map.

### No behavior loss

Every existing intent remains available: load older, send, steer, queue, interrupt, question draft/submit, open Work, concept switch, back, New, Settings, and Voice. The redesign changes where evidence is viewed, not which source events are retained or which commands can run.

Questions preserve option, multi-select, note, fallback, decide, and skip semantics. Pending/accepted/failed mutation state and exact draft restoration stay store-owned. Activity still exposes tasks, delegates, jobs, watches, usage, and diagnostics. A transcript marker deep-links to the same shared Activity/Evidence model; Work remains the session-wide activity surface.

## Virtualization and Scroll Behavior

### Variable-height windowing

`VirtualTranscript` generalizes the existing production timeline around `@tanstack/react-virtual`. It uses:

- stable display keys through `getItemKey`;
- a family/type-scale estimate (56 pixels for markers, 96 for user rows, 128 for assistant rows, and 160 for question/failure rows at the standard scale) used only until measurement;
- `measureElement` and `ResizeObserver` for actual variable heights;
- overscan of six items before and after the visible range;
- a hard render-window ceiling of 48 transcript row containers for the required device matrix; and
- no index-based identity or concept-specific virtualizer.

The 500-item retained projection must therefore mount a bounded window, not 500 articles. Evidence lists are independently virtualized above 50 rows and use the same 48-row mounted-window ceiling.

### Initial open and follow mode

A newly opened conversation starts at the latest content after the first measurement pass. The composer is already visible because it is outside the transcript. The frame follows the tail only while the reader is within 48 CSS pixels of the measured end.

When the reader scrolls upward, streaming or inserted items do not move the current reading position. A `New activity` control reports unseen logical items and returns to the tail. If an explicit saved anchor exists for `{concept, threadKey}`, restoring it takes precedence over initial-tail behavior.

### Streaming and measurement

Streaming updates retain one stable item key. Resize measurement updates only that item's cached height. If the reader is following, the frame stays at the tail; otherwise it compensates the anchor so content above or within the viewport does not jump. Transcript updates do not trigger a route animation.

Reasoning/tool/system detail never expands inline. Marker state or preview can still change height, so the same stable-key compensation applies. Opening Activity/Evidence records the triggering marker key and offset, locks the feed, and restores both on close.

### Prepend, authoritative replacement, and eviction

Before loading older content, the frame records the first visible stable key and its pixel offset from the transcript viewport top. After prepend and measurement it restores that key to the same offset within 2 CSS pixels. It does not infer prepend from item-count growth alone.

Authoritative rereads reconcile stable keys. If the anchor remains, it is restored. If the anchor was removed, the frame chooses the next surviving chronological neighbor, then the previous neighbor, then the tail. Store eviction follows the same rule. Eviction cannot leave a blank spacer, stale focus, or an operational mapping to a removed item.

The frame keeps measured-size caches scoped to thread identity and semantic text scale. A Dynamic Type category change invalidates measurements, captures the current stable key and offset, remeasures, and restores the logical anchor.

### Concept switching

Local UI state stores an anchor as:

```ts
interface ConversationAnchor {
  threadKey: string;
  itemKey: string;
  offsetPx: number;
  following: boolean;
}
```

Anchors are keyed by `{concept, threadKey}`. On switch, the outgoing frame captures the first visible key plus offset. The incoming skin restores its own anchor when present; otherwise it uses the outgoing shared item key, and otherwise the tail. Absolute `scrollTop` is not persisted.

Concept switching preserves thread identity, production draft, pending mutation, question drafts, evidence/disclosure state, focused logical item, unseen count, and composer mode. It performs no reconnect, reread, resubscribe, or mutation.

## Visual Identities

The concepts remain recognizably different without changing frame mechanics.

### Stillwater

Stillwater is calm and sparse: light grouped surfaces, restrained forest accent, generous but bounded rhythm, full-width assistant prose, compact trailing user treatment, and quiet hairline activity markers. Chrome prioritizes title and status over ornament.

### Constellation

Constellation uses a dark technical field, luminous state accents, small local star/connection motifs, and compact telemetry-like markers. Decorative spatial elements remain behind content, ignore pointer/accessibility input, and never determine row or viewport geometry.

### Field Notes

Field Notes uses warm paper tones, editorial type contrast, ruled/divider motifs, and a chronology accent contained inside each virtual row. It cannot draw a single absolute-positioned rail across the full unmounted transcript; each row renders its own segment so virtualization remains correct.

### Responsive and Dynamic Type rules

- Layout responds to available inline size, never viewport-based font size.
- Portrait widths 375, 393, and 430 pixels use the same structural frame.
- Landscape condenses optional subtitle/status copy before reducing primary controls.
- Standard, XXL, and AX-XXXL use semantic production type tokens from the platform category.
- Text wraps naturally; labels and code use `overflow-wrap: anywhere` where needed.
- There is no document-level horizontal overflow at any required matrix point.
- Interactive targets measure at least 44 by 44 CSS pixels.
- The composer grows to a bounded maximum of six text lines; its input then scrolls internally without displacing the frame beyond the visual viewport.
- Chrome may wrap to two rows at AX sizes. It remains non-scrolling and leaves a positive transcript viewport.

### Motion

A conversation route may enter once with opacity plus at most 16 CSS pixels of translation over at most 180 ms. Concept switches use a crossfade of at most 120 ms and no lateral full-screen slide. Transcript insertions, streaming deltas, measurement corrections, marker state changes, and composer changes do not replay route motion.

When reduced motion is active, route and concept transitions are immediate, smooth scrolling is disabled, and activity animation becomes a static state mark. No motion is required to understand state.

## Loading, Error, Offline, and Reconnect

The frame structure remains mounted whenever a thread identity is known:

- **Initial loading:** chrome and docked disabled composer render immediately; the transcript region shows a bounded skeleton or progress status.
- **Empty:** the transcript region shows `No transcript yet`; the enabled composer remains docked.
- **Offline:** the last authoritative transcript remains readable, a compact non-scrolling status appears above the composer, drafts remain editable, and unavailable actions explain their state.
- **Reconnect:** show one polite `Reconnecting` status, retain draft/anchor/evidence state, reject stale generations, and replace the projection atomically after the bounded authoritative read.
- **Read failure with last good data:** retain the last good transcript and marker evidence; show a retry action without moving the composer.
- **Read failure without data:** show a focused error state inside the transcript region; Back and retry remain available.
- **Mutation pending:** preserve the current mode and expose interrupt independently when supported.
- **Mutation failure:** restore the exact draft snapshot and announce the error once.
- **Action unavailable:** publish refreshed capabilities before the error, as the existing store contract requires.
- **Stream overload or malformed data:** keep the last good bounded projection, close/reject the affected generation, and expose a redacted compatibility error.

No error path falls back to fixtures, mounts a second scroller, clears an unrelated draft, or replaces the production profile/service graph.

## Accessibility and Security

- The frame uses a labeled `main`, stable heading hierarchy, a virtualized `feed`, and a labeled composer region.
- The top Back, Work, concept switch, evidence, mode, submit, and interrupt controls have exact names independent of icon skins.
- Focus enters an Activity/Evidence sheet at its heading or first action, is trapped while open, and returns to the triggering marker on close.
- Keyboard focus and VoiceOver traversal can reach `New activity`, load older, questions, evidence markers, composer modes, and submit without scrolling the document.
- Color is never the only state signal. All themes meet the existing contrast requirements.
- Raw HTML is never accepted from source text. Only assistant Markdown uses the existing fixed sanitizer and link policy.
- Preview and evidence projection occurs before DOM creation. CSS clipping, `aria-hidden`, collapsed height, and offscreen positioning are not data-safety controls.
- Hidden instructions, task prompts, credentials, authorization URLs, private profile IDs, operational IDs, and unapproved filesystem paths remain absent from DOM text, attributes, accessible descriptions, analytics, screenshots, and errors.
- Detail redaction runs before truncation so a truncation boundary cannot reveal a prefix of a sensitive value.
- External links continue through the native browser allowlist. Markdown images do not load automatically.

## Implementation Boundaries

The implementation should create or generalize these production-owned modules:

| Path | Responsibility |
|---|---|
| `mobile/src/live-concepts/conversation/ConversationFrame.tsx` | Stable chrome/transcript/status/composer composition |
| `mobile/src/live-concepts/conversation/VirtualTranscript.tsx` | Variable-height windowing, follow, anchoring, prepend, eviction |
| `mobile/src/live-concepts/conversation/DockedComposer.tsx` | Shared send/steer/queue/interrupt and question dock |
| `mobile/src/live-concepts/conversation/ActivityEvidenceSheet.tsx` | Bounded on-demand evidence and focus lifecycle |
| `mobile/src/live-concepts/conversation/conversation-frame.css` | Shared viewport, safe-area, scroll, and responsive mechanics |
| `mobile/src/live-concepts/conversation/contract.ts` | Frame and skin interfaces |
| `mobile/src/live-concepts/project-conversation.ts` | Source classification, bounds, display/evidence snapshot, operational map |
| `mobile/src/live-concepts/model.ts` | Narrative/marker/evidence display contracts |
| `mobile/src/live-concepts/accessibility-semantics.ts` | Shared exact labels and state announcements |
| `mobile/src/live-concepts/live-ui-store.ts` | Stable key+offset anchor and evidence/disclosure state |
| `mobile/src/live-concepts/LiveConceptHost.tsx` | Project once and compose the shared frame with the selected skin |
| `mobile/src/ui/platformPresentation.ts` | Existing sole visual viewport coordinator; no second coordinator |

`mobile/src/components/timeline/Timeline.tsx` and paging helpers provide the starting mechanics. Shared logic should be generalized or extracted once; the live frame must not fork a second approximate prepend algorithm.

Each concept directory retains sessions/work presentation and adds only conversation skin/item-renderer modules and scoped visual CSS. Its old `ConversationView` shell, item loop, composer markup, shell scroller rules, and viewport-height declarations are deleted after migration.

Test-only boundaries include:

| Path | Responsibility |
|---|---|
| `mobile/src/test/live-conversation-pathological.fixture.ts` | 39-item/44.7 KiB and 500-item variable-height fixtures |
| `mobile/src/test/LiveConceptBrowserHarness.tsx` | Actual production `LiveConceptHost` with injected production store/service fakes |
| `mobile/scripts/live-conversation-geometry.mjs` | Real-browser geometry, DOM, accessibility, keyboard, and anchor assertions |
| `mobile/src/test/live-concepts-real-bridge.test.tsx` | AppWire vertical slice with corrected system semantics |
| `mobile/scripts/smoke-live-concepts.mjs` | Simulator/device semantic and geometry evidence |

Exact filenames may be consolidated with an existing production component when responsibilities remain singular. Ownership and boundaries are normative.

## Migration and TDD Order

### 1. Projection correctness first

Write failing tests for the observed 44.7 KiB `systemMessage`, repeated preludes, warning system events, hidden instructions, hostile Unicode, every text family, truncation markers, redaction, stable keys, and exact accessibility labels. Correct the display contracts and projector before changing layout. The canonical source/store behavior stays intact.

### 2. Shared frame and virtual transcript

Write frame tests for sibling geometry, one scroll owner, initial tail, saved anchor, variable-height measurement, streaming growth, evidence open/close, prepend, authoritative replacement, eviction, focus, and bounded 500-item DOM. Build the shared production frame by generalizing existing timeline mechanics.

### 3. Migrate Stillwater

Replace Stillwater's conversation shell with a skin. Prove visual identity, all actions, questions, evidence, Dynamic Type, and pathological geometry. Remove Stillwater's old conversation item loop, relative composer, full-document scroller, and `--visual-viewport-height` use.

### 4. Migrate Constellation

Apply the same contract without adding structural exceptions. Verify decorative layers and motion cannot affect measurements or replay during streaming. Remove duplicated conversation shell code.

### 5. Migrate Field Notes

Render chronology motifs per virtual row, not as a document-height rail. Verify variable-height measurement and anchor restoration. Remove duplicated conversation shell code.

### 6. Production real-browser and simulator harness

Render the actual `mobile/` `LiveConceptHost`, not representative HTML. Run all concepts against the long pathological fixture and scripted AppWire slice. Retain screenshots, geometry JSON, accessibility snapshots, DOM counts, and anchor measurements from the standard iOS Simulator.

### 7. Final development-device smoke

Install without clearing app data. Against a real Hub, validate initial composer geometry, keyboard clearance, safe areas, concept switching, send/steer/queue/interrupt, evidence, reconnect, background/foreground, Dynamic Type, and raw-system absence. Record commit-, app-, Hub-, OS-, and device-bound evidence. Simulator results do not satisfy this final step.

### Rollout and deletion

Migration occurs on the prototype branch without a user-facing mixed-layout feature flag. During development, the host may select the shared frame only for a migrated test adapter, but the branch is not considered integrated until all three concepts use it. Then:

- remove all three old conversation shell and composer implementations;
- remove duplicated conversation scroll/viewport CSS;
- remove stale tests that assert relative composers or full item maps;
- update boundary checks to reject their return;
- retain no fallback that renders raw canonical items through an old concept page.

Sessions, Work, roster, profile, transport, appwire, store, New, Settings, Voice, and offline-lab behavior remain on their existing paths unless a narrow adapter update is required by the new display contract.

## Acceptance Criteria

The redesign is accepted only when all of the following pass against the actual live host:

1. A 39-item thread containing a 44.7 KB system message does not render that raw body in the primary transcript DOM, accessibility tree, attributes, screenshots, or serialized display model.
2. System and diagnostic rows never receive `Your message; completed`. For every mounted virtual window, the count of that exact label equals the count of mounted canonical user items; notice, system, reasoning, tool, question, failure, and assistant counts match their exact shared semantics.
3. Warning and error notices remain visible as concise, correctly labeled rows. Ordinary prelude/diagnostic material is subordinate, typed, and bounded.
4. Tool, system, reasoning, and diagnostic raw detail is absent from the primary DOM until explicitly requested. Hidden instructions and system preludes remain absent even after disclosure.
5. At 393×852, the composer and enabled primary action are visible on the initial conversation render without document or transcript scrolling. The measured document height does not grow with transcript length.
6. With the software keyboard open, the composer bottom is at or above `visualViewport.offsetTop + visualViewport.height`, with zero overlap and bottom safe-area padding applied exactly once.
7. Top chrome, transcript viewport, status, and composer are siblings. Exactly one page/content region has active vertical scrolling: the transcript viewport, or the modal sheet while it is open and the transcript is locked. Native scrolling inside the bounded composer text control is permitted after six lines.
8. A retained 500-item transcript mounts no more than 48 transcript row containers at any required matrix point and never mounts 500 articles. Scrolling still reaches every logical item.
9. A new open follows the live tail unless restoring an explicit prior anchor. Streaming while not following does not move the anchor. Loading older, authoritative replacement, measurement changes, and eviction preserve the stable first-visible item within 2 CSS pixels when that item survives.
10. Switching among Stillwater, Constellation, and Field Notes preserves stable item key plus offset, thread identity, draft, composer mode, pending mutation, question drafts, evidence/disclosure state, focus intent, and unseen count without reconnect or reread.
11. Standard, XXL, and AX-XXXL Dynamic Type pass without horizontal overflow, clipped focused controls, hidden composer actions, or viewport-based body sizing. Every interactive target is at least 44×44 CSS pixels.
12. Conversation route motion occurs once with at most 16 pixels translation and 180 ms duration; concept crossfade is at most 120 ms. Reduced motion removes both, and transcript updates never replay full-screen movement.
13. All primary/evidence text respects the exact UTF-8 bounds in this spec, emits at most one truncation marker, preserves valid Unicode, and never leaks operational or sensitive fields.
14. Send, steer, queue, interrupt, load older, structured-question behavior, Work, Activity/Evidence, Back, New, Settings, Voice, exact draft restoration, and capability refresh retain their current production behavior in every concept.
15. Loading, empty, offline, reconnect, read failure, mutation pending/failure, and malformed-data states retain a stable frame and reachable composer/navigation without fixture fallback or stale-generation content.

## Verification Matrix

### Unit and component TDD

- source-to-display classification for every known and unknown item family;
- all primary and evidence bounds, redaction-before-truncation, split Unicode, hostile strings, and repeated truncation;
- transactional stable key/evidence maps and no operational ID serialization;
- exact semantic labels and user-message accessibility counts;
- frame structure and one-scroll-owner invariant;
- composer action/capability/pending/failure and exact draft restoration;
- questions including multi-select, notes, fallback, decide, skip, and resolved state;
- variable-height measurement, tail following, unseen activity, disclosure sheet focus, prepend, eviction, and concept anchor restoration;
- static boundaries against concept-owned virtualizers, scrollers, composers, raw source DTOs, and old viewport tokens.

### Actual production live-host browser matrix

Run the actual `LiveConceptHost` for all three concepts at:

- portrait: 375×667, 393×852, and 430×932;
- landscape: 852×393;
- semantic type: standard, XXL, and AX-XXXL;
- normal and reduced motion;
- light and dark appearance;
- safe-area insets `{top: 0, right: 0, bottom: 0, left: 0}` and `{top: 59, right: 0, bottom: 34, left: 0}`; and
- keyboard closed and a representative 320-pixel keyboard visual viewport.

Every point uses the 39-item/44.7 KB fixture and a 500-item variable-height fixture containing long user/assistant text, warnings, failures, questions, streaming assistant growth, tool/reasoning markers, safe evidence, suppressed preludes, prepend, and eviction. Assertions measure initial composer visibility, keyboard geometry, one active page/content scroller, document and horizontal overflow, 44×44 targets, focused-control visibility, DOM/AX counts, 2-pixel anchors, and motion replay. Screenshots support review but do not replace geometry assertions.

### Scripted production AppWire slice

Update the current real-bridge test to prove:

- `systemMessage` and notices project to typed markers, never user items;
- authoritative read, item lifecycle, reasoning, tools, resync, and tree refresh update the bounded display/evidence snapshot;
- send, steer, queue, and interrupt keep independent capabilities and receipts;
- concept switching does not create a second AppWire connection or request; and
- loading older uses the retained cursor without reopen or resubscribe.

### Current deterministic gates

All existing gates remain required and must exit zero:

```text
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
git diff --check
```

The new real-browser host matrix is added as a required mobile gate. A Tauri iOS Simulator build and the scripted simulator smoke must also pass before device installation. Timeout, missing runtime, sandbox denial, or launch failure is incomplete verification, not a pass.

### Final physical development-device evidence

The final smoke records:

- app commit, bundle ID/version/hash, Hub version/protocol, device model, and OS;
- initial composer and visual viewport coordinates before any swipe;
- keyboard-open composer and safe-area coordinates;
- the exact user-message accessibility count and absence of raw system text;
- mounted transcript DOM count for the pathological thread;
- concept-switch item key and before/after offset;
- send, steer, queue, interrupt, question, evidence, and Work results;
- background/foreground and reconnect generations; and
- screenshots/accessibility trees with sensitive text redacted.

The future development iPhone is the authoritative physical lane. A simulator pass does not prove local-network permissions, physical keyboard/safe-area behavior, or background lifecycle.

## Non-Goals

- redesigning pairing, Keychain storage, native transport, AppWire, profile lifecycle, roster, or production service composition;
- changing Hub protocol methods or source retention solely for presentation;
- adding Search, concept-specific New/Settings/Voice, attachment viewing, or Android release hardening;
- changing mutation semantics, question answer encoding, activity truth, or usage computation;
- shrinking fonts as a substitute for containment;
- hiding unsafe content with CSS or accessibility attributes;
- making the offline concept lab a credentialed client;
- adopting `vphone-cli` or weakening the development Mac's security; or
- selecting a final product concept. This remains a full working three-concept prototype for informed critique.

## Success Definition

The prototype succeeds when the same real conversation behaves identically through all three visual identities, the composer is immediately usable and remains keyboard-safe, transcript length cannot inflate the document or DOM, only genuine user content receives user semantics, evidence is available without dominating the narrative, and the required browser, simulator, and development-device matrices prove the result against pathological and live data.
