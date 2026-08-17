import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { WireError } from "../../../protocol/errors";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { Thread, ThreadCapabilities, ThreadReadResponse } from "../../../protocol/types.gen";
import { paletteStore } from "../../../shell/palette/paletteController";
import { useCommandCatalog } from "../../../stores/commandCatalog";
import { connectionStore } from "../../../stores/connection";
import { MutationOutboxIndexedDB } from "../../../stores/mutationOutboxIndexedDB";
import { prefsStore, resetPrefsStoreForTests } from "../../../stores/prefs";
import { resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "../../../stores/threads";
import { Toast } from "../../../widgets";
import buttonStyles from "../../../widgets/button/button.module.css";
import iconButtonStyles from "../../../widgets/iconbutton/iconbutton.module.css";
import promptCardStyles from "../../../widgets/promptcard/promptcard.module.css";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import { resetAskDockStoreForTests } from "./askDock/askDockStore";
import { Composer } from "./Composer";
import { requestComposerFocus, resetComposerFocusStoreForTests } from "./composerFocus";
import { draftStorageKey } from "./draft";
import {
  refreshPendingTurnsProjection,
  resetPendingTurnsStoreForTests,
  settlePendingTurnsProjectionForTests,
} from "./queue/pendingTurnsStore";
import { requestQuoteInsert, resetQuoteInsertStoreForTests } from "./quoteInsert";

// See draft.test.ts's identical comment: Node 26 shadows jsdom's real
// window.localStorage with its own (non-functional under vitest) global.
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

beforeAll(() => {
  // @ts-expect-error see MemoryStorage's own comment for why this is needed
  globalThis.localStorage = new MemoryStorage();
});

const FULL_CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  queue: true,
  goal: true,
  rename: true,
};

// What a real daemon publishes for an IDLE thread, read off
// server/appwire_runtime.go's appCapabilities: `active` is false there, and
// both Steer and Queue are gated on it, so an idle thread advertises
// queue:false. Clear and ForkFromTurn are hardcoded false. This is the set the
// client is actually holding in the window kata 8c65 describes, and it is not
// FULL_CAPABILITIES.
const DAEMON_IDLE_CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: false,
  interrupt: true,
  compact: true,
  clear: false,
  forkFromTurn: false,
  shutdown: true,
  changeModel: true,
  queue: false,
  goal: true,
  rename: true,
};

function testThread(ref: string, overrides: Partial<Thread> = {}): Thread {
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
    source: "serf",
    serf: { ref, capabilities: FULL_CAPABILITIES, queue: { revision: 0 } },
    ...overrides,
  };
}

function readResponse(ref: string, overrides: Partial<Thread> = {}): ThreadReadResponse {
  return { thread: testThread(ref, overrides) };
}

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

class PausedCommitStorage extends MutationOutboxIndexedDB {
  readonly commitStarted: Promise<void>;
  private markCommitStarted: (() => void) | undefined;
  private releaseCommit: (() => void) | undefined;
  private readonly commitGate: Promise<void>;

  constructor() {
    super();
    this.commitStarted = new Promise((resolve) => {
      this.markCommitStarted = resolve;
    });
    this.commitGate = new Promise((resolve) => {
      this.releaseCommit = resolve;
    });
  }

  release(): void {
    this.releaseCommit?.();
  }

  override async enqueueIntent(
    intent: Parameters<MutationOutboxIndexedDB["enqueueIntent"]>[0],
  ): ReturnType<MutationOutboxIndexedDB["enqueueIntent"]> {
    this.markCommitStarted?.();
    await this.commitGate;
    return super.enqueueIntent(intent);
  }
}

class PausedRecoveryReadStorage extends MutationOutboxIndexedDB {
  private recoveryReadGate: Promise<void> = Promise.resolve();
  private resumeRecoveryReads: (() => void) | undefined;

  pauseRecoveryReads(): void {
    this.recoveryReadGate = new Promise((resolve) => {
      this.resumeRecoveryReads = resolve;
    });
  }

  resume(): void {
    this.resumeRecoveryReads?.();
    this.resumeRecoveryReads = undefined;
  }

  override async listRecovery(targetRef?: string): ReturnType<MutationOutboxIndexedDB["listRecovery"]> {
    await this.recoveryReadGate;
    return super.listRecovery(targetRef);
  }
}

// Recovery IndexedDB work that lands well after the flush a mount can drive,
// the way a loaded machine makes the real work land: mount-to-activation
// latency on this path measured 124-1246ms across 12 runs (kata 3c7t) while a
// React flush is single-digit ms. Nothing waits on the delay - the tests await
// the operation's own completion through settleRecoveryProjection - so the
// number only has to be long enough that an unawaited path cannot have
// finished yet.
const SLOW_RECOVERY_WORK_MS = 150;

class SlowRecoveryStorage extends MutationOutboxIndexedDB {
  override async listRecovery(targetRef?: string): ReturnType<MutationOutboxIndexedDB["listRecovery"]> {
    await new Promise((resolve) => setTimeout(resolve, SLOW_RECOVERY_WORK_MS));
    return super.listRecovery(targetRef);
  }

  override async updateRecoveryInput(
    ...args: Parameters<MutationOutboxIndexedDB["updateRecoveryInput"]>
  ): ReturnType<MutationOutboxIndexedDB["updateRecoveryInput"]> {
    await new Promise((resolve) => setTimeout(resolve, SLOW_RECOVERY_WORK_MS));
    return super.updateRecoveryInput(...args);
  }
}

class CountingRecoveryStorage extends MutationOutboxIndexedDB {
  recoveryInputWrites = 0;

  override async updateRecoveryInput(
    ...args: Parameters<MutationOutboxIndexedDB["updateRecoveryInput"]>
  ): ReturnType<MutationOutboxIndexedDB["updateRecoveryInput"]> {
    this.recoveryInputWrites += 1;
    return super.updateRecoveryInput(...args);
  }
}

// Awaits the durable projection work itself rather than polling the DOM for
// its effects. The composer starts that work from React effects, so a round
// has to flush React and then re-check: a round that awaited nothing is the
// proof that nothing is left. The bound is a tripwire for a livelock - it
// throws rather than letting a test pass on a half-settled projection.
async function settleRecoveryProjection(): Promise<void> {
  for (let round = 0; round < 10; round += 1) {
    let awaited = 0;
    await act(async () => {
      awaited = await settlePendingTurnsProjectionForTests();
    });
    if (awaited === 0) return;
  }
  throw new Error("pending-turns projection never settled");
}

async function mountComposerWithHandle(ref: string, overrides: Partial<Thread> = {}) {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse(ref, overrides));
  await threadsStore.getState().ensureThread(ref);
  const view = render(
    <>
      <Toast />
      <Composer ref={ref} />
    </>,
  );
  return { fake, ...view };
}

async function mountComposer(ref: string, overrides: Partial<Thread> = {}): Promise<FakeClient> {
  return (await mountComposerWithHandle(ref, overrides)).fake;
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function notAcceptedError(clientMutationId: string): WireError {
  return new WireError("validation failed", -32602, {
    clientMutationId,
    mutationOutcome: "notAccepted",
    retryDisposition: "none",
  });
}

async function seedRejectedRecovery(storage: MutationOutboxIndexedDB, ref: string, text: string) {
  const input = [{ type: "text", text }];
  const outbox = await storage.enqueueIntent({
    targetRef: ref,
    threadId: "thread_a",
    method: "turn/start",
    payload: { ref, input },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input },
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
async function seedRejectedRecoveryWithAttachment(storage: MutationOutboxIndexedDB, ref: string) {
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
const ONE_ASK_QUESTION = [{ header: "Deploy?", question: "Ship now?", options: [{ label: "Yes", detail: "" }] }];

function askUserArgs(questions: Array<Record<string, unknown>> = ONE_ASK_QUESTION): string {
  return JSON.stringify({ questions });
}

// pendingAskTurns is a Partial<Thread> overrides fragment - spread it into
// mountComposer's own `overrides` (alongside a "status"/"serf" override, if
// the test also needs the session busy) rather than calling it standalone,
// since a real ask-pending thread is still just a thread with turns, not a
// different shape.
function pendingAskTurns(): Pick<Thread, "turns"> {
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

test("while ask_pending is open, the AskDock replacement surface is exposed and the message textbox is hidden", async () => {
  await mountComposer("ref_a", {
    ...pendingAskTurns(),
  });

  expect(screen.getByText("Answer the agent’s questions.")).toBeTruthy();
  expect(screen.getByText("Ship now?")).toBeTruthy();
  expect(screen.queryByRole("textbox", { name: /message/i })).toBeNull();
});

test("the composer region fills pane height and bottom-anchors the replacement slot", () => {
  const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "composer.module.css"), "utf8");
  expect(css).toContain("flex: 1 1 auto");
  expect(css).toContain("min-height: 0");
  expect(css).toContain("justify-content: flex-end");
});

// The word beside the paper plane collapses by PANE width, not viewport
// width: a docked pane squeezed narrow on a desktop display needs the same
// icon-only Send the phone gets, and a viewport media query cannot see that
// (the overflowguard's 390px-pane-in-desktop-window measurement proved it).
// The 559px boundary matches SessionChrome's own GoalControl chip swap.
test("the Send button's word collapses to the glyph below the compact pane threshold", () => {
  const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "composer.module.css"), "utf8");
  expect(css).toMatch(/\.composer\s*\{[^}]*container-type:\s*inline-size/);
  expect(css).toMatch(/@container \(max-width: 559px\)[\s\S]*?\.submitLabel\s*\{[^}]*display:\s*none/);
});

beforeEach(() => {
  globalThis.indexedDB = new IDBFactory();
  localStorage.clear();
  resetPrefsStoreForTests();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
  // askDockStore reconciles reactively off threadsStore (registered once at
  // module load - askDockStore.ts's own header comment), so its byRef map
  // outlives resetThreadsStoreForTests() the same way the toast store
  // outlives RTL's cleanup below: without this, a pending-ask batch minted
  // for "ref_a" in one test would still be sitting there for the next
  // test's own "ref_a" mount.
  resetAskDockStoreForTests();
  resetQuoteInsertStoreForTests();
  resetComposerFocusStoreForTests();
  // useCommandCatalog is module state the same way, and is entirely unused
  // by every OTHER test in this file - only the slash-completion tests
  // below ever populate it - so resetting it here is purely additive
  // isolation, never a behavior change for the rest of the suite.
  useCommandCatalog.setState({ commands: [], loaded: false });
  // The toast store is module state that outlives RTL's own cleanup, so a
  // toast pushed by one test would otherwise still be in the next test's
  // tree and make a getByText for the same message ambiguous.
  resetToastStoreForTests();
  paletteStore.setState({ open: false, query: "" });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.useRealTimers();
  // Every test here calls ensureThread(ref) directly for setup - Composer
  // takes its ref as a prop and never calls ensureThread/releaseThread
  // itself, so cleanup()'s unmount leaves that ref refcounted after the LAST
  // test. Under isolate:false that is what a later file's own
  // connectionStore.connect() re-triggers via rewireClient.
  resetThreadsStoreForTests();
  // Every test here writes real durable outbox records into this file's own
  // globalThis.indexedDB instance (one exercises the unavailable-storage
  // boundary by setting it to undefined) - the beforeEach above only
  // replaces it BEFORE each test, so whatever the LAST test left in place
  // (populated, or undefined) stays installed as the global indexedDB after
  // this file finishes. Under isolate:false that is what a later file's own
  // default getMutationRuntime() (no setMutationStorageForTests override)
  // discovers - either a stale re-pinned record, or a hard throw.
  globalThis.indexedDB = new IDBFactory();
});

function textarea(): HTMLTextAreaElement {
  return screen.getByRole("textbox", { name: /^message$/i }) as HTMLTextAreaElement;
}

// The composer's controls are addressed by their stable data-testid, not by
// accessible name: two different buttons in this tree start with "Steer"
// (this component's own and QueueStrip's "Steer queue now"), and the
// submit button's own name tracks the send/queue routing and the keyboard
// hint. The accessible names are still a real contract - see the dedicated
// "spoken name" tests below - they just aren't how tests navigate.
function submitButton(): HTMLButtonElement {
  return screen.getByTestId("composer-submit") as HTMLButtonElement;
}

function steerButton(): HTMLButtonElement {
  return screen.getByTestId("composer-steer") as HTMLButtonElement;
}

function stopButton(): HTMLButtonElement {
  return screen.getByTestId("composer-stop") as HTMLButtonElement;
}

// --- basic surface ---------------------------------------------------------

test("renders a textarea with an accessible name", async () => {
  await mountComposer("ref_a");
  expect(textarea()).toBeTruthy();
});

test("restores a stored draft into the textarea on mount", async () => {
  localStorage.setItem("serf.composer.draft.v1.ref_a", "unsent thought");
  await mountComposer("ref_a");
  expect(textarea().value).toBe("unsent thought");
});

test("typing persists the draft under this ref's storage key", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a");
  await user.type(textarea(), "hi");
  expect(localStorage.getItem("serf.composer.draft.v1.ref_a")).toBe("hi");
});

// --- quote-insert (SelectionQuote's "Quote in reply" seam) -----------------
//
// SelectionQuote.tsx is a sibling component under Session.tsx, never a
// child of Composer - it hands quoted markdown to this ref's Composer via
// quoteInsert.ts's requestQuoteInsert/useQuoteInsertRequest pub/sub (that
// file's own header comment), not a prop. These tests exercise that seam
// from the Composer side only: they never render SelectionQuote, just call
// requestQuoteInsert directly, the same way the real bar would.

test("a quote-insert request writes the quoted markdown into an empty composer and focuses it", async () => {
  await mountComposer("ref_a");
  act(() => {
    requestQuoteInsert("ref_a", "> quoted line\n\n");
  });
  await waitFor(() => expect(textarea().value).toBe("> quoted line\n\n"));
  expect(document.activeElement).toBe(textarea());
});

test("a quote-insert request appends after a blank line, keeping whatever the user already typed", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a");
  await user.type(textarea(), "my own note");
  act(() => {
    requestQuoteInsert("ref_a", "> quoted line\n\n");
  });
  await waitFor(() => expect(textarea().value).toBe("my own note\n\n> quoted line\n\n"));
});

test("a quote-insert request for a DIFFERENT ref never reaches this composer", async () => {
  await mountComposer("ref_a");
  act(() => {
    requestQuoteInsert("ref_other", "> quoted line\n\n");
  });
  await new Promise((resolve) => setTimeout(resolve, 0));
  expect(textarea().value).toBe("");
});

test("the composer persists the quote-inserted text as this ref's draft", async () => {
  await mountComposer("ref_a");
  act(() => {
    requestQuoteInsert("ref_a", "> quoted line\n\n");
  });
  await waitFor(() => expect(localStorage.getItem("serf.composer.draft.v1.ref_a")).toBe("> quoted line\n\n"));
});

// SHOULD-FIX: requestQuoteInsert's own placement param (quoteInsert.ts) -
// "append" (the default, exercised above) keeps a quote after whatever the
// user already typed; "prefix" (the command palette's own slash-command
// insert - CommandPalette.tsx's activateCommand) puts the addition FIRST
// instead, with no separator, so a command lands where it can actually
// parse even against a non-empty draft.

test("placement 'append' on a seeded draft matches the unqualified default: appended after a blank line", async () => {
  localStorage.setItem(draftStorageKey("ref_a"), "my own note");
  await mountComposer("ref_a");
  expect(textarea().value).toBe("my own note");

  act(() => {
    requestQuoteInsert("ref_a", "> quoted line\n\n", "append");
  });

  await waitFor(() => expect(textarea().value).toBe("my own note\n\n> quoted line\n\n"));
  expect(textarea().selectionStart).toBe(textarea().value.length);
});

test("placement 'prefix' on a seeded draft inserts the addition BEFORE the existing text, with no separator", async () => {
  localStorage.setItem(draftStorageKey("ref_a"), "my own note");
  await mountComposer("ref_a");
  expect(textarea().value).toBe("my own note");

  act(() => {
    requestQuoteInsert("ref_a", "/p:review ", "prefix");
  });

  await waitFor(() => expect(textarea().value).toBe("/p:review my own note"));
  // The cursor lands right after the inserted invocation, not at the very
  // end of the merged text - see Composer.tsx's own comment on the effect.
  expect(textarea().selectionStart).toBe("/p:review ".length);
});

// --- composer-focus seam (composerFocus.ts) ---------------------------------
//
// A global Mod+I chord (owned elsewhere - see composerFocus.ts's own header
// comment) will call requestComposerFocus(ref) to move keyboard focus into
// this ref's Composer. These tests exercise that seam from the Composer
// side only, the same way the quote-insert tests above call
// requestQuoteInsert directly rather than rendering the chord's own owner.

test("a composer-focus request focuses this ref's textarea", async () => {
  await mountComposer("ref_a");
  act(() => {
    requestComposerFocus("ref_a");
  });
  await waitFor(() => expect(document.activeElement).toBe(textarea()));
});

test("a composer-focus request for a DIFFERENT ref never focuses this composer", async () => {
  await mountComposer("ref_a");
  act(() => {
    requestComposerFocus("ref_other");
  });
  await new Promise((resolve) => setTimeout(resolve, 0));
  expect(document.activeElement).not.toBe(textarea());
});

// The card is widgets/promptcard now, so the focus-ring RULE is that widget's
// own contract (promptcard.test.tsx pins the declaration). What stays this
// component's business is that it renders the shared card at all and that the
// seamless field inside really drives the card's focus state. Only the
// post-focus state is queried: jsdom's selector engine caches a :focus-within
// result per element, so an earlier "not yet focused" call on the same node
// would keep answering false afterwards.
test("focusing the message field lights the shared prompt card's own focus affordance", async () => {
  await mountComposer("ref_a");
  textarea().focus();
  expect(screen.getByTestId("composer-input-card").matches(":focus-within")).toBe(true);
});

// The composer and the spawn form are the SAME object: both render
// widgets/promptcard, not two components that merely resemble each other. The
// class on the rendered card is the proof that reaches across both files. The
// session chrome shares PromptCard's leading run with the attachment control,
// after the paperclip, so there is exactly one status row in this surface.
test("the composer's shared PromptCard leads with the attachment and inline session controls", async () => {
  await mountComposer("ref_a", {
    serf: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      queue: { revision: 0 },
      contextUsed: 64_000,
      contextWindow: 128_000,
      contextPressure: 0.5,
    },
  });
  const card = screen.getByTestId("composer-input-card");
  const attach = within(card).getByTestId("composer-attach");
  const inline = within(card).getByTestId("session-chrome-inline");

  expect(card.className.split(" ")).toContain(promptCardStyles.card);
  expect(attach.compareDocumentPosition(inline) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  expect(within(card).getByTestId("status-row-context")).toBeTruthy();
  expect(screen.queryAllByTestId("status-row")).toHaveLength(1);
});

// The chords moved out of the buttons into their tooltips, so each control's
// spoken name is now just its verb. These are the ONE place that asserts
// accessible names deliberately; everywhere else addresses controls by testid.
test("each control's spoken name is its bare verb - no chord glyphs in the name or the label", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  expect(screen.getByRole("button", { name: "Stop" })).toBe(stopButton());
  expect(screen.getByRole("button", { name: "Send" })).toBe(submitButton());
  expect(screen.getByRole("button", { name: "Steer" })).toBe(steerButton());
  expect(screen.getByRole("button", { name: "Attach image" })).toBe(screen.getByTestId("composer-attach"));
});

// The boxed <kbd> runs are gone from inside the buttons - three nested boxes
// dominated the button they annotated. The chord still has to be DISCOVERABLE,
// which is what each button's Tooltip is for.
test("no button renders a chord hint inside itself; the chord lives in the button's tooltip", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  const modWord = /Mac|iPhone|iPad|iPod/.test(window.navigator.platform) ? "⌘" : "Ctrl";

  for (const control of [stopButton(), submitButton(), steerButton()]) {
    expect(control.querySelector("kbd")).toBeNull();
    expect(control.textContent).not.toMatch(/↵/);
  }

  // Tooltip shows after its own 300ms delay; user-event's fake-free setup
  // advances real time, so this waits for the bubble rather than assuming it.
  await user.hover(submitButton());
  const tip = await screen.findByRole("tooltip");
  expect(tip.textContent).toContain(`${modWord}+Enter`);
});

test("the Steer tooltip names the chord that fires it", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  await user.hover(steerButton());
  const tip = await screen.findByRole("tooltip");
  expect(tip.textContent).toContain("Shift+Enter");
});

// Density: the control row is the 24px (xs) size. Three nested gaps plus the
// card's padding plus a taller row stacked up to a block far taller than the
// input it framed.
test("every control in the composer's button row is the xs (24px) size", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });

  // IconButton overrides Button's own xs/sm/md with its square sizing (see
  // iconbutton.module.css), so the one icon control carries that module's xs.
  expect(screen.getByTestId("composer-attach").className.split(" ")).toContain(iconButtonStyles.xs);
  for (const control of [stopButton(), steerButton(), submitButton()]) {
    expect(control.className.split(" ")).toContain(buttonStyles.xs);
  }
});

// Stop is the WORD, in danger ink, not a filled square glyph: it is chrome, and
// chrome speaks. dangerQuiet keeps the hue on the label rather than as a fill
// competing with the primary control beside it.
test("Stop renders as the word in the dangerQuiet variant, not as an icon", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  expect(stopButton().textContent).toBe("Stop");
  expect(stopButton().querySelector("svg")).toBeNull();
  expect(stopButton().className.split(" ")).toContain(buttonStyles.dangerQuiet);
});

// The attach control stays an icon (a paperclip needs no word), and stays a
// real SVG rather than a "📎"/"+" character whose weight and baseline shift
// from font to font.
test("the attach control draws an SVG glyph, not a literal text character", async () => {
  await mountComposer("ref_a");
  const attach = screen.getByTestId("composer-attach");
  expect(attach.querySelector("svg")).toBeTruthy();
  expect(attach.textContent).toBe("");
});

// --- the verb cluster's order and emphasis ---------------------------------
//
// Stop is pinned LEFTMOST so it never trades places with the verbs that come
// and go: it is the one control here whose misfire cannot be undone. Send holds
// the middle, Steer the right.
test("the cluster order is Stop, Send, Steer left to right", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  const card = screen.getByTestId("composer-input-card");
  const order = [...card.querySelectorAll("button")]
    .map((b) => b.getAttribute("data-testid"))
    .filter((id) => id?.startsWith("composer-") && id !== "composer-attach");
  expect(order).toEqual(["composer-stop", "composer-submit", "composer-steer"]);
});

// Jesse's own correction, honored exactly: both verbs stay, with distinct jobs.
// While a turn runs Steer is the primary (interrupt and redirect NOW) and Send
// sits beside it quiet, queueing until the agent stops. Idle, Send is the
// primary and sends immediately. A label never changes meaning under the user.
test("while a turn runs, Steer is primary and Send is quiet", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  expect(steerButton().className.split(" ")).toContain(buttonStyles.primary);
  expect(submitButton().className.split(" ")).toContain(buttonStyles.quiet);
});

test("with nothing running, Send is the primary and there is no Steer to outrank it", async () => {
  await mountComposer("ref_a", { status: { type: "idle" } });
  expect(submitButton().className.split(" ")).toContain(buttonStyles.primary);
  expect(screen.queryByTestId("composer-steer")).toBeNull();
});

// The label is stable across states even though the ROUTE isn't: a mid-turn
// Send queues (turn/queue, proven by the routing tests below) but still reads
// "Send", because the change is one of timing, not of verb. The tooltip is
// where the timing is spelled out.
test("Send keeps its label while a turn runs, and its tooltip explains the queueing", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 } },
  });
  expect(submitButton().textContent).toBe("Send");
  await user.hover(submitButton());
  expect((await screen.findByRole("tooltip")).textContent).toMatch(/queue until the agent stops/i);
});

test("Send's tooltip says it sends now when nothing is running", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a", { status: { type: "idle" } });
  await user.hover(submitButton());
  expect((await screen.findByRole("tooltip")).textContent).toMatch(/send now/i);
});

// cezn: Send and Steer sit side by side while a turn runs, and Send's own
// label never changes whether it fires now or queues - only the tooltip
// above disambiguates, and it needs a 300ms hover nobody mid-flow gives it.
// This caption says the same thing the tooltip does, WITHOUT a hover -
// always on screen the instant the control row itself is, non-color
// (plain caption text), and it never touches the Send/Steer labels
// themselves (additive only, per Composer.tsx's own top-of-file reasoning
// for keeping one label).
test("the timing caption is absent when a turn is busy and queueing is available", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  expect(screen.queryByText(/send queues until the agent stops/i)).toBeNull();
});

// Idle Send is unambiguous (there's no Steer beside it, and no timing
// question to answer), so the caption would be noise there - it only earns
// its place while the ambiguity it resolves actually exists.
test("the timing caption is absent when nothing is running", async () => {
  await mountComposer("ref_a", { status: { type: "idle" } });
  expect(screen.queryByText(/queues until the agent stops/i)).toBeNull();
});

// A turn can be busy (Stop/Steer showing) while the source has explicitly
// advertised no queue capability - deriveSendQueueAvailability's own
// both-false branch. Send is disabled there (canCompose is false), so a
// caption claiming it queues would be a lie; this pins the caption to
// availability.canQueue, not to `busy` alone.
// status can read "active" before turn/started has populated activeTurnId
// (isTurnActive's own doc comment on that race window) - Stop/Steer stay
// hidden there since neither is meaningful without a real turn to act on,
// and the caption follows the same rule: nothing to explain the timing of
// until Send/Steer's own ambiguity actually exists.
test("the timing caption is absent while status reads active but no turn has actually started yet", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 } },
  });
  expect(screen.queryByText(/queues until the agent stops/i)).toBeNull();
});

test("the timing caption is absent when busy but the source advertises no queue capability", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    serf: {
      ref: "ref_a",
      capabilities: { ...FULL_CAPABILITIES, queue: false },
      queue: { revision: 0 },
      activeTurnId: "turn_1",
    },
  });
  expect(screen.queryByText(/queues until the agent stops/i)).toBeNull();
});

// The timing caption has been removed entirely (kata mx43), but this test
// verifies the removal is complete by confirming it does not appear even in
// scenarios where it previously would have shown.
test("the timing caption is absent while an ask_user question is pending, even though the turn is busy and queueing is available", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
    ...pendingAskTurns(),
  });
  expect(screen.queryByText(/queues until the agent stops/i)).toBeNull();
});

// --- send / queue routing ---------------------------------------------------

test("idle session: submit button reads Send and posts turn/start with the composer text", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", { status: { type: "idle" } });
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  await user.type(textarea(), "hello agent");
  expect(submitButton().textContent).toMatch(/send/i);
  await user.click(submitButton());

  await waitFor(() => {
    expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true);
  });
  const call = fake.calls.find((c) => c.method === "turn/start");
  expect(call?.params).toMatchObject({ ref: "ref_a", input: [{ type: "text", text: "hello agent" }] });
});

test("the unchanged submitted payload clears as soon as its local outbox commit succeeds", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", () => new Promise<never>(() => undefined));

  await user.type(textarea(), "hello");
  await user.click(submitButton());

  await waitFor(() => expect(textarea().value).toBe(""));
  expect(localStorage.getItem("serf.composer.draft.v1.ref_a")).toBeNull();
});

test("text edited while the local outbox commit is pending survives that commit", async () => {
  const storage = new PausedCommitStorage();
  setMutationStorageForTests(storage);
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", () => new Promise<never>(() => undefined));

  await user.type(textarea(), "original");
  fireEvent.click(submitButton());
  await storage.commitStarted;

  fireEvent.change(textarea(), { target: { value: "original plus more" } });
  expect(localStorage.getItem("serf.composer.draft.v1.ref_a")).toBe("original plus more");

  storage.release();
  await waitFor(() => expect(fake.calls.some((call) => call.method === "turn/start")).toBe(true));

  expect(textarea().value).toBe("original plus more");
  expect(localStorage.getItem("serf.composer.draft.v1.ref_a")).toBe("original plus more");
});

test("a local outbox failure leaves the composer untouched and sends no RPC", async () => {
  // @ts-expect-error this test exercises the explicit unavailable-storage boundary
  globalThis.indexedDB = undefined;
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");

  await user.type(textarea(), "hello");
  await user.click(submitButton());

  await waitFor(() => expect(screen.getByText(/send failed/i)).toBeTruthy());
  expect(textarea().value).toBe("hello");
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
});

test("a lost response never restores submitted content over a newer composer draft", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", () => {
    throw new Error("response lost");
  });

  await user.type(textarea(), "submitted");
  await user.click(submitButton());
  await waitFor(() => expect(textarea().value).toBe(""));
  await user.type(textarea(), "new draft");

  await waitFor(() => expect(fake.calls.some((call) => call.method === "turn/start")).toBe(true));
  expect(textarea().value).toBe("new draft");
  expect(screen.queryByText(/reload before retrying/i)).toBeNull();
});

test("active session with queue capability: Send routes to turn/queue", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 } },
  });
  fake.on("turn/queue", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await user.type(textarea(), "queued message");
  await user.click(submitButton());

  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/queue")).toBe(true));
});

// kata 8c65. Two messages sent quickly: the first turn/start is ACCEPTED (the
// daemon answers every turn/start with projectionState "pending" -
// agent/session_client_mutation_queue.go's acceptedClientMutationProjection)
// but no thread/status/changed has arrived yet, so the thread still reads idle
// and still carries the IDLE capability set. The second message used to be
// built as another turn/start and refused with
// Conflict("turn is already active").
//
// This has to be mounted rather than unit-tested on the routing table: the
// table takes the capability set as an argument, so it cannot notice that the
// set it was handed is one no daemon ever sends. The first attempt at this fix
// was proved only against an all-true fixture, routed to BOTH_UNAVAILABLE
// against the real idle set, and disabled Send outright.
test("a second message composed before the first turn's status frame arrives queues instead of bouncing", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "idle" },
    serf: { ref: "ref_a", capabilities: DAEMON_IDLE_CAPABILITIES, queue: { revision: 0 } },
  });
  // The response's own `turn` never reaches the model - MutationDispatcher
  // reads the receipt and nothing else - and no thread/status/changed is
  // pushed, so the thread stays idle with its idle capabilities throughout.
  // That IS the window.
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "pending",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));
  fake.on("turn/queue", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "pending",
    },
  }));

  await user.type(textarea(), "first message");
  await user.click(submitButton());
  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true));

  await user.type(textarea(), "second message");
  // Still composable: an idle queue:false means "no turn to queue behind", and
  // must never be read as "this session takes no input".
  await waitFor(() => expect(submitButton().disabled).toBe(false));
  await user.click(submitButton());

  await waitFor(() => {
    const queued = fake.calls.find((c) => c.method === "turn/queue");
    expect(queued?.params).toMatchObject({ ref: "ref_a", input: [{ type: "text", text: "second message" }] });
  });
  // turn/queue carries no expectedTurnId to be wrong about: appwire v3 dropped
  // the field from the method outright (appwire/types.go's ProtocolVersion
  // note), which is what lets the queue land before any turn id exists.
  expect(fake.calls.find((c) => c.method === "turn/queue")?.params).not.toHaveProperty("expectedTurnId");
  expect(fake.calls.filter((c) => c.method === "turn/start")).toHaveLength(1);
});

test("submitting an empty composer fires no request", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));
  expect(submitButton().disabled).toBe(true);
  await user.click(submitButton());
  // fake.calls already carries mountComposer's own thread/read hydration -
  // only the absence of an actual submit call is under test here.
  expect(fake.calls.filter((c) => c.method === "turn/start" || c.method === "turn/queue")).toHaveLength(0);
});

// --- keyboard shortcuts ------------------------------------------------------

test("Cmd+Enter always submits, regardless of the enterToSend preference", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  await user.type(textarea(), "quick send");
  await user.keyboard("{Meta>}{Enter}{/Meta}");

  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true));
});

test("bare Enter does not submit when enterToSend is off (default)", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  await user.type(textarea(), "line one{Enter}");
  expect(fake.calls.filter((c) => c.method === "turn/start")).toHaveLength(0);
  expect(textarea().value).toBe("line one\n");
});

// IME guard: while an IME composition is in progress (e.g. finishing a
// Japanese/Chinese candidate with Enter), that Enter keydown must never be
// read as "submit" - it is the IME's own confirm keystroke, not the user
// asking to send.
test("Enter is ignored while an IME composition is in progress, even with enterToSend on", async () => {
  prefsStore.getState().setEnterToSend(true);
  await mountComposer("ref_a");
  const requestSubmitSpy = vi.spyOn(HTMLFormElement.prototype, "requestSubmit").mockImplementation(() => {});

  fireEvent.change(textarea(), { target: { value: "composing" } });
  fireEvent.keyDown(textarea(), { key: "Enter", isComposing: true });

  expect(requestSubmitSpy).not.toHaveBeenCalled();
});

test("bare Enter submits when enterToSend is on", async () => {
  prefsStore.getState().setEnterToSend(true);
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  await user.type(textarea(), "go{Enter}");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true));
});

test("Shift+Enter with an empty queue and text steers instead of submitting", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  fake.on("turn/steer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await user.type(textarea(), "steer this");
  await user.keyboard("{Shift>}{Enter}{/Shift}");

  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/steer")).toBe(true));
  const call = fake.calls.find((c) => c.method === "turn/steer");
  expect(call?.params).toMatchObject({ ref: "ref_a" });
});

test("with enterToSend on, Shift+Enter is a literal newline and does not steer", async () => {
  prefsStore.getState().setEnterToSend(true);
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  fake.on("turn/steer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await user.type(textarea(), "abc");
  await user.keyboard("{Shift>}{Enter}{/Shift}");

  expect(fake.calls.filter((c) => c.method === "turn/steer")).toHaveLength(0);
  expect(textarea().value).toBe("abc\n");
});

// The chord a hint advertises has to track the preference that actually fires
// it: enterToSend on means bare Enter submits, so the tooltip must say Enter,
// not Mod+Enter. Now asserted on the tooltip, since that's where the chord
// moved.
test("the submit tooltip's chord switches from Mod+Enter to a bare Enter when enterToSend is on", async () => {
  const user = userEvent.setup();
  const modWord = /Mac|iPhone|iPad|iPod/.test(window.navigator.platform) ? "⌘" : "Ctrl";

  await mountComposer("ref_a");
  await user.hover(submitButton());
  expect((await screen.findByRole("tooltip")).textContent).toContain(`${modWord}+Enter`);

  cleanup();
  prefsStore.getState().setEnterToSend(true);
  await mountComposer("ref_a");
  await user.hover(submitButton());
  const tip = await screen.findByRole("tooltip");
  expect(tip.textContent).toMatch(/·\s*Enter$/);
  expect(tip.textContent).not.toContain(modWord);
});

// enterToSend on makes Shift+Enter a literal newline rather than a steer (see
// handleKeyDown), so Steer's tooltip must stop advertising a chord that no
// longer reaches it.
test("Steer's tooltip drops the chord when enterToSend has taken Shift+Enter away from it", async () => {
  prefsStore.getState().setEnterToSend(true);
  const user = userEvent.setup();
  await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  await user.hover(steerButton());
  const tip = await screen.findByRole("tooltip");
  expect(tip.textContent).toMatch(/interrupt and redirect now/i);
  expect(tip.textContent).not.toMatch(/Shift/);
});

// --- steer / drain-as-steer routing -----------------------------------------

test("clicking steer with an empty textarea and empty queue is a focus-only no-op", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  fake.on("turn/steer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));
  fake.on("turn/drainAsSteer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await user.click(steerButton());

  expect(fake.calls.filter((c) => c.method === "turn/steer" || c.method === "turn/drainAsSteer")).toHaveLength(0);
  expect(document.activeElement).toBe(textarea());
});

test("a successful classic steer also clears the textarea and its draft (contracts §Drafts)", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  fake.on("turn/steer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await user.type(textarea(), "steer this");
  await user.click(steerButton());

  await waitFor(() => expect(textarea().value).toBe(""));
  expect(localStorage.getItem("serf.composer.draft.v1.ref_a")).toBeNull();
});

test("clicking steer with a non-empty queue routes to drain-as-steer, carrying the composer text", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    serf: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      queue: { revision: 0, depth: 2, preview: ["a", "b"] },
      activeTurnId: "turn_1",
    },
  });
  fake.on("turn/drainAsSteer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await user.type(textarea(), "drain me");
  await user.click(steerButton());

  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/drainAsSteer")).toBe(true));
  const call = fake.calls.find((c) => c.method === "turn/drainAsSteer");
  expect(call?.params).toMatchObject({ ref: "ref_a", input: [{ type: "text", text: "drain me" }] });
});

// Steer and Stop only act on an in-flight turn, so neither is rendered
// during the window after status flips "active" but before activeTurnId
// arrives - the same isTurnActive gate their handlers need.
test("neither steer nor stop renders during the window after status flips active but before activeTurnId arrives", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 } }, // no activeTurnId yet
  });
  fake.on("turn/steer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await user.type(textarea(), "hi");
  expect(screen.queryByTestId("composer-steer")).toBeNull();
  expect(screen.queryByTestId("composer-stop")).toBeNull();

  expect(fake.calls.filter((c) => c.method === "turn/steer")).toHaveLength(0);
});

// Shift+Enter reaches the steer handler directly off the keydown event, so
// it works whether or not the Steer BUTTON is on screen at all - exactly
// mirroring legacy's own "keyboard equivalent of clicking the steer button"
// (the SAME function, not a separately-gated path). The handler's own
// internal activeTurnId check is therefore the only thing standing between
// the keyboard and a doomed steer, and these two cases are where it earns
// its keep: no turn is in flight, so no Steer button is rendered to gate on.
test("Shift+Enter with no active turn id shows a 'no active turn' toast rather than attempting a doomed steer", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 } }, // no activeTurnId
  });
  fake.on("turn/steer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await user.type(textarea(), "hi");
  await user.keyboard("{Shift>}{Enter}{/Shift}");

  await waitFor(() => expect(screen.getByText(/no active turn/i)).toBeTruthy());
  expect(fake.calls.filter((c) => c.method === "turn/steer")).toHaveLength(0);
});

// The drain route has the same doomed-without-a-turn shape as steer — the hub
// rejects an empty expectedTurnId before forwarding — but the handler only
// guarded the steer branch, so a Steer-click that routed to drain (non-empty
// queue, or staged attachments) minted a durable poison intent instead of a
// toast. That is the exact first stuck message of the kata-wr3s incident.
test("Shift+Enter routing to drain with no active turn id toasts rather than minting a doomed drain", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    serf: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      queue: { revision: 0, depth: 1, preview: ["queued follow-up"] }, // non-empty queue → drain route
    }, // no activeTurnId
  });
  fake.on("turn/drainAsSteer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await user.type(textarea(), "hi");
  await user.keyboard("{Shift>}{Enter}{/Shift}");

  await waitFor(() => expect(screen.getByText(/no active turn/i)).toBeTruthy());
  expect(fake.calls.filter((c) => c.method === "turn/drainAsSteer")).toHaveLength(0);
});

test("Shift+Enter on an idle session, where no Steer button renders at all, still reaches the handler and toasts", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", { status: { type: "idle" } });
  fake.on("turn/steer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await user.type(textarea(), "hi");
  expect(screen.queryByTestId("composer-steer")).toBeNull(); // nothing to click; the keybinding is the only route
  await user.keyboard("{Shift>}{Enter}{/Shift}");

  await waitFor(() => expect(screen.getByText(/no active turn/i)).toBeTruthy());
  expect(fake.calls.filter((c) => c.method === "turn/steer")).toHaveLength(0);
});

test("an indefinitely pending steer never emits a timeout warning or reload instruction", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    serf: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      queue: { revision: 1 },
      activeTurnId: "turn_1",
    },
  });
  fake.on("turn/steer", () => new Promise<never>(() => undefined));

  await user.type(textarea(), "patient steer");
  await user.click(steerButton());
  await waitFor(() => expect(fake.calls.some((call) => call.method === "turn/steer")).toBe(true));

  vi.useFakeTimers();
  await act(() => vi.advanceTimersByTimeAsync(60_000));

  expect(screen.queryByText(/accepted.*view didn't update/i)).toBeNull();
  expect(screen.queryByText(/reload/i)).toBeNull();
});

test("an explicit rejection returns to the sole Composer textarea", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "idle" },
  });
  fake.on("turn/start", (params) => {
    throw new WireError("validation failed", -32602, {
      clientMutationId: params.clientMutationId,
      mutationOutcome: "notAccepted",
      retryDisposition: "none",
    });
  });

  await user.type(textarea(), "rejected draft");
  await user.click(submitButton());

  await waitFor(() => expect(textarea().value).toBe("rejected draft"));
  expect(screen.getAllByRole("textbox")).toEqual([textarea()]);
  expect(screen.queryByText("Recovery drafts")).toBeNull();
  expect(screen.queryByRole("textbox", { name: "Recovered message" })).toBeNull();
});

test("an occupied Composer is not overwritten by a later rejection", async () => {
  const rejection = deferred<never>();
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", { status: { type: "idle" } });
  let clientMutationId = "";
  fake.on("turn/start", (params) => {
    clientMutationId = String(params.clientMutationId);
    return rejection.promise;
  });

  await user.type(textarea(), "rejected draft");
  await user.click(submitButton());
  await waitFor(() => expect(textarea().value).toBe(""));
  await user.type(textarea(), "current work");
  act(() => rejection.reject(notAcceptedError(clientMutationId)));

  await waitFor(() => expect(screen.getByText("rejected draft")).toBeTruthy());
  expect(textarea().value).toBe("current work");
});

test("editing a rejected queue row merges it through the normal Composer", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const user = userEvent.setup();
  await mountComposer("ref_a", { status: { type: "idle" } });
  await user.type(textarea(), "current work");
  await seedRejectedRecovery(storage, "ref_a", "rejected draft");
  await refreshPendingTurnsProjection("ref_a");

  const row = screen.getByText("rejected draft").closest("li");
  if (!row) throw new Error("missing rejected queue row");
  await user.click(within(row).getByRole("button", { name: "Edit message" }));

  await settleRecoveryProjection();
  expect(textarea().value).toBe("current work\n\nrejected draft");
  expect(screen.getAllByRole("textbox")).toEqual([textarea()]);
  expect(screen.queryByText("rejected draft")).toBeNull();
});

test("sending recovered text uses current Composer routing and consumes the recovery record", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "retry me");
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    serf: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      activeTurnId: "turn-current",
      queue: { revision: 4 },
    },
  });
  fake.on("turn/queue", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await settleRecoveryProjection();
  expect(textarea().value).toBe("retry me");
  await user.click(submitButton());
  await settleRecoveryProjection();

  expect(await storage.getRecovery(recovered.clientMutationId)).toBeUndefined();
  // The wire call itself stays a waitFor: dispatch is deliberately
  // fire-and-forget off the durable resend (threads.ts's own
  // handleDiscoveredMutations call), so it is not projection work to await.
  await waitFor(() => expect(fake.calls.some((call) => call.method === "turn/queue")).toBe(true));
  expect(fake.calls.find((call) => call.method === "turn/queue")?.params).toMatchObject({
    input: [{ type: "text", text: "retry me" }],
  });
});

test("a losing cross-tab recovered send does not issue a second request", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "one winner");
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", { status: { type: "idle" } });
  await settleRecoveryProjection();
  expect(textarea().value).toBe("one winner");
  const otherTab = new MutationOutboxIndexedDB();
  await otherTab.resendRecovery(recovered.clientMutationId, {
    targetRef: "ref_a",
    threadId: "thread_a",
    method: "turn/start",
    payload: { ref: "ref_a", input: [{ type: "text", text: "one winner" }] },
    attachments: [],
    optimisticDisplay: {
      method: "turn/start",
      input: [{ type: "text", text: "one winner" }],
    },
  });

  await user.click(submitButton());

  await waitFor(() => expect(textarea().value).toBe(""));
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
  otherTab.close();
});

// An activated recovery draft republishes the projection on every durable
// write, and every projection publish re-renders this component. So an effect
// that writes on each render is a self-feeding IndexedDB loop that never
// stops while the draft sits there - it burns the event loop the composer's
// own awaits need, and it means "the projection has settled" is never true.
test("a recovered draft nobody is touching stops writing itself back to IndexedDB", async () => {
  const storage = new CountingRecoveryStorage();
  setMutationStorageForTests(storage);
  await seedRejectedRecovery(storage, "ref_a", "sitting still");
  await mountComposer("ref_a", { status: { type: "idle" } });
  await settleRecoveryProjection();
  expect(textarea().value).toBe("sitting still");

  const afterActivation = storage.recoveryInputWrites;
  await settleRecoveryProjection();

  expect(storage.recoveryInputWrites).toBe(afterActivation);
});

// The two below pin the mount-time recovery gate and the recovery write to an
// AWAITABLE completion. Both hold a rejected record behind IndexedDB work that
// outlasts the mount's own flush, so a Composer whose activation or whose
// durable edit is merely polled for cannot have landed by the assertion.

test("a slow recovery read still activates before the mount's projection work is awaited out", async () => {
  const storage = new SlowRecoveryStorage();
  setMutationStorageForTests(storage);
  await seedRejectedRecovery(storage, "ref_a", "slow to arrive");
  await mountComposer("ref_a", { status: { type: "idle" } });
  expect(textarea().value).toBe("");

  await settleRecoveryProjection();

  expect(textarea().value).toBe("slow to arrive");
});

test("a slow recovery write is durable before the edit's projection work is awaited out", async () => {
  const storage = new SlowRecoveryStorage();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "before");
  const user = userEvent.setup();
  await mountComposer("ref_a", { status: { type: "idle" } });
  await settleRecoveryProjection();
  await user.type(textarea(), "!");

  await settleRecoveryProjection();

  expect((await storage.getRecovery(recovered.clientMutationId))?.payload.input).toEqual([
    { type: "text", text: "before!" },
  ]);
});

test("recovered edits and attachment removal survive Composer remount", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecoveryWithAttachment(storage, "ref_a");
  const user = userEvent.setup();
  const first = await mountComposerWithHandle("ref_a", { status: { type: "idle" } });
  await settleRecoveryProjection();
  expect(textarea().value).toBe("edit me [image 1]");
  expect(screen.getByRole("button", { name: "Remove proof.png" })).toBeTruthy();

  await user.clear(textarea());
  await user.type(textarea(), "edited");
  await user.click(screen.getByRole("button", { name: "Remove proof.png" }));
  await settleRecoveryProjection();
  expect((await storage.getRecovery(recovered.clientMutationId))?.payload.input).toEqual([
    { type: "text", text: "edited" },
  ]);
  first.unmount();

  await mountComposer("ref_a", { status: { type: "idle" } });
  await settleRecoveryProjection();
  expect(textarea().value).toBe("edited");
  // Any remove control at all, not one named for this file: a tile carries
  // its filename in labels rather than as a text node, so a text query would
  // report "gone" for an attachment still sitting there - and a query naming
  // the file would report the same if only the label changed.
  expect(screen.queryAllByRole("button", { name: /^Remove/ })).toHaveLength(0);
});

test("blanking an attachment-free recovered draft discards it durably", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "discard me");
  const user = userEvent.setup();
  const first = await mountComposerWithHandle("ref_a", { status: { type: "idle" } });
  await settleRecoveryProjection();
  expect(textarea().value).toBe("discard me");
  await user.clear(textarea());
  await settleRecoveryProjection();
  expect(await storage.getRecovery(recovered.clientMutationId)).toBeUndefined();
  first.unmount();

  await mountComposer("ref_a", { status: { type: "idle" } });
  await settleRecoveryProjection();
  expect(textarea().value).toBe("");
  expect(screen.queryByText("discard me")).toBeNull();
});

test("a remounted Composer does not activate a stale recovery projection", async () => {
  const storage = new PausedRecoveryReadStorage();
  setMutationStorageForTests(storage);
  const recovered = await seedRejectedRecovery(storage, "ref_a", "already discarded");
  await refreshPendingTurnsProjection("ref_a");
  await storage.discardRecovery(recovered.clientMutationId);
  storage.pauseRecoveryReads();

  try {
    await mountComposer("ref_a", { status: { type: "idle" } });
    expect(textarea().value).toBe("");
  } finally {
    storage.resume();
  }
  await waitFor(() => expect(screen.queryByText("already discarded")).toBeNull());
});

// --- which controls the row shows ------------------------------------------
//
// Steer and Stop both act on an IN-FLIGHT turn: with nothing running there is
// no turn to steer into and none to interrupt, so an idle composer shows only
// attach + the submit button rather than two permanently-dead controls.
// Capability still gates them independently for a session whose harness
// can't steer or can't interrupt.

test("an idle session renders neither steer nor stop - only attach and submit", async () => {
  await mountComposer("ref_a", { status: { type: "idle" } });
  expect(screen.queryByTestId("composer-steer")).toBeNull();
  expect(screen.queryByTestId("composer-stop")).toBeNull();
  expect(screen.getByTestId("composer-attach")).toBeTruthy();
  expect(submitButton()).toBeTruthy();
});

test("a busy session renders both steer and stop, enabled", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  expect(steerButton().disabled).toBe(false);
  expect(stopButton().disabled).toBe(false);
});

test("a busy session on a harness that can't interrupt renders steer but not stop", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    serf: {
      ref: "ref_a",
      capabilities: { ...FULL_CAPABILITIES, interrupt: false },
      queue: { revision: 0 },
      activeTurnId: "turn_1",
    },
  });
  expect(steerButton()).toBeTruthy();
  expect(screen.queryByTestId("composer-stop")).toBeNull();
});

test("a busy session on a harness that can't steer renders stop but not steer", async () => {
  await mountComposer("ref_a", {
    status: { type: "active" },
    serf: {
      ref: "ref_a",
      capabilities: { ...FULL_CAPABILITIES, steer: false },
      queue: { revision: 0 },
      activeTurnId: "turn_1",
    },
  });
  expect(stopButton()).toBeTruthy();
  expect(screen.queryByTestId("composer-steer")).toBeNull();
});

test("the stop button is absent once the session has ended", async () => {
  await mountComposer("ref_a", { status: { type: "ended" } });
  expect(screen.queryByTestId("composer-stop")).toBeNull();
});

// --- the ended state: an epitaph, not a cockpit ---------------------------
//
// A cold exited serf session arrives as "notLoaded" and STILL advertises Send
// (cmd/serf-hub/app_threadread.go's pastEntryThread: the hub auto-resumes it on
// the first message), so it keeps a card - collapsed to a one-line invitation
// AT REST, since chrome around an empty invitation is noise. Engaging it
// (focus, or any content) grows the real control row: a field you can type into
// with no visible way to send is a dead end, and a keyboard chord is not an
// affordance anyone can see.

const ENDED_STATUSES = ["ended", "closed", "notLoaded"] as const;

test.each(ENDED_STATUSES)("a %s session's card rests as a bare invitation with no control row", async (type) => {
  await mountComposer("ref_a", { status: { type } });
  const card = screen.getByTestId("composer-input-card");
  expect(textarea().getAttribute("placeholder")).toBe("Send a follow-up…");
  expect(card.querySelectorAll("button")).toHaveLength(0);
  expect(screen.queryByTestId("composer-attach")).toBeNull();
  expect(screen.queryByTestId("session-chrome-inline")).toBeNull();
  expect(screen.queryByTestId("composer-submit")).toBeNull();
});

test.each(ENDED_STATUSES)("a %s session's card grows a usable Send once focused", async (type) => {
  await mountComposer("ref_a", { status: { type } });
  await userEvent.setup().click(textarea());

  const submit = screen.getByTestId("composer-submit");
  expect(submit.textContent).toContain("Send");
  expect(screen.getByTestId("composer-attach")).toBeTruthy();
  // Steer and Stop stay absent: there is no turn in flight to act on.
  expect(screen.queryByTestId("composer-steer")).toBeNull();
  expect(screen.queryByTestId("composer-stop")).toBeNull();
});

// The button has to be ENABLED, not merely present. deriveSendQueueAvailability
// reports canSend===canQueue===false for ended/closed (no turn to send to or
// queue behind), so gating the control on it renders a permanently dead Send at
// exactly the sessions the hub resumes on demand.
test.each(ENDED_STATUSES)("a %s session's Send enables as soon as there is something to send", async (type) => {
  const user = userEvent.setup();
  await mountComposer("ref_a", { status: { type } });
  await user.click(textarea());
  expect((screen.getByTestId("composer-submit") as HTMLButtonElement).disabled).toBe(true); // nothing typed yet

  await user.type(textarea(), "follow up on this");
  expect((screen.getByTestId("composer-submit") as HTMLButtonElement).disabled).toBe(false);
});

test.each(ENDED_STATUSES)(
  "clicking Send on a %s session really sends, rather than toasting a refusal",
  async (type) => {
    const user = userEvent.setup();
    const fake = await mountComposer("ref_a", { status: { type } });
    fake.on("turn/start", (params) => ({
      receipt: {
        clientMutationId: params.clientMutationId,
        disposition: "applied",
        threadId: "thread_a",
        projectionState: "reflected",
      },
      turn: { id: "turn_1", status: "inProgress", itemsView: "" },
    }));

    await user.click(textarea());
    await user.type(textarea(), "wake up and finish the job");
    await user.click(screen.getByTestId("composer-submit"));

    await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true));
    expect(screen.queryByText(/Send is not available/)).toBeNull();
  },
);

// Blur must not strand a typed message: the control row is gated on engagement
// (focus OR content), so text left in the field keeps its Send.
test("an ended session that still holds text keeps its control row after blur", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a", { status: { type: "ended" } });
  await user.click(textarea());
  await user.type(textarea(), "draft I walked away from");
  await user.tab();

  expect(screen.getByTestId("composer-submit")).toBeTruthy();
});

// One line at rest, opening to a real writing surface once focused. Driven from
// React state rather than a :focus-within CSS rule because the floor has to
// reach the field's own `rows` attribute to take effect at all (widgets/textarea
// documents why), and only the prop can do that. Verified in Chrome too: before
// the rows half of that fix, the collapsed field measured 39px - two lines - no
// matter what the floor said.
test("an ended session's field rests at one line and opens to three on focus", async () => {
  await mountComposer("ref_a", { status: { type: "notLoaded" } });
  expect(textarea().getAttribute("rows")).toBe("1");
  expect(textarea().style.getPropertyValue("--textarea-min-lines")).toBe("1");

  act(() => textarea().focus());
  expect(textarea().getAttribute("rows")).toBe("3");
  expect(textarea().style.getPropertyValue("--textarea-min-lines")).toBe("3");

  act(() => textarea().blur());
  expect(textarea().getAttribute("rows")).toBe("1");
});

// A live session's field must NOT pick up the collapsed floor - it keeps the
// widget's own MIN_ROWS default, so a running composer is a comfortable target.
test("a live session's field keeps the widget's own default line floor", async () => {
  await mountComposer("ref_a", { status: { type: "idle" } });
  expect(textarea().getAttribute("rows")).toBe("2");
  expect(textarea().getAttribute("style") ?? "").not.toContain("--textarea-min-lines");
});

test("an ended session can still be typed into and submitted with the Mod+Enter chord", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", { status: { type: "notLoaded" } });
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  await user.type(textarea(), "one more thing");
  await user.keyboard("{Meta>}{Enter}{/Meta}");

  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true));
});

// The other half of the rule: when the source really cannot take input, there
// is nothing a card could accomplish, so none is rendered. An unusable field is
// worse than no field - which is exactly what a disabled one was.
test("a session whose harness advertises no send at all renders NO card, not a dead one", async () => {
  await mountComposer("ref_a", {
    status: { type: "closed" },
    serf: { ref: "ref_a", capabilities: { ...FULL_CAPABILITIES, send: false }, queue: { revision: 0 } },
  });
  expect(screen.queryByTestId("composer-input-card")).toBeNull();
  expect(screen.queryByRole("textbox", { name: /message/i })).toBeNull();
});

// --- interrupt ---------------------------------------------------------------

test("clicking Stop calls turn/interrupt", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 }, activeTurnId: "turn_1" },
  });
  fake.on("turn/interrupt", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await user.click(stopButton());

  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/interrupt")).toBe(true));
});

// --- attachments (paste -> tile -> submit) ----------------------------------

function pastePngInto(el: HTMLElement, name = "shot.png"): void {
  const file = new File([new Uint8Array([1, 2, 3])], name, { type: "image/png" });
  const event = new Event("paste", { bubbles: true, cancelable: true });
  Object.defineProperty(event, "clipboardData", {
    value: { items: [{ kind: "file", type: "image/png", getAsFile: () => file }] },
  });
  el.dispatchEvent(event);
}

function installCanvasStubs(): void {
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
function installStalledDecodeStub(): void {
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

test("pasting an image renders a removable attachment tile and inserts its marker", async () => {
  installCanvasStubs();
  await mountComposer("ref_a");

  pastePngInto(textarea());
  await waitFor(() => expect(textarea().value).toBe("[image 1]"));
  expect(screen.getByRole("button", { name: /remove/i })).toBeTruthy();
});

test("the remove button names the specific attachment it removes", async () => {
  installCanvasStubs();
  await mountComposer("ref_a");

  pastePngInto(textarea(), "shot.png");
  await waitFor(() => expect(textarea().value).toBe("[image 1]"));

  expect(screen.getByRole("button", { name: "Remove shot.png" })).toBeTruthy();
});

test("a successful submit includes the pasted image as a base64 InputAttachment", async () => {
  installCanvasStubs();
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  pastePngInto(textarea());
  await waitFor(() => expect(textarea().value).toBe("[image 1]"));
  await waitFor(() => expect(screen.queryByRole("button", { name: /remove/i })).toBeTruthy());

  await user.click(submitButton());
  await waitFor(() => expect(fake.calls.some((c) => c.method === "turn/start")).toBe(true));
  const call = fake.calls.find((c) => c.method === "turn/start");
  const params = call?.params as { input: Array<{ type: string; mediaType?: string; data?: string }> };
  const imageEntry = params.input.find((i) => i.type === "image");
  expect(imageEntry?.mediaType).toBe("image/png");
  expect(typeof imageEntry?.data).toBe("string");
});

test("submitting while an attachment is still mid-encode is blocked with a toast, no request fires", async () => {
  installStalledDecodeStub();
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a");
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  pastePngInto(textarea());
  await waitFor(() => expect(textarea().value).toBe("[image 1]"));
  await user.click(submitButton());

  await waitFor(() => expect(screen.getByText(/still processing/i)).toBeTruthy());
  expect(fake.calls.filter((c) => c.method === "turn/start")).toHaveLength(0);
});

test("pasted image renders as a thumbnail tile with dimensions, remove button, and lightbox on click", async () => {
  installCanvasStubs();
  const user = userEvent.setup();
  await mountComposer("ref_a");

  pastePngInto(textarea(), "screenshot.png");
  await waitFor(() => expect(textarea().value).toBe("[image 1]"));

  // The whole thumbnail is the control that opens the lightbox, and it is
  // named for the file it shows.
  const openButton = await screen.findByRole("button", { name: "View screenshot.png" });
  const thumbnail = openButton.querySelector("img") as HTMLImageElement;
  expect(thumbnail.src).toMatch(/^data:image\/png;base64,/);

  // Assert the dimensions are displayed
  expect(screen.getByText(/4×4/)).toBeTruthy();

  // Assert clicking the thumbnail opens the lightbox (before removing)
  await user.click(openButton);
  const lightboxImg = screen.getByRole("img", { name: "screenshot.png" }) as HTMLImageElement;
  expect(lightboxImg.src).toMatch(/^data:image\/png;base64,/);

  // Close the lightbox by clicking outside (Esc or backdrop)
  const dialogBackdrop = lightboxImg.parentElement?.parentElement;
  if (dialogBackdrop) fireEvent.click(dialogBackdrop);

  // Assert clicking the ✕ removes the attachment
  const removeButton = screen.getByRole("button", { name: /remove screenshot\.png/i });
  await user.click(removeButton);
  await waitFor(() => expect(textarea().value).toBe(""));
});

// kata edhz. The settled tile used to draw its image twice - <ImageGallery>,
// whose own 96px thumbnail button came along with the lightbox it was
// imported for, PLUS a plain 80px <img> for the tile's own cover crop - as
// flex siblings in one 80x80 overflow:hidden box. Measured in a real headless
// Chrome against the real CSS: the gallery thumb rendered at y=-20..78 and
// the plain img at y=78..156 inside a tile spanning 24..104, so the two
// clipped crops met in a visible seam at y=78. Neither was vestigial; they
// were there for different reasons, and the tile now draws its own image and
// opens the shared Dialog itself.
//
// jsdom cannot see the seam (it computes no boxes at all), but it can see the
// cause: two image elements where the design has one. The geometric half is
// scripts/layoutguard/cases/edhz-attachment-tile-single-image.
test("a settled attachment tile draws exactly one image, not a stack of them (kata edhz)", async () => {
  installCanvasStubs();
  await mountComposer("ref_a");

  pastePngInto(textarea(), "screenshot.png");
  await waitFor(() => expect(textarea().value).toBe("[image 1]"));
  const openButton = await screen.findByRole("button", { name: "View screenshot.png" });

  const tile = openButton.parentElement as HTMLElement;
  expect(tile.querySelectorAll("img")).toHaveLength(1);
});

// The fourth decode stub: settles on demand rather than on a microtask
// (installCanvasStubs) or never (installStalledDecodeStub). release()
// resolves only once the whole encode chain - Image.onload, canvas.toBlob,
// Blob.arrayBuffer - has actually delivered its bytes, so a caller can
// await the pending -> settled transition as a real completion instead of
// polling for its side effects.
function installGatedDecodeStub(): { release: () => Promise<void> } {
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

// kata 39xe. A pending attachment and a settled one are the SAME element
// tree at the same list position, differing only in what fills the tile -
// so React updates the remove button rather than unmounting it, and a user
// who has tab-focus on that button when the decode lands keeps it.
//
// This asserts the mechanism (the identical node is still focused), not a
// side effect: an implementation that remounted an identically-labelled
// button would satisfy "a focused remove button exists" while still
// dropping the user's focus. Before the tile was unified, pending rendered
// a <Chip> and settled a <div>, React remounted across that type boundary,
// and this test failed on both of its last two assertions - activeElement
// was <body> and the captured node reported isConnected === false.
test("focus on an attachment's remove button survives its decode settling (kata 39xe)", async () => {
  const gate = installGatedDecodeStub();
  await mountComposer("ref_a");

  act(() => {
    pastePngInto(textarea(), "shot.png");
  });
  const removeButton = screen.getByRole("button", { name: "Remove shot.png" });
  removeButton.focus();
  expect(document.activeElement).toBe(removeButton);

  await act(async () => {
    await gate.release();
  });

  // The transition really happened: the tile now offers the decoded image,
  // so the assertions below are about the settled state, not a decode that
  // quietly never landed.
  expect(screen.getByRole("button", { name: "View shot.png" })).toBeTruthy();
  expect(removeButton.isConnected).toBe(true);
  expect(document.activeElement).toBe(removeButton);
});

// installFailingDecodeStub mirrors installCanvasStubs but rejects (via
// Image.onerror) on a microtask, instead of resolving - so a test gets a
// window between the synchronous marker-insertion and the decode's
// eventual rejection in which to make further synchronous changes (typing)
// before that rejection settles.
function installFailingDecodeStub(): void {
  HTMLCanvasElement.prototype.getContext = (() => ({
    drawImage() {},
  })) as unknown as typeof HTMLCanvasElement.prototype.getContext;
  class FailingImage {
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    private _src = "";
    set src(value: string) {
      this._src = value;
      Promise.resolve().then(() => this.onerror?.());
    }
    get src(): string {
      return this._src;
    }
  }
  // @ts-expect-error stubbing the global Image constructor for this test file only
  globalThis.Image = FailingImage;
  URL.createObjectURL = () => "blob:fake";
  URL.revokeObjectURL = () => {};
}

// CRITICAL regression test (reviewer-reproduced): useAttachments' decode-
// failure path calls the composer's TextEditor.read()/write() long after
// the render that registered it. If read() ever mixes a live cursor with a
// stale per-render `text` closure, write()'s call to the (always-stable)
// setText silently reverts the ENTIRE composer to that stale value -
// discarding both the marker insertion and anything typed since, and
// desyncing the draft (a revert that bypasses writeDraft). Reproduction:
// paste an image whose decode later fails, then type SYNCHRONOUSLY (no
// yield to the microtask queue) before that rejection settles.
test("typing synchronously after a paste whose decode later fails survives - the failed marker alone is stripped (critical)", async () => {
  installFailingDecodeStub();
  await mountComposer("ref_a");

  // act() forces React to flush the paste's resulting state update
  // synchronously (pastePngInto's plain el.dispatchEvent isn't
  // auto-wrapped the way fireEvent/user-event are) - still entirely
  // synchronous JS, so this runs before the decode's microtask-deferred
  // rejection has any chance to fire.
  act(() => {
    pastePngInto(textarea());
  });
  expect(textarea().value).toBe("[image 1]");

  // Also synchronous (fireEvent, not user.type - no per-keystroke delay
  // that could yield to the microtask queue): types "hello" at the
  // (cursor-restored) end of the marker, landing entirely before the
  // decode's rejection settles.
  fireEvent.change(textarea(), { target: { value: "[image 1]hello" } });
  expect(textarea().value).toBe("[image 1]hello");
  expect(localStorage.getItem("serf.composer.draft.v1.ref_a")).toBe("[image 1]hello");

  // Now let the decode's rejection actually settle.
  await waitFor(() => expect(screen.queryByRole("button", { name: /remove/i })).toBeNull());

  expect(textarea().value).toBe("hello"); // typed text survives; only the failed marker is gone
  expect(localStorage.getItem("serf.composer.draft.v1.ref_a")).toBe("hello"); // draft matches, not stale
});

// Minor (reviewer-requested): two attachment gestures fired back-to-back
// with NO intervening render (both dispatched inside one `act()` block, so
// React has no chance to commit the first gesture's setText before the
// second gesture's own TextEditor.read() runs) must still chain correctly
// rather than the second clobbering the first.
test("two attachment gestures fired back-to-back with no intervening render still chain their markers, not clobber", async () => {
  installCanvasStubs();
  await mountComposer("ref_a");

  act(() => {
    pastePngInto(textarea(), "a.png");
    pastePngInto(textarea(), "b.png");
  });

  expect(textarea().value).toBe("[image 1][image 2]");
  await waitFor(() => expect(screen.getAllByRole("button", { name: /remove/i })).toHaveLength(2));
});

// Removing an attachment while its decode is STILL IN FLIGHT, which the
// stalled stub pins for the whole gesture: the marker has to come out of the
// textarea even though there is no decoded image behind it yet, and
// useAttachments has to remember the removal so the decode that eventually
// lands doesn't resurrect it (kata kt4j). Removing a SETTLED attachment is
// the thumbnail/lightbox test above.
test("removing a still-encoding attachment strips its marker from the textarea", async () => {
  installStalledDecodeStub();
  const user = userEvent.setup();
  await mountComposer("ref_a");

  pastePngInto(textarea());
  await waitFor(() => expect(textarea().value).toBe("[image 1]"));

  await user.click(screen.getByRole("button", { name: /remove/i }));
  expect(textarea().value).toBe("");
});

test("picking a file via the hidden input attaches it, same as paste/drop", async () => {
  installCanvasStubs();
  await mountComposer("ref_a");

  // The picker input is `hidden` (only ever reached via the visible Attach
  // button proxying a click to it - see the next test) - user-event's own
  // upload() expects a pointer-interactable target, so this uses RTL's
  // fireEvent.change with a target.files override instead, the documented
  // idiom for a file input's change event.
  const file = new File([new Uint8Array([1, 2, 3])], "picked.png", { type: "image/png" });
  const input = document.querySelector('input[type="file"]') as HTMLInputElement;
  fireEvent.change(input, { target: { files: [file] } });

  await waitFor(() => expect(textarea().value).toBe("[image 1]"));
  expect(screen.getByRole("button", { name: /remove/i })).toBeTruthy();
});

test("clicking the attach button triggers the hidden file input", async () => {
  const user = userEvent.setup();
  await mountComposer("ref_a");

  const input = document.querySelector('input[type="file"]') as HTMLInputElement;
  const clickSpy = vi.spyOn(input, "click");

  await user.click(screen.getByTestId("composer-attach"));
  expect(clickSpy).toHaveBeenCalledTimes(1);
});

// The former "reserves marked slots for T3/T4" test lived here, pinning the
// two slots via source-text scraping (comments aren't queryable via the DOM,
// and there was nothing else there yet to query). Now that the wave
// integration has actually mounted <AskDock>/<QueueStrip> in those slots
// (Composer.integration.test.tsx), a real behavioral DOM-order assertion
// supersedes it - see that file's "the ask dock renders above the queue
// strip when both are visible at once" test.

// --- leading-"/" on an empty composer (product decision, SHOULD-FIX) -------
//
// "/" at the start of an empty composer used to preventDefault() and open
// the MODAL command palette instead of typing (floor §2.1) - which made the
// inline slash menu below unreachable in its single most common case. It is
// now always a literal keystroke, same as any other character: it lands in
// the draft and the inline menu opens off it, exactly like "/" typed
// anywhere else in a non-empty composer. Mod+K (AppShell.tsx) is the one
// remaining way to open the modal palette.

test('"/" at the start of an empty composer types a literal slash and opens the INLINE menu, not the modal palette', async () => {
  useCommandCatalog.setState({ commands: [{ name: "review", description: "review the diff" }], loaded: true });
  const user = userEvent.setup();
  await mountComposer("ref_slash");

  await user.type(textarea(), "/");

  expect(textarea().value).toBe("/");
  expect(paletteStore.getState().open).toBe(false);
  expect(screen.getByTestId("composer-slash-menu")).toBeTruthy();
});

test('"/" in a NON-empty composer is a literal slash, not a palette trigger', async () => {
  const user = userEvent.setup();
  await mountComposer("ref_slash2");
  await user.type(textarea(), "hello");

  fireEvent.keyDown(textarea(), { key: "/" });

  expect(paletteStore.getState().open).toBe(false);
});

// --- inline slash-command completion (Beautiful UI prompt-bar port) --------
//
// "/" is a literal keystroke everywhere in this composer (see the leading-
// "/" tests just above), including on an otherwise-empty composer, so it
// always reaches handleTextChange and the trailing-token parser
// (slashCompletion.ts) the same way. Most cases below still type the slash
// after some other text anyway, purely to also exercise the "mid-draft, not
// just at the very start" shape of that parser.

const REVIEW_RELEASE_CATALOG = [
  { name: "review", description: "review the diff" },
  { name: "release", description: "cut a release" },
];

function slashMenu() {
  return screen.getByTestId("composer-slash-menu");
}

function slashOptions() {
  return within(slashMenu()).getAllByRole("option");
}

test("a trailing slash token opens a completion menu merging session-scoped built-ins with the plugin command catalog", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG, loaded: true });
  const user = userEvent.setup();
  await mountComposer("ref_slash3");

  await user.type(textarea(), "hi /re");

  // "re" matches the built-in /reasoning-effort too (mergeSlashCommands puts
  // built-ins first), not just the two catalog entries.
  expect(slashOptions().map((el) => el.textContent)).toEqual([
    expect.stringContaining("/reasoning-effort"),
    expect.stringContaining("/review"),
    expect.stringContaining("/release"),
  ]);
});

test("typing further narrows the menu live", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG, loaded: true });
  const user = userEvent.setup();
  await mountComposer("ref_slash4");

  await user.type(textarea(), "hi /rev");

  expect(slashOptions().map((el) => el.textContent)).toEqual([expect.stringContaining("/review")]);
});

test("a mid-word slash never opens the menu", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG, loaded: true });
  const user = userEvent.setup();
  await mountComposer("ref_slash5");

  await user.type(textarea(), "foo/bar");

  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
});

test("a token with no catalog match shows no menu", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG, loaded: true });
  const user = userEvent.setup();
  await mountComposer("ref_slash6");

  await user.type(textarea(), "hi /zzz");

  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
});

test("ArrowDown/ArrowUp move the highlighted option and wrap at both ends", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG, loaded: true });
  const user = userEvent.setup();
  await mountComposer("ref_slash7");
  await user.type(textarea(), "hi /re");
  // Three matches: the built-in /reasoning-effort, then /review, /release.

  expect(slashOptions()[0]?.getAttribute("aria-selected")).toBe("true");
  await user.keyboard("{ArrowDown}");
  expect(slashOptions()[1]?.getAttribute("aria-selected")).toBe("true");
  await user.keyboard("{ArrowDown}");
  expect(slashOptions()[2]?.getAttribute("aria-selected")).toBe("true");
  await user.keyboard("{ArrowDown}"); // wraps past the last option back to the first
  expect(slashOptions()[0]?.getAttribute("aria-selected")).toBe("true");
  await user.keyboard("{ArrowUp}"); // wraps the other way, back to the last
  expect(slashOptions()[2]?.getAttribute("aria-selected")).toBe("true");
});

test("Tab commits the highlighted option: splices /name<space> at the token start, caret after the space", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG, loaded: true });
  const user = userEvent.setup();
  await mountComposer("ref_slash8");
  await user.type(textarea(), "hi /re");
  // index 0 is the built-in /reasoning-effort, 1 /review, 2 /release.
  await user.keyboard("{ArrowDown}{ArrowDown}"); // highlight "release"

  await user.keyboard("{Tab}");

  expect(textarea().value).toBe("hi /release ");
  expect(textarea().selectionStart).toBe("hi /release ".length);
  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
  expect(document.activeElement).toBe(textarea());
});

test("committing a plugin-sourced catalog entry inserts the QUALIFIED /plugin:name invocation, not the bare name", async () => {
  // The unqualified "/name" form only resolves to the FIRST plugin
  // registering that name on the hub side (app_rpc.go's dispatch) - see
  // shell/palette/commands.ts's slashCommandInvocation, the single source
  // of truth this insert and the modal palette's own activateCommand both
  // go through. Queries "/rev" rather than "/re" so the built-in
  // /reasoning-effort (which also starts with "re") never enters this
  // single-match scenario.
  useCommandCatalog.setState({
    commands: [{ name: "review", description: "review the diff", source: "plugin", pluginName: "p" }],
    loaded: true,
  });
  const user = userEvent.setup();
  await mountComposer("ref_slash_qualified");
  await user.type(textarea(), "hi /rev");

  await user.keyboard("{Tab}");

  expect(textarea().value).toBe("hi /p:review ");
  expect(textarea().selectionStart).toBe("hi /p:review ".length);
});

test("Enter commits the highlighted option and does NOT fall through to the composer's send routing", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG, loaded: true });
  const user = userEvent.setup();
  const fake = await mountComposer("ref_slash9", { status: { type: "idle" } });
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));
  await user.type(textarea(), "hi /rev"); // single match (review) - avoids the /reasoning-effort built-in collision on "re"

  await user.keyboard("{Enter}");

  expect(textarea().value).toBe("hi /review ");
  expect(fake.calls.filter((c) => c.method === "turn/start")).toHaveLength(0);
});

test("Escape closes the menu without clearing the draft, and typing further reopens it", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG, loaded: true });
  const user = userEvent.setup();
  await mountComposer("ref_slash10");
  await user.type(textarea(), "hi /re");
  expect(screen.queryByTestId("composer-slash-menu")).not.toBeNull();

  await user.keyboard("{Escape}");

  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
  expect(textarea().value).toBe("hi /re"); // draft untouched

  await user.type(textarea(), "v");

  expect(textarea().value).toBe("hi /rev");
  expect(slashOptions().map((el) => el.textContent)).toEqual([expect.stringContaining("/review")]);
});

test("blur closes the menu", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG, loaded: true });
  const user = userEvent.setup();
  await mountComposer("ref_slash11");
  await user.type(textarea(), "hi /re");
  expect(screen.queryByTestId("composer-slash-menu")).not.toBeNull();

  fireEvent.blur(textarea());

  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
});

test("clicking an option commits it without ever blurring the textarea", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG, loaded: true });
  const user = userEvent.setup();
  await mountComposer("ref_slash12");
  await user.type(textarea(), "hi /re");
  // index 0 is the built-in /reasoning-effort, 1 /review, 2 /release.

  await user.click(slashOptions()[2]!); // "release"

  expect(textarea().value).toBe("hi /release ");
  expect(document.activeElement).toBe(textarea());
  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
});

test("the open menu wires listbox/option roles and aria-activedescendant on the textarea", async () => {
  useCommandCatalog.setState({ commands: REVIEW_RELEASE_CATALOG, loaded: true });
  const user = userEvent.setup();
  await mountComposer("ref_slash13");
  await user.type(textarea(), "hi /re");

  expect(slashMenu().getAttribute("role")).toBe("listbox");
  const activeId = textarea().getAttribute("aria-activedescendant");
  expect(activeId).toBeTruthy();
  expect(document.getElementById(activeId ?? "")).toBe(slashOptions()[0]);

  await user.keyboard("{Escape}");
  expect(textarea().getAttribute("aria-activedescendant")).toBeNull();
});

// --- Enter/submit interception: the composer as the session command line
// (2026-08-14 decision, "the composer is where you act on this session") ---
//
// A draft that PARSES as a known BUILT-IN session command runs that
// command's RPC instead of being sent as a chat message - matching Slack/
// Discord muscle memory (decisions.md). Plugin catalog commands and any
// unrecognized "/name" keep sending as plain text: the escape hatch.

test("a built-in invocation (/goal) runs the RPC instead of sending, and clears the draft on success", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_builtin_goal");
  let goalCall: unknown;
  fake.on("goal/set", (params) => {
    goalCall = params;
    return { started: false };
  });

  await user.type(textarea(), "/goal fix the login bug");
  await user.click(submitButton());

  await waitFor(() => expect(textarea().value).toBe(""));
  expect(goalCall).toEqual({ ref: "ref_builtin_goal", objective: "fix the login bug" });
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
  expect(localStorage.getItem("serf.composer.draft.v1.ref_builtin_goal")).toBeNull();
});

test("a successful /goal shows the goal chip immediately - no rehydrate needed (goal/set has no live push)", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_builtin_goal_chip");
  fake.on("goal/set", () => ({ started: true }));

  await user.type(textarea(), "/goal ship the demo");
  await user.click(submitButton());

  // The wire carries no goal-changed notification and the fake never
  // rehydrates the thread, so the chip can only come from the optimistic
  // override the command applies (GoalControl's own module cache).
  await waitFor(() => expect(screen.getByTestId("goal-chip-trigger")).toBeTruthy());
  expect(screen.getByTestId("goal-chip-trigger").textContent).toContain("Goal: active");
});

test("a failed built-in invocation preserves the draft and toasts a friendly message", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_builtin_fail");
  fake.on("goal/set", () => {
    throw new Error("boom");
  });

  await user.type(textarea(), "/goal fix the login bug");
  await user.click(submitButton());

  await waitFor(() => expect(screen.getByText("Something went wrong.")).toBeTruthy());
  expect(textarea().value).toBe("/goal fix the login bug");
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
});

test("an argless built-in invocation (/compact) runs and clears the draft", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_builtin_compact");
  let compactCalled = false;
  fake.on("thread/compact/start", () => {
    compactCalled = true;
    return {};
  });

  await user.type(textarea(), "/compact");
  await user.click(submitButton());

  await waitFor(() => expect(textarea().value).toBe(""));
  expect(compactCalled).toBe(true);
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
});

test("a no-active-turn built-in (/steer) is blocked with the floor's message, draft preserved", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_builtin_steer", { status: { type: "idle" } });

  await user.type(textarea(), "/steer go left");
  await user.click(submitButton());

  await waitFor(() => expect(screen.getByText(/steer failed: no active turn/i)).toBeTruthy());
  expect(textarea().value).toBe("/steer go left");
  expect(fake.calls.filter((call) => call.method === "turn/start")).toEqual([]);
});

test("an unknown /foo sends as a plain message - the escape hatch", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_builtin_unknown", { status: { type: "idle" } });
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  await user.type(textarea(), "/foo bar");
  await user.click(submitButton());

  await waitFor(() => expect(fake.calls.some((call) => call.method === "turn/start")).toBe(true));
  expect(textarea().value).toBe("");
});

test("a plugin catalog command still sends as text - only BUILT-INS are intercepted", async () => {
  useCommandCatalog.setState({
    commands: [{ name: "review", description: "review the diff", source: "plugin" }],
    loaded: true,
  });
  const user = userEvent.setup();
  const fake = await mountComposer("ref_builtin_plugin", { status: { type: "idle" } });
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  await user.type(textarea(), "/review please");
  await user.click(submitButton());

  await waitFor(() => expect(fake.calls.some((call) => call.method === "turn/start")).toBe(true));
  expect(textarea().value).toBe("");
  expect(fake.calls.some((call) => call.method === "goal/set")).toBe(false);
});

test("a message carrying an attachment is never read as a command invocation, even if the text looks like one", async () => {
  installCanvasStubs();
  const user = userEvent.setup();
  const fake = await mountComposer("ref_builtin_attachment", { status: { type: "idle" } });
  fake.on("turn/start", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
    turn: { id: "turn_1", status: "inProgress", itemsView: "" },
  }));

  await user.type(textarea(), "/goal fix it ");
  pastePngInto(textarea());
  await waitFor(() => expect(textarea().value).toBe("/goal fix it [image 1]"));

  await user.click(submitButton());

  await waitFor(() => expect(fake.calls.some((call) => call.method === "turn/start")).toBe(true));
  expect(fake.calls.some((call) => call.method === "goal/set")).toBe(false);
});
