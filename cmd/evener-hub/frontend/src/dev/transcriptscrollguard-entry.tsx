// Browser-verification harness for the session transcript's jump-to-latest
// pill (NewContentPill + useTranscriptScroll's jumpToBottom, PR #851).
//
// The bug this harness exists to catch: jumpToBottom used to trust the
// virtualizer's estimate-derived landing. With dynamic rows whose measured
// heights far exceed the 96px estimate (TranscriptBody's
// ESTIMATED_TURN_HEIGHT), scrollToIndex(count-1, {align:"end"}) can settle
// short of the true DOM bottom - and whether a correction arrives afterward
// is timing-dependent (the reconcile loop settles after one stable frame,
// ResizeObserver delivery is async). The fix pins el.scrollTop to
// scrollHeight - clientHeight from LIVE geometry right after engaging the
// virtualizer. jsdom cannot see any of this (no layout, no ResizeObserver,
// no native scroll events - kata tzqz), so the only honest reproduction is a
// real browser running the real Session pane.
//
// Renders the REAL Session (transcript VirtualList, NewContentPill,
// useTranscriptScroll, the lot) against a FakeClient with a scripted thread
// whose turn heights are deliberately divergent, then exposes the geometry
// probes scripts/transcriptscrollguard/run.mjs drives over CDP. The scenario
// mirrors production: scroll away (a REAL scrollTop assignment - the browser
// dispatches the native scroll event that reveals the pill), new large turns
// arrive while away (never rendered, so the virtualizer holds only estimates
// for them), then the pill is clicked and the landing must settle at the
// true bottom, with the pill cleared by the landing's own native scroll
// event.
import { createRoot } from "react-dom/client";
import Session from "../panes/session/Session";
import { FakeClient } from "../protocol/testing/fakeClient";
import type { AnyNotification, Thread, ThreadCapabilities, ThreadReadResponse, Turn } from "../protocol/types.gen";
import { ClientProvider } from "../shell/clientContext";
import { connectionStore } from "../stores/connection";
import { threadsStore } from "../stores/threads";
import { Toast } from "../widgets";
import "../styles/tokens.css";
import "../styles/global.css";

window.addEventListener("error", (event) => {
  const target = window as typeof window & { __tsgErrors?: string[] };
  target.__tsgErrors = [...(target.__tsgErrors ?? []), event.error?.stack ?? event.message];
});
window.addEventListener("unhandledrejection", (event) => {
  const target = window as typeof window & { __tsgErrors?: string[] };
  target.__tsgErrors = [...(target.__tsgErrors ?? []), event.reason?.stack ?? String(event.reason)];
});

function pageErrors(): string[] {
  return (window as typeof window & { __tsgErrors?: string[] }).__tsgErrors ?? [];
}

function throwOnPageErrors(context: string): void {
  const errors = pageErrors();
  if (errors.length > 0) throw new Error(`transcriptscrollguard page errors during ${context}: ${errors.join("\n")}`);
}

const REF = "local:transcriptscrollguard";
const THREAD_ID = "thr_transcriptscrollguard";

// Deterministic PRNG (mulberry32): the transcript's row heights must be
// variable AND identical on every run - a guard whose geometry depends on
// Math.random() cannot be bisected or reproduced.
function mulberry32(seed: number): () => number {
  let state = seed;
  return () => {
    state = (state + 0x6d2b79f5) | 0;
    let t = Math.imul(state ^ (state >>> 15), 1 | state);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}
const rand = mulberry32(0x5eed);

const WORDS = [
  "transcript",
  "scroll",
  "landing",
  "estimate",
  "measure",
  "viewport",
  "virtualizer",
  "correction",
  "anchor",
  "reader",
  "bottom",
  "pill",
  "native",
  "event",
  "geometry",
  "settle",
];

function sentence(wordCount: number, tag: string): string {
  const words: string[] = [];
  for (let i = 0; i < wordCount; i++) words.push(WORDS[Math.floor(rand() * WORDS.length)] ?? "jump");
  return `${tag}: ${words.join(" ")}.`;
}

function paragraphs(count: number, tag: string): string {
  const blocks: string[] = [];
  for (let i = 0; i < count; i++) blocks.push(sentence(9 + Math.floor(rand() * 14), `${tag} p${i}`));
  return blocks.join("\n\n");
}

function wireTurn(id: string, userText: string, agentText: string): Turn {
  return {
    id,
    itemsView: "full",
    status: "completed",
    items: [
      { type: "userMessage", id: `${id}_user`, turnId: id, text: userText, status: "completed" },
      { type: "agentMessage", id: `${id}_agent`, turnId: id, text: agentText, status: "completed" },
    ],
  };
}

// 42 turns, heights deliberately divergent: most turns are short (1-3
// paragraphs, ~100-250px rendered), every sixth turn carries 14-28
// paragraphs (~700-1500px) - against the virtualizer's flat 96px estimate,
// every row measures differently from its estimate in both directions of
// error, which is what makes an estimate-derived landing land short.
const INITIAL_TURN_COUNT = 42;
const initialTurns: Turn[] = Array.from({ length: INITIAL_TURN_COUNT }, (_, i) => {
  const id = `turn_${i + 1}`;
  const agentParagraphs = i % 6 === 5 ? 14 + Math.floor(rand() * 15) : 1 + Math.floor(rand() * 3);
  return wireTurn(
    id,
    sentence(4 + Math.floor(rand() * 8), `turn ${i + 1} ask`),
    paragraphs(agentParagraphs, `turn ${i + 1}`),
  );
});

// Turns appended WHILE the reader is scrolled away: never rendered, so at
// click time the virtualizer holds only the 96px estimate for them while
// their real rendered height is ~1-2 viewport heights each. This is the
// exact production shape the fix targets (new content arrives, pill shows
// "N new", the jump must land on the true bottom).
const APPEND_TURN_COUNT = 3;
const APPEND_PARAGRAPHS = 26;

const CAPABILITIES: ThreadCapabilities = {
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

const THREAD: Thread = {
  id: THREAD_ID,
  sessionId: "sess_transcriptscrollguard",
  preview: "transcript scroll guard",
  ephemeral: false,
  modelProvider: "anthropic/claude-sonnet-4-5",
  createdAt: 1000,
  updatedAt: 1000,
  status: { type: "idle" },
  cwd: "/tmp/project",
  cliVersion: "1.0.0",
  source: "evener",
  evener: { ref: REF, capabilities: CAPABILITIES, queue: { revision: 0 } },
  turns: initialTurns,
};

const fake = new FakeClient("ready");
fake.on("thread/read", () => ({ thread: THREAD }) satisfies ThreadReadResponse);
// SessionChrome/Composer idle-time reads; scripted so nothing rejects into an
// unhandledrejection and pollutes the page-error probe.
fake.on("evener/tasks/list", () => ({ data: [] }));
fake.on("model/list", () => ({ data: [{ provider: "anthropic", model: "claude-sonnet-4-5" }] }));
connectionStore.getState().connect(fake);

// The model's turn count, tracked store-side so appendLargeTurns() can prove
// the append actually landed in the model (not just that a notification was
// emitted) before the guard clicks the pill.
let modelTurnCount = 0;
threadsStore.subscribe((state) => {
  const model = state.threads.get(REF);
  if (model) modelTurnCount = model.turns.length;
});

const rootEl = document.getElementById("root");
if (!rootEl) throw new Error("transcriptscrollguard.html is missing #root");

document.documentElement.style.height = "100%";
document.body.style.height = "100%";
document.body.style.margin = "0";
document.body.style.background = "var(--surface-0)";
rootEl.style.height = "100%";

createRoot(rootEl).render(
  <ClientProvider client={fake}>
    <div id="transcriptscrollguard-pane" style={{ height: "100%" }}>
      <Session params={{ ref: REF }} paneId="transcriptscrollguard" focused />
    </div>
    <Toast />
  </ClientProvider>,
);

// --- probes the runner drives over CDP -----------------------------------

// The transcript's scroll container: TranscriptBody's
// [data-testid="transcript-virtual-list"] section directly wraps the
// VirtualList root (the one overflow-y:auto node the whole flow hangs off).
function scrollElement(): HTMLElement {
  const section = document.querySelector('[data-testid="transcript-virtual-list"]');
  const el = section?.querySelector(":scope > div");
  if (!(el instanceof HTMLElement)) throw new Error("transcript VirtualList scroll element is not mounted");
  return el;
}

function pillElement(): HTMLElement | null {
  return document.querySelector('[data-testid="new-content-pill"]');
}

interface TranscriptScrollMetrics {
  scrollTop: number;
  scrollHeight: number;
  clientHeight: number;
  /** Bottom gap: scrollHeight - clientHeight - scrollTop. 0 at the true bottom. */
  bottomGap: number;
  pill: boolean;
  pillText: string | null;
  turns: number;
  renderedRows: number;
  errors: string[];
}

function metrics(): TranscriptScrollMetrics {
  const el = scrollElement();
  const pill = pillElement();
  return {
    scrollTop: el.scrollTop,
    scrollHeight: el.scrollHeight,
    clientHeight: el.clientHeight,
    bottomGap: el.scrollHeight - el.clientHeight - el.scrollTop,
    pill: pill !== null,
    pillText: pill?.textContent ?? null,
    turns: modelTurnCount,
    renderedRows: document.querySelectorAll('[data-testid="transcript-row"]').length,
    errors: pageErrors(),
  };
}

function nextFrame(): Promise<void> {
  return new Promise((resolve) => requestAnimationFrame(() => resolve()));
}

// Settle condition for the initial mount: the full scripted thread is in the
// model, the scroll element exists, the transcript overflows the viewport by
// a wide margin, AND the mount has gone QUIESCENT - geometry unchanged for
// SETTLE_QUIESCENT_FRAMES consecutive frames with the reader at the bottom
// and the pill hidden. The quiescence half is load-bearing: the mount's own
// scrollToIndex(...) reconcile loop stays active while post-mount
// measurement corrections keep moving its target, and while it is active it
// claims ANY scroll-offset change (including the guard's scroll-away) as
// part of its programmatic scroll and yanks back - a spurious failure the
// runner cannot tell from a product regression. Waiting it out here makes
// the phases downstream deterministic. The runner asserts the overflow
// margin itself; here it is only the readiness signal, so a harness that
// rendered nothing fails HERE (naming the harness) instead of as a geometry
// mystery downstream (docs/developing-evener/testing.md's
// unfalsifiable-fixture trap).
const SETTLE_QUIESCENT_FRAMES = 20;
// Callable, not a module-load one-shot: the runner awaits webfonts FIRST
// (waitForFonts over CDP) and only then calls this, because a late-arriving
// font changes row geometry after load - settling must measure the
// post-font geometry, or a font-driven shift could surface the pill before
// the scroll-away phase and make the runner's pill assertions pass for the
// wrong reason.
async function waitForTranscriptSettled(): Promise<TranscriptScrollMetrics> {
  const deadline = performance.now() + 15_000;
  let quiescentFrames = 0;
  let lastScrollHeight = -1;
  let lastScrollTop = -1;
  for (;;) {
    await nextFrame();
    throwOnPageErrors("initial render");
    const section = document.querySelector('[data-testid="transcript-virtual-list"]');
    const el = section?.querySelector(":scope > div");
    if (el instanceof HTMLElement && modelTurnCount === INITIAL_TURN_COUNT) {
      const overflowed = el.scrollHeight > el.clientHeight * 4;
      const atBottom = el.scrollHeight - el.clientHeight - el.scrollTop <= 1;
      const still = el.scrollHeight === lastScrollHeight && el.scrollTop === lastScrollTop;
      if (overflowed && atBottom && still && pillElement() === null) quiescentFrames++;
      else quiescentFrames = 0;
      if (quiescentFrames >= SETTLE_QUIESCENT_FRAMES) return metrics();
      lastScrollHeight = el.scrollHeight;
      lastScrollTop = el.scrollTop;
    }
    if (performance.now() > deadline) {
      throw new Error(
        `transcript harness: transcript never settled into an overflow within 15s ` +
          `(turns=${modelTurnCount}/${INITIAL_TURN_COUNT}, section=${section !== null}, ` +
          `quiescentFrames=${quiescentFrames}, ` +
          `body children: ${[...document.body.children].map((el) => el.tagName).join(",")})`,
      );
    }
  }
}

// Scrolls away from the bottom by a REAL scroll - assigning scrollTop lets
// the browser dispatch the native scroll event itself, which is the event
// useTranscriptScroll's listener must answer by revealing the pill. A
// synthetic dispatchEvent would prove nothing about that wiring.
//
// The assignment is RE-ASSERTED every frame until it holds: if the mount's
// scrollToIndex reconcile loop is somehow still active (or a late correction
// re-engages it), it claims the offset change and yanks back toward its
// target - re-asserting wins as soon as the loop settles, and requiring
// SCROLL_AWAY_HELD_FRAMES consecutive frames with the pill visible AND the
// offset held away proves downstream phases (appends, the pill click) run
// with no yank still in flight.
const SCROLL_AWAY_HELD_FRAMES = 6;
async function scrollAwayAndWaitForPill(): Promise<TranscriptScrollMetrics> {
  const el = scrollElement();
  const deadline = performance.now() + 10_000;
  let heldFrames = 0;
  for (;;) {
    el.scrollTop = 0;
    await nextFrame();
    throwOnPageErrors("scroll away");
    const away = el.scrollTop < el.scrollHeight - el.clientHeight - 4;
    if (pillElement() !== null && away) heldFrames++;
    else heldFrames = 0;
    if (heldFrames >= SCROLL_AWAY_HELD_FRAMES) return metrics();
    if (performance.now() > deadline) {
      throw new Error(
        `transcript harness: pill never appeared and held after a real scroll to the top (10s); ${JSON.stringify(metrics())}`,
      );
    }
  }
}

// Appends APPEND_TURN_COUNT large turns through the REAL notification path
// (turn/started + turn/completed, the same frames the live wire sends) while
// the reader is scrolled away. Resolves once the model and the DOM both
// carry the appends.
async function appendLargeTurns(): Promise<TranscriptScrollMetrics> {
  const before = modelTurnCount;
  const beforeScrollHeight = scrollElement().scrollHeight;
  for (let k = 0; k < APPEND_TURN_COUNT; k++) {
    const id = `turn_append_${k + 1}`;
    const turn = wireTurn(
      id,
      sentence(6, `appended turn ${k + 1} ask`),
      paragraphs(APPEND_PARAGRAPHS, `appended turn ${k + 1}`),
    );
    fake.emitNotification({
      method: "turn/started",
      params: { threadId: THREAD_ID, ref: REF, turn },
    } as AnyNotification);
    fake.emitNotification({
      method: "turn/completed",
      params: { threadId: THREAD_ID, ref: REF, turnId: id, turn: { id, itemsView: "", status: "completed" } },
    } as AnyNotification);
  }
  const deadline = performance.now() + 8_000;
  for (;;) {
    await nextFrame();
    throwOnPageErrors("appending turns");
    if (modelTurnCount === before + APPEND_TURN_COUNT && scrollElement().scrollHeight > beforeScrollHeight) {
      return metrics();
    }
    if (performance.now() > deadline) {
      throw new Error(`transcript harness: appended turns never landed (8s); ${JSON.stringify(metrics())}`);
    }
  }
}

// Clicks the pill (a real .click() on the rendered button, invoking
// Session's own onClick -> useTranscriptScroll's jumpToBottom) and waits out
// the settle: native scroll events plus the virtualizer's post-jump
// measurement corrections. "Settled" requires the pill GONE and the scroll
// offset pinned at the true bottom (bottomGap within 1px) to hold for
// SETTLED_FRAMES consecutive frames - a landing that a late correction
// leaves short flips the condition back off and is reported, not timed out
// silently.
const SETTLED_FRAMES = 30;
async function clickPillAndSettle(): Promise<
  { settled: boolean; tail: TranscriptScrollMetrics[] } & TranscriptScrollMetrics
> {
  const pill = pillElement();
  if (!pill) throw new Error("transcript harness: no pill to click");
  pill.click();
  const deadline = performance.now() + 10_000;
  let stableFrames = 0;
  const tail: TranscriptScrollMetrics[] = [];
  for (;;) {
    await nextFrame();
    throwOnPageErrors("jump to bottom");
    const m = metrics();
    tail.push(m);
    if (tail.length > SETTLED_FRAMES) tail.shift();
    if (!m.pill && Math.abs(m.bottomGap) <= 1) stableFrames++;
    else stableFrames = 0;
    if (stableFrames >= SETTLED_FRAMES) return { settled: true, tail, ...m };
    if (performance.now() > deadline) return { settled: false, tail, ...m };
  }
}

declare global {
  interface Window {
    waitForTranscriptSettled: typeof waitForTranscriptSettled;
    transcriptScrollMetrics: typeof metrics;
    scrollAwayAndWaitForPill: typeof scrollAwayAndWaitForPill;
    appendLargeTurns: typeof appendLargeTurns;
    clickPillAndSettle: typeof clickPillAndSettle;
  }
}

window.waitForTranscriptSettled = waitForTranscriptSettled;
window.transcriptScrollMetrics = metrics;
window.scrollAwayAndWaitForPill = scrollAwayAndWaitForPill;
window.appendLargeTurns = appendLargeTurns;
window.clickPillAndSettle = clickPillAndSettle;
