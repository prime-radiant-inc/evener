// threads.retirement.test.tsx — proves the user-visible recovery contract when
// the daemon backing a selected thread retires and is replaced.
//
// Uses a real AppwireClient connecting through a scripted in-process WebSocket
// transport (not FakeClient). The scripted transport exercises the full
// AppwireClient handshake, request-tracking, and notification-routing
// machinery: the seam under test is the hub relay/resync path, not the
// FakeClient adapter. A FakeSocket-style inline WebSocket is the transport
// substitute; a real TCP server would prove the same contract but adds a
// port-allocation dependency this test does not need.
//
// See plan/task-12 for the step-2 red-evidence protocol. RED_SUPPRESS_REPLACEMENT
// below is the fixture-owned scripted-source switch: with it on, the scripted
// server keeps answering from the retired generation and rejects the mutation
// the replacement would have accepted, so the "one new turn" assertion fails.
// The committed value is false; the RED run flips it, observes the named
// assertion fail, then restores it.
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test } from "vitest";
import { Composer as ComposerView } from "../panes/session/composer/Composer";
import { readDraft } from "../panes/session/composer/draft";
import {
  flushPendingTurnsProjectionForTests,
  resetPendingTurnsStoreForTests,
  subscribeComposerSubmissionCommitted,
} from "../panes/session/composer/queue/pendingTurnsStore";
import { APPWIRE_PROTOCOL_VERSION, AppwireClient } from "../protocol/client";
import { FAKE_INITIALIZE_RESULT } from "../protocol/testing/fakeSocket";
import type { WebSocketLike } from "../protocol/transport";
import type {
  AnyNotification,
  Thread,
  ThreadCapabilities,
  ThreadReadResponse,
  TurnStartParams,
} from "../protocol/types.gen";
import { ClientProvider } from "../shell/clientContext";
import { connectionStore } from "./connection";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import { resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "./threads";

// React's concurrent root only runs act() without its "not configured" /
// "not wrapped in act" diagnostics when this flag is set for the jsdom
// environment. testing.md's pristine-output rule therefore requires the flag;
// it is configuration, not output filtering. Everything that settles React
// state in this file is awaited inside act(), and no waitFor() (which flips the
// flag back off, RTL's asyncWrapper) is used.
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// MemoryStorage: Node 26 shadows jsdom's working window.localStorage with its
// own non-functional global (same workaround as Composer.integration.test.tsx).
class MemoryStorage {
  private store = new Map<string, string>();
  getItem(key: string): string | null {
    return this.store.has(key) ? (this.store.get(key) ?? null) : null;
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value));
  }
  removeItem(key: string): void {
    this.store.delete(key);
  }
  clear(): void {
    this.store.clear();
  }
}

// ---------------------------------------------------------------------------
// Scripted in-process WebSocket transport
// ---------------------------------------------------------------------------

// RED_SUPPRESS_REPLACEMENT is the step-2 broken boundary, held in the
// fixture-owned scripted source. With it false (committed value) the scripted
// server binds the replacement generation after the resync and accepts the
// user's mutation there. With it true it behaves as a client still bound to
// the retired generation: the post-resync read keeps returning INSTANCE_V1 and
// turn/start is rejected, so the streamed turn never arrives and
// `expect(after.length).toBe(before.length + 1)` fails by name instead of the
// fixture deadlocking. Flip it to true only to capture red-boundary evidence.
const RED_SUPPRESS_REPLACEMENT = false;

// RetirementScriptedSocket implements WebSocketLike and stands in as the
// "server" side. It auto-handles the AppwireClient handshake and routes
// protocol messages to registered handlers. retire() emits evener/thread/resync
// so the real client machinery re-subscribes and re-reads, exactly the
// notification the Hub broadcasts when the relay re-acquires a replacement
// source (cmd/evener-hub/app_relay.go's NotifyEvenerThreadResync).
class RetirementScriptedSocket implements WebSocketLike {
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  onclose: ((ev: { code: number }) => void) | null = null;
  onerror: (() => void) | null = null;

  private readonly methodHandlers = new Map<string, (id: number, params: unknown) => unknown>();
  private closed = false;

  // open() simulates the connection being established (the client calls open
  // after setting its onopen handler).
  open(): void {
    this.onopen?.();
  }

  send(data: string): void {
    if (this.closed) return;
    const msg = JSON.parse(data) as { id?: number; method?: string; params?: unknown };
    const { id, method, params } = msg;
    // Server-sent frames (initialized, not a request): no response needed.
    if (id == null) return;
    if (method === "ping") {
      this.receive({ id, result: {} });
      return;
    }
    if (method === "initialize") {
      this.receive({
        id,
        result: {
          ...FAKE_INITIALIZE_RESULT,
          protocolVersion: APPWIRE_PROTOCOL_VERSION,
        },
      });
      return;
    }
    const handler = method ? this.methodHandlers.get(method) : undefined;
    if (handler) {
      const result = handler(id, params);
      if (result !== null && typeof result === "object" && "__wireError" in result) {
        this.receive({ id, error: (result as { __wireError: unknown }).__wireError });
        return;
      }
      this.receive({ id, result });
      return;
    }
    // Scripted fall-through for known idle-time requests Composer/Session make.
    if (method === "model/list" || method === "evener/tasks/list") {
      this.receive({ id, result: { data: [] } });
      return;
    }
    // Any unscripted method — let the client timeout (the test will catch it
    // if unexpected methods are called for the tested flows).
  }

  close(_code?: number): void {
    if (this.closed) return;
    this.closed = true;
    this.onclose?.({ code: _code ?? 1000 });
  }

  // receive pushes a frame from the "server" side to the client.
  receive(obj: Record<string, unknown>): void {
    if (this.closed) return;
    this.onmessage?.({ data: JSON.stringify(obj) });
  }

  // emit pushes a server-initiated notification (no id).
  emit(notification: AnyNotification): void {
    this.receive(notification as unknown as Record<string, unknown>);
  }

  // on registers a handler for a specific method.
  on(method: string, handler: (id: number, params: unknown) => unknown): this {
    this.methodHandlers.set(method, handler);
    return this;
  }
}

// ---------------------------------------------------------------------------
// Thread fixture data
// ---------------------------------------------------------------------------

const REF = "local:retirement-test-ref";
const THREAD_ID = "thr_retirement_test";
const INSTANCE_V1 = "instance_retirement_v1";
const INSTANCE_V2 = "instance_retirement_v2";
const DRAFT_TEXT = "unsent retirement draft";
// The deterministic UUID boundary: the fixture owns the mutation identity
// instead of letting createSecureUUID mint a random one, so the assertion can
// tie the accepted mutation back to the submitted intent.
const MUTATION_ID = "retirement-view-mutation";

const FULL_CAPABILITIES: ThreadCapabilities = {
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
};

function makeThread(instanceId: string, turnIds: string[]): Thread {
  return {
    id: THREAD_ID,
    sessionId: "sess_retirement_test",
    preview: "retirement test thread",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "idle" },
    cwd: "/tmp/retirement-test",
    cliVersion: "1.0.0",
    source: "local",
    evener: {
      ref: REF,
      instanceId,
      capabilities: FULL_CAPABILITIES,
      queue: { revision: 0 },
      mutationStateAuthoritative: true,
    },
    turns: turnIds.map((id) => ({
      id,
      status: "completed" as const,
      itemsView: "full" as const,
      items: [
        {
          type: "userMessage" as const,
          id: `${id}_user`,
          turnId: id,
          text: `User turn ${id}`,
          status: "completed" as const,
        },
      ],
    })),
  };
}

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

interface RetirementClientFixture {
  draft(): string;
  turnIDs(): string[];
  instanceId(): string;
  retire(): Promise<void>;
  send(text: string, mutationId: string): Promise<void>;
  close(): Promise<void>;
}

// awaitHydrationBump is the awaitable completion retire() waits on: the store's
// own per-ref full-snapshot counter advancing past the watermark captured
// before the resync. It is a store subscription, not a poll or a deadline, so
// it settles exactly when the replacement read has published.
function awaitHydrationBump(ref: string, threshold: number): Promise<void> {
  return new Promise((resolve) => {
    const check = (state: { hydrations: Map<string, number> }): void => {
      if ((state.hydrations.get(ref) ?? 0) > threshold) {
        unsubscribe();
        resolve();
      }
    };
    const unsubscribe = threadsStore.subscribe(check);
    check(threadsStore.getState());
  });
}

async function openRetirementClientFixture(): Promise<RetirementClientFixture> {
  const socket = new RetirementScriptedSocket();
  const sentTurnIds = ["turn_fixture_1", "turn_fixture_2"];
  const newTurnId = "turn_fixture_new";
  let nextMutationId = MUTATION_ID;

  // The replacement contract: after evener/thread/resync the next read answers
  // from the replacement generation and turn/start is accepted there. When
  // RED_SUPPRESS_REPLACEMENT is on, neither happens.
  let replacementBound = false;

  socket.on("thread/read", (_id, _params) => {
    const instanceId = replacementBound && !RED_SUPPRESS_REPLACEMENT ? INSTANCE_V2 : INSTANCE_V1;
    const response: ThreadReadResponse = {
      thread: makeThread(instanceId, sentTurnIds),
    };
    return response;
  });

  // turnAttemptSettled resolves once the scripted source has answered the
  // user's mutation — accepted on the replacement generation, rejected while
  // still bound to the retired one. send() awaits it, so the broken boundary
  // surfaces as the caller's assertion and never as a fixture hang.
  let settleTurnAttempt: (() => void) | null = null;
  const turnAttemptSettled = new Promise<void>((resolve) => {
    settleTurnAttempt = resolve;
  });

  socket.on("turn/start", (_id, params) => {
    const p = params as TurnStartParams & { expectedInstanceId?: string };
    const newTurn = {
      id: newTurnId,
      status: "completed" as const,
      itemsView: "full" as const,
      items: [
        {
          type: "userMessage" as const,
          id: `${newTurnId}_user`,
          turnId: newTurnId,
          text: p.input?.[0]?.text ?? DRAFT_TEXT,
          status: "completed" as const,
        },
      ],
    };
    if (RED_SUPPRESS_REPLACEMENT) {
      settleTurnAttempt?.();
      return {
        __wireError: {
          code: -32000,
          message: "LifecycleUnavailable: retired generation",
          data: { evenerErrorInfo: "lifecycleUnavailable" },
        },
      };
    }
    const receipt = {
      clientMutationId: p.clientMutationId,
      disposition: "applied",
      threadId: THREAD_ID,
      instanceId: INSTANCE_V2,
      turnId: newTurnId,
      projectionState: "reflected",
    };
    // Emit turn/started + turn/completed after the response so the thread
    // model gains the new turn through the real notification path.
    queueMicrotask(() => {
      socket.emit({
        method: "turn/started",
        params: { threadId: THREAD_ID, ref: REF, turn: newTurn },
      } as AnyNotification);
      socket.emit({
        method: "turn/completed",
        params: { threadId: THREAD_ID, ref: REF, turn: newTurn },
      } as AnyNotification);
      settleTurnAttempt?.();
    });
    return { turn: newTurn, receipt };
  });

  // Create the real AppwireClient.
  const client = new AppwireClient({
    url: "ws://retirement-test.local/rpc",
    socketFactory: () => {
      // The socket opens asynchronously (per AppwireClient's
      // waitForOpen contract) after socketFactory returns.
      queueMicrotask(() => socket.open());
      return socket;
    },
  });

  // Install the deterministic mutation boundary before anything can start the
  // singleton mutation runtime (connectionStore.connect below reaches
  // handleReady -> getMutationRuntime).
  setMutationStorageForTests(
    new MutationOutboxIndexedDB({
      indexedDB: globalThis.indexedDB,
      createMutationId: () => nextMutationId,
      databaseName: `retirement-mutations-${MUTATION_ID}`,
    }),
  );

  // Wire into connectionStore so threads.ts rewireClient sees it.
  connectionStore.getState().connect(client);

  // Start the client handshake.
  const connectPromise = client.connect();
  await act(async () => {
    await connectPromise;
  });

  // ensureThread opens the ref through the real thread store, which sends
  // thread/read with subscribe:true and hydrates the model with the persisted
  // transcript from the retired generation.
  await act(async () => {
    await threadsStore.getState().ensureThread(REF);
  });

  // Mount the real Composer (with ClientProvider) so the draft lifecycle is
  // live, then enter the unsent draft through the native textarea.
  const { unmount } = render(
    <ClientProvider client={client}>
      <ComposerView ref={REF} />
    </ClientProvider>,
  );

  // Flush projection work so the pending-turns machinery is fully active.
  await flushPendingTurnsProjectionForTests();

  const draftTextarea = screen.getByRole("textbox", { name: /message/i }) as HTMLTextAreaElement;
  await act(async () => {
    fireEvent.change(draftTextarea, { target: { value: DRAFT_TEXT } });
  });
  await flushPendingTurnsProjectionForTests();

  return {
    draft(): string {
      return readDraft(REF);
    },

    turnIDs(): string[] {
      return (
        threadsStore
          .getState()
          .threads.get(REF)
          ?.turns.map((t) => t.id) ?? []
      );
    },

    instanceId(): string {
      return threadsStore.getState().threads.get(REF)?.instanceId ?? "";
    },

    async retire(): Promise<void> {
      const beforeHydrations = threadsStore.getState().hydrations.get(REF) ?? 0;
      // Bind the replacement generation BEFORE emitting the resync. The resync
      // is what triggers the re-read, so the read must observe the replacement
      // already bound; binding it afterwards would answer the resync-triggered
      // read from the retired generation and the model would never carry
      // INSTANCE_V2, contradicting this fixture's own contract.
      replacementBound = true;
      // The daemon-side source closes and the Hub re-acquires the replacement,
      // broadcasting evener/thread/resync to the already-connected client.
      // This is the same-socket recovery path: the browser never reconnects.
      await act(async () => {
        socket.emit({
          method: "evener/thread/resync",
          params: { ref: REF },
        } as AnyNotification);
        await awaitHydrationBump(REF, beforeHydrations);
      });
    },

    async send(text: string, mutationId: string): Promise<void> {
      nextMutationId = mutationId;
      // Subscribe to the committed event BEFORE clicking to avoid a race.
      const committed = new Promise<void>((resolve) => {
        const unsub = subscribeComposerSubmissionCommitted(() => {
          unsub();
          resolve();
        });
      });

      // The draft is already in the real textarea from the native input above;
      // only re-enter it if the fixture was driven with different text.
      const ta = screen.getByRole("textbox", { name: /message/i }) as HTMLTextAreaElement;
      if (ta.value !== text) {
        await act(async () => {
          fireEvent.change(ta, { target: { value: text } });
        });
      }

      const sendButton = screen.getByRole("button", { name: /^send$/i });
      await act(async () => {
        fireEvent.click(sendButton);
        // Await the local IndexedDB commit (subscribeComposerSubmissionCommitted).
        await committed;
      });

      // Flush projection work so the dispatcher picks up the mutation.
      await flushPendingTurnsProjectionForTests();

      // Await the authoritative transport outcome: the replacement generation's
      // streamed turn completion, or its rejection at the retired boundary.
      await act(async () => {
        await turnAttemptSettled;
      });

      // One final flush so all store subscribers run.
      await flushPendingTurnsProjectionForTests();
    },

    async close(): Promise<void> {
      unmount();
      cleanup();
      threadsStore.getState().releaseThread(REF);
      client.close();
    },
  };
}

// ---------------------------------------------------------------------------
// Test setup / teardown
// ---------------------------------------------------------------------------

beforeEach(() => {
  // @ts-expect-error — Node 26 localStorage workaround (same as integration test)
  globalThis.localStorage = new MemoryStorage();
  globalThis.indexedDB = new IDBFactory();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
});

afterEach(async () => {
  cleanup();
  await act(async () => {
    resetThreadsStoreForTests();
    resetPendingTurnsStoreForTests();
  });
  globalThis.indexedDB = new IDBFactory();
});

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test("selected transcript and unsent draft survive source retirement", async () => {
  const f = await openRetirementClientFixture();
  try {
    const before = f.turnIDs();
    const draft = f.draft();
    // The pre-retirement read is answered from the retired generation...
    expect(f.instanceId()).toBe(INSTANCE_V1);
    await f.retire();
    expect(f.turnIDs()).toEqual(before);
    expect(f.draft()).toBe(draft);
    await f.send(draft, MUTATION_ID);
    const after = f.turnIDs();
    expect(new Set(after).size).toBe(after.length);
    expect(after.length).toBe(before.length + 1);
    expect(f.draft()).toBe("");
    // ...and the resync-triggered read genuinely rebound to the replacement
    // generation, so the turn above was accepted on INSTANCE_V2.
    expect(f.instanceId()).toBe(INSTANCE_V2);
  } finally {
    await f.close();
  }
});
