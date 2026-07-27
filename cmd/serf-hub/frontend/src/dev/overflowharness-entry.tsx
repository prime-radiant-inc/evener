// Browser-verification harness for the transcript's horizontal overflow.
//
// The bug it exists to find: a transcript pane containing a delegation
// module scrolls sideways, clipping the left edge of every line above it.
// jsdom cannot see this - it evaluates no cascade and reports zero for every
// box (kata tzqz) - so the only honest reproduction is a real browser
// measuring real boxes against the real cascade.
//
// Renders the REAL Session pane (not a hand-authored stand-in) at a width
// taken from ?w=, seeded through the REAL reducer so the ThreadModel it
// measures is the one hydrateThread would build from a real wire snapshot.
//
// window.measure() returns, for every scroll container under the pane, how
// far its content escapes its own content box, plus the deepest elements
// responsible - which is the answer the fix has to be aimed at.
import { createRoot } from "react-dom/client";
import Session from "../panes/session/Session";
import { hydrateThread } from "../protocol/reducer";
import { FakeClient } from "../protocol/testing/fakeClient";
import type { ThreadCapabilities, ThreadReadResponse } from "../protocol/types.gen";
import { connectionStore } from "../stores/connection";
import { threadsStore } from "../stores/threads";
import "../styles/tokens.css";
import "../styles/global.css";

const params = new URLSearchParams(window.location.search);
const width = Number(params.get("w") ?? "1400");
const theme = params.get("theme");
if (theme === "light" || theme === "dark") document.documentElement.dataset.theme = theme;

const REF = "overflowharness";

const CAPABILITIES: ThreadCapabilities = {
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

// The exact delegation the screenshot was taken from: a long single-sentence
// mandate, a mono meta cluster, and a job-completed notification carrying an
// absolute path. Long content is the point - a short task would not reproduce.
const TASK =
  "Please test delegation by inspecting the current working directory. " +
  "Report the directory and three notable entries. Do not change any files.";

const SYSTEM_PROMPT =
  "## Identity\n\nYou are a careful assistant working inside a transcript UI.\n\n" +
  "## Working agreement\n\n" +
  "Use the available context precisely, explain decisions plainly, and keep useful output readable at narrow widths.\n\n".repeat(
    233,
  );

const RAW_UNBROKEN_PAYLOAD = "R".repeat(12_000);

const snapshot: ThreadReadResponse = {
  thread: {
    id: "thr_overflow",
    sessionId: "sess_overflow",
    name: "List Current Working Directory",
    preview: "delegation",
    ephemeral: false,
    modelProvider: "openai/gpt-5.6-luna",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "idle" },
    cwd: "/Users/jesse/prime-radiant/toil-suite/serf",
    cliVersion: "1.0.0",
    source: "serf",
    serf: { ref: REF, capabilities: CAPABILITIES, queue: {}, cost: "~$0.06" },
    turns: [
      {
        id: "turn_1",
        itemsView: "full",
        status: "completed",
        durationMs: 27000,
        items: [
          { type: "userMessage", id: "i1", turnId: "turn_1", text: "Can you test out delegation?" },
          // "reasoning", not "thinking" — the registered item types are
          // agentMessage / reasoning / steering / systemMessage / userMessage /
          // warning (grep registerItemRenderer). An unregistered type silently
          // falls through to RawItemView, the debug fallback, which renders the
          // type name in uppercase mono and exercises none of the real
          // component. A harness that measures the fallback is measuring
          // nothing the app ships.
          { type: "reasoning", id: "i2", turnId: "turn_1", text: "Testing delegation" },
          {
            type: "commandExecution",
            id: "i3",
            turnId: "turn_1",
            toolName: "delegate",
            callId: "call_1",
            status: "completed",
            durationMs: 1500,
            argumentsJson: JSON.stringify({ task: TASK, mode: "foreground_timeout", delegateId: "dlg_1" }),
          },
          {
            type: "agentMessage",
            id: "i4",
            turnId: "turn_1",
            text: "Delegation test started. I asked a delegate to inspect the current directory and report back without changing files.",
          },
          {
            type: "systemMessage",
            id: "i5",
            turnId: "turn_1",
            text: "Directory: /Users/jesse/prime-radiant/toil-suite/serf Three notable entries: README.md, go.mod, agent/",
            eventKind: "compaction",
          },
          {
            type: "systemMessage",
            id: "i6",
            turnId: "turn_1",
            text: SYSTEM_PROMPT,
            eventKind: "system_prompt",
          },
          {
            type: "steering",
            id: "i7",
            turnId: "turn_1",
            steeringKind: "notification",
            text: `<job-notification job_id="job_overflow" event="completed" job_type="delegate" status="completed" output_bytes="4" transcript_ref="local:child">
Job job_overflow completed.
excerpt:
${RAW_UNBROKEN_PAYLOAD}
</job-notification>`,
          },
        ],
      },
    ],
  },
};

const fake = new FakeClient("ready");
fake.on("thread/read", () => snapshot);
fake.on("serf/tasks/list", () => ({ data: [] }));
connectionStore.getState().connect(fake);
threadsStore.setState((s) => ({
  threads: new Map(s.threads).set(REF, hydrateThread(snapshot, REF, 1000)),
}));

const rootEl = document.getElementById("root");
if (!rootEl) throw new Error("overflowharness.html is missing #root");

document.body.style.margin = "0";
document.body.style.background = "var(--surface-0)";

createRoot(rootEl).render(
  <div id="oh-pane" style={{ width, height: 900, padding: 8 }}>
    <Session params={{ ref: REF }} paneId="oh" focused />
  </div>,
);

interface Escapee {
  tag: string;
  cls: string;
  overflowPx: number;
  depth: number;
}
interface Scroller {
  tag: string;
  cls: string;
  scrollWidth: number;
  clientWidth: number;
  overflowPx: number;
  escapees: Escapee[];
}

interface DisclosureContract {
  kind: string;
  originalOpen: boolean;
  openDuringOverflowScan: boolean;
  restoredOpen: boolean;
  summaryDisplay: string;
  markerDisplay: string;
  summaryWidth: number;
  bodyWidth: number;
  bodyTextLength: number;
  summaryLeft: number;
  bodyLeft: number;
  bodyTop: number;
  summaryBottom: number;
  expectedWidth: number;
}

interface DisclosureTarget {
  kind: string;
  details: HTMLDetailsElement;
  originalOpen: boolean;
}

// A scroll container is horizontally overflowing when its content is wider
// than its own content box. Reporting the container alone is not actionable,
// so each one also names the deepest descendants whose right edge is past its
// content edge - those, not the container, are what has to be fixed.
//
// Two exclusions, both reported rather than dropped silently.
//
// `overflow-x: hidden` / `clip` is the big one, and getting it wrong inverts
// this guard's meaning. `scrollWidth > clientWidth` is true for ANY element
// whose content exceeds its box, including one deliberately clipping with
// `text-overflow: ellipsis` — which is the recommended FIX for overflow, not
// an instance of it. Only `auto` and `scroll` put a scrollbar under the
// reader's finger, so only they are findings. (This is also exactly why the
// bug that prompted this harness was possible: `overflow-y: auto` with no
// overflow-x declared computes overflow-x to `auto`, not `visible`, so those
// containers ARE scrollable and do get measured.)
//
// The second: the standard visually-hidden recipe (`width: 1px; overflow:
// hidden`) is a 1px box on every page that has one, and is not a pane anyone
// can see or scroll.
function disclosureContract(target: DisclosureTarget): DisclosureContract | null {
  const { kind, details, originalOpen } = target;
  const summary = details.firstElementChild;
  const body = details.children[1];
  if (!(summary instanceof HTMLElement) || !(body instanceof HTMLElement) || summary.tagName !== "SUMMARY") return null;

  const summaryBox = summary.getBoundingClientRect();
  const bodyBox = body.getBoundingClientRect();
  const detailsStyle = getComputedStyle(details);
  const expectedWidth =
    details.clientWidth - Number.parseFloat(detailsStyle.paddingLeft) - Number.parseFloat(detailsStyle.paddingRight);
  const result = {
    kind,
    originalOpen,
    openDuringOverflowScan: originalOpen,
    restoredOpen: originalOpen,
    summaryDisplay: getComputedStyle(summary).display,
    markerDisplay: getComputedStyle(summary, "::marker").display,
    summaryWidth: summaryBox.width,
    bodyWidth: bodyBox.width,
    bodyTextLength: body.textContent?.length ?? 0,
    summaryLeft: summaryBox.left,
    bodyLeft: bodyBox.left,
    bodyTop: bodyBox.top,
    summaryBottom: summaryBox.bottom,
    expectedWidth,
  };
  return result;
}

function measure(): { width: number; scrollers: Scroller[]; ignored: string[]; disclosures: DisclosureContract[] } {
  const pane = document.getElementById("oh-pane");
  if (!pane) throw new Error("harness pane never mounted");
  const scrollers: Scroller[] = [];
  const ignored: string[] = [];
  const disclosureTargets: DisclosureTarget[] = [];
  let disclosures: DisclosureContract[] = [];

  try {
    const systemPrompt = Array.from(
      pane.querySelectorAll<HTMLDetailsElement>('[data-testid="system-notice-scaffold"]'),
    ).find((details) => details.querySelector(":scope > summary")?.textContent?.startsWith("System prompt"));
    if (systemPrompt)
      disclosureTargets.push({ kind: "system-prompt", details: systemPrompt, originalOpen: systemPrompt.open });

    const rawNotification = pane.querySelector<HTMLDetailsElement>('[data-testid="notification-raw-disclosure"]');
    if (rawNotification) {
      disclosureTargets.push({
        kind: "raw-notification",
        details: rawNotification,
        originalOpen: rawNotification.open,
      });
    }

    for (const target of disclosureTargets) target.details.open = true;
    disclosures = disclosureTargets.flatMap((target) => {
      const measured = disclosureContract(target);
      return measured ? [measured] : [];
    });

    const openDuringOverflowScan =
      disclosureTargets.length === 2 && disclosureTargets.every((target) => target.details.open);
    for (const disclosure of disclosures) disclosure.openDuringOverflowScan = openDuringOverflowScan;

    for (const el of Array.from(pane.querySelectorAll<HTMLElement>("*"))) {
      const overflowPx = el.scrollWidth - el.clientWidth;
      if (overflowPx <= 1) continue;
      if (el.clientWidth <= 1) {
        ignored.push(`${el.tagName.toLowerCase()}.${el.className || ""} (1px clip box)`);
        continue;
      }
      const overflowX = getComputedStyle(el).overflowX;
      if (overflowX === "hidden" || overflowX === "clip") {
        ignored.push(
          `${el.tagName.toLowerCase()}.${el.className || ""} (overflow-x: ${overflowX}, clipped not scrollable)`,
        );
        continue;
      }

      const before = el.scrollLeft;
      el.scrollLeft = 0;
      const box = el.getBoundingClientRect();
      const contentRight = box.left + el.clientLeft + el.clientWidth;

      const escapees: Escapee[] = [];
      for (const child of Array.from(el.querySelectorAll<HTMLElement>("*"))) {
        const over = child.getBoundingClientRect().right - contentRight;
        if (over <= 1) continue;
        let depth = 0;
        for (let p = child.parentElement; p && p !== el; p = p.parentElement) depth++;
        escapees.push({ tag: child.tagName.toLowerCase(), cls: child.className || "", overflowPx: over, depth });
      }
      el.scrollLeft = before;

      // Deepest first: the innermost escapee is the one actually too wide;
      // everything above it is just carrying the width upward.
      escapees.sort((a, b) => b.depth - a.depth || b.overflowPx - a.overflowPx);
      scrollers.push({
        tag: el.tagName.toLowerCase(),
        cls: el.className || "",
        scrollWidth: el.scrollWidth,
        clientWidth: el.clientWidth,
        overflowPx,
        escapees: escapees.slice(0, 12),
      });
    }
  } finally {
    for (const target of disclosureTargets) target.details.open = target.originalOpen;
    for (const disclosure of disclosures) {
      const target = disclosureTargets.find((candidate) => candidate.kind === disclosure.kind);
      disclosure.restoredOpen = target?.details.open ?? false;
    }
  }
  return { width, scrollers, ignored, disclosures };
}

// Direct-children geometry for one selector: which flex line each item landed
// on, how wide it is, and the computed properties that decide whether it can
// shrink. This is what tells a "the row is too wide" report apart from a
// "one item refuses to shrink" one.
function dump(selector: string) {
  const el = document.querySelector<HTMLElement>(selector);
  if (!el) return { error: `no element matches ${selector}` };
  const box = el.getBoundingClientRect();
  return {
    host: { cls: el.className, clientWidth: el.clientWidth, scrollWidth: el.scrollWidth, left: box.left },
    style: (({ display, flexWrap, gap, padding, boxSizing }) => ({ display, flexWrap, gap, padding, boxSizing }))(
      getComputedStyle(el),
    ),
    children: Array.from(el.children).map((c) => {
      const r = c.getBoundingClientRect();
      const cs = getComputedStyle(c);
      return {
        tag: c.tagName.toLowerCase(),
        cls: c.className,
        text: (c.textContent ?? "").slice(0, 40),
        left: +(r.left - box.left).toFixed(1),
        right: +(r.right - box.left).toFixed(1),
        width: +r.width.toFixed(1),
        top: +(r.top - box.top).toFixed(1),
        flex: cs.flex,
        minWidth: cs.minWidth,
        display: cs.display,
        transform: cs.transform,
        marginLeft: cs.marginLeft,
      };
    }),
  };
}

// The tree is not done assembling at load: the delegate module claims its
// turn's leadership in a layout effect, and the virtualizer replaces its
// estimated row heights with measured ones a frame later. A driver that
// measured on `load` would measure a tree mid-assembly, which is how a guard
// starts reporting numbers nobody can reproduce by hand. Resolving after two
// painted frames plus a macrotask is the settle point, and it is awaited
// rather than slept on.
const settled = new Promise<true>((resolve) => {
  requestAnimationFrame(() => requestAnimationFrame(() => setTimeout(() => resolve(true), 0)));
});

declare global {
  interface Window {
    measure: typeof measure;
    dump: typeof dump;
    settled: Promise<true>;
  }
}
window.measure = measure;
window.dump = dump;
window.settled = settled;
