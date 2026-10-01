// Browser-verification harness for the daemon-retirement recovery contract.
//
// Renders the REAL Session pane through a real AppwireClient connecting to a
// fixture Hub (URL from ?hub=). When the fixture Hub's daemon retires, the
// client receives evener/thread/resync, re-reads from the replacement source,
// and the UI must preserve the transcript and the unsent draft. Submitting
// the draft afterwards produces exactly one new turn.
//
// The runner (scripts/retirementguard/run.mjs) passes three URL query params:
//   ?hub=ws://HOST:PORT/rpc  — the fixture Hub WebSocket endpoint
//   ?retire=http://HOST:PORT/fixture/retire — HTTP trigger for daemon retirement
//   ?ref=local:SESSION_ID   — the thread ref to open
//
// window.retirementHarness exposes the test-control surface the runner drives
// over CDP. Values are read from real stores/rendered DOM, never maintained as
// parallel expected state.
import { createRoot } from "react-dom/client";
import { readDraft, writeDraft } from "../panes/session/composer/draft";

// The harness writes a known draft after the thread loads so the
// "draft survives retirement" assertion is non-trivial — it mirrors a user
// who typed a message and then left it unsent while the daemon retired.
const FIXTURE_DRAFT = "retirement harness unsent draft";
// The lost-reply scenario's draft and the turn the replacement produces once
// the replayed mutation is accepted.
const RETRY_DRAFT = "retirement harness retry draft";
const RETRY_TURN_ID = "turn_retirement_retry";

import type { InitializeResponse } from "@evener/appwire-client";
import { APPWIRE_PROTOCOL_VERSION, AppwireClient } from "@evener/appwire-client";
import Session from "../panes/session/Session";
import { ClientProvider } from "../shell/clientContext";
import { connectionStore } from "../stores/connection";
import { readMutationPersistence, subscribeMutationPersistence, threadsStore } from "../stores/threads";
import {
  observeRestartSubmission,
  type RestartMutationSnapshot,
  type RestartSubmissionObserver,
  type RestartSubmissionSnapshot,
} from "./retirementharness-persistence";
import "../styles/tokens.css";
import "../styles/global.css";

// ---- Error collection (runner probes this before asserting) ----------------

window.addEventListener("error", (event) => {
  const target = window as typeof window & { __rhErrors?: string[] };
  target.__rhErrors = [...(target.__rhErrors ?? []), event.error?.stack ?? event.message];
});
window.addEventListener("unhandledrejection", (event) => {
  const target = window as typeof window & { __rhErrors?: string[] };
  target.__rhErrors = [...(target.__rhErrors ?? []), event.reason?.stack ?? String(event.reason)];
});

function pageErrors(): string[] {
  return (window as typeof window & { __rhErrors?: string[] }).__rhErrors ?? [];
}

// ---- URL params ------------------------------------------------------------

const params = new URLSearchParams(window.location.search);
const HUB_WS_URL = params.get("hub") ?? "";
const RETIRE_URL = params.get("retire") ?? "";
const DEGRADE_URL = params.get("degrade") ?? "";
const RESTART_URL = params.get("restart") ?? "";
const RELEASE_URL = params.get("release") ?? "";
const REF = params.get("ref") ?? "local:retirement-harness";
const RESTART_DRAFT = params.get("restartDraft") ?? "";
const RESTART_TURN_ID = params.get("restartTurn") ?? "";

if (!HUB_WS_URL) {
  console.error("retirementharness: missing ?hub= query param");
}

// ---- Client setup ----------------------------------------------------------

const client = new AppwireClient({
  url: HUB_WS_URL,
  clientInfo: { name: "retirementharness", version: "0.0.0-fixture" },
});

// Wire into connectionStore so threadsStore rides it.
connectionStore.getState().connect(client);

// Same-socket recovery evidence: `readyTransitions` counts every transition
// into "ready" (the initial connect plus any real reconnect), and
// `resyncCount` counts the hub's evener/thread/resync notifications. Recovery
// with readyTransitions === 1 and resyncCount >= 1 is recovery the browser
// reached WITHOUT reconnecting — a reconnect alone is not the test.
let readyTransitions = 0;
client.onReady(() => {
  readyTransitions += 1;
});
let resyncCount = 0;
client.onNotification((notification) => {
  if (notification.method === "evener/thread/resync") resyncCount += 1;
});

// ---- Ready promise ---------------------------------------------------------

// readyPromise resolves once the client is ready AND the initial thread/read
// has populated the thread model. The runner awaits this before any assertion.
let resolveReady: (() => void) | undefined;
let rejectReady: ((err: Error) => void) | undefined;
const readyPromise = new Promise<void>((resolve, reject) => {
  resolveReady = resolve;
  rejectReady = reject;
});

let initializeResult: InitializeResponse | null = null;

async function boot() {
  try {
    initializeResult = await client.connect();
    // Populate the thread model through the real thread store.
    await threadsStore.getState().ensureThread(REF);
    // Write the fixture draft so it is present before retirement.
    writeDraft(REF, FIXTURE_DRAFT);
    resolveReady?.();
  } catch (err) {
    rejectReady?.(err instanceof Error ? err : new Error(String(err)));
  }
}

void boot();

// ---- Mount -----------------------------------------------------------------

const rootEl = document.getElementById("root");
if (!rootEl) throw new Error("retirementharness.html is missing #root");

document.documentElement.style.height = "100%";
document.body.style.height = "100%";
document.body.style.margin = "0";

rootEl.style.height = "100%";
rootEl.style.display = "flex";
rootEl.style.flexDirection = "column";

createRoot(rootEl).render(
  <ClientProvider client={client}>
    <div id="retirement-session-pane" style={{ flex: "1 1 0", minHeight: 0, display: "flex", flexDirection: "column" }}>
      <Session params={{ ref: REF }} paneId="retirement-harness" focused />
    </div>
  </ClientProvider>,
);

// ---- window.retirementHarness ---------------------------------------------

interface RetirementHarnessSnapshot {
  ref: string;
  draft: string;
  turnIDs: string[];
  text: string;
  sourceGeneration: string;
}

interface RetirementHarness {
  /** Resolves when the client is ready and the initial thread is hydrated. */
  ready: Promise<void>;
  /** Triggers daemon retirement via the fixture HTTP endpoint. */
  retire(): Promise<void>;
  /**
   * Resolves after the replacement source is bound (a second hydration
   * following the evener/thread/resync the retirement triggers).
   */
  settled(): Promise<void>;
  /** Reads current store/DOM state into a plain snapshot object. */
  snapshot(): RetirementHarnessSnapshot;
  /** Returns any page errors collected since mount. */
  errors(): string[];
  /** Returns the fixture Hub's reported protocol version (sanity probe). */
  protocolVersion(): string;
  /** Diagnostic snapshot used by the runner when `ready` does not settle. */
  debug(): Record<string, unknown>;
  /** Transitions into "ready": 1 means the original socket never reconnected. */
  readyTransitions(): number;
  /** evener/thread/resync notifications the hub delivered on this socket. */
  resyncCount(): number;
  /** Types the retry draft and submits it once; resolves when its turn lands. */
  retryDraft(): Promise<void>;
  /** Makes the replacement advertise unavailable queue/steer/settings. */
  degrade(): Promise<void>;
  /** Types text into the real composer WITHOUT submitting it. */
  typeDraft(text: string): void;
  /** Closes the serving replacement and holds its successor behind a gate. */
  beginReplacementRestart(): Promise<void>;
  /** Submits once and resolves when the exact prompt is durably pending. */
  submitDuringReplacementRestart(): Promise<RestartSubmissionSnapshot>;
  /** Opens the replacement gate and waits for the mutation and turn to settle. */
  releaseReplacement(): Promise<RestartSubmissionSnapshot>;
  /** Reads the exact restart prompt's current durable mutation state. */
  restartSubmission(): Promise<RestartSubmissionSnapshot>;
}

function snapshot(): RetirementHarnessSnapshot {
  const model = threadsStore.getState().threads.get(REF);
  const turnIDs = model?.turns.map((t) => t.id) ?? [];
  const lastTurn = model?.turns[model.turns.length - 1];
  const textItems = (lastTurn?.items ?? []).filter((item) => "text" in item) as Array<{ text?: string }>;
  const lastText = textItems[textItems.length - 1]?.text ?? "";
  const draft = readDraft(REF);
  return {
    ref: REF,
    draft,
    turnIDs,
    text: String(lastText),
    sourceGeneration: model?.instanceId ?? "",
  };
}

function debug(): Record<string, unknown> {
  const state = threadsStore.getState();
  const model = state.threads.get(REF);
  return {
    connection: connectionStore.getState().state,
    hasThread: model !== undefined,
    turnCount: model?.turns.length ?? 0,
    hydrations: Object.fromEntries(state.hydrations),
    deleted: state.deletedRefs.has(REF),
    errors: pageErrors(),
  };
}

// retire() fetches the fixture's safe-retire endpoint. The fixture Hub then
// sends evener/thread/resync to this client, which re-reads the thread from
// the replacement source. retire() resolves once the re-hydration is visible
// in the store (hydrations count bumps), confirming the replacement source
// responded.
// retireThreshold is the hydration watermark the last retirement advanced past.
// settled() consults it so awaiting settlement twice (retire() already waits,
// then the runner awaits settled() as an independent confirmation) cannot wait
// forever for a third hydration that never comes.
let retireThreshold: number | null = null;

function hydrationCount(): number {
  return threadsStore.getState().hydrations.get(REF) ?? 0;
}

// awaitHydrationAbove resolves on the store's own per-ref full-snapshot counter
// advancing past `threshold` — an awaitable store event, not a poll.
function awaitHydrationAbove(threshold: number): Promise<void> {
  return new Promise<void>((resolve) => {
    const check = (state: { hydrations: Map<string, number> }): void => {
      if ((state.hydrations.get(REF) ?? 0) > threshold) {
        unsubscribe();
        resolve();
      }
    };
    const unsubscribe = threadsStore.subscribe(check);
    check(threadsStore.getState());
  });
}

async function retire(): Promise<void> {
  if (!RETIRE_URL) throw new Error("retirementharness: missing ?retire= query param");
  const beforeHydrations = hydrationCount();
  // Ask the fixture to retire the daemon.
  const resp = await fetch(RETIRE_URL, { method: "POST" });
  if (!resp.ok) throw new Error(`retirementharness: retire endpoint returned ${resp.status}`);
  // Wait for the re-hydration to complete.
  await awaitHydrationAbove(beforeHydrations);
  retireThreshold = beforeHydrations;
}

// settled() resolves once the replacement source is bound and its read has
// published. It is idempotent: when retire() already advanced past its
// threshold, a second call returns immediately instead of waiting for a
// further hydration.
async function settled(): Promise<void> {
  if (retireThreshold !== null && hydrationCount() > retireThreshold) return;
  const threshold = hydrationCount();
  await awaitHydrationAbove(threshold);
  retireThreshold = threshold;
}

// awaitTurn resolves when a turn with this id is published in the model — the
// authoritative completion the replacement's turn/completed notification
// produces, not a poll.
function awaitTurn(turnID: string): Promise<void> {
  return new Promise<void>((resolve) => {
    const check = (state: { threads: Map<string, { turns: Array<{ id: string }> }> }): void => {
      const model = state.threads.get(REF);
      if (model?.turns.some((turn) => turn.id === turnID)) {
        unsubscribe();
        resolve();
      }
    };
    const unsubscribe = threadsStore.subscribe(check);
    check(threadsStore.getState());
  });
}

// enterIntoComposer replaces the composer's contents the way a user would:
// focus it, select everything, then paste. The composer is a ProseMirror
// editor, not a textarea, so the native value setter belongs to the wrong
// element type and throws instead of entering anything.
function enterIntoComposer(text: string): void {
  const composer = document.querySelector<HTMLElement>('[role="textbox"]');
  if (!composer) throw new Error("retirementharness: composer textbox not found");
  composer.focus();
  const selection = window.getSelection();
  if (selection) {
    const range = document.createRange();
    range.selectNodeContents(composer);
    selection.removeAllRanges();
    selection.addRange(range);
  }
  const clipboard = new DataTransfer();
  clipboard.setData("text/plain", text);
  composer.dispatchEvent(new ClipboardEvent("paste", { clipboardData: clipboard, bubbles: true, cancelable: true }));
}

async function submitComposer(text: string, disabledError: string): Promise<void> {
  enterIntoComposer(text);
  const sendBtn = [...document.querySelectorAll("button")].find((button) => button.textContent?.trim() === "Send");
  if (!sendBtn) throw new Error("retirementharness: Send button not found");
  if (sendBtn.disabled) {
    await new Promise<void>((resolve, reject) => {
      let timer = 0;
      let observer: MutationObserver;
      const finish = (error?: Error): void => {
        observer.disconnect();
        window.clearTimeout(timer);
        if (error) reject(error);
        else resolve();
      };
      observer = new MutationObserver(() => {
        if (!sendBtn.disabled) finish();
      });
      timer = window.setTimeout(() => finish(new Error(disabledError)), 15000);
      observer.observe(sendBtn, { attributes: true, attributeFilter: ["disabled"] });
      if (!sendBtn.disabled) finish();
    });
  }
  sendBtn.click();
}

// retryDraft drives the real composer a second time after retirement: enter
// the retry draft, click Send, and await the replacement's accepted turn. The
// source drops the first attempt's reply, so the turn only appears if the
// client replayed the same mutation id.
async function retryDraft(): Promise<void> {
  await submitComposer(RETRY_DRAFT, "retirementharness: Send stayed disabled before the retry");
  await awaitTurn(RETRY_TURN_ID);
}

// degrade asks the fixture to withdraw queue/steer/settings on the replacement
// and waits for the re-read that publishes the new capability set.
async function degrade(): Promise<void> {
  if (!DEGRADE_URL) throw new Error("retirementharness: missing ?degrade= query param");
  const before = hydrationCount();
  const resp = await fetch(DEGRADE_URL, { method: "POST" });
  if (!resp.ok) throw new Error(`retirementharness: degrade endpoint returned ${resp.status}`);
  await awaitHydrationAbove(before);
  retireThreshold = before;
}

// typeDraft enters text through the real composer without submitting. Used to
// prove unsent input is retained, and never auto-submitted, when
// queue/steer/settings are unavailable.
function typeDraft(text: string): void {
  enterIntoComposer(text);
}

async function beginReplacementRestart(): Promise<void> {
  if (!RESTART_URL) throw new Error("retirementharness: missing ?restart= query param");
  if (!RESTART_DRAFT || !RESTART_TURN_ID) throw new Error("retirementharness: missing restart fixture identifiers");
  retireThreshold = hydrationCount();
  const resp = await fetch(RESTART_URL, { method: "POST" });
  if (!resp.ok) throw new Error(`retirementharness: restart endpoint returned ${resp.status}`);
}

function restartRecord(record: {
  clientMutationId: string;
  method: string;
  state: string;
  composerText?: string;
}): RestartMutationSnapshot {
  return {
    clientMutationId: record.clientMutationId,
    method: record.method,
    state: record.state,
    composerText: record.composerText ?? "",
  };
}

async function restartSubmission(): Promise<RestartSubmissionSnapshot> {
  const snapshot = await readMutationPersistence(REF);
  const matches = (record: { composerText?: string }): boolean => record.composerText === RESTART_DRAFT;
  return {
    outbox: snapshot.outbox.filter(matches).map(restartRecord),
    recovery: snapshot.recovery.filter(matches).map(restartRecord),
  };
}

let restartObserver: RestartSubmissionObserver | undefined;

async function submitDuringReplacementRestart(): Promise<RestartSubmissionSnapshot> {
  restartObserver = observeRestartSubmission({
    read: restartSubmission,
    subscribe: (listener) =>
      subscribeMutationPersistence((refs) => {
        if (refs.includes(REF)) listener();
      }),
    timeoutMs: 15000,
  });
  // Keep an early rejection handled while the runner examines the pending
  // snapshot; releaseReplacement still awaits the original promise below.
  void restartObserver.settled.catch(() => {});
  await submitComposer(RESTART_DRAFT, "retirementharness: Send stayed disabled before restart-gated submission");
  return restartObserver.pending;
}

async function releaseReplacement(): Promise<RestartSubmissionSnapshot> {
  if (!RELEASE_URL) throw new Error("retirementharness: missing ?release= query param");
  if (!restartObserver) throw new Error("retirementharness: restart submission was not observed");
  const threshold = retireThreshold ?? hydrationCount();
  const resp = await fetch(RELEASE_URL, { method: "POST" });
  if (!resp.ok) throw new Error(`retirementharness: release endpoint returned ${resp.status}`);
  const [, , durable] = await Promise.all([
    awaitHydrationAbove(threshold),
    awaitTurn(RESTART_TURN_ID),
    restartObserver.settled,
  ]);
  return durable;
}

declare global {
  interface Window {
    retirementHarness: RetirementHarness;
  }
}

window.retirementHarness = {
  ready: readyPromise,
  retire,
  settled: () => settled(),
  snapshot,
  errors: pageErrors,
  protocolVersion: () => initializeResult?.protocolVersion ?? APPWIRE_PROTOCOL_VERSION,
  debug,
  readyTransitions: () => readyTransitions,
  resyncCount: () => resyncCount,
  retryDraft,
  degrade,
  typeDraft,
  beginReplacementRestart,
  submitDuringReplacementRestart,
  releaseReplacement,
  restartSubmission,
};
