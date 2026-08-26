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
import { isElementVisible } from "./guardVisibility";
import "../panes/session";
import Session from "../panes/session/Session";
import Settings from "../panes/settings/Settings";
import "../panes/sessionPanels";
import { hydrateThread } from "../protocol/reducer";
import { FakeClient } from "../protocol/testing/fakeClient";
import type { ThreadCapabilities, ThreadReadResponse } from "../protocol/types.gen";
import { DockHost } from "../shell/DockHost";
import { workspaceStore } from "../shell/workspace";
import { connectionStore } from "../stores/connection";
import { threadsStore } from "../stores/threads";
import { initTranscriptDisplay } from "../stores/transcriptDisplay";
import "../styles/tokens.css";
import "../styles/global.css";

const params = new URLSearchParams(window.location.search);
window.addEventListener("error", (event) => {
  const target = window as typeof window & { __panelGuardErrors?: string[] };
  target.__panelGuardErrors = [...(target.__panelGuardErrors ?? []), event.error?.stack ?? event.message];
});
window.addEventListener("unhandledrejection", (event) => {
  const target = window as typeof window & { __panelGuardErrors?: string[] };
  target.__panelGuardErrors = [...(target.__panelGuardErrors ?? []), event.reason?.stack ?? String(event.reason)];
});
const width = Number(params.get("w") ?? "1400");
const theme = params.get("theme");
const settingsMode = params.get("settings") === "1";
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
  changeVisionModel: true,
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
const CARD_LONG_TOKEN = `https://example.invalid/${"delegate-card-segment".repeat(80)}`;

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
    status: { type: "active" },
    cwd: "/Users/jesse/prime-radiant/toil-suite/evener",
    cliVersion: "1.0.0",
    source: "evener",
    evener: {
      ref: REF,
      capabilities: CAPABILITIES,
      queue: { revision: 0, depth: 12 },
      // Deliberately pressured live footer state, using EvenerThread's actual
      // wire fields. At the narrowest requested pane this keeps supported
      // effort, measured context, and a nonzero queue in the real React tree;
      // at the full-row threshold it also exercises work and goal sizing.
      contextUsed: 67_345,
      contextWindow: 100_001,
      contextPressure: 0.673,
      workMillis: 754_000,
      activeTurnStartedAt: 1,
      goal: { status: "awaiting stakeholder approval for compact session footer geometry", iterations: 12 },
      reasoningEffort: "high",
      reasoningEffortLevels: ["low", "medium", "high"],
      supportsReasoning: true,
      cost: "~$0.06",
    },
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
            output: JSON.stringify({ delegate_id: "dlg_1", status: "running", reason: CARD_LONG_TOKEN }),
          },
          {
            // A purpose-bearing shell row: the two-line composition (italic
            // rationale over the demoted verb/target line) is the transcript's
            // most common tool-row shape, and this guard is the only thing
            // that measures IT for horizontal escape at every width.
            type: "commandExecution",
            id: "i3b",
            turnId: "turn_1",
            toolName: "shell",
            callId: "call_1b",
            status: "completed",
            durationMs: 800,
            description:
              "Merging the transcript redesign into webui-workspace-shell and verifying the merged result is green",
            argumentsJson: JSON.stringify({
              command:
                "cd ~/prime-radiant/toil-suite/evener/.claude/worktrees/webui-workspace-shell && git merge --no-ff --no-edit transcript-view-design",
            }),
            output: "Merge made by the 'ort' strategy.",
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
            text: "Directory: /Users/jesse/prime-radiant/toil-suite/evener Three notable entries: README.md, go.mod, agent/",
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
            id: "i6b",
            turnId: "turn_1",
            // A labelled steering divider (NOT the notification kind, which
            // routes to a card): exercises the rail-icon treatment and the
            // body-size steering row inside the run column.
            steeringKind: "loop-detected",
            text: "You seem to be repeating the same action. Consider a different approach.",
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
fake.on("evener/tasks/list", () => ({ data: [] }));
connectionStore.getState().connect(fake);
threadsStore.setState((s) => ({
  threads: new Map(s.threads).set(REF, hydrateThread(snapshot, REF, 1000)),
}));
initTranscriptDisplay();

const rootEl = document.getElementById("root");
if (!rootEl) throw new Error("overflowharness.html is missing #root");

document.body.style.margin = "0";
document.body.style.background = "var(--surface-0)";

if (settingsMode) {
  createRoot(rootEl).render(
    <div id="oh-pane" style={{ width, height: 900, padding: 8 }}>
      <Settings params={{ section: "transcript" }} paneId="oh-settings" focused />
    </div>,
  );
} else if (params.get("panels") === "1") {
  workspaceStore.getState().openPane("session", { ref: REF });
  createRoot(rootEl).render(
    <div id="oh-pane" style={{ width, height: 900, padding: 8 }}>
      <DockHost />
    </div>,
  );
} else {
  createRoot(rootEl).render(
    <div id="oh-pane" style={{ width, height: 900, padding: 8 }}>
      <Session params={{ ref: REF }} paneId="oh" focused />
    </div>,
  );
}

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

interface DetailGeometry {
  found: boolean;
  mobile: boolean;
  triggerReachable: boolean;
  trigger: { left: number; right: number; top: number; bottom: number; width: number; height: number } | null;
  open: boolean;
  portalContained: boolean;
  panel: { left: number; right: number; top: number; bottom: number; width: number; height: number } | null;
  horizontalOverflowCount: number;
  targetHeights: number[];
  fieldsetStacked: boolean;
}

interface SettingsGeometry {
  mode: "settings";
  cardsFound: number;
  cardsStacked: boolean;
  cardOverflowCount: number;
  previewOverflowCount: number;
  previewInnerScrollCount: number;
}

function geometryOf(element: Element) {
  const box = element.getBoundingClientRect();
  return {
    left: box.left,
    right: box.right,
    top: box.top,
    bottom: box.bottom,
    width: box.width,
    height: box.height,
  };
}

function scrollOverflowCount(root: Element): number {
  return [root, ...Array.from(root.querySelectorAll<HTMLElement>("*"))].filter((element) => {
    if (!(element instanceof HTMLElement) || element.clientWidth <= 1) return false;
    const style = getComputedStyle(element);
    if (style.overflowX === "hidden" || style.overflowX === "clip") return false;
    return element.scrollWidth > element.clientWidth + 1;
  }).length;
}

function measureSettings(): SettingsGeometry {
  const content = document.querySelector<HTMLElement>('[data-testid="settings-content"]');
  if (!content) {
    return {
      mode: "settings",
      cardsFound: 0,
      cardsStacked: false,
      cardOverflowCount: 0,
      previewOverflowCount: 0,
      previewInnerScrollCount: 0,
    };
  }
  const cards = Array.from(content.querySelectorAll<HTMLElement>('[data-testid^="transcript-display-card-"]'));
  const previews = cards.flatMap((card) =>
    Array.from(card.querySelectorAll<HTMLElement>('[data-testid^="transcript-display-preview-"]')),
  );
  const cardBoxes = cards.map(geometryOf);
  const firstCard = cardBoxes[0];
  const secondCard = cardBoxes[1];
  const cardsStacked =
    cards.length === 2 &&
    firstCard !== undefined &&
    secondCard !== undefined &&
    secondCard.top >= firstCard.bottom - 1 &&
    cards.every((card) => card.getBoundingClientRect().width > 0);
  return {
    mode: "settings",
    cardsFound: cards.length,
    cardsStacked,
    cardOverflowCount: cards.reduce((count, card) => count + scrollOverflowCount(card), 0),
    previewOverflowCount: previews.reduce((count, preview) => count + scrollOverflowCount(preview), 0),
    // A preview's production TranscriptBody is normal flow. Count only
    // descendants other than the preview root here so the assertion names an
    // accidental nested scroller instead of the card's own containing box.
    previewInnerScrollCount: previews.reduce(
      (count, preview) =>
        count +
        Array.from(preview.querySelectorAll<HTMLElement>("*")).filter(
          (element) => element.clientWidth > 1 && element.scrollWidth > element.clientWidth + 1,
        ).length,
      0,
    ),
  };
}

async function inspectDetail(): Promise<DetailGeometry> {
  const pane = document.getElementById("oh-pane");
  const trigger = Array.from(document.querySelectorAll<HTMLButtonElement>("button")).find((button) =>
    button.textContent?.trim().startsWith("Detail:"),
  );
  if (!pane || !trigger) {
    return {
      found: trigger !== undefined,
      mobile: window.matchMedia("(max-width: 899px)").matches,
      triggerReachable: false,
      trigger: null,
      open: false,
      portalContained: false,
      panel: null,
      horizontalOverflowCount: 0,
      targetHeights: [],
      fieldsetStacked: false,
    };
  }
  const mobile = window.matchMedia("(max-width: 899px)").matches;
  const paneBox = pane.getBoundingClientRect();
  const triggerBox = geometryOf(trigger);
  const triggerReachable =
    !trigger.disabled &&
    triggerBox.width > 0 &&
    triggerBox.height > 0 &&
    triggerBox.left >= paneBox.left - 1 &&
    triggerBox.right <= paneBox.right + 1 &&
    triggerBox.top >= paneBox.top - 1;
  trigger.click();
  await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())));
  const finite = document
    .getAnimations()
    .filter((animation) => animation.effect?.getTiming().iterations !== Number.POSITIVE_INFINITY);
  await Promise.all(finite.map((animation) => animation.finished.catch(() => undefined)));

  const panel = mobile
    ? document.querySelector<HTMLElement>('[role="dialog"][aria-modal="true"]')
    : document.querySelector<HTMLElement>('[data-testid="transcript-detail-popover"]');
  if (!panel) {
    return {
      found: true,
      mobile,
      triggerReachable,
      trigger: triggerBox,
      open: false,
      portalContained: false,
      panel: null,
      horizontalOverflowCount: 0,
      targetHeights: [],
      fieldsetStacked: false,
    };
  }
  const panelBox = geometryOf(panel);
  const targetHeights = Array.from(panel.querySelectorAll<HTMLElement>("button, select")).map(
    (element) => element.getBoundingClientRect().height,
  );
  const fieldsets = Array.from(panel.querySelectorAll<HTMLElement>("fieldset"));
  const fieldsetBoxes = fieldsets.map(geometryOf);
  const fieldsetStacked =
    !mobile ||
    (fieldsets.length === 3 &&
      fieldsetBoxes.every((box, index) => {
        const previous = fieldsetBoxes[index - 1];
        return index === 0 || (previous !== undefined && box.top >= previous.bottom - 1);
      }));
  return {
    found: true,
    mobile,
    triggerReachable,
    trigger: triggerBox,
    open: true,
    portalContained:
      panelBox.left >= -1 &&
      panelBox.right <= window.innerWidth + 1 &&
      panelBox.top >= -1 &&
      panelBox.bottom <= window.innerHeight + 1,
    panel: panelBox,
    horizontalOverflowCount: scrollOverflowCount(panel),
    targetHeights,
    fieldsetStacked,
  };
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

// Whether a required footer fact (effort, context, queue) is on the screen.
// The definition, and why each clause is there, lives in guardVisibility.ts -
// shared with spawnguard so one word cannot mean two things. The whole weight
// of footer.*Visible rests on it, so it also gets the live fixture below rather
// than a line in MUTATIONS.md.
const visible = isElementVisible;

interface VisibilityProbe {
  rendered: boolean;
  ancestorHidden: boolean;
  visuallyHidden: boolean;
  visibilityHiddenAncestor: boolean;
  zeroArea: boolean;
}

function probeSpan(cssText: string): HTMLSpanElement {
  const el = document.createElement("span");
  el.style.cssText = cssText;
  el.textContent = "probe";
  return el;
}

/**
 * A mutation test for the shared visibility predicate, run every sweep instead
 * of recorded once - and the only fixture either guard has for it, since
 * spawnguard uses the same function.
 *
 * One probe per clause, and every one is load-bearing:
 *
 *   rendered                  the positive control. Without it, a predicate
 *                             that answered "not visible" to everything would
 *                             satisfy every other probe here.
 *   ancestorHidden            the bsq9 regression: a span whose OWN computed
 *                             display is `flex`, inside a `display: none` div -
 *                             the exact shape a footer fact takes when its
 *                             subtree collapses.
 *   visuallyHidden            statusrow.module.css's `.srOnly` recipe verbatim.
 *                             Measured exactly 1x1, so it satisfies the area
 *                             clause: present for the reader it is written for,
 *                             not missing.
 *   visibilityHiddenAncestor  a plain span under a `visibility: hidden` div.
 *                             Geometry alone cannot see this one - the span
 *                             keeps its full box - so it is what pins the
 *                             inherited-visibility clause.
 *   zeroArea                  `transform: scale(0)`: one client rect enclosing
 *                             nothing. Pins the area clause, which the rects
 *                             clause alone does not cover.
 *
 * Mounted outside #oh-pane and position:fixed, so it is invisible to the
 * horizontal-overflow scan and adds nothing to the document's scroll size.
 */
function visibilityProbe(): VisibilityProbe {
  const host = document.createElement("div");
  host.style.cssText = "position:fixed;top:0;left:0";

  const rendered = probeSpan("");
  const hiddenAncestor = document.createElement("div");
  hiddenAncestor.style.cssText = "display:none";
  const underHiddenAncestor = probeSpan("display:flex");
  hiddenAncestor.appendChild(underHiddenAncestor);
  const visuallyHidden = probeSpan(
    "position:absolute;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0",
  );
  const invisibleAncestor = document.createElement("div");
  invisibleAncestor.style.cssText = "visibility:hidden";
  const underInvisibleAncestor = probeSpan("");
  invisibleAncestor.appendChild(underInvisibleAncestor);
  const zeroArea = probeSpan("display:inline-block;transform:scale(0)");

  host.append(rendered, hiddenAncestor, visuallyHidden, invisibleAncestor, zeroArea);
  document.body.appendChild(host);
  try {
    return {
      rendered: visible(rendered),
      ancestorHidden: visible(underHiddenAncestor),
      visuallyHidden: visible(visuallyHidden),
      visibilityHiddenAncestor: visible(underInvisibleAncestor),
      zeroArea: visible(zeroArea),
    };
  } finally {
    host.remove();
  }
}

function measure() {
  if (settingsMode) return measureSettings();
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
  const status = pane.querySelector<HTMLElement>('[data-testid="status-row"]');
  const effort = pane.querySelector<HTMLElement>('[data-testid="status-row-effort"]');
  const context = pane.querySelector<HTMLElement>('[data-testid="status-row-context"]');
  const queue = pane.querySelector<HTMLElement>('[data-testid="status-row-queue"]');
  const model = pane.querySelector<HTMLElement>('[data-testid="model-switch-value"]');
  const subagentCard = pane.querySelector<HTMLElement>('[data-testid="subagent-row"]');
  const subagentQuote = subagentCard?.querySelector<HTMLElement>('[data-testid="subagent-quote"]');
  const subagentStats = subagentCard?.querySelector<HTMLElement>('[data-testid="subagent-stats"]');
  const quoteFontSize = subagentQuote ? Number.parseFloat(getComputedStyle(subagentQuote).fontSize) : 0;
  return {
    width,
    scrollers,
    ignored,
    disclosures,
    visibility: visibilityProbe(),
    subagentCard: {
      found: subagentCard !== null,
      contained: !!subagentCard && subagentCard.scrollWidth <= subagentCard.clientWidth + 1,
      quoteWrapped: !!subagentQuote && subagentQuote.getBoundingClientRect().height > quoteFontSize * 1.5,
      statsContained: !!subagentStats && subagentStats.scrollWidth <= subagentStats.clientWidth + 1,
    },
    footer: {
      effortVisible: visible(effort),
      contextVisible: visible(context),
      queueVisible: visible(queue),
      queueLabel: queue?.getAttribute("aria-label") ?? null,
      statusClientWidth: status?.clientWidth ?? 0,
      statusScrollWidth: status?.scrollWidth ?? 0,
      modelClientWidth: model?.clientWidth ?? 0,
    },
  };
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

// The tree is not done assembling at load: delegate cards update from layout
// effects, and the virtualizer replaces its estimated row heights with
// measured ones a frame later. A driver that
// measured on `load` would measure a tree mid-assembly, which is how a guard
// starts reporting numbers nobody can reproduce by hand. Resolving after two
// painted frames plus a macrotask is the settle point, and it is awaited
// rather than slept on.
const settled = new Promise<true>((resolve) => {
  requestAnimationFrame(() =>
    requestAnimationFrame(() =>
      setTimeout(() => {
        // NotificationCard renders its body (and with it the raw-notification
        // overflow fixture) only when expanded. Expand it the way a user would
        // - through React's synthetic click - so the guard's two disclosure
        // fixtures exist by the time measure() runs.
        document.querySelector<HTMLElement>('[data-testid="notification-card"]')?.click();
        requestAnimationFrame(() =>
          setTimeout(() => {
            // Boot-time expansions start CSS transitions, and a square chevron
            // caught MID-rotation paints wider than its layout box (14px at
            // 22° bounds ~18px) - a transient escape measure() would flag as
            // real (it did, at 390px, 2026-07-28). Wait out every finite
            // animation so the guard always measures a UI at rest. Infinite
            // animations (live cadence pulses) are excluded - they never
            // finish, and they are not the thing being measured.
            const finite = document
              .getAnimations()
              .filter((a) => a.effect?.getTiming().iterations !== Number.POSITIVE_INFINITY);
            Promise.all(finite.map((a) => a.finished.catch(() => undefined))).then(() => resolve(true));
          }, 0),
        );
      }, 0),
    ),
  );
});

declare global {
  interface Window {
    measure: typeof measure;
    dump: typeof dump;
    inspectDetail: typeof inspectDetail;
    settled: Promise<true>;
  }
}
window.measure = measure;
window.dump = dump;
window.inspectDetail = inspectDetail;
window.settled = settled;
