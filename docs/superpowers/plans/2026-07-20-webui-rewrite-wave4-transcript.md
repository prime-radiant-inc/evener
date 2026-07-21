# Web Rewrite Wave 4 — Transcript (M4) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development
> (recommended) or superpowers:executing-plans to implement this plan task-by-task. Waves 1-3
> conventions apply (wave worktree + sub-streams, wave-local SDD artifacts, exclusive manifests,
> controller-owned integration, honest exit-code gates with commits as separate invocations).

**Goal:** The real session pane — a virtualized, streaming, fully-rendered transcript replacing
the Wave-3 placeholder, at parity with the legacy renderer and beyond it.

**Parity floor:** `docs/web-ui/parity/parity-m4-transcript.md` (261 items, file:line receipts)
and `docs/web-ui/parity/contracts-transcript-scroll-liveness.md` (jstest behavior contracts).
Beyond-parity license stands: clean modern treatment beats matching old pixels; behaviors the
checklist marks as dead code in the legacy renderer (its own highlights list several) are NOT
carried.

**Prereqs:** Wave 3 merged to integration. Executes on a wave worktree (`webui-w4-transcript`)
off integration; sub-streams branch off the wave branch.

## Binding constraints (every task)

- **Remount-safe by design**: dockview/StackHost UNMOUNT inactive panes (see PaneHost's
  comment). All durable transcript state lives in the refcounted threads store; component-local
  state is limited to what may honestly die on a tab switch (transient UI like an expanded
  tool row is acceptable to lose; scroll position should survive via the store — see T4).
- **Streaming fast path**: per-delta work never re-renders the settled transcript. In-flight
  item text accumulates via the reducer's `pendingText` chunks; the live leaf appends
  imperatively (surrogate-pair safe) and markdown parses ONCE at item completion. The wave
  gate includes a token-flood benchmark (recorded 10k-delta stream replayed through the store;
  frame budget documented, no dropped-chunk correctness failures).
- Wire truth: ThreadModel/ItemModel from the reducer only — components never touch
  notifications directly. `ensureThread` on mount, `releaseThread` on unmount, exactly once.
- Widgets only (Markdown/CodeBlock/DiffBlock/VirtualList/PaneScaffold/Cadence/Chip/…);
  tokens-only CSS; sentence case; honest liveness (no idle animation — Cadence + quiet text).
- Go wire-nullable arrays `?? []`; await-behavior testing; TDD; pristine output.

## Locked interfaces (streams import these; T1 ships them)

```ts
// panes/session/transcript/types.ts
export interface ItemRenderProps { item: ItemModel; turn: TurnModel; live: boolean }
// One registry entry per ThreadItem.type; unknown types fall back to a raw view.
export function registerItemRenderer(type: string, c: React.ComponentType<ItemRenderProps>): void;

// panes/session/transcript/toolRenderers.ts (T1 ships the registry + fallback; T3 fills it)
export interface ToolRenderProps { item: ItemModel; live: boolean }
export interface ToolRendererDescriptor {
  match: string | ((toolName: string) => boolean);   // exact name or predicate (job_* family)
  summary(item: ItemModel): string;                   // one-line purpose-first summary
  body?: React.ComponentType<ToolRenderProps>;        // expanded content; default raw output
  autoExpand?(item: ItemModel): boolean;              // e.g. shell on nonzero exit
}
export function registerToolRenderer(d: ToolRendererDescriptor): void;

// panes/session/transcript/useTranscript.ts (T1)
// Selects the ThreadModel, exposes turns + streaming handle + paging + liveness inputs.
export function useTranscript(ref: string): {
  model: ThreadModel | undefined;
  loadOlder(): Promise<void>;        // thread/turns/list via olderCursor → prependOlderTurns
  loadingOlder: boolean;
};
```

## Tasks

### T1 (sequential): transcript core
SessionPane (replaces the Wave-3 placeholder in `panes/session/`): PaneScaffold with the
thread's name + Cadence (state + REAL frameTimes — derive from the model's lastFrameAt ring;
add a small frameTimes ring buffer to the threads store keyed by ref, capped 60s/64 entries —
sanctioned store extension, TDD); ensure/release lifecycle; VirtualList over turns; a
TurnBlock skeleton (items in order, plain text rendering for every type via a fallback
ItemView); the item-renderer + tool-renderer registries with fallbacks; StreamingText (the
imperative leaf: props {chunks: string[], onCommit}, appends only new chunks, joins on
completion — TDD with chunk-sequence fixtures incl. surrogate pairs); useTranscript. Gate:
suite green; a live smoke (dev hub) showing raw streaming text in the real pane.

### T2 ∥ T3 ∥ T4 (streams off the wave branch after T1):

- **T2 message renderers** (`transcript/messages/**`): agentMessage (Markdown settled /
  StreamingText live), userMessage, steering rendered as user-sourced messages, system/skill
  notices (quiet, collapsed-by-default groups), reasoning "think" blocks (collapsible; open
  while live via StreamingText; collapse to "Thought for Ns" + preview on completion),
  turn separators (timing/duration/usage/cost from TurnModel — compact, ink-mid).
- **T3 tool renderers + agent-work surfaces** (`transcript/tools/**`): descriptors for
  read/grep/ls/glob (path + match-count summaries), shell (command summary, output body,
  autoExpand on failure), web fetch/search, delegate, the job_* family; the subagent module
  (one aggregated block per turn: job rows spawned→running→done/failed with duration + result
  preview + open-transcript action wiring `openPane("transcript",{ref})` — the transcript
  pane TYPE registers in Wave 8/periphery, so the action may open a session pane for now with
  a comment; watched-child live rows via additive `readThread(ref,false,true,false)` through
  a sanctioned threads-store `watchThread(ref)` helper); ask_user question cards (from
  toolCall items, questions parsed from argumentsJSON, answered via the composer in Wave 5 —
  render read-only cards with a "answer in composer (wave 5)" note this wave); sandbox
  escalation cards (serf/sandbox/escalation/requested → resolve via
  `serf/sandbox/escalation/resolve` — THIS one is fully interactive this wave: approve/deny
  buttons calling the wire method; TDD with fakeClient).
- **T4 flow: scroll/liveness/paging/media** (`transcript/flow/**` + SessionPane edits by
  manifest): stick-to-bottom only when at bottom before mutation; "↓ N new" pill (Badge) with
  attention-aware variant; prepend anchoring for loadOlder; scroll position persisted to the
  threads store per ref (survives remount — sanctioned extension); inline + output images
  with a lightbox (Dialog-based); warnings rendering; honest liveness line (quiet ~Nm →
  may-be-stalled per the legacy thresholds 20s/180s, driven by lastFrameAt — timers test via
  fake timers, display only, no network).

### T5: wave close
Parity sweep against all 261 checklist items (report maps each to shipped/deferred-with-reason/
dead-in-legacy); jstest-contract sweep; the token-flood benchmark; live streaming proof
(real hub + real session: streaming text, think block, a tool call, scroll behavior — evidence
committed); full gates; wave4-report.md; merge to integration.
