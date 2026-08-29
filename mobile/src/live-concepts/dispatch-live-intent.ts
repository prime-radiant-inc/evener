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
// generic fixed message to the owning store rather than swallowing.
//
// Error safety: rejection handlers NEVER publish the raw Error.message — a
// secret/ref-bearing rejection must not leak into the store. Every conversation
// rejection goes through the store-owned publishExternalError action, passing
// the exact ref + conversationGeneration captured before the Promise so a late
// rejection after a generation/ref change produces zero publication. Roster
// rejections publish a generic fixed message only if the roster generation
// captured before the Promise is still current.

import type { StoreApi } from "zustand";
import type { UseBoundStore } from "zustand/react";
import type { InputItem } from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type {
  AskAnswerItem,
  AskResolution,
} from "../components/composer/composeAskAnswers";
import { composeAskAnswers } from "../components/composer/composeAskAnswers";
import type {
  MobileAskQuestion,
  MobileConversation,
} from "../conversation/model";
import type { LiveConversationService } from "../services/conversation";
import type { RosterService } from "../services/roster";
import type {
  LiveActivitySink,
  LiveConversationState,
} from "../state/conversation";
import type { RosterState } from "../state/roster";
import type { LiveConceptIntent } from "./contract";
import type { QuestionDraft } from "./conversation/primitives";
import type { ConceptId, LiveConversationView } from "./model";
import type {
  ConversationOperationalMap,
  QuestionLink,
} from "./project-conversation";

// ---------------------------------------------------------------------------
// Narrowed runtime: conversation store is the live (not base) store.
// ---------------------------------------------------------------------------

type BoundStore<State> = UseBoundStore<StoreApi<State>>;

// Generic fixed error messages. Never the raw Error.message — a secret or
// ref-bearing rejection must not leak into the store.
const ROSTER_ERROR_MESSAGE = "Roster operation failed";
const CONVERSATION_ERROR_MESSAGE = "Conversation operation failed";

/**
 * The locally narrowed runtime the dispatcher needs. The base
 * LiveConceptRuntime.conversationStore is typed as the base
 * ConversationState; the dispatcher requires the narrowed
 * UseBoundStore<StoreApi<LiveConversationState>> and a
 * LiveConversationService | null so call sites never cast.
 *
 * The conversation store must expose a store-owned publishExternalError action
 * (being added independently on the integrated parent) so the dispatcher never
 * calls external StoreApi.setState for error publication. It passes the exact
 * ref + conversationGeneration captured before each Promise; the store action
 * is responsible for suppressing publication when they no longer match.
 */
export interface LiveIntentDispatcherRuntime {
  readonly rosterStore: BoundStore<RosterState> | null;
  readonly rosterService: RosterService | null;
  readonly conversationStore: BoundStore<
    LiveConversationState & {
      publishExternalError(
        message: string,
        expectedRef: string | null,
        expectedGeneration: number,
      ): void;
    }
  >;
  readonly conversationService: LiveConversationService | null;
  readonly activitySink: LiveActivitySink;
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
  /** Open bounded evidence by its opaque display key. */
  onOpenEvidence(evidenceKey: string, triggerKey: string): void;
  /** Close the bounded evidence presentation. */
  onCloseEvidence(): void;
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
 * store methods own normal publication; a rejection handler publishes a
 * generic fixed message to the owning store. No transport, no retry, no timers.
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
        const rosterGen = rosterStore.getState().generation;
        observe(rosterStore.getState().refresh(rosterService), () => {
          // Publish a generic fixed message only if the roster generation
          // captured before the Promise is still current — a late rejection
          // after a profile switch / reset produces zero publication.
          if (rosterStore.getState().generation === rosterGen) {
            rosterStore.setState({ error: ROSTER_ERROR_MESSAGE });
          }
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
        observeConversation(runtime, (store, service) =>
          store.loadOlder(service),
        );
        return;
      }
      case "retryRead": {
        observeConversation(runtime, (store, service) =>
          store.rehydrate(service, runtime.activitySink),
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
        observeConversation(runtime, (store, service) =>
          method === "send"
            ? store.send(service, input)
            : method === "steer"
              ? store.steer(service, input)
              : store.queue(service, input),
        );
        return;
      }
      case "interrupt": {
        observeConversation(runtime, (store, service) =>
          store.interrupt(service),
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
      case "openEvidence": {
        callbacks.onOpenEvidence(intent.evidenceKey, intent.triggerKey);
        return;
      }
      case "closeEvidence": {
        callbacks.onCloseEvidence();
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
// Conversation error observation — capture ref + generation, publish generic
// ---------------------------------------------------------------------------

// Observe a conversation Promise: capture the exact ref + conversationGeneration
// before the Promise; on rejection call the store-owned publishExternalError
// with the generic fixed message and the captured identity. The store action
// is responsible for suppressing publication when the identity no longer
// matches. Never calls external StoreApi.setState, never publishes the raw
// Error.message.
function observeConversation(
  runtime: LiveIntentDispatcherRuntime,
  call: (
    store: LiveConversationState & {
      publishExternalError(
        message: string,
        expectedRef: string | null,
        expectedGeneration: number,
      ): void;
    },
    service: LiveConversationService,
  ) => Promise<void>,
): void {
  const { conversationStore, conversationService } = runtime;
  if (conversationService === null) return;
  const store = conversationStore.getState();
  const expectedRef = store.ref;
  const expectedGen = store.conversationGeneration;
  observe(call(store, conversationService), () => {
    conversationStore
      .getState()
      .publishExternalError(
        CONVERSATION_ERROR_MESSAGE,
        expectedRef,
        expectedGen,
      );
  });
}

// ---------------------------------------------------------------------------
// submitQuestion — canonical all-or-nothing answers payload
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

  // C1: the clicked key must be in BOTH the current view.questions AND the
  // current operational questionKeys. If either is missing, return with zero
  // send — never continue, never compose a fallback.
  const clickedInView = view.questions.some((q) => q.key === clickedKey);
  if (!clickedInView) return;
  if (!operational.questionKeys.has(clickedKey)) return;

  // Compose ONE canonical [answers] payload over ALL current projected pending
  // questions in view order — not only the clicked card.
  const answerItems: AskAnswerItem[] = [];
  for (const questionView of view.questions) {
    // C1: every view question MUST have an operational link. A missing link
    // for ANY question (not just the clicked one) is all-or-nothing: return
    // with zero send, never skip and continue.
    const link = operational.questionKeys.get(questionView.key);
    if (link === undefined) return;

    // C1: every view question MUST have an exact source MobileAskQuestion
    // matching callId + questionKey in the retained conversation. A missing
    // source for ANY question is all-or-nothing: return with zero send, never
    // compose a fallback header/ifUnanswered.
    const sourceQuestion = findSourceQuestion(conversation, link);
    if (sourceQuestion === undefined) return;

    const draft = uiStore.getState().questionDrafts[questionView.key];
    const note = draft?.note ?? "";
    const selectedOptionKeys = draft?.selectedOptionKeys ?? [];
    const resolution = draft?.resolution ?? null;

    // Validate the source header + ifUnanswered come from the exact source
    // question — no fallback composition.
    const header = sourceQuestion.header;
    const ifUnanswered = sourceQuestion.ifUnanswered;

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
  observeConversation(runtime, (store, service) => store.send(service, input));
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
): MobileAskQuestion | undefined {
  for (const item of conversation.items) {
    if (item.kind !== "question") continue;
    if (item.batch.callId !== link.callId) continue;
    for (const q of item.batch.questions) {
      if (q === undefined) continue;
      if (q.key === link.questionKey) {
        return q;
      }
    }
  }
  return undefined;
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// Observe a Promise: store methods own normal publication; on rejection call
// the onReject callback. Never swallow, never leave an unhandled rejection,
// never retry.
function observe<T>(promise: Promise<T>, onReject: () => void): void {
  promise.then(undefined, onReject);
}

// Compile-time exhaustiveness guard. A new LiveConceptIntent variant without
// a case in the dispatch switch is a type error here.
function assertNever(intent: never): never {
  throw new Error(`unhandled intent: ${JSON.stringify(intent)}`);
}

// Re-export the narrowed link types so tests and the host can reference them
// without importing the projector module separately.
export type { OptionLink, QuestionLink } from "./project-conversation";
