# Mobile Concept Lab Experiences Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the complete Stillwater, Constellation, and Field Notes experiences against one shared behavioral contract, then integrate them into the Evener Concepts app.

**Architecture:** Each concept owns its composition, components, tokens, and platform expression in a separate directory. All three receive the same immutable `PrototypeState` and typed dispatch function from the foundation; they never fork product state or fixture logic. Shared code provides semantic primitives and contract tests, not a universal themed screen tree.

**Tech Stack:** React 19.2, TypeScript 6.0, Zustand 5.0, semantic HTML/CSS, Vitest 4.1, Testing Library 16.3.

**Spec:** `docs/superpowers/specs/2026-08-25-mobile-concept-lab-design.md`

**Prerequisite plan:** `docs/superpowers/plans/2026-08-25-mobile-concept-lab-1-foundation.md`

**Next plan:** `docs/superpowers/plans/2026-08-25-mobile-concept-lab-3-packaging-verification.md`

## Global Constraints

- Complete and verify the foundation plan before this plan.
- All concepts implement Sessions, Search, Conversation, Work, Structured Questions, New Session, Settings, and Voice.
- All concepts use the same fixtures, routes, reducer, queries, answer state, new-session state, and voice state.
- Keep composition concept-specific. Do not implement one universal screen tree controlled only by colors and radii.
- Every action has a semantic button, input, link, disclosure, tab, or form control. Do not make non-interactive elements clickable.
- State meaning must never depend on color alone.
- Use stable domain IDs as React keys; never use array indexes.
- Avoid non-null assertions. Narrow optional data at boundaries and render explicit recovery states.
- Use one primary vertical scroll owner per destination.
- Respect safe areas, keyboard viewport, large text, dark appearance, and reduced motion.
- iOS targets are at least 44 by 44 CSS pixels; Android controls follow a 48 by 48 minimum where Material controls require it.
- No network, native invoke, microphone, speech, file, QR, credential, or production-service code.
- Run Biome with `--write` on touched `src/` files before checks.
- Stage only task-owned files and preserve dirty production-mobile files.
- Every commit uses `git commit --only -- <task paths>` so pre-existing staged files remain outside the commit.

## Post-Green Falsification Rule

Before each task's commit, copy the named task-owned source to `$EVENER_SCRATCH_DIR`, apply the table's mutation, run the focused test and require the named assertion failure, restore the byte-for-byte copy, verify it with `cmp`, and rerun green. Do not restore with checkout/reset.

| Task | Load-bearing mutation | Required red evidence |
|---|---|---|
| 1 | Remove `aria-expanded` from `Disclosure` | disclosure semantic-state assertion fails |
| 2 | Make Stillwater's Refresh control dispatch no action | Stillwater refresh contract fails |
| 3 | Remove Constellation's visible attention status text while keeping its color | non-color status assertion fails |
| 4 | Make Field Notes' question Skip control dispatch `decide` | exact structured-question outcome assertion fails |
| 5 | Dispatch `reset` after `selectConcept` in `ConceptSwitcher` | route/draft/scenario preservation assertion fails |
| 6 | Make Stillwater's Stop control dispatch `endVoice` | cross-concept Stop-without-End parity assertion fails |

## Shared Renderer Contract

Every concept exports this exact interface:

```ts
export interface ConceptRendererProps {
  state: PrototypeState;
  dispatch: (action: PrototypeAction) => void;
  platform: Platform;
  primitives: PlatformPrimitives;
  onOpenConceptSwitcher: () => void;
  onOpenLabControls: () => void;
}

export interface ConceptModule {
  id: ConceptId;
  Renderer: ComponentType<ConceptRendererProps>;
}
```

The renderer owns platform chrome and route composition. It obtains all content from `state.projection.fixture` and all behavior through `dispatch`.

## File Map

| Path | Responsibility |
|---|---|
| `mobile-concepts/src/concepts/contract.ts` | Renderer interface and route-surface contract |
| `mobile-concepts/src/concepts/shared/` | Semantic icons, status text, screen-state, disclosures, and test builders |
| `mobile-concepts/src/concepts/stillwater/` | Complete quiet-instrument composition and styles |
| `mobile-concepts/src/concepts/constellation/` | Complete living-system composition and styles |
| `mobile-concepts/src/concepts/field-notes/` | Complete transcript-studio composition and styles |
| `mobile-concepts/src/concepts/registry.ts` | Total concept-to-renderer registry |
| `mobile-concepts/src/app/ConceptSwitcher.tsx` | In-app concept sheet preserving current state |
| `mobile-concepts/src/app/RootApp.tsx` | Gallery-first launch and selected renderer integration |
| `mobile-concepts/src/test/conceptContract.tsx` | Reusable behavior assertions run against each module |
| `mobile-concepts/src/test/conceptParity.test.tsx` | Cross-concept route, scenario, platform, and action matrix |
| `mobile-concepts/src/test/networkTrap.test.tsx` | Full local-flow smoke with browser network APIs disabled |

---

### Task 1: Define the Renderer Contract and Semantic Primitives

**Files:**
- Create: `mobile-concepts/src/concepts/contract.ts`
- Create: `mobile-concepts/src/concepts/shared/Icon.tsx`
- Create: `mobile-concepts/src/concepts/shared/Icon.test.tsx`
- Create: `mobile-concepts/src/concepts/shared/ScreenState.tsx`
- Create: `mobile-concepts/src/concepts/shared/ScreenState.test.tsx`
- Create: `mobile-concepts/src/concepts/shared/StatusLabel.tsx`
- Create: `mobile-concepts/src/concepts/shared/Disclosure.tsx`
- Create: `mobile-concepts/src/concepts/shared/Disclosure.test.tsx`
- Create: `mobile-concepts/src/concepts/shared/format.ts`
- Create: `mobile-concepts/src/concepts/shared/format.test.ts`
- Create: `mobile-concepts/src/test/renderConcept.tsx`
- Create: `mobile-concepts/src/test/conceptContract.tsx`

**Interfaces:**
- Produces: `ConceptRendererProps` and `ConceptModule` exactly as declared above, consuming `PlatformPrimitives` from the foundation.
- Produces: `<Icon name={IconName} decorative={boolean} />` with inline local SVG paths.
- Produces: `<ScreenState state retryAction? />` for loading, empty, offline, and recoverable error.
- Produces: `<StatusLabel state />` with icon, visible text, and machine-readable state.
- Produces: `<Disclosure summary expanded onToggle children />` with semantic expanded state.
- Produces: `renderConcept(module, options)` and `runConceptContract(module)` test helpers.

```ts
export interface RenderConceptOptions {
  platform: Platform;
  scenario: ScenarioId;
  route: Route;
  concept?: ConceptId;
}

export interface RenderConceptResult extends RenderResult {
  store: StoreApi<PrototypeStore>;
}

export function renderConcept(
  module: ConceptModule,
  options: RenderConceptOptions,
): RenderConceptResult;

export function runConceptContract(module: ConceptModule): void;
```

- [ ] **Step 1: Write failing primitive tests**

Test positive accessibility behavior:

```tsx
it("keeps decorative icons out of the accessibility tree", () => {
  const { container } = render(<Icon name="sessions" decorative />);
  expect(container.querySelector("svg")).toHaveAttribute("aria-hidden", "true");
});

it("labels non-decorative icons", () => {
  render(<Icon name="warning" decorative={false} label="Needs attention" />);
  expect(screen.getByRole("img", { name: "Needs attention" })).toBeInTheDocument();
});

it("exposes disclosure state through a button", () => {
  render(<Disclosure summary="Read output" expanded={false} onToggle={() => {}}>Body</Disclosure>);
  expect(screen.getByRole("button", { name: "Read output" })).toHaveAttribute(
    "aria-expanded",
    "false",
  );
});
```

`ScreenState` tests assert role/status semantics, not copy snapshots. `StatusLabel` tests assert that attention, running, complete, waiting, and failed states each expose a visible text node plus a distinct icon or shape.

- [ ] **Step 2: Run the tests and verify missing-module failures**

Run:

```bash
cd mobile-concepts
npx vitest run src/concepts/shared/Icon.test.tsx src/concepts/shared/ScreenState.test.tsx src/concepts/shared/Disclosure.test.tsx
```

Expected: FAIL because the primitive modules do not exist.

- [ ] **Step 3: Implement local semantic primitives**

`IconName` covers only the app's local needs:

```ts
export type IconName =
  | "sessions"
  | "search"
  | "new"
  | "settings"
  | "back"
  | "switch"
  | "lab"
  | "warning"
  | "running"
  | "complete"
  | "waiting"
  | "failed"
  | "tool"
  | "work"
  | "voice"
  | "send"
  | "stop"
  | "close"
  | "chevron";
```

Use an SVG `<symbol>`-style local path map or one `<svg>` per icon. No remote icon font, emoji-as-control, or text glyph masquerades as an icon.

`Disclosure` generates stable `aria-controls` with `useId`. It renders children only when expanded. `ScreenState` accepts a structured state object and an optional retry callback; no retry callback appears for the offline prototype's intentionally static states.

- [ ] **Step 4: Define reusable formatters with deterministic inputs**

Create pure functions for fixture-provided relative time, duration, usage, and path labels. Functions receive values and never call `Date.now()`.

```ts
export function formatDuration(milliseconds: number): string;
export function formatUsage(tokens: number): string;
export function basename(path: string): string;
```

Tests cover zero, boundary units, Unicode paths, and invalid negative values.

- [ ] **Step 5: Create the renderer contract harness**

`renderConcept` constructs a real store from foundation fixtures and returns Testing Library's render result plus the store. `runConceptContract` is a helper called inside each concept's own test file. It verifies required roles and state data for every route:

- primary navigation with four labeled root controls;
- Switch concept and Lab Controls actions;
- one `main` with `data-route`;
- Sessions groups, filter, deterministic Refresh control, and visible refresh state;
- Search empty-query guidance, typed result links with kind/context, no-result state that retains the query, and focused represented item after open;
- Conversation transcript, tool disclosure, composer, Work, and Voice actions;
- Work task/subagent/job groups plus tokens, fictional cost, duration, and context usage;
- Structured Question form with option, note, fallback, decide, skip, and submit actions permitted by its fixture, with submit disabled until valid;
- New Session recent-project choice, direct fixture-path entry, prompt, model, effort, submit, starting, success, and failure states;
- Settings appearance, voice preference, two clearly fictional Hub presentation rows, offline-prototype explanation, concept, and lab controls; and
- Voice status, caption, deterministic visual level meter, mute, distinct Stop and End transitions, and close controls.

The harness queries semantics and domain IDs. For each platform, it also asserts the root navigation, title, sheet/dialog, minimum-target, feedback, back, and safe-area data contracts match the supplied `PlatformPrimitives`. It does not assert concept CSS class names or duplicate each concept's visual copy.

- [ ] **Step 6: Run primitive and contract-helper gates**

Run:

```bash
cd mobile-concepts
npx biome check --write src/concepts/shared src/concepts/contract.ts src/test
npx vitest run src/concepts/shared
npm run check
npm run boundary
```

Expected: every command exits 0.

- [ ] **Step 7: Commit the shared contract**

```bash
git add mobile-concepts/src/concepts/contract.ts mobile-concepts/src/concepts/shared mobile-concepts/src/test/renderConcept.tsx mobile-concepts/src/test/conceptContract.tsx
git commit --only -m "feat(concepts): define shared experience contract" -- mobile-concepts/src/concepts/contract.ts mobile-concepts/src/concepts/shared mobile-concepts/src/test/renderConcept.tsx mobile-concepts/src/test/conceptContract.tsx
```

### Task 2: Implement Stillwater

**Files:**
- Create: `mobile-concepts/src/concepts/stillwater/index.ts`
- Create: `mobile-concepts/src/concepts/stillwater/StillwaterRenderer.tsx`
- Create: `mobile-concepts/src/concepts/stillwater/StillwaterShell.tsx`
- Create: `mobile-concepts/src/concepts/stillwater/SessionsView.tsx`
- Create: `mobile-concepts/src/concepts/stillwater/SearchView.tsx`
- Create: `mobile-concepts/src/concepts/stillwater/ConversationView.tsx`
- Create: `mobile-concepts/src/concepts/stillwater/WorkView.tsx`
- Create: `mobile-concepts/src/concepts/stillwater/QuestionCard.tsx`
- Create: `mobile-concepts/src/concepts/stillwater/NewSessionView.tsx`
- Create: `mobile-concepts/src/concepts/stillwater/SettingsView.tsx`
- Create: `mobile-concepts/src/concepts/stillwater/VoiceView.tsx`
- Create: `mobile-concepts/src/concepts/stillwater/stillwater.css`
- Create: `mobile-concepts/src/concepts/stillwater/Stillwater.contract.test.tsx`
- Create: `mobile-concepts/src/concepts/stillwater/Stillwater.interactions.test.tsx`

**Interfaces:**
- Produces: `stillwaterModule: ConceptModule` with ID `stillwater`.
- Consumes: only foundation state/model exports, the renderer contract, and shared semantic primitives.

- [ ] **Step 1: Write the failing Stillwater contract test**

```tsx
import { runConceptContract } from "../../test/conceptContract";
import { stillwaterModule } from "./index";

runConceptContract(stillwaterModule);
```

Add direct interaction tests for:

- grouped attention/running/recent sessions and local filtering;
- deterministic refresh start and explicit completion;
- Search empty-query, contextual-result, unmatched no-result, and focused-result states;
- session open and back;
- tool disclosure and Work opening;
- question option, note, disabled-until-valid submit, and valid submit;
- fallback, “you decide,” and skip outcomes on fixtures that permit them;
- recent-project selection versus direct fixture-path entry, followed by separate new-session starting, success, and failure completions;
- voice open, deterministic level changes across lifecycle advance, mute, Stop-without-End, and End; and
- iOS and Android root-navigation variants.

- [ ] **Step 2: Run the tests and verify the missing-module failure**

Run: `cd mobile-concepts && npx vitest run src/concepts/stillwater`

Expected: FAIL because `stillwaterModule` does not exist.

- [ ] **Step 3: Implement Stillwater route composition**

`StillwaterRenderer` switches exhaustively on `state.route.kind`. `StillwaterShell` owns the top bar, Switch concept action, Lab Controls action where appropriate, scroll region, and four-item root navigation. Conversation, Work, and Voice use pushed navigation with an explicit Back or Close action. Navigation, title, sheet/dialog, target size, feedback, back, and safe-area choices come from `props.primitives`, not duplicate platform conditionals.

Sessions use quiet grouped lists. Conversation gives assistant prose a full-width reading column, user messages a restrained trailing surface, and tool activity a one-line disclosure. Work appears as an iOS sheet treatment or Android modal-bottom-sheet treatment without changing its semantic route.

- [ ] **Step 4: Implement every shared flow**

Wire controls directly to typed actions:

```tsx
onClick={() => dispatch({ type: "openSession", sessionId: session.id })}
onChange={(event) => dispatch({ type: "setSessionQuery", value: event.currentTarget.value })}
onClick={() => dispatch({ type: "toggleTool", itemId: item.id })}
onClick={() => dispatch({ type: "openWork", sessionId: session.id })}
```

Narrow `selectedSessionId` before rendering session-specific routes. If a route references absent fixture data, render `ScreenState` recovery rather than asserting non-null.

- [ ] **Step 5: Add Stillwater visual and platform tokens**

Use these roots:

```css
.concept-stillwater {
  --sw-bg: #fbfcfb;
  --sw-surface: #eef3f0;
  --sw-ink: #17221e;
  --sw-muted: #67726d;
  --sw-line: #dce4df;
  --sw-accent: #18775f;
  --sw-attention: #9a5b12;
  --sw-danger: #a23932;
  color: var(--sw-ink);
  background: var(--sw-bg);
}
```

Add explicit dark equivalents with AA contrast. iOS uses system typography, large-title hierarchy, inset grouping, hairlines, and 44-pixel controls. Android uses Material 3 tonal surfaces, 48-pixel controls, top app bars, visible state layers, and navigation-bar spacing. Motion is limited to insert, resolve, disclose, and route transitions, and is removed under reduced motion.

- [ ] **Step 6: Run Stillwater gates**

Run:

```bash
cd mobile-concepts
npx biome check --write src/concepts/stillwater
npx vitest run src/concepts/stillwater
npm run check
npm run boundary
npm run build
```

Expected: every command exits 0 and the complete concept contract passes.

- [ ] **Step 7: Commit Stillwater**

```bash
git add mobile-concepts/src/concepts/stillwater
git commit --only -m "feat(concepts): implement Stillwater experience" -- mobile-concepts/src/concepts/stillwater
```

### Task 3: Implement Constellation

**Files:**
- Create: `mobile-concepts/src/concepts/constellation/index.ts`
- Create: `mobile-concepts/src/concepts/constellation/ConstellationRenderer.tsx`
- Create: `mobile-concepts/src/concepts/constellation/ConstellationShell.tsx`
- Create: `mobile-concepts/src/concepts/constellation/SessionsView.tsx`
- Create: `mobile-concepts/src/concepts/constellation/SearchView.tsx`
- Create: `mobile-concepts/src/concepts/constellation/ConversationView.tsx`
- Create: `mobile-concepts/src/concepts/constellation/WorkView.tsx`
- Create: `mobile-concepts/src/concepts/constellation/QuestionCard.tsx`
- Create: `mobile-concepts/src/concepts/constellation/NewSessionView.tsx`
- Create: `mobile-concepts/src/concepts/constellation/SettingsView.tsx`
- Create: `mobile-concepts/src/concepts/constellation/VoiceView.tsx`
- Create: `mobile-concepts/src/concepts/constellation/constellation.css`
- Create: `mobile-concepts/src/concepts/constellation/Constellation.contract.test.tsx`
- Create: `mobile-concepts/src/concepts/constellation/Constellation.interactions.test.tsx`

**Interfaces:**
- Produces: `constellationModule: ConceptModule` with ID `constellation`.
- Consumes: the same state, actions, fixtures, and primitives as Stillwater; imports nothing from Stillwater.

- [ ] **Step 1: Write the failing Constellation contract and interaction tests**

Call `runConceptContract(constellationModule)`. Direct tests must additionally prove:

- active session relationships include visible text and structural connection markers;
- parent-child work order follows fixture `parentId` relationships;
- no relationship or state disappears under reduced motion;
- attention remains visible in forced-colors-friendly semantics;
- expanded tools remain readable rather than entering a spatial graph; and
- Android back closes Work/Conversation/Voice in route order.

- [ ] **Step 2: Run the tests and verify the missing-module failure**

Run: `cd mobile-concepts && npx vitest run src/concepts/constellation`

Expected: FAIL because `constellationModule` does not exist.

- [ ] **Step 3: Implement Constellation composition**

Sessions foreground active work and attention with disciplined relationship rails. Work renders a vertical orchestration map whose DOM remains a nested semantic list. CSS lines and depth express relationships; no canvas, remote graphic, or inaccessible free-form graph is allowed.

The transcript retains the same reading order as the fixture. Active tools may gain a restrained luminous edge, while assistant prose stays on a stable opaque reading surface. Voice concentrates visual energy around the current state but keeps captions and controls static and readable.

- [ ] **Step 4: Wire the complete shared behavior contract**

Use the same typed reducer actions as Stillwater. Do not create Constellation-only state for session, question, composer, work, or voice outcomes. Local component state is limited to transient focus or sheet animation state that does not affect product behavior.

- [ ] **Step 5: Add Constellation visual and platform tokens**

Use these dark roots and provide measured light counterparts:

```css
.concept-constellation {
  --co-bg: #090d18;
  --co-surface: #11192c;
  --co-raised: #171f33;
  --co-ink: #f2f4ff;
  --co-muted: #9ca8c2;
  --co-line: #303a57;
  --co-accent: #7fe6c4;
  --co-violet: #8c7cf0;
  --co-attention: #f1bf67;
  color: var(--co-ink);
  background: var(--co-bg);
}
```

Restrained pulses appear only on current work and voice level. `prefers-reduced-motion`, store reduced-motion state, and accessibility text mode remove pulses, parallax, and spatial translation. Shape, icon, and text remain.

Use the shared platform primitives to express iOS sheets/navigation and Android Material 3 top bars, navigation, elevation, and system back. Do not copy iOS glass effects into Android.

- [ ] **Step 6: Run Constellation gates**

Run:

```bash
cd mobile-concepts
npx biome check --write src/concepts/constellation
npx vitest run src/concepts/constellation
npm run check
npm run boundary
npm run build
```

Expected: every command exits 0.

- [ ] **Step 7: Commit Constellation**

```bash
git add mobile-concepts/src/concepts/constellation
git commit --only -m "feat(concepts): implement Constellation experience" -- mobile-concepts/src/concepts/constellation
```

### Task 4: Implement Field Notes

**Files:**
- Create: `mobile-concepts/src/concepts/field-notes/index.ts`
- Create: `mobile-concepts/src/concepts/field-notes/FieldNotesRenderer.tsx`
- Create: `mobile-concepts/src/concepts/field-notes/FieldNotesShell.tsx`
- Create: `mobile-concepts/src/concepts/field-notes/SessionsView.tsx`
- Create: `mobile-concepts/src/concepts/field-notes/SearchView.tsx`
- Create: `mobile-concepts/src/concepts/field-notes/ConversationView.tsx`
- Create: `mobile-concepts/src/concepts/field-notes/WorkView.tsx`
- Create: `mobile-concepts/src/concepts/field-notes/QuestionCard.tsx`
- Create: `mobile-concepts/src/concepts/field-notes/NewSessionView.tsx`
- Create: `mobile-concepts/src/concepts/field-notes/SettingsView.tsx`
- Create: `mobile-concepts/src/concepts/field-notes/VoiceView.tsx`
- Create: `mobile-concepts/src/concepts/field-notes/field-notes.css`
- Create: `mobile-concepts/src/concepts/field-notes/FieldNotes.contract.test.tsx`
- Create: `mobile-concepts/src/concepts/field-notes/FieldNotes.interactions.test.tsx`

**Interfaces:**
- Produces: `fieldNotesModule: ConceptModule` with ID `field-notes`.
- Consumes: the same state, actions, fixtures, and primitives as the other concepts; imports neither concept directory.

- [ ] **Step 1: Write the failing Field Notes contract and interaction tests**

Call `runConceptContract(fieldNotesModule)`. Direct tests must additionally prove:

- transcript items remain in chronological DOM order;
- heading levels form one coherent outline;
- work nesting uses lists and visible annotations;
- the Android variant uses its sans body role while retaining editorial hierarchy;
- active and attention states remain immediate, not archival; and
- long tool output is disclosed and scrolls internally only within its bounded code block.

- [ ] **Step 2: Run the tests and verify the missing-module failure**

Run: `cd mobile-concepts && npx vitest run src/concepts/field-notes`

Expected: FAIL because `fieldNotesModule` does not exist.

- [ ] **Step 3: Implement Field Notes composition**

Treat the session as a live annotated record. Use date/time rails, rules, labels, indentation, and whitespace for chronology and work nesting. Keep controls clearly interactive and visually separate from editorial content. User input remains visually distinct without imitating a consumer chat application.

Question cards read as inserted review forms. Work reads as a referenced work ledger. Voice uses the warm palette and clear captions while remaining a live control surface.

- [ ] **Step 4: Wire the complete shared behavior contract**

Use only foundation actions. Preserve draft, answers, query, route, scenario, and voice state across concept switches through the shared store. Error, offline, loading, and empty projections use the same `ScreenState` semantics as the other concepts.

- [ ] **Step 5: Add Field Notes visual and platform tokens**

```css
.concept-field-notes {
  --fn-bg: #fffaf1;
  --fn-surface: #f3eadc;
  --fn-ink: #312d28;
  --fn-muted: #74695e;
  --fn-line: #d9cbb8;
  --fn-accent: #a14d36;
  --fn-attention: #8a5312;
  --fn-danger: #9a3531;
  color: var(--fn-ink);
  background: var(--fn-bg);
}
```

Provide dark warm-neutral equivalents with AA contrast. Use the shared platform primitives for navigation, sheets/dialogs, targets, back, feedback, and insets. On iOS, use `Iowan Old Style`, `Palatino Linotype`, Georgia, then serif for editorial reading roles; controls remain in the system sans stack. On Android, use Roboto/system sans for both controls and body while preserving editorial scale, rules, and spacing. Texture uses local CSS gradients at very low contrast and disappears in high-contrast or reduced-transparency conditions.

- [ ] **Step 6: Run Field Notes gates**

Run:

```bash
cd mobile-concepts
npx biome check --write src/concepts/field-notes
npx vitest run src/concepts/field-notes
npm run check
npm run boundary
npm run build
```

Expected: every command exits 0.

- [ ] **Step 7: Commit Field Notes**

```bash
git add mobile-concepts/src/concepts/field-notes
git commit --only -m "feat(concepts): implement Field Notes experience" -- mobile-concepts/src/concepts/field-notes
```

### Task 5: Integrate the Three Concepts and Persistent Switching

**Files:**
- Create: `mobile-concepts/src/concepts/registry.ts`
- Create: `mobile-concepts/src/concepts/registry.test.ts`
- Create: `mobile-concepts/src/app/ConceptSwitcher.tsx`
- Create: `mobile-concepts/src/app/ConceptSwitcher.test.tsx`
- Create: `mobile-concepts/src/app/RootApp.tsx`
- Create: `mobile-concepts/src/app/RootApp.test.tsx`
- Modify: `mobile-concepts/src/app/App.tsx`
- Modify: `mobile-concepts/src/app/App.test.tsx`
- Modify: `mobile-concepts/src/app/ConceptGallery.tsx`
- Modify: `mobile-concepts/src/styles/foundation.css`

**Interfaces:**
- Produces: `conceptRegistry: Record<ConceptId, ConceptModule>`.
- Produces: `<ConceptSwitcher open onClose />`.
- Produces: `<RootApp />` that renders gallery or the selected concept.
- Consumes: all three concept modules and the foundation store.

- [ ] **Step 1: Write failing registry and integration tests**

Registry tests assert exact total coverage:

```ts
expect(Object.keys(conceptRegistry).sort()).toEqual([
  "constellation",
  "field-notes",
  "stillwater",
]);
expect(conceptRegistry.stillwater.id).toBe("stillwater");
```

Integration tests prove:

- no persisted concept starts at the gallery;
- selecting a concept opens Sessions, not a static preview;
- every selected renderer receives actual platform and shared state;
- Switch concept opens a labeled modal sheet from Sessions, Conversation, Work, and Voice;
- each sheet corresponds to one owned browser-history entry, and real `popstate` closes it before the underlying route;
- switching from a conversation preserves route, focused item, draft, scenario, answers, and disclosure state;
- closing without selection changes nothing;
- selection persists, but relaunch resets interaction state to baseline Sessions; and
- Reset prototype clears selection and returns to the gallery.

- [ ] **Step 2: Run tests and verify missing registry/integration failures**

Run:

```bash
cd mobile-concepts
npx vitest run src/concepts/registry.test.ts src/app/ConceptSwitcher.test.tsx src/app/RootApp.test.tsx
```

Expected: FAIL because registry and integrated components do not exist.

- [ ] **Step 3: Implement the total registry**

```ts
export const conceptRegistry = {
  stillwater: stillwaterModule,
  constellation: constellationModule,
  "field-notes": fieldNotesModule,
} as const satisfies Record<ConceptId, ConceptModule>;
```

`RootApp` narrows `state.concept`. A null concept renders the gallery. A selected concept resolves the module and renders it with the same store state, the `NavigationController.dispatch`, and `getPlatformPrimitives(state.platform)`. Concept/Lab sheet visibility is derived only from `state.overlay`; callbacks dispatch `openOverlay` through the controller rather than keeping local modal state.

- [ ] **Step 4: Implement the concept switcher**

Use the same metadata cards as the gallery inside an iOS sheet or Android modal-bottom-sheet presentation selected by `platform`. Focus moves into the sheet. Escape and explicit Close dispatch `goBack` through the navigation controller; Android system Back reaches the same reducer transition through `popstate`. Overlay close restores focus to the invoking Switch concept button before a second Back can pop the route.

Selecting dispatches `{ type: "selectConcept", concept }`, then controller `goBack` to close the owned overlay entry. It must not reset or navigate the underlying route.

- [ ] **Step 5: Wire first selection and relaunch behavior**

First gallery selection dispatches concept selection and `{ type: "navigateRoot", tab: "sessions" }`. Store initialization with a persisted concept starts at Sessions with canonical interaction state. This is distinct from in-run concept switching, which preserves the route.

- [ ] **Step 6: Run integration gates**

Run:

```bash
cd mobile-concepts
npx biome check --write src/app src/concepts/registry.ts
npx vitest run src/concepts/registry.test.ts src/app
npm run check
npm test
npm run boundary
npm run build
```

Expected: every command exits 0.

- [ ] **Step 7: Commit integration**

```bash
git add mobile-concepts/src/concepts/registry.ts mobile-concepts/src/concepts/registry.test.ts mobile-concepts/src/app/ConceptSwitcher.tsx mobile-concepts/src/app/ConceptSwitcher.test.tsx mobile-concepts/src/app/RootApp.tsx mobile-concepts/src/app/RootApp.test.tsx mobile-concepts/src/app/App.tsx mobile-concepts/src/app/App.test.tsx mobile-concepts/src/app/ConceptGallery.tsx mobile-concepts/src/styles/foundation.css
git commit --only -m "feat(concepts): integrate live concept switching" -- mobile-concepts/src/concepts/registry.ts mobile-concepts/src/concepts/registry.test.ts mobile-concepts/src/app/ConceptSwitcher.tsx mobile-concepts/src/app/ConceptSwitcher.test.tsx mobile-concepts/src/app/RootApp.tsx mobile-concepts/src/app/RootApp.test.tsx mobile-concepts/src/app/App.tsx mobile-concepts/src/app/App.test.tsx mobile-concepts/src/app/ConceptGallery.tsx mobile-concepts/src/styles/foundation.css
```

### Task 6: Prove Cross-Concept Parity, Scenarios, and Runtime Isolation

**Files:**
- Create: `mobile-concepts/src/test/conceptParity.test.tsx`
- Create: `mobile-concepts/src/test/scenarioMatrix.test.tsx`
- Create: `mobile-concepts/src/test/networkTrap.test.tsx`
- Create: `mobile-concepts/src/test/accessibilityContract.test.tsx`

**Interfaces:**
- Produces: one parameterized behavioral matrix over all three `ConceptModule` values.
- Produces: one render matrix over all concepts, platforms, routes, scenarios, appearances, text scales, and reduced-motion states.
- Produces: one full interaction smoke with browser network entry points replaced by throwing sentinels.

- [ ] **Step 1: Write the cross-concept behavior matrix**

Use `it.each(Object.values(conceptRegistry))` and execute the same action sequence against every concept:

1. filter Sessions, start/complete Refresh, and open a result;
2. visit Search in its empty-query state, open a contextual transcript result and verify focus, then enter an unmatched query and verify no-result context;
3. disclose a tool and open Work;
4. expand a task, subagent, and job, assert all usage fields, and return;
5. prove structured-question submit is disabled, satisfy single/multi selection with a note, and submit;
6. exercise composer Send, Steer, Queue, running, and Stop transitions;
7. switch concept and prove route/scenario/focus/draft/answers/disclosures remain;
8. select a recent project and complete successful New Session starting/navigation, then reset, type a fixture path, and complete failure with form preservation;
9. open Voice, assert deterministic level changes through idle/ready/listening/processing/speaking/interrupted/denied/error, prove Stop does not End, then End; and
10. change appearance, text, motion, and voice preferences in Settings and reset.

Assert structured state and accessible roles after each completion. Do not use sleeps or snapshots.

Run a second parameterized question sequence for fallback, “you decide,” and skip. Each permitted outcome must be reachable in every concept and produce the same reducer state; a concept may not hide an outcome that the fixture advertises.

- [ ] **Step 2: Run the parity test and capture real defects**

Run: `cd mobile-concepts && npx vitest run src/test/conceptParity.test.tsx`

Expected before fixes: FAIL only where a concept omitted or miswired the shared contract. Record each failing concept and boundary.

- [ ] **Step 3: Implement the scenario render matrix**

The matrix covers:

```ts
for (const concept of conceptIds)
  for (const platform of ["ios", "android"] as const)
    for (const scenario of scenarioIds)
      for (const appearance of ["light", "dark"] as const)
        for (const textScale of ["standard", "accessibility"] as const)
          for (const reducedMotion of [false, true] as const)
            renderAndAssertSingleMain(...);
```

Keep one assertion per render: one main landmark, no uncaught error, current concept/platform/scenario attributes, and reachable Switch concept/Lab Controls. Route-specific contract tests carry detailed behavior so this matrix does not duplicate them.

- [ ] **Step 4: Add the runtime network trap smoke**

Before importing the app, replace `fetch`, `XMLHttpRequest`, `WebSocket`, `EventSource`, `navigator.sendBeacon`, `navigator.mediaDevices.getUserMedia`, SpeechRecognition/webkitSpeechRecognition, `speechSynthesis`, `SpeechSynthesisUtterance`, AudioContext/webkitAudioContext, `showOpenFilePicker`/`showSaveFilePicker`/`showDirectoryPicker`, geolocation methods, Notification constructor/requestPermission, service-worker notification/push entry points, and `navigator.vibrate` with sentinels that throw and record attempts. Render the real RootApp and drive the complete flow through Testing Library. Assert zero attempts and that no file/capture input exists, then restore original descriptors in `afterEach`.

This test proves the app remains usable when browser network APIs are unavailable; it does not claim OS-level permission denial.

- [ ] **Step 5: Add semantic accessibility checks**

Parameterize every concept/platform and assert:

- one main landmark and no duplicate IDs;
- heading levels do not skip within each destination;
- root navigation has one current item;
- icon-only controls have accessible names;
- disclosures expose expanded state and controlled regions;
- question controls have labels, descriptions, and selected state;
- status changes do not mark continuously updating decorative elements as live regions;
- dialogs have names, modal semantics, focus entry, Escape handling, and focus return; and
- attention, running, waiting, complete, and failure each have visible non-color text.

- [ ] **Step 6: Fix only defects exposed by the matrices**

For each defect, append a separate fix task that names the exact concept/shared files, failing focused assertion, root-cause correction, and rerun command. Commit that correction before returning to this parity task. Do not weaken assertions to accommodate a missing flow.

- [ ] **Step 7: Run the complete experience gates**

Run:

```bash
cd mobile-concepts
npx biome check --write src
npm run check
npm test
npm run boundary
npm run build
source "$HOME/.cargo/env"
cargo test --manifest-path src-tauri/Cargo.toml
cargo fmt --manifest-path src-tauri/Cargo.toml --check
cargo clippy --manifest-path src-tauri/Cargo.toml --all-targets -- -D warnings
git diff --check
```

Expected: every command exits 0. Record Vitest and Node test counts.

- [ ] **Step 8: Commit parity and isolation proof**

```bash
git add mobile-concepts/src/test/conceptParity.test.tsx mobile-concepts/src/test/scenarioMatrix.test.tsx mobile-concepts/src/test/networkTrap.test.tsx mobile-concepts/src/test/accessibilityContract.test.tsx
git commit --only -m "test(concepts): prove cross-concept parity" -- mobile-concepts/src/test/conceptParity.test.tsx mobile-concepts/src/test/scenarioMatrix.test.tsx mobile-concepts/src/test/networkTrap.test.tsx mobile-concepts/src/test/accessibilityContract.test.tsx
```

## Experience Completion Check

Before native packaging, confirm the selected concept app has no static-only route and no behavior implemented inside an individual concept that should live in the reducer. Search for forbidden duplicated state and network code:

```bash
rg -n "useState\(.*(draft|answer|session|voice|scenario)|fetch\(|XMLHttpRequest|WebSocket|EventSource|sendBeacon|from ['\"][^'\"]*mobile/" mobile-concepts/src
```

Review every match. Local modal/focus state is allowed; duplicated product state is not. Then rerun `npm run check && npm test && npm run boundary && npm run build` from `mobile-concepts/`.
