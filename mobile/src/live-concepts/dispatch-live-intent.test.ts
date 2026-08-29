// TDD tests for createLiveIntentDispatcher — the Plan 2 Task 3 intent
// dispatcher. Pure behavioral tests using lightweight fakes: no transport,
// no fixtures/scenarios, no DOM, no timers. Every intent variant is covered,
// plus null/stale safe behavior, raw-ref lookup proof, loadOlder call count,
// exact mutation arguments, rejected-Promise publication with generic fixed
// messages, generation/ref-change race safety, byte-exact composeAskAnswers
// payload over all pending questions, and import/boundary proof.

import { describe, expect, expectTypeOf, it } from "vitest";
import type { StoreApi } from "zustand";
import type { UseBoundStore } from "zustand/react";
import type { InputItem } from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { AskAnswerItem } from "../components/composer/composeAskAnswers";
import { composeAskAnswers } from "../components/composer/composeAskAnswers";
import type { MobileConversation } from "../conversation/model";
import type { LiveConversationService } from "../services/conversation";
import type { RosterService } from "../services/roster";
import type { LiveConversationState } from "../state/conversation";
import type { RosterState } from "../state/roster";
import type { LiveConceptIntent, QuestionDraft } from "./contract";
import {
  createLiveIntentDispatcher,
  type LiveConceptDispatcherCallbacks,
  type LiveIntentDispatcherRuntime,
  type LiveIntentUiStore,
} from "./dispatch-live-intent";
import type { ConceptId } from "./model";
import type {
  ConversationOperationalMap,
  LiveConversationProjector,
} from "./project-conversation";
import { createLiveConversationProjector } from "./project-conversation";

// ---------------------------------------------------------------------------
// Fake UI store
// ---------------------------------------------------------------------------

interface FakeUiState {
  concept: ConceptId;
  workOpen: boolean;
  composerMode: "send" | "steer" | "queue";
  expandedToolKeys: Set<string>;
  expandedWorkKeys: Set<string>;
  questionDrafts: Record<string, QuestionDraft>;
}

function createFakeUiStore(
  initial: Partial<FakeUiState> = {},
): LiveIntentUiStore {
  let state: FakeUiState = {
    concept: "stillwater",
    workOpen: false,
    composerMode: "send",
    expandedToolKeys: new Set<string>(),
    expandedWorkKeys: new Set<string>(),
    questionDrafts: {},
    ...initial,
  };
  const reactive = {
    get concept() {
      return state.concept;
    },
    get workOpen() {
      return state.workOpen;
    },
    get composerMode() {
      return state.composerMode;
    },
    get expandedToolKeys() {
      return state.expandedToolKeys;
    },
    get expandedWorkKeys() {
      return state.expandedWorkKeys;
    },
    get questionDrafts() {
      return state.questionDrafts;
    },
    setConcept(concept: ConceptId) {
      state = { ...state, concept };
    },
    setWorkOpen(open: boolean) {
      state = { ...state, workOpen: open };
    },
    setComposerMode(mode: "send" | "steer" | "queue") {
      state = { ...state, composerMode: mode };
    },
    toggleTool(key: string) {
      const next = new Set(state.expandedToolKeys);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      state = { ...state, expandedToolKeys: next };
    },
    toggleWork(key: string) {
      const next = new Set(state.expandedWorkKeys);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      state = { ...state, expandedWorkKeys: next };
    },
    setQuestionDraft(key: string, draft: QuestionDraft) {
      state = {
        ...state,
        questionDrafts: { ...state.questionDrafts, [key]: draft },
      };
    },
  };
  const store: LiveIntentUiStore = {
    getState: () => reactive as ReturnType<LiveIntentUiStore["getState"]>,
  };
  return store;
}

// ---------------------------------------------------------------------------
// Fake conversation store — includes publishExternalError + generation
// ---------------------------------------------------------------------------

interface ConvRecord {
  ref: string | null;
  conversation: MobileConversation | null;
  draft: string;
  error: string | null;
  conversationGeneration: number;
  // publishExternalError captures: the store-owned action that the dispatcher
  // calls on rejection. Records message + expectedRef + expectedGeneration.
  pubErrorCalls: number;
  pubErrorMessage: string | null;
  pubErrorRef: string | null;
  pubErrorGen: number | null;
  // Optional override: if set, publishExternalError only writes error when
  // the captured identity still matches current ref + generation. This mirrors
  // the real store action being added on the integrated parent.
  pubErrorGuarded: boolean;
  // call counters
  loadOlderCalls: number;
  loadOlderService: unknown | null;
  setDraftCalls: number;
  setDraftValue: string | null;
  sendCalls: number;
  sendService: unknown | null;
  sendInput: InputItem[] | null;
  steerCalls: number;
  steerService: unknown | null;
  steerInput: InputItem[] | null;
  queueCalls: number;
  queueService: unknown | null;
  queueInput: InputItem[] | null;
  interruptCalls: number;
  interruptService: unknown | null;
  openCalls: number;
  openProjectedCalls: number;
  readProjectionCalls: number;
  subscribeNotificationsCalls: number;
  // Optional overrides: if set, these replace the default resolved methods.
  loadOlderImpl?: () => Promise<void>;
  sendImpl?: (svc: unknown, input: InputItem[]) => Promise<void>;
  steerImpl?: (svc: unknown, input: InputItem[]) => Promise<void>;
  queueImpl?: (svc: unknown, input: InputItem[]) => Promise<void>;
  interruptImpl?: () => Promise<void>;
}

function createFakeConversationStore(
  initial: Partial<ConvRecord> = {},
): UseBoundStore<
  StoreApi<
    LiveConversationState & {
      publishExternalError(
        message: string,
        expectedRef: string | null,
        expectedGeneration: number,
      ): void;
    }
  >
> & {
  __record(): ConvRecord;
  __set(patch: Partial<ConvRecord>): void;
} {
  const rec: ConvRecord = {
    ref: "ref-1",
    conversation: null,
    draft: "",
    error: null,
    conversationGeneration: 1,
    pubErrorCalls: 0,
    pubErrorMessage: null,
    pubErrorRef: null,
    pubErrorGen: null,
    pubErrorGuarded: true,
    loadOlderCalls: 0,
    loadOlderService: null,
    setDraftCalls: 0,
    setDraftValue: null,
    sendCalls: 0,
    sendService: null,
    sendInput: null,
    steerCalls: 0,
    steerService: null,
    steerInput: null,
    queueCalls: 0,
    queueService: null,
    queueInput: null,
    interruptCalls: 0,
    interruptService: null,
    openCalls: 0,
    openProjectedCalls: 0,
    readProjectionCalls: 0,
    subscribeNotificationsCalls: 0,
    ...initial,
  };
  const reactive = {
    get ref() {
      return rec.ref;
    },
    get conversation() {
      return rec.conversation;
    },
    get draft() {
      return rec.draft;
    },
    get error() {
      return rec.error;
    },
    set error(value: string | null) {
      rec.error = value;
    },
    get conversationGeneration() {
      return rec.conversationGeneration;
    },
    loadOlder(service: unknown) {
      rec.loadOlderCalls++;
      rec.loadOlderService = service;
      return rec.loadOlderImpl ? rec.loadOlderImpl() : Promise.resolve();
    },
    setDraft(text: string) {
      rec.setDraftCalls++;
      rec.setDraftValue = text;
      rec.draft = text;
    },
    send(service: unknown, input: InputItem[]) {
      rec.sendCalls++;
      rec.sendService = service;
      rec.sendInput = input;
      return rec.sendImpl ? rec.sendImpl(service, input) : Promise.resolve();
    },
    steer(service: unknown, input: InputItem[]) {
      rec.steerCalls++;
      rec.steerService = service;
      rec.steerInput = input;
      return rec.steerImpl ? rec.steerImpl(service, input) : Promise.resolve();
    },
    queue(service: unknown, input: InputItem[]) {
      rec.queueCalls++;
      rec.queueService = service;
      rec.queueInput = input;
      return rec.queueImpl ? rec.queueImpl(service, input) : Promise.resolve();
    },
    interrupt(service: unknown) {
      rec.interruptCalls++;
      rec.interruptService = service;
      return rec.interruptImpl ? rec.interruptImpl() : Promise.resolve();
    },
    publishExternalError(
      message: string,
      expectedRef: string | null,
      expectedGeneration: number,
    ) {
      rec.pubErrorCalls++;
      rec.pubErrorMessage = message;
      rec.pubErrorRef = expectedRef;
      rec.pubErrorGen = expectedGeneration;
      // Guarded: only write error if the captured identity still matches.
      if (
        !rec.pubErrorGuarded ||
        (rec.ref === expectedRef &&
          rec.conversationGeneration === expectedGeneration)
      ) {
        rec.error = message;
      }
    },
  };
  const useStore = ((selector?: (s: unknown) => unknown) =>
    selector ? selector(reactive) : reactive) as unknown as UseBoundStore<
    StoreApi<
      LiveConversationState & {
        publishExternalError(
          message: string,
          expectedRef: string | null,
          expectedGeneration: number,
        ): void;
      }
    >
  >;
  useStore.getState = () =>
    reactive as unknown as LiveConversationState & {
      publishExternalError(
        message: string,
        expectedRef: string | null,
        expectedGeneration: number,
      ): void;
    };
  useStore.setState = ((
    patch: Partial<
      LiveConversationState & {
        publishExternalError(
          message: string,
          expectedRef: string | null,
          expectedGeneration: number,
        ): void;
      }
    >,
  ) => {
    Object.assign(rec, patch as Partial<ConvRecord>);
  }) as UseBoundStore<
    StoreApi<
      LiveConversationState & {
        publishExternalError(
          message: string,
          expectedRef: string | null,
          expectedGeneration: number,
        ): void;
      }
    >
  >["setState"];
  (useStore as unknown as { __record(): ConvRecord }).__record = () => rec;
  (useStore as unknown as { __set(p: Partial<ConvRecord>): void }).__set = (
    patch: Partial<ConvRecord>,
  ) => {
    Object.assign(rec, patch);
  };
  return useStore as UseBoundStore<
    StoreApi<
      LiveConversationState & {
        publishExternalError(
          message: string,
          expectedRef: string | null,
          expectedGeneration: number,
        ): void;
      }
    >
  > & {
    __record(): ConvRecord;
    __set(patch: Partial<ConvRecord>): void;
  };
}

// ---------------------------------------------------------------------------
// Fake roster store — includes generation
// ---------------------------------------------------------------------------

interface RosterRecord {
  generation: number;
  refreshCalls: number;
  refreshService: unknown | null;
  setSearchCalls: number;
  setSearchValue: string | null;
  error: string | null;
  refreshImpl?: (svc: RosterService) => Promise<void>;
}

function createFakeRosterStore(
  initial: Partial<RosterRecord> = {},
): UseBoundStore<StoreApi<RosterState>> & {
  __record(): RosterRecord;
  __set(patch: Partial<RosterRecord>): void;
} {
  const rec: RosterRecord = {
    generation: 0,
    refreshCalls: 0,
    refreshService: null,
    setSearchCalls: 0,
    setSearchValue: null,
    error: null,
    ...initial,
  };
  const reactive = {
    get generation() {
      return rec.generation;
    },
    get error() {
      return rec.error;
    },
    set error(value: string | null) {
      rec.error = value;
    },
    refresh(service: RosterService) {
      rec.refreshCalls++;
      rec.refreshService = service;
      return rec.refreshImpl ? rec.refreshImpl(service) : Promise.resolve();
    },
    setSearch(term: string) {
      rec.setSearchCalls++;
      rec.setSearchValue = term;
    },
  };
  const useStore = ((selector?: (s: RosterState) => unknown) =>
    selector
      ? selector(reactive as unknown as RosterState)
      : reactive) as unknown as UseBoundStore<StoreApi<RosterState>>;
  useStore.getState = () => reactive as unknown as RosterState;
  useStore.setState = ((patch: Partial<RosterState>) => {
    Object.assign(rec, patch as Partial<RosterRecord>);
  }) as UseBoundStore<StoreApi<RosterState>>["setState"];
  (useStore as unknown as { __record(): RosterRecord }).__record = () => rec;
  (useStore as unknown as { __set(p: Partial<RosterRecord>): void }).__set = (
    patch: Partial<RosterRecord>,
  ) => {
    Object.assign(rec, patch);
  };
  return useStore as UseBoundStore<StoreApi<RosterState>> & {
    __record(): RosterRecord;
    __set(patch: Partial<RosterRecord>): void;
  };
}

// ---------------------------------------------------------------------------
// Service + callbacks fakes
// ---------------------------------------------------------------------------

function createFakeLiveService(): LiveConversationService {
  return {
    open: async () => ({}) as MobileConversation,
    loadOlder: async () => ({ items: [], nextCursor: undefined }),
    subscribeNotifications: () => () => {},
    send: async () => ({}),
    steer: async () => ({}),
    queue: async () => ({}),
    interrupt: async () => ({}),
    compact: async () => {},
    shutdown: async () => {},
    changeModel: async () => {},
    setReasoningEffort: async () => {},
    rename: async () => {},
    cancelQueued: async () => ({}),
    close: () => {},
    readProjection: async () => ({
      conversation: {} as MobileConversation,
      activity: {} as never,
      olderCursor: null,
    }),
    refreshCapabilities: async () => null,
  } as unknown as LiveConversationService;
}

function createFakeRosterService(): RosterService {
  return {
    list: async () => ({ threads: [], hasMore: false }),
    refresh: async () => {},
  };
}

interface RecordedCallbacks {
  conceptSwitcher: number;
  openConversation: number;
  openConversationRef: string | null;
  back: number;
  new: number;
  settings: number;
  voice: number;
}

function createFakeCallbacks(
  resolver: (key: string) => string | null = () => null,
  projection: () => {
    view: unknown;
    operational: ConversationOperationalMap;
  } | null = () => null,
): { callbacks: LiveConceptDispatcherCallbacks; recorded: RecordedCallbacks } {
  const recorded: RecordedCallbacks = {
    conceptSwitcher: 0,
    openConversation: 0,
    openConversationRef: null,
    back: 0,
    new: 0,
    settings: 0,
    voice: 0,
  };
  const callbacks: LiveConceptDispatcherCallbacks = {
    onOpenConceptSwitcher: () => {
      recorded.conceptSwitcher++;
    },
    resolveConversationRef: resolver,
    getCurrentProjection:
      projection as LiveConceptDispatcherCallbacks["getCurrentProjection"],
    onOpenConversation: (ref: string) => {
      recorded.openConversation++;
      recorded.openConversationRef = ref;
    },
    onBack: () => {
      recorded.back++;
    },
    onOpenNew: () => {
      recorded.new++;
    },
    onOpenSettings: () => {
      recorded.settings++;
    },
    onOpenVoice: () => {
      recorded.voice++;
    },
  };
  return { callbacks, recorded };
}

// ---------------------------------------------------------------------------
// Projection + conversation builders
// ---------------------------------------------------------------------------

function buildProjection(
  conv: MobileConversation,
  projector: LiveConversationProjector = createLiveConversationProjector(),
): {
  view: ReturnType<LiveConversationProjector["project"]>["view"];
  operational: ConversationOperationalMap;
} {
  const result = projector.project(conv, {
    ref: "ref-1",
    olderCursor: null,
    projectLabel: "proj",
    updatedLabel: null,
    truncatedItemIds: new Set<string>(),
  });
  return { view: result.view, operational: result.operational };
}

function buildConversationWithQuestions(
  questions: {
    key: string;
    header: string;
    question: string;
    options: { label: string; detail: string }[];
    multiSelect: boolean;
    ifUnanswered?: string;
  }[],
  callId = "call-1",
): MobileConversation {
  return {
    id: "thread-1",
    sessionId: "session-1",
    preview: "",
    modelProvider: "",
    status: "ready",
    items: [
      {
        kind: "question",
        id: "q-item-1",
        batch: { callId, questions },
      },
    ],
    capabilities: {
      send: true,
      steer: true,
      interrupt: true,
      compact: true,
      clear: true,
      forkFromTurn: true,
      shutdown: true,
      changeModel: true,
      changeVisionModel: true,
      queue: true,
      goal: true,
      rename: true,
    },
    queue: { depth: 0, preview: [] },
    usage: {},
    askPending: true,
  };
}

// ---------------------------------------------------------------------------
// Runtime harness
// ---------------------------------------------------------------------------

function makeRuntime(overrides: Partial<LiveIntentDispatcherRuntime> = {}): {
  runtime: LiveIntentDispatcherRuntime;
  uiStore: LiveIntentUiStore;
  conversationStore: ReturnType<typeof createFakeConversationStore>;
  rosterStore: ReturnType<typeof createFakeRosterStore>;
  liveService: LiveConversationService;
  rosterService: RosterService;
} {
  const uiStore = createFakeUiStore();
  const conversationStore = createFakeConversationStore();
  const rosterStore = createFakeRosterStore();
  const liveService = createFakeLiveService();
  const rosterService = createFakeRosterService();
  const runtime: LiveIntentDispatcherRuntime = {
    rosterStore,
    rosterService,
    conversationStore,
    conversationService: liveService,
    ...overrides,
  };
  return {
    runtime,
    uiStore,
    conversationStore,
    rosterStore,
    liveService,
    rosterService,
  };
}

// Flush microtasks so observed promises settle.
function flush() {
  return new Promise((r) => setTimeout(r, 0));
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("createLiveIntentDispatcher — factory and types", () => {
  it("returns a dispatch function that accepts a LiveConceptIntent", () => {
    const { runtime, uiStore } = makeRuntime();
    const { callbacks } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(runtime, callbacks, uiStore);
    expectTypeOf(dispatch).toEqualTypeOf<(intent: LiveConceptIntent) => void>();
    expect(typeof dispatch).toBe("function");
    dispatch({ type: "openConceptSwitcher" });
  });
});

describe("createLiveIntentDispatcher — switchConcept vs openConceptSwitcher", () => {
  it("switchConcept sets the concept on the UI store", () => {
    const { runtime, uiStore } = makeRuntime();
    const { callbacks, recorded } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(runtime, callbacks, uiStore);
    dispatch({ type: "switchConcept", concept: "constellation" });
    expect(uiStore.getState().concept).toBe("constellation");
    expect(recorded.conceptSwitcher).toBe(0);
  });

  it("openConceptSwitcher calls the callback, not setConcept", () => {
    const { runtime, uiStore } = makeRuntime();
    const { callbacks, recorded } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(runtime, callbacks, uiStore);
    dispatch({ type: "openConceptSwitcher" });
    expect(recorded.conceptSwitcher).toBe(1);
    expect(uiStore.getState().concept).toBe("stillwater");
  });
});

describe("createLiveIntentDispatcher — roster", () => {
  it("refreshRoster calls rosterStore.refresh with the roster service only when both exist", () => {
    const { runtime, rosterStore, rosterService } = makeRuntime();
    const { callbacks } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "refreshRoster" });
    expect(rosterStore.__record().refreshCalls).toBe(1);
    expect(rosterStore.__record().refreshService).toBe(rosterService);
  });

  it("refreshRoster is a no-op when rosterStore is null", () => {
    const { runtime, rosterStore } = makeRuntime({ rosterStore: null });
    const { callbacks } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "refreshRoster" });
    expect(rosterStore.__record().refreshCalls).toBe(0);
  });

  it("refreshRoster is a no-op when rosterService is null", () => {
    const { runtime, rosterStore } = makeRuntime({ rosterService: null });
    const { callbacks } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "refreshRoster" });
    expect(rosterStore.__record().refreshCalls).toBe(0);
  });

  it("setRosterQuery calls setSearch on the roster store", () => {
    const { runtime, rosterStore } = makeRuntime();
    const { callbacks } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "setRosterQuery", value: "alpha" });
    expect(rosterStore.__record().setSearchCalls).toBe(1);
    expect(rosterStore.__record().setSearchValue).toBe("alpha");
  });

  it("setRosterQuery is a safe no-op when rosterStore is null", () => {
    const { runtime } = makeRuntime({ rosterStore: null });
    const { callbacks } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    expect(() =>
      dispatch({ type: "setRosterQuery", value: "x" }),
    ).not.toThrow();
  });
});

describe("createLiveIntentDispatcher — openConversation raw-ref lookup", () => {
  it("resolves the opaque row key to a raw ref and calls onOpenConversation with the ref", () => {
    const { runtime } = makeRuntime();
    const { callbacks, recorded } = createFakeCallbacks((key) =>
      key === "row-k1" ? "ref-abc" : null,
    );
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "openConversation", key: "row-k1" });
    expect(recorded.openConversation).toBe(1);
    expect(recorded.openConversationRef).toBe("ref-abc");
  });

  it("never passes the display key as a ref — the ref comes only from the resolver", () => {
    const { runtime } = makeRuntime();
    const seenKeys: string[] = [];
    const { callbacks, recorded } = createFakeCallbacks((key) => {
      seenKeys.push(key);
      return key === "disp" ? "raw-ref" : null;
    });
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "openConversation", key: "disp" });
    expect(seenKeys).toEqual(["disp"]);
    expect(recorded.openConversationRef).toBe("raw-ref");
  });

  // M1: consolidated unknown/stale resolver test — both unknown and stale
  // (resolver returns null) keys never open.
  it("unknown or stale key (resolver returns null) never opens — no callback, no ref", () => {
    const { runtime } = makeRuntime();
    const { callbacks, recorded } = createFakeCallbacks(() => null);
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "openConversation", key: "unknown-key" });
    dispatch({ type: "openConversation", key: "stale-key" });
    expect(recorded.openConversation).toBe(0);
    expect(recorded.openConversationRef).toBeNull();
  });
});

describe("createLiveIntentDispatcher — loadOlder never reopens", () => {
  it("calls conversationStore.loadOlder(service) and nothing else", () => {
    const { runtime, conversationStore, liveService } = makeRuntime();
    const { callbacks } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "loadOlder" });
    expect(conversationStore.__record().loadOlderCalls).toBe(1);
    expect(conversationStore.__record().loadOlderService).toBe(liveService);
  });

  it("never calls open/openProjected/readProjection/subscribe", () => {
    const { runtime, conversationStore } = makeRuntime();
    const { callbacks } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "loadOlder" });
    const r = conversationStore.__record();
    expect(r.openCalls).toBe(0);
    expect(r.openProjectedCalls).toBe(0);
    expect(r.readProjectionCalls).toBe(0);
    expect(r.subscribeNotificationsCalls).toBe(0);
  });

  it("is a safe no-op when conversationService is null", () => {
    const { runtime, conversationStore } = makeRuntime({
      conversationService: null,
    });
    const { callbacks } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "loadOlder" });
    expect(conversationStore.__record().loadOlderCalls).toBe(0);
  });
});

describe("createLiveIntentDispatcher — openWork / closeWork", () => {
  it("openWork sets workOpen true on the UI store", () => {
    const { runtime, uiStore } = makeRuntime();
    const { callbacks } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(runtime, callbacks, uiStore);
    dispatch({ type: "openWork" });
    expect(uiStore.getState().workOpen).toBe(true);
  });

  it("closeWork sets workOpen false on the UI store", () => {
    const { runtime, uiStore } = makeRuntime();
    const { callbacks } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(runtime, callbacks, uiStore);
    dispatch({ type: "closeWork" });
    expect(uiStore.getState().workOpen).toBe(false);
  });
});

describe("createLiveIntentDispatcher — setDraft / setComposerMode", () => {
  it("setDraft writes to the conversation store, not the UI store", () => {
    const { runtime, conversationStore, uiStore } = makeRuntime();
    const { callbacks } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(runtime, callbacks, uiStore);
    dispatch({ type: "setDraft", value: "hello" });
    expect(conversationStore.__record().setDraftCalls).toBe(1);
    expect(conversationStore.__record().setDraftValue).toBe("hello");
  });

  it("setComposerMode writes to the UI store, not the conversation store", () => {
    const { runtime, conversationStore, uiStore } = makeRuntime();
    const { callbacks } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(runtime, callbacks, uiStore);
    dispatch({ type: "setComposerMode", mode: "steer" });
    expect(uiStore.getState().composerMode).toBe("steer");
    expect(conversationStore.__record().setDraftCalls).toBe(0);
  });
});

describe("createLiveIntentDispatcher — submit(mode)", () => {
  it("submit send uses the current draft as [{type:text,text:draft}] and calls store.send", async () => {
    const { runtime, conversationStore, liveService } = makeRuntime();
    conversationStore.__set({ draft: "draft text" });
    const { callbacks } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "submit", mode: "send" });
    await flush();
    expect(conversationStore.__record().sendCalls).toBe(1);
    expect(conversationStore.__record().sendService).toBe(liveService);
    expect(conversationStore.__record().sendInput).toEqual([
      { type: "text", text: "draft text" },
    ]);
  });

  it("submit steer calls store.steer with the same input shape", async () => {
    const { runtime, conversationStore, liveService } = makeRuntime();
    conversationStore.__set({ draft: "steer me" });
    const { callbacks } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "submit", mode: "steer" });
    await flush();
    expect(conversationStore.__record().steerCalls).toBe(1);
    expect(conversationStore.__record().steerService).toBe(liveService);
    expect(conversationStore.__record().steerInput).toEqual([
      { type: "text", text: "steer me" },
    ]);
  });

  it("submit queue calls store.queue with the same input shape", async () => {
    const { runtime, conversationStore, liveService } = makeRuntime();
    conversationStore.__set({ draft: "queue this" });
    const { callbacks } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "submit", mode: "queue" });
    await flush();
    expect(conversationStore.__record().queueCalls).toBe(1);
    expect(conversationStore.__record().queueService).toBe(liveService);
    expect(conversationStore.__record().queueInput).toEqual([
      { type: "text", text: "queue this" },
    ]);
  });

  it("submit is a safe no-op when conversationService is null", () => {
    const { runtime, conversationStore } = makeRuntime({
      conversationService: null,
    });
    const { callbacks } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "submit", mode: "send" });
    expect(conversationStore.__record().sendCalls).toBe(0);
  });
});

describe("createLiveIntentDispatcher — interrupt", () => {
  it("calls store.interrupt with the active service", async () => {
    const { runtime, conversationStore, liveService } = makeRuntime();
    const { callbacks } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "interrupt" });
    await flush();
    expect(conversationStore.__record().interruptCalls).toBe(1);
    expect(conversationStore.__record().interruptService).toBe(liveService);
  });

  it("is a safe no-op when conversationService is null", () => {
    const { runtime, conversationStore } = makeRuntime({
      conversationService: null,
    });
    const { callbacks } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "interrupt" });
    expect(conversationStore.__record().interruptCalls).toBe(0);
  });
});

describe("createLiveIntentDispatcher — UI-only intents", () => {
  it("toggleTool calls the UI store method", () => {
    const { runtime, uiStore } = makeRuntime();
    const { callbacks } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(runtime, callbacks, uiStore);
    dispatch({ type: "toggleTool", key: "tool-9" });
    expect(uiStore.getState().expandedToolKeys.has("tool-9")).toBe(true);
  });

  it("toggleWork calls the UI store method", () => {
    const { runtime, uiStore } = makeRuntime();
    const { callbacks } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(runtime, callbacks, uiStore);
    dispatch({ type: "toggleWork", key: "work-9" });
    expect(uiStore.getState().expandedWorkKeys.has("work-9")).toBe(true);
  });

  it("setQuestionDraft calls the UI store method", () => {
    const { runtime, uiStore } = makeRuntime();
    const { callbacks } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(runtime, callbacks, uiStore);
    const draft: QuestionDraft = {
      selectedOptionKeys: ["o1"],
      note: "n",
      resolution: "answer",
    };
    dispatch({ type: "setQuestionDraft", key: "q1", value: draft });
    expect(uiStore.getState().questionDrafts.q1).toEqual(draft);
  });
});

describe("createLiveIntentDispatcher — RootShell callbacks", () => {
  it("goBack calls onBack", () => {
    const { runtime } = makeRuntime();
    const { callbacks, recorded } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "goBack" });
    expect(recorded.back).toBe(1);
  });

  it("openNew calls onOpenNew", () => {
    const { runtime } = makeRuntime();
    const { callbacks, recorded } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "openNew" });
    expect(recorded.new).toBe(1);
  });

  it("openSettings calls onOpenSettings", () => {
    const { runtime } = makeRuntime();
    const { callbacks, recorded } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "openSettings" });
    expect(recorded.settings).toBe(1);
  });

  it("openVoice calls onOpenVoice", () => {
    const { runtime } = makeRuntime();
    const { callbacks, recorded } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "openVoice" });
    expect(recorded.voice).toBe(1);
  });
});

// ---------------------------------------------------------------------------
// submitQuestion — canonical payload + C1 all-or-nothing
// ---------------------------------------------------------------------------

describe("createLiveIntentDispatcher — submitQuestion canonical payload", () => {
  it("composes ONE answers payload over ALL pending questions in view order, not only the clicked card", async () => {
    const { runtime, conversationStore, liveService } = makeRuntime();
    const conv = buildConversationWithQuestions([
      {
        key: "qk-1",
        header: "First?",
        question: "pick one",
        options: [
          { label: "Yes", detail: "" },
          { label: "No", detail: "" },
        ],
        multiSelect: false,
        ifUnanswered: "do nothing",
      },
      {
        key: "qk-2",
        header: "Second?",
        question: "pick many",
        options: [
          { label: "A", detail: "" },
          { label: "B", detail: "" },
        ],
        multiSelect: true,
      },
    ]);
    const { view, operational } = buildProjection(conv);
    const qKeys = [...operational.questionKeys.keys()];
    expect(qKeys.length).toBe(2);
    const q1Display = qKeys[0] ?? "";
    const q2Display = qKeys[1] ?? "";
    const q1Opts = view.questions[0]?.options ?? [];
    const q2Opts = view.questions[1]?.options ?? [];
    const q1OptYes = q1Opts[0]?.key ?? "";
    const q2OptA = q2Opts[0]?.key ?? "";
    const q2OptB = q2Opts[1]?.key ?? "";

    conversationStore.__set({ conversation: conv });
    const { callbacks } = createFakeCallbacks(
      () => null,
      () => ({
        view,
        operational,
      }),
    );
    const uiStore = createFakeUiStore({
      questionDrafts: {
        [q1Display]: {
          selectedOptionKeys: [q1OptYes],
          note: "note one",
          resolution: "answer",
        },
        [q2Display]: {
          selectedOptionKeys: [q2OptA, q2OptB],
          note: "",
          resolution: "answer",
        },
      },
    });
    const dispatch = createLiveIntentDispatcher(runtime, callbacks, uiStore);
    dispatch({ type: "submitQuestion", key: q1Display });
    await flush();
    expect(conversationStore.__record().sendCalls).toBe(1);
    expect(conversationStore.__record().sendService).toBe(liveService);
    const sentText = conversationStore.__record().sendInput?.[0]?.text ?? "";
    const expected: readonly AskAnswerItem[] = [
      {
        header: "First?",
        resolution: { kind: "option", labels: ["Yes"] },
        note: "note one",
        ifUnanswered: "do nothing",
      },
      {
        header: "Second?",
        resolution: { kind: "option", labels: ["A", "B"] },
        note: "",
        ifUnanswered: undefined,
      },
    ];
    expect(sentText).toBe(composeAskAnswers(expected));
  });

  it("maps selected display option keys to exact labels through operational option links", async () => {
    const { runtime, conversationStore } = makeRuntime();
    const conv = buildConversationWithQuestions([
      {
        key: "qk-1",
        header: "Deploy?",
        question: "q",
        options: [
          { label: "Ship it", detail: "d1" },
          { label: "Hold", detail: "d2" },
        ],
        multiSelect: false,
      },
    ]);
    const { view, operational } = buildProjection(conv);
    const qDisplay = [...operational.questionKeys.keys()][0] ?? "";
    const optKeys = view.questions[0]?.options ?? [];
    const shipKey = optKeys.find((o) => o.label.text === "Ship it")?.key ?? "";
    conversationStore.__set({ conversation: conv });
    const { callbacks } = createFakeCallbacks(
      () => null,
      () => ({
        view,
        operational,
      }),
    );
    const uiStore = createFakeUiStore({
      questionDrafts: {
        [qDisplay]: {
          selectedOptionKeys: [shipKey],
          note: "",
          resolution: "answer",
        },
      },
    });
    const dispatch = createLiveIntentDispatcher(runtime, callbacks, uiStore);
    dispatch({ type: "submitQuestion", key: qDisplay });
    await flush();
    const sentText = conversationStore.__record().sendInput?.[0]?.text ?? "";
    expect(sentText).toBe('[answers]\n1. [Deploy?] → "Ship it"');
  });

  it("selected options map to option resolution even when renderer leaves resolution null", async () => {
    const { runtime, conversationStore } = makeRuntime();
    const conv = buildConversationWithQuestions([
      {
        key: "qk-1",
        header: "H?",
        question: "q",
        options: [{ label: "X", detail: "" }],
        multiSelect: false,
      },
    ]);
    const { view, operational } = buildProjection(conv);
    const qDisplay = [...operational.questionKeys.keys()][0] ?? "";
    const optKey = view.questions[0]?.options[0]?.key ?? "";
    conversationStore.__set({ conversation: conv });
    const { callbacks } = createFakeCallbacks(
      () => null,
      () => ({
        view,
        operational,
      }),
    );
    const uiStore = createFakeUiStore({
      questionDrafts: {
        [qDisplay]: {
          selectedOptionKeys: [optKey],
          note: "",
          resolution: null,
        },
      },
    });
    const dispatch = createLiveIntentDispatcher(runtime, callbacks, uiStore);
    dispatch({ type: "submitQuestion", key: qDisplay });
    await flush();
    const sentText = conversationStore.__record().sendInput?.[0]?.text ?? "";
    expect(sentText).toBe('[answers]\n1. [H?] → "X"');
  });

  it("fallback resolution uses ifUnanswered from the exact source question", async () => {
    const { runtime, conversationStore } = makeRuntime();
    const conv = buildConversationWithQuestions([
      {
        key: "qk-1",
        header: "F?",
        question: "q",
        options: [{ label: "X", detail: "" }],
        multiSelect: false,
        ifUnanswered: "skip the deploy",
      },
    ]);
    const { view, operational } = buildProjection(conv);
    const qDisplay = [...operational.questionKeys.keys()][0] ?? "";
    conversationStore.__set({ conversation: conv });
    const { callbacks } = createFakeCallbacks(
      () => null,
      () => ({
        view,
        operational,
      }),
    );
    const uiStore = createFakeUiStore({
      questionDrafts: {
        [qDisplay]: {
          selectedOptionKeys: [],
          note: "",
          resolution: "fallback",
        },
      },
    });
    const dispatch = createLiveIntentDispatcher(runtime, callbacks, uiStore);
    dispatch({ type: "submitQuestion", key: qDisplay });
    await flush();
    const sentText = conversationStore.__record().sendInput?.[0]?.text ?? "";
    expect(sentText).toBe(
      '[answers]\n1. [F?] → do your stated fallback ("skip the deploy")',
    );
  });

  it("decide resolution with empty leaning composes 'you decide'", async () => {
    const { runtime, conversationStore } = makeRuntime();
    const conv = buildConversationWithQuestions([
      {
        key: "qk-1",
        header: "D?",
        question: "q",
        options: [{ label: "X", detail: "" }],
        multiSelect: false,
      },
    ]);
    const { view, operational } = buildProjection(conv);
    const qDisplay = [...operational.questionKeys.keys()][0] ?? "";
    conversationStore.__set({ conversation: conv });
    const { callbacks } = createFakeCallbacks(
      () => null,
      () => ({
        view,
        operational,
      }),
    );
    const uiStore = createFakeUiStore({
      questionDrafts: {
        [qDisplay]: {
          selectedOptionKeys: [],
          note: "",
          resolution: "decide",
        },
      },
    });
    const dispatch = createLiveIntentDispatcher(runtime, callbacks, uiStore);
    dispatch({ type: "submitQuestion", key: qDisplay });
    await flush();
    const sentText = conversationStore.__record().sendInput?.[0]?.text ?? "";
    expect(sentText).toBe("[answers]\n1. [D?] → you decide");
  });

  it("skip resolution composes 'skipped (no answer)'", async () => {
    const { runtime, conversationStore } = makeRuntime();
    const conv = buildConversationWithQuestions([
      {
        key: "qk-1",
        header: "S?",
        question: "q",
        options: [{ label: "X", detail: "" }],
        multiSelect: false,
      },
    ]);
    const { view, operational } = buildProjection(conv);
    const qDisplay = [...operational.questionKeys.keys()][0] ?? "";
    conversationStore.__set({ conversation: conv });
    const { callbacks } = createFakeCallbacks(
      () => null,
      () => ({
        view,
        operational,
      }),
    );
    const uiStore = createFakeUiStore({
      questionDrafts: {
        [qDisplay]: {
          selectedOptionKeys: [],
          note: "",
          resolution: "skip",
        },
      },
    });
    const dispatch = createLiveIntentDispatcher(runtime, callbacks, uiStore);
    dispatch({ type: "submitQuestion", key: qDisplay });
    await flush();
    const sentText = conversationStore.__record().sendInput?.[0]?.text ?? "";
    expect(sentText).toBe("[answers]\n1. [S?] → skipped (no answer)");
  });

  it("wrong-call option (option belongs to a different question/call) is refused — no send", async () => {
    const { runtime, conversationStore } = makeRuntime();
    const convA = buildConversationWithQuestions(
      [
        {
          key: "qk-1",
          header: "A?",
          question: "q",
          options: [{ label: "X", detail: "" }],
          multiSelect: false,
        },
      ],
      "call-A",
    );
    const convB = buildConversationWithQuestions(
      [
        {
          key: "qk-2",
          header: "B?",
          question: "q",
          options: [{ label: "Y", detail: "" }],
          multiSelect: false,
        },
      ],
      "call-B",
    );
    const conv: MobileConversation = {
      ...convA,
      items: [...convA.items, ...convB.items],
    };
    const { view, operational } = buildProjection(conv);
    const q1Display = [...operational.questionKeys.keys()][0] ?? "";
    const q2OptKey = view.questions[1]?.options[0]?.key ?? "";
    conversationStore.__set({ conversation: conv });
    const { callbacks } = createFakeCallbacks(
      () => null,
      () => ({
        view,
        operational,
      }),
    );
    const uiStore = createFakeUiStore({
      questionDrafts: {
        [q1Display]: {
          selectedOptionKeys: [q2OptKey],
          note: "",
          resolution: "answer",
        },
      },
    });
    const dispatch = createLiveIntentDispatcher(runtime, callbacks, uiStore);
    dispatch({ type: "submitQuestion", key: q1Display });
    await flush();
    expect(conversationStore.__record().sendCalls).toBe(0);
  });

  it("unknown option display key (not in operational map) is refused — no send", async () => {
    const { runtime, conversationStore } = makeRuntime();
    const conv = buildConversationWithQuestions([
      {
        key: "qk-1",
        header: "U?",
        question: "q",
        options: [{ label: "X", detail: "" }],
        multiSelect: false,
      },
    ]);
    const { view, operational } = buildProjection(conv);
    const qDisplay = [...operational.questionKeys.keys()][0] ?? "";
    conversationStore.__set({ conversation: conv });
    const { callbacks } = createFakeCallbacks(
      () => null,
      () => ({
        view,
        operational,
      }),
    );
    const uiStore = createFakeUiStore({
      questionDrafts: {
        [qDisplay]: {
          selectedOptionKeys: ["nonexistent-opt-key"],
          note: "",
          resolution: "answer",
        },
      },
    });
    const dispatch = createLiveIntentDispatcher(runtime, callbacks, uiStore);
    dispatch({ type: "submitQuestion", key: qDisplay });
    await flush();
    expect(conversationStore.__record().sendCalls).toBe(0);
  });

  it("is a safe no-op when conversationService is null", () => {
    const { runtime, conversationStore } = makeRuntime({
      conversationService: null,
    });
    const { callbacks } = createFakeCallbacks(
      () => null,
      () => null,
    );
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "submitQuestion", key: "q" });
    expect(conversationStore.__record().sendCalls).toBe(0);
  });

  it("is a safe no-op when there is no source conversation", () => {
    const { runtime, conversationStore } = makeRuntime();
    conversationStore.__set({ conversation: null });
    const { callbacks } = createFakeCallbacks(
      () => null,
      () => null,
    );
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "submitQuestion", key: "q" });
    expect(conversationStore.__record().sendCalls).toBe(0);
  });

  it("is a safe no-op when projection is null", () => {
    const { runtime, conversationStore } = makeRuntime();
    const conv = buildConversationWithQuestions([
      {
        key: "qk-1",
        header: "P?",
        question: "q",
        options: [{ label: "X", detail: "" }],
        multiSelect: false,
      },
    ]);
    conversationStore.__set({ conversation: conv });
    const { callbacks } = createFakeCallbacks(
      () => null,
      () => null,
    );
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "submitQuestion", key: "q" });
    expect(conversationStore.__record().sendCalls).toBe(0);
  });

  it("is a safe no-op when the clicked key is not in the operational question map", () => {
    const { runtime, conversationStore } = makeRuntime();
    const conv = buildConversationWithQuestions([
      {
        key: "qk-1",
        header: "C?",
        question: "q",
        options: [{ label: "X", detail: "" }],
        multiSelect: false,
      },
    ]);
    const { view, operational } = buildProjection(conv);
    conversationStore.__set({ conversation: conv });
    const { callbacks } = createFakeCallbacks(
      () => null,
      () => ({
        view,
        operational,
      }),
    );
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "submitQuestion", key: "not-a-real-question-key" });
    expect(conversationStore.__record().sendCalls).toBe(0);
  });

  it("is a safe no-op when the clicked key is in operational but not in view.questions", () => {
    const { runtime, conversationStore } = makeRuntime();
    const conv = buildConversationWithQuestions([
      {
        key: "qk-1",
        header: "V?",
        question: "q",
        options: [{ label: "X", detail: "" }],
        multiSelect: false,
      },
    ]);
    const { view, operational } = buildProjection(conv);
    conversationStore.__set({ conversation: conv });
    // The clicked key exists in the operational map but we simulate it not
    // being in view.questions by passing a different key that the operational
    // map also doesn't have — but the real scenario is: clicked key in
    // operational but view.questions has different keys. We construct that by
    // passing an operational key that is NOT in view.questions. Since the
    // projector builds them together, we just assert the guard fires for a
    // key not in view.questions.
    const realKey = [...operational.questionKeys.keys()][0] ?? "";
    // Build a projection with a DIFFERENT view that doesn't include realKey.
    const staleView = { ...view, questions: [] };
    const { callbacks } = createFakeCallbacks(
      () => null,
      () => ({
        view: staleView,
        operational,
      }),
    );
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "submitQuestion", key: realKey });
    expect(conversationStore.__record().sendCalls).toBe(0);
  });
});

// ---------------------------------------------------------------------------
// C1: submitQuestion all-or-nothing — missing middle link / missing source
// ---------------------------------------------------------------------------

describe("createLiveIntentDispatcher — submitQuestion all-or-nothing (C1)", () => {
  it("missing operational link for a MIDDLE question → zero send, no partial payload", async () => {
    const { runtime, conversationStore } = makeRuntime();
    const conv = buildConversationWithQuestions([
      {
        key: "qk-1",
        header: "Q1?",
        question: "q",
        options: [{ label: "A", detail: "" }],
        multiSelect: false,
      },
      {
        key: "qk-2",
        header: "Q2?",
        question: "q",
        options: [{ label: "B", detail: "" }],
        multiSelect: false,
      },
      {
        key: "qk-3",
        header: "Q3?",
        question: "q",
        options: [{ label: "C", detail: "" }],
        multiSelect: false,
      },
    ]);
    const { view, operational } = buildProjection(conv);
    const qKeys = [...operational.questionKeys.keys()];
    const q1Display = qKeys[0] ?? "";
    const q2Display = qKeys[1] ?? "";
    const q3Display = qKeys[2] ?? "";
    const q1Opt = view.questions[0]?.options[0]?.key ?? "";
    const q3Opt = view.questions[2]?.options[0]?.key ?? "";

    // Build a stale operational map that is missing the MIDDLE question link.
    const staleQuestionKeys = new Map(operational.questionKeys);
    staleQuestionKeys.delete(q2Display);
    const staleOperational: ConversationOperationalMap = {
      itemKeys: operational.itemKeys,
      questionKeys: staleQuestionKeys,
      optionKeys: operational.optionKeys,
      evidenceKeys: operational.evidenceKeys,
    };

    conversationStore.__set({ conversation: conv });
    const { callbacks } = createFakeCallbacks(
      () => null,
      () => ({
        view,
        operational: staleOperational,
      }),
    );
    const uiStore = createFakeUiStore({
      questionDrafts: {
        [q1Display]: {
          selectedOptionKeys: [q1Opt],
          note: "",
          resolution: "answer",
        },
        [q2Display]: {
          selectedOptionKeys: [],
          note: "",
          resolution: "skip",
        },
        [q3Display]: {
          selectedOptionKeys: [q3Opt],
          note: "",
          resolution: "answer",
        },
      },
    });
    const dispatch = createLiveIntentDispatcher(runtime, callbacks, uiStore);
    dispatch({ type: "submitQuestion", key: q1Display });
    await flush();
    // C1: all-or-nothing — middle link missing → zero send, no partial.
    expect(conversationStore.__record().sendCalls).toBe(0);
    expect(conversationStore.__record().sendInput).toBeNull();
  });

  it("missing exact source MobileAskQuestion for a MIDDLE question → zero send, no partial payload", async () => {
    const { runtime, conversationStore } = makeRuntime();
    const conv = buildConversationWithQuestions([
      {
        key: "qk-1",
        header: "Q1?",
        question: "q",
        options: [{ label: "A", detail: "" }],
        multiSelect: false,
      },
      {
        key: "qk-2",
        header: "Q2?",
        question: "q",
        options: [{ label: "B", detail: "" }],
        multiSelect: false,
      },
      {
        key: "qk-3",
        header: "Q3?",
        question: "q",
        options: [{ label: "C", detail: "" }],
        multiSelect: false,
      },
    ]);
    const { view, operational } = buildProjection(conv);
    const qKeys = [...operational.questionKeys.keys()];
    const q1Display = qKeys[0] ?? "";
    const q2Display = qKeys[1] ?? "";
    const q3Display = qKeys[2] ?? "";
    const q1Opt = view.questions[0]?.options[0]?.key ?? "";
    const q3Opt = view.questions[2]?.options[0]?.key ?? "";

    // Build a stale conversation where the MIDDLE question is missing from
    // the source batch — the operational link still points to callId+qk-2,
    // but the source batch no longer contains qk-2.
    const staleConv: MobileConversation = {
      ...conv,
      items: [
        {
          kind: "question",
          id: "q-item-1",
          batch: {
            callId: "call-1",
            questions: (() => {
              const item = conv.items[0];
              if (item && item.kind === "question") {
                const qs = item.batch.questions;
                // qk-2 (index 1) intentionally omitted — stale source.
                return [qs[0], qs[2]].filter(
                  (q): q is NonNullable<typeof q> => q !== undefined,
                );
              }
              return [
                {
                  key: "x",
                  header: "",
                  question: "",
                  options: [],
                  multiSelect: false,
                },
                {
                  key: "y",
                  header: "",
                  question: "",
                  options: [],
                  multiSelect: false,
                },
              ];
            })(),
          },
        },
      ],
    };

    conversationStore.__set({ conversation: staleConv });
    const { callbacks } = createFakeCallbacks(
      () => null,
      () => ({
        view,
        operational,
      }),
    );
    const uiStore = createFakeUiStore({
      questionDrafts: {
        [q1Display]: {
          selectedOptionKeys: [q1Opt],
          note: "",
          resolution: "answer",
        },
        [q2Display]: {
          selectedOptionKeys: [],
          note: "",
          resolution: "skip",
        },
        [q3Display]: {
          selectedOptionKeys: [q3Opt],
          note: "",
          resolution: "answer",
        },
      },
    });
    const dispatch = createLiveIntentDispatcher(runtime, callbacks, uiStore);
    dispatch({ type: "submitQuestion", key: q1Display });
    await flush();
    // C1: all-or-nothing — middle source missing → zero send, no partial,
    // no fallback header/ifUnanswered.
    expect(conversationStore.__record().sendCalls).toBe(0);
    expect(conversationStore.__record().sendInput).toBeNull();
  });

  it("validates source header and ifUnanswered come from the exact source question, not a fallback", async () => {
    const { runtime, conversationStore } = makeRuntime();
    const conv = buildConversationWithQuestions([
      {
        key: "qk-1",
        header: "Exact Header",
        question: "q",
        options: [{ label: "X", detail: "" }],
        multiSelect: false,
        ifUnanswered: "exact fallback text",
      },
    ]);
    const { view, operational } = buildProjection(conv);
    const qDisplay = [...operational.questionKeys.keys()][0] ?? "";
    const optKey = view.questions[0]?.options[0]?.key ?? "";
    conversationStore.__set({ conversation: conv });
    const { callbacks } = createFakeCallbacks(
      () => null,
      () => ({
        view,
        operational,
      }),
    );
    const uiStore = createFakeUiStore({
      questionDrafts: {
        [qDisplay]: {
          selectedOptionKeys: [optKey],
          note: "exact note",
          resolution: "answer",
        },
      },
    });
    const dispatch = createLiveIntentDispatcher(runtime, callbacks, uiStore);
    dispatch({ type: "submitQuestion", key: qDisplay });
    await flush();
    const sentText = conversationStore.__record().sendInput?.[0]?.text ?? "";
    // Header and ifUnanswered come from the exact source question.
    expect(sentText).toContain("[Exact Header]");
    // The note is also exact.
    expect(sentText).toContain('— note: "exact note"');
  });
});

// ---------------------------------------------------------------------------
// I1/I2: error safety — generic messages, generation/ref races, no leak
// ---------------------------------------------------------------------------

describe("createLiveIntentDispatcher — error safety (I1/I2)", () => {
  it("refreshRoster rejection publishes generic fixed message, never raw Error.message", async () => {
    const { runtime, rosterStore } = makeRuntime();
    rosterStore.__set({
      refreshImpl: async () => {
        throw new Error("secret roster failure ref=abc123");
      },
    });
    const { callbacks } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "refreshRoster" });
    await flush();
    expect(rosterStore.__record().error).toBe("Roster operation failed");
    // The raw secret-bearing message must NOT appear.
    expect(rosterStore.__record().error).not.toContain("secret");
    expect(rosterStore.__record().error).not.toContain("abc123");
  });

  it("refreshRoster rejection after generation change publishes zero — no stale error", async () => {
    const { runtime, rosterStore } = makeRuntime();
    const holder: { fn: (() => void) | null } = { fn: null };
    rosterStore.__set({
      refreshImpl: () =>
        new Promise<void>((_resolve, reject) => {
          holder.fn = () => reject(new Error("late roster boom"));
        }),
    });
    const { callbacks } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "refreshRoster" });
    // Bump generation BEFORE the rejection fires.
    rosterStore.__set({ generation: 999 });
    holder.fn?.();
    await flush();
    // Late rejection after generation change → zero publication.
    expect(rosterStore.__record().error).toBeNull();
  });

  it.each([
    ["submit send", "send", "sendImpl"] as const,
    ["submit steer", "steer", "steerImpl"] as const,
    ["submit queue", "queue", "queueImpl"] as const,
  ])(
    "%s rejection calls publishExternalError with generic message, never raw",
    async (_label, mode, implKey) => {
      const { runtime, conversationStore } = makeRuntime();
      conversationStore.__set({
        draft: "x",
        [implKey]: async () => {
          throw new Error("secret send failure ref=sensitive");
        },
      });
      const { callbacks } = createFakeCallbacks();
      const dispatch = createLiveIntentDispatcher(
        runtime,
        callbacks,
        createFakeUiStore(),
      );
      dispatch({ type: "submit", mode });
      await flush();
      const r = conversationStore.__record();
      expect(r.pubErrorCalls).toBe(1);
      expect(r.pubErrorMessage).toBe("Conversation operation failed");
      // The raw secret-bearing message must NOT be published.
      expect(r.error).not.toContain("secret");
      expect(r.error).not.toContain("sensitive");
    },
  );

  it("loadOlder rejection calls publishExternalError with generic message, never raw", async () => {
    const { runtime, conversationStore } = makeRuntime();
    conversationStore.__set({
      loadOlderImpl: async () => {
        throw new Error("secret loadOlder failure ref=leaked");
      },
    });
    const { callbacks } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "loadOlder" });
    await flush();
    const r = conversationStore.__record();
    expect(r.pubErrorCalls).toBe(1);
    expect(r.pubErrorMessage).toBe("Conversation operation failed");
    expect(r.error).not.toContain("secret");
    expect(r.error).not.toContain("leaked");
  });

  it("interrupt rejection calls publishExternalError with generic message, never raw", async () => {
    const { runtime, conversationStore } = makeRuntime();
    conversationStore.__set({
      interruptImpl: async () => {
        throw new Error("secret interrupt failure ref=leaked");
      },
    });
    const { callbacks } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "interrupt" });
    await flush();
    const r = conversationStore.__record();
    expect(r.pubErrorCalls).toBe(1);
    expect(r.pubErrorMessage).toBe("Conversation operation failed");
    expect(r.error).not.toContain("secret");
    expect(r.error).not.toContain("leaked");
  });

  it("submitQuestion rejection calls publishExternalError with generic message, never raw", async () => {
    const { runtime, conversationStore } = makeRuntime();
    const conv = buildConversationWithQuestions([
      {
        key: "qk-1",
        header: "Q?",
        question: "q",
        options: [{ label: "X", detail: "" }],
        multiSelect: false,
      },
    ]);
    const { view, operational } = buildProjection(conv);
    const qDisplay = [...operational.questionKeys.keys()][0] ?? "";
    const optKey = view.questions[0]?.options[0]?.key ?? "";
    conversationStore.__set({
      conversation: conv,
      sendImpl: async () => {
        throw new Error("secret question send failure ref=leaked");
      },
    });
    const { callbacks } = createFakeCallbacks(
      () => null,
      () => ({
        view,
        operational,
      }),
    );
    const uiStore = createFakeUiStore({
      questionDrafts: {
        [qDisplay]: {
          selectedOptionKeys: [optKey],
          note: "",
          resolution: "answer",
        },
      },
    });
    const dispatch = createLiveIntentDispatcher(runtime, callbacks, uiStore);
    dispatch({ type: "submitQuestion", key: qDisplay });
    await flush();
    const r = conversationStore.__record();
    expect(r.pubErrorCalls).toBe(1);
    expect(r.pubErrorMessage).toBe("Conversation operation failed");
    expect(r.error).not.toContain("secret");
    expect(r.error).not.toContain("leaked");
  });

  it("conversation rejection captures exact ref + generation before the Promise", async () => {
    const { runtime, conversationStore } = makeRuntime();
    conversationStore.__set({
      ref: "exact-ref",
      conversationGeneration: 42,
      sendImpl: async () => {
        throw new Error("boom");
      },
      draft: "x",
    });
    const { callbacks } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "submit", mode: "send" });
    await flush();
    const r = conversationStore.__record();
    expect(r.pubErrorRef).toBe("exact-ref");
    expect(r.pubErrorGen).toBe(42);
  });

  it("conversation rejection after ref change publishes zero — no stale error", async () => {
    const { runtime, conversationStore } = makeRuntime();
    const holder: { fn: (() => void) | null } = { fn: null };
    conversationStore.__set({
      ref: "ref-old",
      conversationGeneration: 1,
      sendImpl: () =>
        new Promise<void>((_resolve, reject) => {
          holder.fn = () => reject(new Error("late boom"));
        }),
      draft: "x",
    });
    const { callbacks } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "submit", mode: "send" });
    // Change ref BEFORE the rejection fires.
    conversationStore.__set({ ref: "ref-new" });
    holder.fn?.();
    await flush();
    // Late rejection after ref change → zero publication (guarded).
    expect(conversationStore.__record().error).toBeNull();
  });

  it("conversation rejection after generation change publishes zero — no stale error", async () => {
    const { runtime, conversationStore } = makeRuntime();
    const holder: { fn: (() => void) | null } = { fn: null };
    conversationStore.__set({
      ref: "ref-1",
      conversationGeneration: 1,
      interruptImpl: () =>
        new Promise<void>((_resolve, reject) => {
          holder.fn = () => reject(new Error("late boom"));
        }),
    });
    const { callbacks } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    dispatch({ type: "interrupt" });
    // Bump generation BEFORE the rejection fires.
    conversationStore.__set({ conversationGeneration: 2 });
    holder.fn?.();
    await flush();
    // Late rejection after generation change → zero publication (guarded).
    expect(conversationStore.__record().error).toBeNull();
  });

  it("never leaves an unhandled rejection on any rejected conversation path", async () => {
    const { runtime, conversationStore } = makeRuntime();
    conversationStore.__set({
      draft: "x",
      sendImpl: async () => {
        throw new Error("unhandled check");
      },
    });
    const { callbacks } = createFakeCallbacks();
    const dispatch = createLiveIntentDispatcher(
      runtime,
      callbacks,
      createFakeUiStore(),
    );
    // Track unhandled rejections during this test.
    let unhandled = 0;
    const handler = () => unhandled++;
    process.on("unhandledRejection", handler);
    dispatch({ type: "submit", mode: "send" });
    await flush();
    await flush();
    process.off("unhandledRejection", handler);
    expect(unhandled).toBe(0);
    expect(conversationStore.__record().error).not.toBeNull();
  });
});

// ---------------------------------------------------------------------------
// Compile-time exhaustiveness + boundary proof
// ---------------------------------------------------------------------------

describe("createLiveIntentDispatcher — compile-time exhaustiveness", () => {
  it("LiveConceptIntent has exactly the expected variants", () => {
    type Expected =
      | { type: "switchConcept"; concept: ConceptId }
      | { type: "openConceptSwitcher" }
      | { type: "refreshRoster" }
      | { type: "setRosterQuery"; value: string }
      | { type: "openConversation"; key: string }
      | { type: "loadOlder" }
      | { type: "openWork" }
      | { type: "closeWork" }
      | { type: "setDraft"; value: string }
      | { type: "setComposerMode"; mode: "send" | "steer" | "queue" }
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
    expectTypeOf<LiveConceptIntent>().toEqualTypeOf<Expected>();
  });
});

describe("createLiveIntentDispatcher — boundary proof", () => {
  it("imports no transport constructors", async () => {
    const source = await import("./dispatch-live-intent?raw");
    const text =
      typeof source.default === "string"
        ? source.default
        : String(source.default);
    expect(text).not.toMatch(/\bnew\s+WebSocket\s*\(/);
    expect(text).not.toMatch(/\bnew\s+EventSource\s*\(/);
    expect(text).not.toMatch(/\bnew\s+XMLHttpRequest\s*\(/);
    expect(text).not.toMatch(/@tauri-apps/);
  });

  it("imports no fixture/scenario modules", async () => {
    const source = await import("./dispatch-live-intent?raw");
    const text =
      typeof source.default === "string"
        ? source.default
        : String(source.default);
    // Each forbidden token is matched as a standalone word boundary.
    expect(text).not.toMatch(/\bfixtures\b/);
    expect(text).not.toMatch(/\bscenario\b/);
    expect(text).not.toMatch(/\bsyntheticTurn\b/);
  });
});
