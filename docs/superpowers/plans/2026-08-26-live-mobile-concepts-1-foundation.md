# Live Mobile Concepts Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Preserve the current production-mobile live wiring and establish the new live concept contract, shared presentation primitives, and enforcement boundary from which parallel Luna worktrees can branch.

**Architecture:** Two reviewed baseline commits capture the already-dirty AppWire and service-composition work without unrelated generated changes. A serialized foundation then defines a live-only renderer contract and shared helpers; it does not copy fixture runtime code or integrate RootShell.

**Tech Stack:** React 19, TypeScript 5, Zustand 5, Vitest, Biome, Node boundary scripts, Rust/Tauri 2, Cargo.

**Spec:** `docs/superpowers/specs/2026-08-26-live-mobile-concepts-integration-design.md`

**Next plan:** `docs/superpowers/plans/2026-08-26-live-mobile-concepts-2-parallel.md`

## Global Constraints

- Production `mobile/` is the sole live runtime; `mobile-concepts/` remains offline.
- Never move tokens, authorization URLs, or unredacted profile data into JS state.
- Do not stage or alter unrelated dirty generated Xcode files, concept-native tooling/generated files, or deleted SDD reports.
- No canonical fixture, scenario, synthetic-turn, Lab Controls, prototype store/reducer, or direct transport enters `mobile/src/live-concepts/`.
- RootShell remains the sole profile, transport, lifecycle, root-navigation, and production-screen owner.
- No new npm dependency is expected; package-lock changes are forbidden unless the plan is amended.
- Before adding/changing tests, read `docs/developing-evener/testing.md`.
- Frontend source edits run `npx biome check --write` on exact touched `src/` paths before gates.
- Every commit uses `git commit --only` with the task's explicitly named paths.
- Record the final serialized foundation commit as `FOUNDATION_SHA`; every parallel worktree in Plan 2 branches from it.

## File Map

| Path | Responsibility |
|---|---|
| `mobile/src/live-concepts/contract.ts` | Live-only concept IDs, surfaces, state, intents, props, host callback contract |
| `mobile/src/live-concepts/model.ts` | Display-safe roster, conversation, activity, composer, connection view models |
| `mobile/src/live-concepts/shared/` | Presentation-only shared Icon, Disclosure, StatusLabel, formatting |
| `mobile/src/live-concepts/ask-answer.ts` | One byte-exact structured-answer composer shared with production AskComposer |
| `mobile/scripts/check-live-concepts-boundary.mjs` | Reject fixture/runtime/transport/global-CSS boundary violations |
| `mobile/scripts/check-live-concepts-boundary.test.mjs` | Owned fixtures proving the boundary rejects each forbidden family |
| `mobile/src/screens/production-services.ts` | Existing reviewed production service composition baseline |
| `mobile/src-tauri/src/appwire_transport.rs` | Existing reviewed native AppWire handshake baseline |

---

### Task 1: Preserve the Native AppWire Baseline

**Files:**
- Review/modify only if a focused gate exposes a defect: `mobile/src-tauri/src/appwire_transport.rs`
- Test: `mobile/src-tauri/tests/appwire_transport_test.rs`

**Interfaces:**
- Produces: reviewed native AppWire open/send/close behavior without an unsupported WebSocket subprotocol header.
- Consumed by: production `createAppwireClient()` and every later live integration task.

- [ ] **Step 1: Inspect the exact dirty diff and prove scope**

```bash
git diff -- mobile/src-tauri/src/appwire_transport.rs mobile/src-tauri/tests/appwire_transport_test.rs
git status --short -- mobile/src-tauri/src/appwire_transport.rs mobile/src-tauri/tests/appwire_transport_test.rs
```

Expected: only the already-implemented transport correction and its regression test. Stop and report if unrelated behavior appears.

- [ ] **Step 2: Read the testing policy and run the focused regression**

```bash
sed -n '1,240p' docs/developing-evener/testing.md
cd mobile
source "$HOME/.cargo/env"
cargo test --manifest-path src-tauri/Cargo.toml --test appwire_transport_test -- --nocapture
```

Expected: exit 0; the no-subprotocol handshake regression passes through the real native transport seam.

- [ ] **Step 3: Run Rust gates for the touched crate**

```bash
cd mobile
source "$HOME/.cargo/env"
cargo fmt --manifest-path src-tauri/Cargo.toml --check
cargo clippy --manifest-path src-tauri/Cargo.toml --all-targets -- -D warnings
cargo test --manifest-path src-tauri/Cargo.toml
```

Expected: every command exits 0.

- [ ] **Step 4: Commit only the native baseline**

```bash
git add mobile/src-tauri/src/appwire_transport.rs mobile/src-tauri/tests/appwire_transport_test.rs
git commit --only -m "fix(mobile): preserve Hub AppWire handshake" -- \
  mobile/src-tauri/src/appwire_transport.rs \
  mobile/src-tauri/tests/appwire_transport_test.rs
```

Record the commit as `BASELINE_A_SHA`.

### Task 2: Preserve the Production Service-Composition Baseline

**Files:**
- Modify only if focused tests expose a defect: `mobile/src/screens/RootShell.tsx`
- Modify only if focused tests expose a defect: `mobile/src/screens/RootShell.test.tsx`
- Modify only if focused tests expose a defect: `mobile/src/screens/SessionsScreen.tsx`
- Modify only if focused tests expose a defect: `mobile/src/screens/production-services.ts`
- Create/preserve: `mobile/src/screens/production-services.test.ts`
- Modify only if focused tests expose a defect: `mobile/src/screens/root-types.ts`

**Interfaces:**
- Produces: `ProfileScopedServices`, `createProfileScopedServices()`, and current RootShell live roster/conversation composition on top of one profile-scoped AppWire client.
- Consumed by: Plan 3 integration and `LiveConceptHost` runtime props.

- [ ] **Step 1: Inspect the exact dirty source diff**

```bash
git diff -- \
  mobile/src/screens/RootShell.tsx \
  mobile/src/screens/RootShell.test.tsx \
  mobile/src/screens/SessionsScreen.tsx \
  mobile/src/screens/production-services.ts \
  mobile/src/screens/root-types.ts
git status --short -- mobile/src/screens/production-services.test.ts
```

Expected: profile-scoped service graph, RootShell connection/roster/conversation wiring, stable fallback store, canonical thread-ref navigation, and tests. Stop if an unrelated feature is mixed in.

- [ ] **Step 2: Run the focused source tests**

```bash
cd mobile
npx vitest run \
  src/screens/production-services.test.ts \
  src/screens/RootShell.test.tsx \
  src/screens/SessionsScreen.test.tsx
```

Expected: exit 0.

- [ ] **Step 3: Format touched frontend source and run full mobile gates**

```bash
cd mobile
npx biome check --write \
  src/screens/RootShell.tsx \
  src/screens/RootShell.test.tsx \
  src/screens/SessionsScreen.tsx \
  src/screens/production-services.ts \
  src/screens/production-services.test.ts \
  src/screens/root-types.ts
npm test
npm run check
npm run boundary
npm run build
git diff --check
```

Expected: every command exits 0. Read every warning; do not reduce coverage or weaken a test.

- [ ] **Step 4: Commit only the approved service baseline**

```bash
git add \
  mobile/src/screens/RootShell.tsx \
  mobile/src/screens/RootShell.test.tsx \
  mobile/src/screens/SessionsScreen.tsx \
  mobile/src/screens/production-services.ts \
  mobile/src/screens/production-services.test.ts \
  mobile/src/screens/root-types.ts
git commit --only -m "feat(mobile): preserve live service composition" -- \
  mobile/src/screens/RootShell.tsx \
  mobile/src/screens/RootShell.test.tsx \
  mobile/src/screens/SessionsScreen.tsx \
  mobile/src/screens/production-services.ts \
  mobile/src/screens/production-services.test.ts \
  mobile/src/screens/root-types.ts
```

Record the commit as `BASELINE_B_SHA`. Confirm `git merge-base --is-ancestor "$BASELINE_A_SHA" "$BASELINE_B_SHA"` exits 0.

### Task 3: Define the Live Concept Contract

**Files:**
- Create: `mobile/src/live-concepts/contract.ts`
- Create: `mobile/src/live-concepts/model.ts`
- Create: `mobile/src/live-concepts/contract.test.ts`

**Interfaces:**
- Produces:
  - `ConceptId`
  - `LiveConceptSurface`
  - `LiveConceptState`
  - `LiveConceptIntent`
  - `LiveConceptRendererProps`
  - `LiveConceptHostProps`
  - `LiveConceptModule`
- Consumed by: every Plan 2 lane and Plan 3 integration.

- [ ] **Step 1: Write the failing contract test**

Create `mobile/src/live-concepts/contract.test.ts`:

```ts
import { describe, expect, expectTypeOf, it } from "vitest";
import type {
  LiveConceptHostProps,
  LiveConceptIntent,
  LiveConceptModule,
  LiveConceptState,
} from "./contract";

const concepts = ["stillwater", "constellation", "field-notes"] as const;
const surfaces = ["sessions", "conversation", "work"] as const;

describe("live concept contract", () => {
  it("keeps the exact concept and milestone surface sets", () => {
    expect(concepts).toEqual(["stillwater", "constellation", "field-notes"]);
    expect(surfaces).toEqual(["sessions", "conversation", "work"]);
  });

  it("requires RootShell callbacks at the host boundary", () => {
    expectTypeOf<LiveConceptHostProps>().toHaveProperty("onOpenConceptSwitcher");
    expectTypeOf<LiveConceptHostProps>().toHaveProperty("onBack");
    expectTypeOf<LiveConceptHostProps>().toHaveProperty("onOpenNew");
    expectTypeOf<LiveConceptHostProps>().toHaveProperty("onOpenSettings");
    expectTypeOf<LiveConceptHostProps>().toHaveProperty("onOpenVoice");
  });

  it("has no scenario or synthetic-turn contract", () => {
    expectTypeOf<LiveConceptState>().not.toHaveProperty("scenario");
    expectTypeOf<LiveConceptState>().not.toHaveProperty("sourceFixture");
    expectTypeOf<LiveConceptState>().not.toHaveProperty("syntheticTurn");
  });

  it("keeps switcher opening distinct from concept selection", () => {
    const open: LiveConceptIntent = { type: "openConceptSwitcher" };
    const select: LiveConceptIntent = {
      type: "switchConcept",
      concept: "stillwater",
    };
    expect(open.type).not.toBe(select.type);
  });

  it("defines a module for one live renderer", () => {
    expectTypeOf<LiveConceptModule>().toHaveProperty("Renderer");
    expectTypeOf<LiveConceptModule>().toHaveProperty("id");
    expectTypeOf<LiveConceptModule>().toHaveProperty("label");
    expectTypeOf<LiveConceptModule["id"]>().toEqualTypeOf<
      (typeof concepts)[number]
    >();
  });
});
```

- [ ] **Step 2: Run the contract test for RED**

```bash
cd mobile
npx vitest run src/live-concepts/contract.test.ts
```

Expected: FAIL because `contract.ts` and `model.ts` do not exist.

- [ ] **Step 3: Implement the exact model and contract**

Create `model.ts` with display-only models:

```ts
export type ConceptId = "stillwater" | "constellation" | "field-notes";
export type LiveConceptSurface = "sessions" | "conversation" | "work";
export type Platform = "ios" | "android";
export type Appearance = "system" | "light" | "dark";
export type TextScale = "standard" | "accessibility";
export type DisplayTone =
  | "attention"
  | "running"
  | "success"
  | "failed"
  | "idle"
  | "unknown";

export interface LiveConnectionView {
  status: "connecting" | "connected" | "offline" | "error";
}

export interface LiveRosterRow {
  key: string;
  title: string;
  project: string;
  summary: string;
  updatedLabel: string;
  tone: DisplayTone;
  connectedWorkCount: number;
}

export interface LiveRosterView {
  status: "idle" | "loading" | "ready" | "error" | "offline";
  query: string;
  groups: ReadonlyArray<{
    id: "needsYou" | "running" | "recent";
    label: string;
    rows: readonly LiveRosterRow[];
  }>;
  hasMore: boolean;
  error: string | null;
}

export interface LiveTranscriptItem {
  key: string;
  kind: "user" | "assistant" | "tool" | "question" | "failure" | "attachment";
  label: string;
  body: string;
  tone: DisplayTone;
  streaming: boolean;
  truncated: boolean;
}

export interface LiveQuestionView {
  key: string;
  header: string;
  prompt: string;
  options: ReadonlyArray<{ key: string; label: string; detail: string }>;
  multiple: boolean;
}

export interface LiveConversationView {
  threadKey: string;
  title: string;
  project: string;
  status: string;
  items: readonly LiveTranscriptItem[];
  questions: readonly LiveQuestionView[];
  olderAvailable: boolean;
}

export interface LiveWorkItem {
  key: string;
  kind: "task" | "delegate" | "job" | "watch";
  title: string;
  detail: string;
  tone: DisplayTone;
  children: LiveWorkItem[];
}

export interface LiveActivityView {
  tasks: ReadonlyArray<{
    status: "active" | "open" | "done";
    count: number;
  }>;
  work: readonly LiveWorkItem[];
  usage: {
    totalTokens?: number;
    cost?: string;
    contextPressure?: number;
    durationMs?: number;
  };
}
```

Create `contract.ts` with these complete signatures:

```ts
import type { ComponentType } from "react";
import type { NativeBridge } from "../native/client";
import type { RosterService } from "../services/roster";
import type { ConversationService } from "../services/conversation";
import type { ActivityState } from "../state/activity";
import type { ConnectionState } from "../state/connection";
import type { ConversationState } from "../state/conversation";
import type { NavigationState } from "../state/navigation";
import type { PreferencesState } from "../state/preferences";
import type { RosterState } from "../state/roster";
import type { StoreApi } from "zustand";
import type { UseBoundStore } from "zustand/react";
import type {
  Appearance,
  ConceptId,
  LiveActivityView,
  LiveConnectionView,
  LiveConversationView,
  LiveConceptSurface,
  LiveRosterView,
  Platform,
  TextScale,
} from "./model";

export interface ConversationMutationState {
  kind: "send" | "steer" | "queue" | "interrupt";
  status: "pending" | "failed";
  draftSnapshot: string | null;
  generation: number;
}

export interface LiveComposerView {
  draft: string;
  canSend: boolean;
  canSteer: boolean;
  canQueue: boolean;
  canInterrupt: boolean;
  pending: ConversationMutationState | null;
  error: string | null;
}

export interface QuestionDraft {
  selectedOptionKeys: readonly string[];
  note: string;
  resolution: "answer" | "fallback" | "decide" | "skip" | null;
}

export interface ScrollAnchor {
  itemKey: string;
  offset: number;
}

export interface LiveConceptUiState {
  concept: ConceptId;
  workOpen: boolean;
  composerMode: "send" | "steer" | "queue";
  expandedToolKeys: ReadonlySet<string>;
  expandedWorkKeys: ReadonlySet<string>;
  questionDrafts: Readonly<Record<string, QuestionDraft>>;
  focusedItemKey: string | null;
  scrollAnchors: Readonly<Record<string, ScrollAnchor>>;
}

export interface LiveConceptState {
  concept: ConceptId;
  platform: Platform;
  appearance: Appearance;
  textScale: TextScale;
  reducedMotion: boolean;
  surface: LiveConceptSurface;
  connection: LiveConnectionView;
  roster: LiveRosterView;
  conversation: LiveConversationView | null;
  activity: LiveActivityView | null;
  composer: LiveComposerView;
  ui: LiveConceptUiState;
}

export type LiveConceptIntent =
  | { type: "switchConcept"; concept: ConceptId }
  | { type: "openConceptSwitcher" }
  | { type: "refreshRoster" }
  | { type: "setRosterQuery"; value: string }
  | { type: "openConversation"; key: string }
  | { type: "openWork" }
  | { type: "closeWork" }
  | { type: "setDraft"; value: string }
  | { type: "submit"; mode: "send" | "steer" | "queue" }
  | { type: "interrupt" }
  | { type: "toggleTool"; key: string }
  | { type: "toggleWork"; key: string }
  | { type: "setQuestionDraft"; key: string; value: QuestionDraft }
  | { type: "submitQuestion"; key: string }
  | { type: "goBack" }
  | { type: "openNew" }
  | { type: "openSettings" }
  | { type: "openVoice" };

export interface LiveConceptRendererProps {
  state: LiveConceptState;
  dispatch(intent: LiveConceptIntent): void;
}

export interface LiveConceptModule {
  id: ConceptId;
  label: string;
  Renderer: ComponentType<LiveConceptRendererProps>;
}

type BoundStore<State> = UseBoundStore<StoreApi<State>>;

export interface LiveConceptRuntime {
  connection: BoundStore<ConnectionState>;
  navigation: BoundStore<NavigationState>;
  preferences: BoundStore<PreferencesState>;
  rosterStore: BoundStore<RosterState> | null;
  rosterService: RosterService | null;
  conversationStore: BoundStore<ConversationState>;
  conversationService: ConversationService | null;
  activityStore: BoundStore<ActivityState>;
  native: NativeBridge;
  profileId: string | null;
}

export interface LiveConceptHostProps {
  runtime: LiveConceptRuntime;
  surface: "sessions" | "conversation" | "work";
  onOpenConceptSwitcher(): void;
  onBack(): void;
  onOpenNew(): void;
  onOpenSettings(): void;
  onOpenVoice(): void;
}
```

- [ ] **Step 4: Run the contract test for GREEN**

```bash
cd mobile
npx vitest run src/live-concepts/contract.test.ts
```

Expected: PASS.

### Task 4: Extract Shared Ask-Answer Composition

**Files:**
- Create: `mobile/src/components/composer/composeAskAnswers.ts`
- Create: `mobile/src/components/composer/composeAskAnswers.test.ts`
- Modify: `mobile/src/components/composer/AskComposer.tsx`
- Test: existing `mobile/src/components/composer/AskComposer.test.tsx`

**Interfaces:**
- Produces: `composeAskAnswers(items: readonly AskAnswerItem[]): string` and exported `AskAnswerItem`.
- Consumed by: canonical `AskComposer` and Plan 2 live intent dispatcher.

- [ ] **Step 1: Move the existing byte-exact vectors into a pure-module test**

Create `composeAskAnswers.test.ts` by copying every existing exact-output vector from `AskComposer.test.tsx` verbatim: single and multi option, free text, decide with and without leaning, fallback, explicit skip, unresolved-as-skip, notes, and quoting/control-character cases. Do not paraphrase expected strings.

- [ ] **Step 2: Run the pure test for RED**

```bash
cd mobile
npx vitest run src/components/composer/composeAskAnswers.test.ts
```

Expected: FAIL because the module does not exist.

- [ ] **Step 3: Extract without changing output**

Move `AskAnswerItem`, `askAnswerHeader`, resolution formatting, quoting, and `composeAskAnswers` from `AskComposer.tsx` into the new module. Import the exported function/type back into `AskComposer.tsx`; do not duplicate logic.

- [ ] **Step 4: Run pure and component tests**

```bash
cd mobile
npx vitest run \
  src/components/composer/composeAskAnswers.test.ts \
  src/components/composer/AskComposer.test.tsx
```

Expected: PASS with unchanged exact output.

### Task 5: Add the Live-Concept Boundary and Shared Presentation Primitives

**Files:**
- Create: `mobile/scripts/check-live-concepts-boundary.mjs`
- Create: `mobile/scripts/check-live-concepts-boundary.test.mjs`
- Modify: `mobile/package.json`
- Create: `mobile/src/live-concepts/shared/Disclosure.tsx`
- Create: `mobile/src/live-concepts/shared/Icon.tsx`
- Create: `mobile/src/live-concepts/shared/StatusLabel.tsx`
- Create: `mobile/src/live-concepts/shared/format.ts`
- Create: `mobile/src/live-concepts/shared/shared.test.tsx`

**Interfaces:**
- Produces: `npm run boundary:live-concepts` and renderer-neutral shared presentation primitives.
- Consumed by: every concept renderer lane.

- [ ] **Step 1: Write boundary tests over owned temporary files**

The test creates an owned temporary `src/live-concepts` tree and calls exported `checkLiveConceptBoundary(root)`. Cover one fixture for each rejection:

```js
[
  ["fixture import", 'import "../../../mobile-concepts/src/core/fixtures"', "forbidden-import"],
  ["prototype type", "type X = PrototypeState", "forbidden-symbol"],
  ["fixture access", "state.projection.fixture.sessions", "forbidden-symbol"],
  ["synthetic control", "syntheticTurn", "forbidden-symbol"],
  ["lab hook", "onOpenLabControls()", "forbidden-symbol"],
  ["transport", "new WebSocket(url)", "forbidden-transport"],
  ["tauri", 'import { invoke } from "@tauri-apps/api/core"', "forbidden-transport"],
  ["global css", "body { color: red; }", "unscoped-css"],
]
```

Also prove scoped `.concept-stillwater .sw-row {}` and production type-only imports outside live-concepts are accepted.

- [ ] **Step 2: Run boundary tests for RED**

```bash
cd mobile
node --test scripts/check-live-concepts-boundary.test.mjs
```

Expected: FAIL because the checker does not exist.

- [ ] **Step 3: Implement deterministic boundary checking**

Export `checkLiveConceptBoundary(root): Promise<Violation[]>`. Walk `.ts`, `.tsx`, and `.css` without following symlinks. Resolve every relative import against its containing file and reject any resolved path under the repository's `mobile-concepts/` tree; also reject bare or aliased specifiers naming fixture/scenario/runtime modules. Return sorted `{code, file, detail}` records. The CLI checks `src/live-concepts`, prints one line per violation, and exits 1 when nonempty.

Add package script:

```json
"boundary:live-concepts": "node scripts/check-live-concepts-boundary.mjs",
"boundary": "node scripts/check-boundary.mjs && node scripts/check-live-concepts-boundary.mjs"
```

- [ ] **Step 4: Copy only presentation-neutral shared components**

Copy from `mobile-concepts/src/concepts/shared/` and adapt imports to `../contract`/`../model`. Do not copy tests wholesale. Add focused semantic tests for disclosure expansion/ARIA, decorative versus labeled icons, status text plus non-color marker, and formatter output.

- [ ] **Step 5: Run foundation gates**

```bash
cd mobile
npx biome check --write \
  src/live-concepts \
  src/components/composer/AskComposer.tsx \
  src/components/composer/composeAskAnswers.ts \
  src/components/composer/composeAskAnswers.test.ts
node --test scripts/check-live-concepts-boundary.test.mjs
npm run boundary:live-concepts
npx vitest run \
  src/live-concepts \
  src/components/composer/composeAskAnswers.test.ts \
  src/components/composer/AskComposer.test.tsx
npm run check
npm run boundary
npm run build
git diff --check
```

Expected: every command exits 0.

- [ ] **Step 6: Commit serialized foundation source**

```bash
git add \
  mobile/package.json \
  mobile/scripts/check-live-concepts-boundary.mjs \
  mobile/scripts/check-live-concepts-boundary.test.mjs \
  mobile/src/live-concepts/contract.ts \
  mobile/src/live-concepts/contract.test.ts \
  mobile/src/live-concepts/model.ts \
  mobile/src/live-concepts/shared \
  mobile/src/components/composer/AskComposer.tsx \
  mobile/src/components/composer/composeAskAnswers.ts \
  mobile/src/components/composer/composeAskAnswers.test.ts
git commit --only -m "feat(mobile): define live concept foundation" -- \
  mobile/package.json \
  mobile/scripts/check-live-concepts-boundary.mjs \
  mobile/scripts/check-live-concepts-boundary.test.mjs \
  mobile/src/live-concepts/contract.ts \
  mobile/src/live-concepts/contract.test.ts \
  mobile/src/live-concepts/model.ts \
  mobile/src/live-concepts/shared \
  mobile/src/components/composer/AskComposer.tsx \
  mobile/src/components/composer/composeAskAnswers.ts \
  mobile/src/components/composer/composeAskAnswers.test.ts
```

Record `git rev-parse HEAD` as `FOUNDATION_SHA`. Confirm `BASELINE_B_SHA` is its ancestor. Plan 2 workers branch only from `FOUNDATION_SHA`.

## Completion Gate

```bash
cd mobile
npm test
npm run check
npm run boundary
npm run build
source "$HOME/.cargo/env"
cargo test --manifest-path src-tauri/Cargo.toml
cargo fmt --manifest-path src-tauri/Cargo.toml --check
cargo clippy --manifest-path src-tauri/Cargo.toml --all-targets -- -D warnings
git diff --check
```

Record `BASELINE_A_SHA`, `BASELINE_B_SHA`, and `FOUNDATION_SHA` in the SDD ledger and in the Plan 2 dispatch context.
