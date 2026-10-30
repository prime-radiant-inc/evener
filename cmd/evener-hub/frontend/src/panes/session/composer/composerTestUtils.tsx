import type { Thread, ThreadCapabilities, ThreadReadResponse } from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import type { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { render, screen, within } from "@testing-library/react";
import { expect } from "vitest";
import { ClientProvider } from "../../../shell/clientContext";
import { connectionStore } from "../../../stores/connection";
import type { MutationOutboxIndexedDB } from "../../../stores/mutationOutboxIndexedDB";
import { threadsStore } from "../../../stores/threads";
import { connectReadyFakeClient } from "../../../testing/connectionTestUtils";
import { ALL_CAPABILITIES as FULL_CAPABILITIES } from "../../../threadTestUtils";
import { Toast } from "../../../widgets";
import { settleActivityDiscovery } from "../testing/activityDiscovery";
import { createTestComposerSource } from "../testing/composerSource";
import { Composer as ComposerView } from "./Composer";
import { flushPendingTurnsProjectionForTests } from "./queue/testing/flushPendingTurnsProjection";

export function Composer(props: React.ComponentProps<typeof ComposerView>) {
  const client = connectionStore.getState().client;
  if (!client) throw new Error("Composer test rendered without a connected client");
  return (
    <ClientProvider client={client}>
      <ComposerView {...props} />
    </ClientProvider>
  );
}

// What the HUB stamps for a thread with no daemon behind it
// (cmd/evener-hub/app_threadread.go's pastThreadCapabilities): send stays true
// because turn/start alone carries the auto-resume retry loop that wakes the
// session (app_rpc.go's resumeTurnStartThread), while steer, interrupt and
// queue are false because the hub cannot carry them out for a thread with no
// daemon - it resumes on send alone. This is what the client holds for a
// "notLoaded" status.
//
// The false queue bit here is the HUB's stub, not a daemon's answer: the
// submit router reads it as authoritative only for a live snapshot status
// (sendQueueAvailability.ts's pending-send tier), so the auto-resume window
// still queues the second message rather than disabling the composer.
export const PAST_THREAD_CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: false,
  interrupt: false,
  compact: true,
  clear: false,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  changeVisionModel: true,
  queue: false,
  goal: true,
  sharedNotes: true,
  rename: true,
};

export function testThread(ref: string, overrides: Partial<Thread> = {}): Thread {
  return {
    id: `thr_${ref}`,
    sessionId: `sess_${ref}`,
    preview: "test",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "idle" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "evener",
    evener: { ref, mutationStateAuthoritative: true, capabilities: FULL_CAPABILITIES, queue: { revision: 0 } },
    ...overrides,
  };
}

export function readResponse(ref: string, overrides: Partial<Thread> = {}): ThreadReadResponse {
  return { thread: testThread(ref, overrides) };
}

export function _emptyActivityTree(ref: string) {
  return {
    revision: 1,
    root: {
      sessionId: `sess_${ref}`,
      ref,
      label: "Root session",
      aggregate: "completed",
      counts: { active: 0, failed: 0, completed: 0, complete: true },
      entries: [],
      branch: {},
    },
  };
}

export async function mountComposerWithHandle(
  ref: string,
  overrides: Partial<Thread> = {},
  options: {
    focused?: boolean;
    prepare?: (fake: FakeClient) => void;
    holdMountProjection?: boolean;
    source?: ReturnType<typeof createTestComposerSource>;
  } = {},
) {
  const fake = connectReadyFakeClient(options.prepare);
  const source = options.source ?? createTestComposerSource(ref);
  fake.on("thread/read", () => readResponse(ref, overrides));
  await threadsStore.getState().ensureThread(ref);
  const view = render(
    <ClientProvider client={fake}>
      <Toast />
      <Composer ref={ref} source={source} focused={options.focused ?? false} />
    </ClientProvider>,
  );
  await settleActivityDiscovery(ref);
  // The mount starts its own pending-turns refresh; leaving it in flight lets
  // its state update land during a later bare await, outside act. A test that
  // holds or observes that work open asks to keep it in flight.
  if (!options.holdMountProjection) await flushPendingTurnsProjectionForTests();
  return { fake, ...view };
}

export async function mountComposer(
  ref: string,
  overrides: Partial<Thread> = {},
  options: { focused?: boolean; holdMountProjection?: boolean } = {},
): Promise<FakeClient> {
  return (await mountComposerWithHandle(ref, overrides, options)).fake;
}

export function notAcceptedError(clientMutationId: string): WireError {
  return new WireError("validation failed", -32602, {
    clientMutationId,
    mutationOutcome: "notAccepted",
    retryDisposition: "none",
  });
}

// `intent` overrides the record's method and input; the default is a turn/start
// carrying `text`, and a drain rejected with nothing queued carries no input.
export async function seedRejectedRecovery(
  storage: MutationOutboxIndexedDB,
  ref: string,
  text: string,
  intent: { method?: string; input?: unknown[] } = {},
) {
  const method = intent.method ?? "turn/start";
  const input = intent.input ?? [{ type: "text", text }];
  const outbox = await storage.enqueueIntent({
    targetRef: ref,
    threadId: "thread_a",
    method,
    payload: { ref, input },
    attachments: [],
    optimisticDisplay: { method, input },
  });
  const recovered = await storage.transferToRecovery(outbox.clientMutationId, "rejected");
  if (!recovered) throw new Error("failed to seed recovery");
  return recovered;
}

// Shaped exactly as composerMutationIntent writes a real submit: the payload
// text is the PROSE the marker was translated to at the submit boundary, and
// the untranslated composer text rides alongside it. Seeding raw markers into
// the payload instead would let this fixture pass on a projection that only
// ever reads the payload back.
export async function seedRejectedRecoveryWithAttachment(storage: MutationOutboxIndexedDB, ref: string) {
  const input = [
    { type: "text" as const, text: "edit me (attached image 1: proof.png)" },
    { type: "image" as const, mediaType: "image/png", data: "AQID", name: "proof.png" },
  ];
  const outbox = await storage.enqueueIntent({
    targetRef: ref,
    threadId: "thread_a",
    method: "turn/start",
    payload: { ref, input },
    attachments: [
      {
        presentationId: "presentation-1",
        marker: 1,
        name: "proof.png",
        mediaType: "image/png",
        blob: new Blob([new Uint8Array([1, 2, 3])], { type: "image/png" }),
      },
    ],
    optimisticDisplay: { method: "turn/start", input },
    composerText: "edit me [image 1]",
  });
  const recovered = await storage.transferToRecovery(outbox.clientMutationId, "rejected");
  if (!recovered) throw new Error("failed to seed attachment recovery");
  return recovered;
}

// --- ask-pending fixture ----------------------------------------------------
//
// askPending (Composer.tsx's own doc comment on the field) is NOT read off
// the thread directly - it comes from useAskDockPending(ref), which reads
// askDockStore, which reconciles itself off liveAskQuestions(model)
// (deriveAskQuestions.ts): a scan of the hydrated thread's OWN turns for a
// completed, unanswered ask_user commandExecution item after the last plain
// user message. There was no way to reach that state from this file before -
// nothing here ever gave a thread any turns at all - so every gate keyed on
// askPending (the timing caption's own !askPending clause, the input row's
// hidden/inert) went untested in both directions (kata yh13). Mirrors
// askDockStore.test.ts's own ONE_QUESTION/askArgs fixture, the file that
// already proves this exact turns shape reconciles into a pending batch.
export const ONE_ASK_QUESTION = [{ header: "Deploy?", question: "Ship now?", options: [{ label: "Yes", detail: "" }] }];

export function askUserArgs(questions: Array<Record<string, unknown>> = ONE_ASK_QUESTION): string {
  return JSON.stringify({ questions });
}

// pendingAskTurns is a Partial<Thread> overrides fragment - spread it into
// mountComposer's own `overrides` (alongside a "status"/"evener" override, if
// the test also needs the session busy) rather than calling it standalone,
// since a real ask-pending thread is still just a thread with turns, not a
// different shape.
export function pendingAskTurns(): Pick<Thread, "turns"> {
  return {
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            type: "commandExecution",
            id: "item_ask_1",
            turnId: "turn_1",
            toolName: "ask_user",
            callId: "call_ask_1",
            status: "completed",
            argumentsJson: askUserArgs(),
          },
        ],
      },
    ],
  };
}

export function currentWorkEvener({ task = false, goal = false }: { task?: boolean; goal?: boolean }) {
  return {
    ref: "ref_a",
    capabilities: FULL_CAPABILITIES,
    queue: { revision: 0 },
    ...(task
      ? { tasks: { total: 1, done: 0, current: { id: 1, description: "Finish the focused composer test" } } }
      : {}),
    ...(goal ? { goal: { objective: "Keep the session focused", status: "active" as const, iterations: 1 } } : {}),
  };
}

// --- narrow (phone-width) layout --------------------------------------------
//
// The phone-width row holds the shared Stop/Send slot, with Steer in the
// session menu instead of wrapping below the status row. The narrow gate reads a real ResizeObserver
// (narrowComposer.ts), which jsdom does not ship, so the narrow side is
// driven through a stub that reports ONLY for the composer root - the one
// element whose subtree contains the input card. Every other ResizeObserver
// in this tree (the textarea's own autoGrow, the popovers') observes an
// element inside the card, so the stub leaves them exactly as inert as they
// are without any ResizeObserver at all, and this file's default state (no
// stub, like every other composer test) pins the wide layout.
export function installNarrowComposer(width: number): void {
  class ComposerRootResizeObserver implements ResizeObserver {
    constructor(private readonly callback: ResizeObserverCallback) {}
    observe(target: Element): void {
      if (target.querySelector('[data-testid="composer-input-card"]') === null) return;
      this.callback([{ contentRect: { width } } as ResizeObserverEntry], this);
    }
    unobserve(): void {}
    disconnect(): void {}
  }
  globalThis.ResizeObserver = ComposerRootResizeObserver as unknown as typeof ResizeObserver;
}

export function textarea(): HTMLDivElement {
  return screen.getByRole("textbox", { name: /^message$/i }) as HTMLDivElement;
}

// The composer's controls are addressed by their stable data-testid, not by
// accessible name: two different buttons in this tree start with "Steer"
// (this component's own and QueueStrip's "Steer queue now"), and the
// submit button's own name tracks the send/queue routing and the keyboard
// hint. The accessible names are still a real contract - see the dedicated
// "spoken name" tests below - they just aren't how tests navigate.
export function submitButton(): HTMLButtonElement {
  return screen.getByTestId("composer-submit") as HTMLButtonElement;
}

export function steerButton(): HTMLButtonElement {
  return screen.getByTestId("composer-steer") as HTMLButtonElement;
}

export function stopButton(): HTMLButtonElement {
  return screen.getByTestId("composer-stop") as HTMLButtonElement;
}

export function routedCalls(fake: FakeClient): string[] {
  return fake.calls.filter((c) => c.method === "turn/start" || c.method === "turn/queue").map((c) => c.method);
}

// A press is judged on the session's controls at the moment it lands, not on
// the render that offered the button. Here the status frame and the press
// share one task (no render between them), so the render-time verdict still
// says active while the store says idle; the handler has to ask the store.
// Each test folds the frame and presses inside one act(): React defers the
// re-render the frame schedules until the act scope exits, after the press.
export function foldStatusFrame(fake: FakeClient, statusType: string): void {
  fake.emitNotification({
    method: "thread/status/changed",
    params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: statusType } },
  });
  expect(threadsStore.getState().threads.get("ref_a")?.status.type).toBe(statusType);
}

// --- attachments (paste -> tile -> submit) ----------------------------------

export function installCanvasStubs(): void {
  HTMLCanvasElement.prototype.getContext = (() => ({
    drawImage() {},
  })) as unknown as typeof HTMLCanvasElement.prototype.getContext;
  HTMLCanvasElement.prototype.toBlob = (callback: BlobCallback): void => {
    callback(new Blob([new Uint8Array([9, 9, 9])], { type: "image/png" }));
  };
  class FakeImage {
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    width = 4;
    height = 4;
    private _src = "";
    set src(value: string) {
      this._src = value;
      Promise.resolve().then(() => this.onload?.());
    }
    get src(): string {
      return this._src;
    }
  }
  // @ts-expect-error stubbing the global Image constructor for this test file only
  globalThis.Image = FakeImage;
  URL.createObjectURL = () => "blob:fake";
  URL.revokeObjectURL = () => {};
}

// The third decode stub, alongside installCanvasStubs (settles) and
// installFailingDecodeStub (rejects) below: this one never settles either
// way, so a staged item stays pending === true for the whole test.
//
// Pending is a real, separately-reachable state, not merely "not finished
// yet": the tile has no image to draw, submit is blocked, and removing the
// item has to cancel a decode that is still in flight (useAttachments'
// removedWhilePendingRef, kata kt4j). Stalling the decode is what pins a
// test to that state - the marker text lands synchronously with the paste,
// so waiting on it can return either side of a settling decode.
//
// What this stub no longer has to defend against is an element swap
// mid-gesture: pending and settled are one tile with one remove button now
// (kata 39xe), so a captured node stays connected across the transition
// rather than being unmounted under the interaction (kata 3rxj's flake).
export function installStalledDecodeStub(): void {
  HTMLCanvasElement.prototype.getContext = (() => ({
    drawImage() {},
  })) as unknown as typeof HTMLCanvasElement.prototype.getContext;
  HTMLCanvasElement.prototype.toBlob = () => {}; // never invokes its callback
  class NeverLoadsImage {
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    src = ""; // a plain field: assigning it never schedules onload/onerror
  }
  // @ts-expect-error stubbing the global Image constructor for this test file only
  globalThis.Image = NeverLoadsImage;
  URL.createObjectURL = () => "blob:fake";
  URL.revokeObjectURL = () => {};
}

// The fourth decode stub: settles on demand rather than on a microtask
// (installCanvasStubs) or never (installStalledDecodeStub). release()
// resolves only once the whole encode chain - Image.onload, canvas.toBlob,
// Blob.arrayBuffer - has actually delivered its bytes, so a caller can
// await the pending -> settled transition as a real completion instead of
// polling for its side effects.
export function installGatedDecodeStub(): { release: () => Promise<void> } {
  HTMLCanvasElement.prototype.getContext = (() => ({
    drawImage() {},
  })) as unknown as typeof HTMLCanvasElement.prototype.getContext;
  let markDelivered!: () => void;
  const delivered = new Promise<void>((resolve) => {
    markDelivered = resolve;
  });
  HTMLCanvasElement.prototype.toBlob = (callback: BlobCallback): void => {
    const blob = new Blob([new Uint8Array([9, 9, 9])], { type: "image/png" });
    const readBytes = blob.arrayBuffer.bind(blob);
    blob.arrayBuffer = async () => {
      const buffer = await readBytes();
      markDelivered();
      return buffer;
    };
    callback(blob);
  };
  const waiting: (() => void)[] = [];
  class GatedImage {
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    width = 4;
    height = 4;
    private _src = "";
    set src(value: string) {
      this._src = value;
      waiting.push(() => this.onload?.());
    }
    get src(): string {
      return this._src;
    }
  }
  // @ts-expect-error stubbing the global Image constructor for this test file only
  globalThis.Image = GatedImage;
  URL.createObjectURL = () => "blob:fake";
  URL.revokeObjectURL = () => {};
  return {
    release: () => {
      for (const fire of waiting.splice(0)) fire();
      return delivered;
    },
  };
}

// --- inline slash-command completion (Beautiful UI prompt-bar port) --------
//
// "/" is a literal keystroke everywhere in this composer (see the leading-
// "/" tests just above), including on an otherwise-empty composer, so it
// always reaches handleTextChange and the trailing-token parser
// (slashCompletion.ts) the same way. Most cases below still type the slash
// after some other text anyway, purely to also exercise the "mid-draft, not
// just at the very start" shape of that parser.

export const REVIEW_RELEASE_CATALOG = [
  { name: "review", description: "review the diff" },
  { name: "release", description: "cut a release" },
];

export function slashMenu() {
  return screen.getByTestId("composer-slash-menu");
}

export function slashOptions() {
  return within(slashMenu()).getAllByRole("option");
}
