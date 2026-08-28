// createLiveIntentDispatcher — Plan 2 Task 3 intent dispatcher.
//
// Translates a LiveConceptIntent (the single union the live-concept renderers
// emit) into the right store/callback action. The dispatcher is a pure local
// translator: it never constructs AppWire/Tauri/WebSocket/native transport,
// never imports concept-lab prototype or runtime modules, owns no timers or polling,
// and never retries. Missing/null runtime handles and stale private keys are
// deterministic safe no-ops.
//
// Two host-owned seams keep the dispatcher transport-free:
//
//  1. LiveConceptDispatcherCallbacks — RootShell-only navigation/lifecycle the
//     dispatcher must NOT own: concept switcher, open conversation by resolved
//     raw ref, Back, New, Settings, Voice. Crucially, the roster row `key` is a
//     display-safe opaque value that MUST NOT be passed as a raw ref; the host
//     contract provides resolveConversationRef(key) backed by the current
//     private refsByKey. Unknown/stale keys resolve to null and never open.
//
//  2. getCurrentProjection() — the host provides the current conversation
//     projection { view, operational } | null backed by the live projector's
//     ConversationOperationalMap. Question/option display keys are display-safe;
//     the dispatcher never derives call IDs, source question keys, option
//     labels, or refs from them. It resolves them ONLY through the operational
//     map's questionKeys/optionKeys, validating callId + source question key on
//     every selected option.
//
// The conversation store is the narrowed LiveConversationState store (not the
// base ConversationState). The dispatcher is the sole writer to its stores via
// the store's own methods; it attaches rejection handlers that publish a
// sanitized message to the owning store rather than swallowing.

import type { StoreApi } from "zustand";
import type { UseBoundStore } from "zustand/react";
import type { InputItem } from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type {
  AskAnswerItem,
  AskResolution,
} from "../components/composer/composeAskAnswers";
import { composeAskAnswers } from "../components/composer/composeAskAnswers";
import type { MobileConversation } from "../conversation/model";
import type { LiveConversationService } from "../services/conversation";
import type { RosterService } from "../services/roster";
import type { LiveConversationState } from "../state/conversation";
import type { RosterState } from "../state/roster";
import type { LiveConceptIntent, QuestionDraft } from "./contract";
import type { ConceptId, LiveConversationView } from "./model";
import type {
  ConversationOperationalMap,
  QuestionLink,
} from "./project-conversation";

// ---------------------------------------------------------------------------
// Narrowed runtime: conversation store is the live (not base) store.
// ---------------------------------------------------------------------------

type BoundStore<State> = UseBoundStore<StoreApi<State>>;

/**
 * The locally narrowed runtime the dispatcher needs. The base
 * LiveConceptRuntime.conversationStore is typed as the base
 * ConversationState; the dispatcher requires the narrowed
 * UseBoundStore<StoreApi<LiveConversationState>> and a
 * LiveConversationService | null so call sites never cast.
 */
export interface LiveIntentDispatcherRuntime {
  readonly rosterStore: BoundStore<RosterState> | null;
  readonly rosterService: RosterService | null;
  readonly conversationStore: BoundStore<LiveConversationState>;
  readonly conversationService: LiveConversationService | null;
}

// ---------------------------------------------------------------------------
// Host callbacks — RootShell-only navigation/lifecycle + private resolvers.
// ---------------------------------------------------------------------------

/**
 * RootShell-only navigation/lifecycle the dispatcher must NOT own, plus the
 * private projection/resolver seams the host backs by current private state.
 * The dispatcher never constructs transport; it routes through these.
 */
export interface LiveConceptDispatcherCallbacks {
  /** Open the concept switcher sheet (distinct from selecting a concept). */
  onOpenConceptSwitcher(): void;
  /**
   * Resolve an opaque roster row `key` (display-safe) to the raw conversation
   * ref, backed by the current private refsByKey. Returns null for
   * unknown/stale keys — the dispatcher never opens on null and never passes
   * the display key as a ref.
   */
  resolveConversationRef(key: string): string | null;
  /**
   * The current conversation projection { view, operational } | null backed by
   * the live projector's ConversationOperationalMap. Null when no conversation
   * is projected. The dispatcher resolves question/option display keys
   * exclusively through `operational`.
   */
  getCurrentProjection(): {
    view: LiveConversationView;
    operational: ConversationOperationalMap;
  } | null;
  /** Open a conversation by its resolved raw ref. */
  onOpenConversation(ref: string): void;
  /** RootShell back action. */
  onBack(): void;
  /** RootShell new-conversation action. */
  onOpenNew(): void;
  /** RootShell settings action. */
  onOpenSettings(): void;
  /** RootShell voice action. */
  onOpenVoice(): void;
}

/**
 * The minimal UI store surface the dispatcher uses. This mirrors the
 * LiveConceptUiStore methods; the dispatcher only calls setConcept,
 * setWorkOpen, setComposerMode, toggleTool, toggleWork, setQuestionDraft.
 */
export interface LiveIntentUiStore {
  getState(): {
    concept: ConceptId;
    workOpen: boolean;
    composerMode: "send" | "steer" | "queue";
    expandedToolKeys: ReadonlySet<string>;
    expandedWorkKeys: ReadonlySet<string>;
    questionDrafts: Readonly<Record<string, QuestionDraft>>;
    setConcept(concept: ConceptId): void;
    setWorkOpen(open: boolean): void;
    setComposerMode(mode: "send" | "steer" | "queue"): void;
    toggleTool(key: string): void;
    toggleWork(key: string): void;
    setQuestionDraft(key: string, draft: QuestionDraft): void;
  };
}

// ---------------------------------------------------------------------------
// Factory
// ---------------------------------------------------------------------------

/**
 * Create an intent dispatcher bound to a narrowed runtime, RootShell
 * callbacks, and UI store. The returned function translates a
 * LiveConceptIntent into store/callback actions. Every Promise is observed;
 * store methods own normal publication; a rejection handler writes a
 * sanitized message to the owning store. No transport, no retry, no timers.
 */
export function createLiveIntentDispatcher(
  runtime: LiveIntentDispatcherRuntime,
  callbacks: LiveConceptDispatcherCallbacks,
  uiStore: LiveIntentUiStore,
): (intent: LiveConceptIntent) => void {
  return function dispatch(intent: LiveConceptIntent): void {
    switch (intent.type) {
      case "switchConcept": {
        uiStore.getState().setConcept(intent.concept);
        return;
      }
      case "openConceptSwitcher": {
        callbacks.onOpenConceptSwitcher();
        return;
      }
      case "refreshRoster": {
        const { rosterStore, rosterService } = runtime;
        if (rosterStore === null || rosterService === null) return;
        observe(rosterStore.getState().refresh(rosterService), (err) => {
          rosterStore.setState({ error: sanitizeError(err) });
        });
        return;
      }
      case "setRosterQuery": {
        const { rosterStore } = runtime;
        if (rosterStore === null) return;
        rosterStore.getState().setSearch(intent.value);
        return;
      }
      case "openConversation": {
        const ref = callbacks.resolveConversationRef(intent.key);
        if (ref === null) return;
        callbacks.onOpenConversation(ref);
        return;
      }
      case "loadOlder": {
        const { conversationStore, conversationService } = runtime;
        if (conversationService === null) return;
        observe(
          conversationStore.getState().loadOlder(conversationService),
          (err) => {
            conversationStore.setState({ error: sanitizeError(err) });
          },
        );
        return;
      }
      case "openWork": {
        uiStore.getState().setWorkOpen(true);
        return;
      }
      case "closeWork": {
        uiStore.getState().setWorkOpen(false);
        return;
      }
      case "setDraft": {
        runtime.conversationStore.getState().setDraft(intent.value);
        return;
      }
      case "setComposerMode": {
        uiStore.getState().setComposerMode(intent.mode);
        return;
      }
      case "submit": {
        const { conversationStore, conversationService } = runtime;
        if (conversationService === null) return;
        const draft = conversationStore.getState().draft;
        const input: InputItem[] = [{ type: "text", text: draft }];
        const method = intent.mode;
        const store = conversationStore.getState();
        const promise =
          method === "send"
            ? store.send(conversationService, input)
            : method === "steer"
              ? store.steer(conversationService, input)
              : store.queue(conversationService, input);
        observe(promise, (err) => {
          conversationStore.setState({ error: sanitizeError(err) });
        });
        return;
      }
      case "interrupt": {
        const { conversationStore, conversationService } = runtime;
        if (conversationService === null) return;
        observe(
          conversationStore.getState().interrupt(conversationService),
          (err) => {
            conversationStore.setState({ error: sanitizeError(err) });
          },
        );
        return;
      }
      case "toggleTool": {
        uiStore.getState().toggleTool(intent.key);
        return;
      }
      case "toggleWork": {
        uiStore.getState().toggleWork(intent.key);
        return;
      }
      case "setQuestionDraft": {
        uiStore.getState().setQuestionDraft(intent.key, intent.value);
        return;
      }
      case "submitQuestion": {
        submitQuestion(runtime, callbacks, uiStore, intent.key);
        return;
      }
      case "goBack": {
        callbacks.onBack();
        return;
      }
      case "openNew": {
        callbacks.onOpenNew();
        return;
      }
      case "openSettings": {
        callbacks.onOpenSettings();
        return;
      }
      case "openVoice": {
        callbacks.onOpenVoice();
        return;
      }
      default: {
        // Compile-time exhaustiveness: adding a new intent variant without a
        // case is a type error here.
        assertNever(intent);
      }
    }
  };
}

// ---------------------------------------------------------------------------
// submitQuestion — canonical all-pending answers payload
// ---------------------------------------------------------------------------

function submitQuestion(
  runtime: LiveIntentDispatcherRuntime,
  callbacks: LiveConceptDispatcherCallbacks,
  uiStore: LiveIntentUiStore,
  clickedKey: string,
): void {
  const { conversationStore, conversationService } = runtime;
  if (conversationService === null) return;

  // Require current source conversation.
  const conversation = conversationStore.getState().conversation;
  if (conversation === null) return;

  // Require current projection.
  const projection = callbacks.getCurrentProjection();
  if (projection === null) return;
  const { view, operational } = projection;

  // Require the clicked key to be in the operational question map.
  if (!operational.questionKeys.has(clickedKey)) return;

  // Compose ONE canonical [answers] payload over ALL current projected pending
  // questions in view order — not only the clicked card.
  const answerItems: AskAnswerItem[] = [];
  for (const questionView of view.questions) {
    const link = operational.questionKeys.get(questionView.key);
    // A projected question view without an operational link is stale/skipped.
    if (link === undefined) continue;

    const draft = uiStore.getState().questionDrafts[questionView.key];
    const note = draft?.note ?? "";
    const selectedOptionKeys = draft?.selectedOptionKeys ?? [];
    const resolution = draft?.resolution ?? null;

    // Resolve the source question from the live conversation by callId + key,
    // so we can read header + ifUnanswered from the retained source data.
    const sourceQuestion = findSourceQuestion(conversation, link);
    const header = sourceQuestion?.header;
    const ifUnanswered = sourceQuestion?.ifUnanswered;

    const answerResolution = resolveResolution(
      resolution,
      selectedOptionKeys,
      link,
      operational,
    );
    // A wrong-call or unknown option refuses the entire submission.
    if (answerResolution === null) return;

    answerItems.push({
      header,
      resolution: answerResolution,
      note,
      ifUnanswered,
    });
  }

  if (answerItems.length === 0) return;

  const text = composeAskAnswers(answerItems);
  const input: InputItem[] = [{ type: "text", text }];
  observe(
    conversationStore.getState().send(conversationService, input),
    (err) => {
      conversationStore.setState({ error: sanitizeError(err) });
    },
  );
}

// Resolve a QuestionDraft.resolution + selected option display keys into an
// AskResolution, validating every selected option belongs to the same
// question/call. Returns null if any selected option is unknown or belongs to
// a different question/call — the caller refuses the whole submission.
function resolveResolution(
  resolution: QuestionDraft["resolution"],
  selectedOptionKeys: readonly string[],
  questionLink: QuestionLink,
  operational: ConversationOperationalMap,
): AskResolution | null {
  // Selected options map to `option` even when the renderer leaves resolution
  // null, as long as the options are valid for this question/call.
  if (
    selectedOptionKeys.length > 0 &&
    (resolution === "answer" || resolution === null)
  ) {
    const labels: string[] = [];
    for (const optKey of selectedOptionKeys) {
      const optLink = operational.optionKeys.get(optKey);
      if (optLink === undefined) return null; // unknown option key
      // Validate the option belongs to this question's call + question key.
      if (
        optLink.callId !== questionLink.callId ||
        optLink.questionKey !== questionLink.questionKey
      ) {
        return null; // wrong-call option
      }
      labels.push(optLink.label);
    }
    return { kind: "option", labels };
  }
  if (resolution === "fallback") {
    return { kind: "fallback" };
  }
  if (resolution === "decide") {
    // decide with empty leaning unless the renderer represented one.
    return { kind: "decide", leaning: "" };
  }
  if (resolution === "skip") {
    return { kind: "skip" };
  }
  // No selection and null/answer resolution with no options → skip.
  return { kind: "skip" };
}

// Find the source MobileAskQuestion in the retained live conversation by
// callId + question key. Returns undefined if not found (stale).
function findSourceQuestion(
  conversation: MobileConversation,
  link: QuestionLink,
): { header: string; ifUnanswered?: string } | undefined {
  for (const item of conversation.items) {
    if (item.kind !== "question") continue;
    if (item.batch.callId !== link.callId) continue;
    for (const q of item.batch.questions) {
      if (q === undefined) continue;
      if (q.key === link.questionKey) {
        return { header: q.header, ifUnanswered: q.ifUnanswered };
      }
    }
  }
  return undefined;
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// Observe a Promise: store methods own normal publication; on rejection write
// a sanitized message via the onReject callback. Never swallow, never leave an
// unhandled rejection, never retry.
function observe<T>(
  promise: Promise<T>,
  onReject: (err: unknown) => void,
): void {
  promise.then(undefined, onReject);
}

// Sanitize an unknown rejection into a plain message. Uses the Error.message
// when available, otherwise String(err). Never leaks non-string values.
function sanitizeError(err: unknown): string {
  if (err instanceof Error) return err.message;
  return String(err);
}

// Compile-time exhaustiveness guard. A new LiveConceptIntent variant without
// a case in the dispatch switch is a type error here.
function assertNever(intent: never): never {
  throw new Error(`unhandled intent: ${JSON.stringify(intent)}`);
}

// Re-export the narrowed link types so tests and the host can reference them
// without importing the projector module separately.
export type { OptionLink, QuestionLink } from "./project-conversation";
