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
import type { NavigationSessionLocation, ThreadCapabilities, ThreadReadResponse } from "../protocol/types.gen";
import { ClientProvider } from "../shell/clientContext";
import { DockHost } from "../shell/DockHost";
import { workspaceStore } from "../shell/workspace";
import { connectionStore } from "../stores/connection";
import { navigationStore } from "../stores/navigation/store";
import { keyID } from "../stores/navigation/types";
import { putThreadModel } from "../stores/threads";
import { initTranscriptDisplay, transcriptDisplayStore } from "../stores/transcriptDisplay";
import { makeTranscriptDisplayConfig } from "../transcriptDisplay/config";
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
      tasks: {
        total: 4,
        done: 1,
        current: {
          id: 2,
          description:
            "Verify that the responsive focus sentence keeps a long live task readable without pushing the composer controls outside the session pane",
        },
      },
      goal: {
        objective:
          "Keep the session focused on proving the current task and goal remain independently ellipsized across every supported composer width",
        status: "awaiting stakeholder approval for compact session footer geometry",
        iterations: 12,
      },
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
            argumentsJson: JSON.stringify({ prompt: TASK, mode: "foreground_timeout", delegateId: "dlg_1" }),
            output: JSON.stringify({ delegate_id: "dlg_1", status: "running", reason: CARD_LONG_TOKEN }),
          },
          {
            // An intent-bearing shell row: the two-line composition (italic
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
// putThreadModel keeps the routing index in step with the seeded map
// entry (the store's membership path for threads).
putThreadModel(REF, hydrateThread(snapshot, REF, 1000));
const locationKey = { kind: "location", ref: REF } as const;
const location: NavigationSessionLocation = {
  generation_id: "overflow_generation",
  revision: 1,
  ref: REF,
  top_level_ref: REF,
  top_level: true,
  tier: "current",
  session: {
    ref: REF,
    host_id: "local",
    session_id: snapshot.thread.sessionId,
    title: snapshot.thread.name ?? REF,
    project: "",
    state: "active",
    kind: "session",
    live: true,
    children: [],
  },
};
navigationStore.setState({
  mode: "v2",
  clientGenerationID: location.generation_id,
  resources: new Map([
    [
      keyID(locationKey),
      {
        key: locationKey,
        data: location,
        loadedRevision: location.revision,
        targetRevision: null,
        forceToken: 0,
        etag: "overflow-location",
        loading: false,
        stale: false,
        error: null,
        generationID: location.generation_id,
      },
    ],
  ]),
});
initTranscriptDisplay();
// The browser guard must not inherit a developer's local transcript settings.
// Tools plus both diagnostic families keeps the real system-prompt and raw
// notification disclosure fixtures mounted in both layout classes. Tools
// (not activity) because activity now has expandByDefault=true, which would
// auto-open the notification card and the harness's click-to-expand step
// would then close it.
const guardTranscriptConfig = makeTranscriptDisplayConfig(
  { kind: "preset", level: "tools" },
  {
    systemEvents: true,
    promptEvents: true,
  },
);
transcriptDisplayStore.setState({
  local: { desktop: guardTranscriptConfig, mobile: guardTranscriptConfig },
});

const rootEl = document.getElementById("root");
if (!rootEl) throw new Error("overflowharness.html is missing #root");

document.body.style.margin = "0";
document.body.style.background = "var(--surface-0)";

if (settingsMode) {
  createRoot(rootEl).render(
    <ClientProvider client={fake}>
      <div id="oh-pane" style={{ width, height: 900 }}>
        <Settings params={{ section: "transcript" }} paneId="oh-settings" focused />
      </div>
    </ClientProvider>,
  );
} else if (params.get("panels") === "1") {
  workspaceStore.getState().openPane("session", { ref: REF });
  createRoot(rootEl).render(
    <ClientProvider client={fake}>
      <div id="oh-pane" style={{ width, height: 900 }}>
        <DockHost />
      </div>
    </ClientProvider>,
  );
} else {
  createRoot(rootEl).render(
    <ClientProvider client={fake}>
      <div id="oh-pane" style={{ width, height: 900 }}>
        <Session params={{ ref: REF }} paneId="oh" focused />
      </div>
    </ClientProvider>,
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
  triggerHitTestable: boolean;
  trigger: { left: number; right: number; top: number; bottom: number; width: number; height: number } | null;
  open: boolean;
  overlayContained: boolean;
  panel: { left: number; right: number; top: number; bottom: number; width: number; height: number } | null;
  horizontalOverflowCount: number;
  targets: Array<{ kind: string; label: string; height: number }>;
  fieldsetsFound: number;
  overflowElements: string[];
  fieldsetStacked: boolean;
  fieldsetsNonOverlapping: boolean;
  rootRemPx: number;
  editorContainerWidth: number;
  fieldsetColumns: number;
  sheetBottomAnchored: boolean;
  dialogCentered: boolean;
  overlayScroll: {
    connected: boolean;
    contained: boolean;
    scrollable: boolean;
    beforeTop: number;
    afterTop: number;
    scrollHeight: number;
    clientHeight: number;
  };
  effectiveTargets: Array<{ kind: string; label: string; height: number }>;
}

interface SettingsGeometry {
  mode: "settings";
  cardsFound: number;
  cardsStacked: boolean;
  cardOverflowCount: number;
  previewOverflowCount: number;
  previewInnerScrollCount: number;
  previewsFound: number;
  editors: EditorMeasurement[];
  canvases: PreviewCanvasMeasurement[];
  fieldsets: Array<{ left: number; right: number; top: number; bottom: number }>;
  trigger: null;
  scrollContainers: Array<{
    testId: string;
    scrollWidth: number;
    clientWidth: number;
    scrollHeight: number;
    clientHeight: number;
  }>;
}

interface EditorMeasurement {
  surface: "live" | "settings";
  layout: "desktop" | "mobile";
  ownerTestId: string;
  track: {
    left: number;
    right: number;
    width: number;
    scrollWidth: number;
    clientWidth: number;
  };
  segments: Array<{
    label: string;
    left: number;
    right: number;
    width: number;
    height: number;
    top: number;
    bottom: number;
    localLeft: number;
    localRight: number;
    checked: boolean;
  }>;
}

interface PreviewCanvasMeasurement {
  layout: "desktop" | "mobile";
  testId: string;
  width: number;
  availableWidth: number;
  scrollWidth: number;
  clientWidth: number;
  scrollHeight: number;
  clientHeight: number;
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

async function waitForStablePanel(panel: HTMLElement): Promise<void> {
  let previous = "";
  let stableFrames = 0;
  for (let frame = 0; frame < 120; frame += 1) {
    await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
    const box = geometryOf(panel);
    const current = [box.left, box.right, box.top, box.bottom, box.width, box.height]
      .map((value) => value.toFixed(3))
      .join(",");
    const finiteAnimations = document
      .getAnimations()
      .filter((animation) => animation.effect?.getTiming().iterations !== Number.POSITIVE_INFINITY);
    if (current === previous && finiteAnimations.length === 0) {
      stableFrames += 1;
      if (stableFrames >= 2) return;
    } else {
      stableFrames = 0;
    }
    previous = current;
  }
  throw new Error("Detail panel geometry did not stabilize after Advanced opened");
}

function isScrollableElement(element: HTMLElement): boolean {
  if (element.clientWidth <= 1) return false;
  const style = getComputedStyle(element);
  return (
    ((style.overflowX === "auto" || style.overflowX === "scroll") && element.scrollWidth > element.clientWidth + 1) ||
    ((style.overflowY === "auto" || style.overflowY === "scroll") && element.scrollHeight > element.clientHeight + 1)
  );
}

function scrollOverflowElements(root: Element): HTMLElement[] {
  return [root, ...Array.from(root.querySelectorAll<HTMLElement>("*"))].filter(
    (element): element is HTMLElement => element instanceof HTMLElement && isScrollableElement(element),
  );
}

function scrollOverflowCount(root: Element): number {
  return scrollOverflowElements(root).length;
}

function effectiveTargetElement(element: HTMLElement): HTMLElement {
  if (element.getAttribute("role") === "switch") {
    return element.parentElement ?? element;
  }
  if (element.tagName === "SELECT") {
    return element.parentElement?.parentElement ?? element;
  }
  return element;
}

function horizontalOverflowElements(root: Element): HTMLElement[] {
  return [root, ...Array.from(root.querySelectorAll<HTMLElement>("*"))].filter(
    (element): element is HTMLElement =>
      element instanceof HTMLElement &&
      element.clientWidth > 1 &&
      (getComputedStyle(element).overflowX === "auto" || getComputedStyle(element).overflowX === "scroll") &&
      element.scrollWidth > element.clientWidth + 1,
  );
}

function actualScrollContainers(root: Element): HTMLElement[] {
  return [root, ...Array.from(root.querySelectorAll<HTMLElement>("*"))].filter((element): element is HTMLElement => {
    if (!(element instanceof HTMLElement) || element.clientWidth <= 1) return false;
    const style = getComputedStyle(element);
    return [style.overflowX, style.overflowY].some((overflow) => overflow === "auto" || overflow === "scroll");
  });
}

function editorOwner(editor: HTMLElement): {
  surface: "live" | "settings";
  layout: "desktop" | "mobile";
  ownerTestId: string;
} {
  const card = editor.closest<HTMLElement>('[data-testid^="transcript-display-card-"]');
  if (card) {
    const ownerTestId = card.dataset.testid ?? "";
    return {
      surface: "settings",
      layout: ownerTestId.endsWith("-mobile") ? "mobile" : "desktop",
      ownerTestId,
    };
  }
  const owner = editor.closest<HTMLElement>('[data-testid="transcript-detail-control"]');
  return {
    surface: "live",
    layout: window.matchMedia("(max-width: 899px)").matches ? "mobile" : "desktop",
    ownerTestId: owner?.dataset.testid ?? "",
  };
}

function measureEditor(editor: HTMLElement): EditorMeasurement {
  const owner = editorOwner(editor);
  const track = editor.querySelector<HTMLElement>('[role="radiogroup"]');
  if (!track) {
    return {
      ...owner,
      track: { left: 0, right: 0, width: 0, scrollWidth: 0, clientWidth: 0 },
      segments: [],
    };
  }
  const trackBox = geometryOf(track);
  const segments = Array.from(track.querySelectorAll<HTMLButtonElement>('[role="radio"]')).map((segment) => {
    const box = geometryOf(segment);
    return {
      label:
        segment.querySelector<HTMLElement>("span")?.textContent?.trim() ?? segment.getAttribute("aria-label") ?? "",
      left: box.left,
      right: box.right,
      width: box.width,
      height: box.height,
      top: box.top,
      bottom: box.bottom,
      localLeft: box.left - trackBox.left,
      localRight: box.right - trackBox.left,
      checked: segment.getAttribute("aria-checked") === "true",
    };
  });
  return {
    ...owner,
    track: {
      left: trackBox.left,
      right: trackBox.right,
      width: trackBox.width,
      scrollWidth: track.scrollWidth,
      clientWidth: track.clientWidth,
    },
    segments,
  };
}

function measureEditors(root: ParentNode): EditorMeasurement[] {
  return Array.from(root.querySelectorAll<HTMLElement>('section[aria-label="Transcript detail editor"]')).map(
    measureEditor,
  );
}

function measureCanvas(canvas: HTMLElement): PreviewCanvasMeasurement {
  const box = geometryOf(canvas);
  const testId = canvas.dataset.testid ?? "";
  const layout = testId.endsWith("-mobile") ? "mobile" : "desktop";
  const card = canvas.closest<HTMLElement>('[data-testid^="transcript-display-card-"]');
  const cardId = card?.dataset.testid ?? "";
  const section = card?.querySelector<HTMLElement>(`section[aria-labelledby="${cardId}-example-heading"]`);
  const sectionStyle = section ? getComputedStyle(section) : null;
  const availableWidth = section
    ? section.clientWidth -
      (Number.parseFloat(sectionStyle?.paddingLeft ?? "0") || 0) -
      (Number.parseFloat(sectionStyle?.paddingRight ?? "0") || 0)
    : 0;
  return {
    layout,
    testId,
    width: box.width,
    availableWidth,
    scrollWidth: canvas.scrollWidth,
    clientWidth: canvas.clientWidth,
    scrollHeight: canvas.scrollHeight,
    clientHeight: canvas.clientHeight,
  };
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
      previewsFound: 0,
      editors: [],
      canvases: [],
      fieldsets: [],
      trigger: null,
      scrollContainers: [],
    };
  }
  const cards = Array.from(content.querySelectorAll<HTMLElement>('[data-testid^="transcript-display-card-"]'));
  const previews = cards.flatMap((card) =>
    Array.from(
      card.querySelectorAll<HTMLElement>(
        '[data-testid="transcript-display-preview-desktop"], [data-testid="transcript-display-preview-mobile"]',
      ),
    ),
  );
  const canvases = cards.flatMap((card) =>
    Array.from(
      card.querySelectorAll<HTMLElement>(
        '[data-testid="transcript-display-preview-canvas-desktop"], [data-testid="transcript-display-preview-canvas-mobile"]',
      ),
    ),
  );
  const editors = cards.flatMap((card) => measureEditors(card));
  const scrollRoots = [content, ...cards];
  const scrollContainers = Array.from(new Set(scrollRoots.flatMap((root) => actualScrollContainers(root))));
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
    previewsFound: previews.length,
    editors,
    canvases: canvases.map(measureCanvas),
    fieldsets: [],
    trigger: null,
    scrollContainers: scrollContainers.map((element) => ({
      testId: element.dataset.testid ?? (element.className || element.tagName.toLowerCase()),
      scrollWidth: element.scrollWidth,
      clientWidth: element.clientWidth,
      scrollHeight: element.scrollHeight,
      clientHeight: element.clientHeight,
    })),
    // A preview's production TranscriptBody is normal flow. Count only
    // descendants other than the preview root here so the assertion names an
    // accidental nested scroll container instead of the card's own box.
    previewInnerScrollCount: previews.reduce(
      (count, preview) =>
        count +
        Array.from(preview.querySelectorAll<HTMLElement>("*")).filter((element) => {
          if (element.clientWidth <= 1) return false;
          const style = getComputedStyle(element);
          return (
            ((style.overflowX === "auto" || style.overflowX === "scroll") &&
              element.scrollWidth > element.clientWidth + 1) ||
            ((style.overflowY === "auto" || style.overflowY === "scroll") &&
              element.scrollHeight > element.clientHeight + 1)
          );
        }).length,
      0,
    ),
  };
}

async function inspectDetail(includeAdvanced = true): Promise<DetailGeometry> {
  const pane = document.getElementById("oh-pane");
  const trigger = Array.from(document.querySelectorAll<HTMLButtonElement>("button")).find((button) =>
    button.textContent?.includes("Session actions"),
  );
  if (!pane || !trigger) {
    return {
      found: trigger !== undefined,
      mobile: window.matchMedia("(max-width: 899px)").matches,
      triggerReachable: false,
      triggerHitTestable: false,
      trigger: null,
      open: false,
      overlayContained: false,
      panel: null,
      horizontalOverflowCount: 0,
      targets: [],
      fieldsetsFound: 0,
      overflowElements: [],
      fieldsetStacked: false,
      fieldsetsNonOverlapping: false,
      rootRemPx: 0,
      editorContainerWidth: 0,
      fieldsetColumns: 0,
      sheetBottomAnchored: false,
      dialogCentered: false,
      overlayScroll: {
        connected: false,
        contained: false,
        scrollable: false,
        beforeTop: 0,
        afterTop: 0,
        scrollHeight: 0,
        clientHeight: 0,
      },
      effectiveTargets: [],
    };
  }
  const mobile = window.matchMedia("(max-width: 899px)").matches;
  trigger.scrollIntoView({ block: "nearest", inline: "nearest" });
  await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())));
  const paneBox = pane.getBoundingClientRect();
  const triggerBox = geometryOf(trigger);
  const triggerCenter = { x: (triggerBox.left + triggerBox.right) / 2, y: (triggerBox.top + triggerBox.bottom) / 2 };
  const hit = document.elementFromPoint(triggerCenter.x, triggerCenter.y);
  const triggerHitTestable = hit === trigger || (hit instanceof Node && trigger.contains(hit));
  const triggerReachable =
    !trigger.disabled &&
    visible(trigger) &&
    triggerBox.width > 0 &&
    triggerBox.height > 0 &&
    triggerBox.left >= paneBox.left - 1 &&
    triggerBox.right <= paneBox.right + 1 &&
    triggerBox.top >= paneBox.top - 1 &&
    triggerBox.bottom <= paneBox.bottom + 1;
  trigger.click();
  await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())));
  const menu = Array.from(document.querySelectorAll<HTMLElement>('[role="menu"]')).find(
    (candidate) => candidate.getAttribute("aria-labelledby") === trigger.id,
  );
  const verbosityItem = Array.from(menu?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? []).find(
    (item) => item.textContent?.trim() === "Verbosity…",
  );
  if (!verbosityItem) throw new Error("Session actions did not expose pane-only Verbosity…");
  const verbosityTarget = {
    kind: "menuitem",
    label: "Verbosity…",
    height: verbosityItem.getBoundingClientRect().height,
  };
  verbosityItem.click();
  await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())));
  const finite = document
    .getAnimations()
    .filter((animation) => animation.effect?.getTiming().iterations !== Number.POSITIVE_INFINITY);
  await Promise.all(finite.map((animation) => animation.finished.catch(() => undefined)));

  const panel = Array.from(document.querySelectorAll<HTMLElement>('[role="dialog"][aria-modal="true"]')).find(
    (dialog) => dialog.querySelector("h2")?.textContent?.trim() === "Verbosity",
  );
  if (!panel) {
    return {
      found: true,
      mobile,
      triggerReachable,
      trigger: triggerBox,
      triggerHitTestable,
      open: false,
      overlayContained: false,
      panel: null,
      horizontalOverflowCount: 0,
      targets: [],
      fieldsetsFound: 0,
      overflowElements: [],
      fieldsetStacked: false,
      fieldsetsNonOverlapping: false,
      rootRemPx: 0,
      editorContainerWidth: 0,
      fieldsetColumns: 0,
      sheetBottomAnchored: false,
      dialogCentered: false,
      overlayScroll: {
        connected: false,
        contained: false,
        scrollable: false,
        beforeTop: 0,
        afterTop: 0,
        scrollHeight: 0,
        clientHeight: 0,
      },
      effectiveTargets: [],
    };
  }
  const owner = panel.querySelector<HTMLElement>('[data-testid="transcript-detail-control"]');
  const editor = owner?.querySelector<HTMLElement>('section[aria-label="Transcript detail editor"]');
  if (!owner || !editor) throw new Error("Verbosity Dialog/Sheet does not own its transcript editor");
  await waitForStablePanel(panel);
  if (includeAdvanced) {
    const advanced = Array.from(panel.querySelectorAll<HTMLElement>("summary")).find((summary) =>
      summary.textContent?.trim().startsWith("Customize & advanced"),
    );
    if (!advanced) throw new Error("Detail editor Advanced disclosure is missing");
    advanced.click();
    await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())));
    const panelAnimations = document
      .getAnimations()
      .filter((animation) => animation.effect?.getTiming().iterations !== Number.POSITIVE_INFINITY);
    await Promise.all(panelAnimations.map((animation) => animation.finished.catch(() => undefined)));
    await waitForStablePanel(panel);
  }
  const panelBox = geometryOf(panel);
  const accessibleName = (element: Element): string => {
    const ariaLabel = element.getAttribute("aria-label");
    if (ariaLabel) return ariaLabel;
    const labelledBy = element.getAttribute("aria-labelledby");
    if (labelledBy) {
      const name = labelledBy
        .split(/\s+/)
        .map((id) => document.getElementById(id)?.textContent ?? "")
        .join(" ")
        .trim();
      if (name) return name.replace(/\s+/g, " ");
    }
    return element.textContent?.trim().replace(/\s+/g, " ").slice(0, 80) ?? "";
  };
  const controls = [
    trigger,
    ...Array.from(panel.querySelectorAll<HTMLElement>("button, select, summary, [role=radio], [role=switch]")),
  ];
  const switchLabels = Array.from(panel.querySelectorAll<HTMLElement>('[role="switch"]')).flatMap((switchElement) => {
    const ids = switchElement.getAttribute("aria-labelledby")?.split(/\s+/) ?? [];
    return ids
      .map((id) => document.getElementById(id))
      .filter((label): label is HTMLElement => label instanceof HTMLElement && panel.contains(label));
  });
  const targets = [
    verbosityTarget,
    ...controls.map((element) => ({
      kind: element === trigger ? "trigger" : (element.getAttribute("role") ?? element.tagName.toLowerCase()),
      label: accessibleName(element),
      height: element.getBoundingClientRect().height,
    })),
    ...switchLabels.map((element) => ({
      kind: "switch-label",
      label: accessibleName(element),
      height: element.getBoundingClientRect().height,
    })),
  ];
  const fieldsets = Array.from(panel.querySelectorAll<HTMLElement>("fieldset"));
  const fieldsetBoxes = fieldsets.map(geometryOf);
  const editorStyle = getComputedStyle(editor);
  const editorContainerWidth =
    editor.clientWidth -
    (Number.parseFloat(editorStyle.paddingLeft) || 0) -
    (Number.parseFloat(editorStyle.paddingRight) || 0);
  const columnLefts: number[] = [];
  for (const box of fieldsetBoxes) {
    if (!columnLefts.some((left) => Math.abs(left - box.left) <= 0.5)) columnLefts.push(box.left);
  }
  const fieldsetColumns = columnLefts.length;
  const rootRemPx = Number.parseFloat(getComputedStyle(document.documentElement).fontSize);
  const effectiveTargets = [
    verbosityTarget,
    ...controls.map((element) => {
      const effectiveElement = effectiveTargetElement(element);
      return {
        kind: element === trigger ? "trigger" : (element.getAttribute("role") ?? element.tagName.toLowerCase()),
        label: accessibleName(element),
        height: effectiveElement.getBoundingClientRect().height,
      };
    }),
  ];
  const fieldsetStacked =
    fieldsets.length === 3 &&
    fieldsetBoxes.every((box, index) => {
      const previous = fieldsetBoxes[index - 1];
      return index === 0 || (previous !== undefined && box.top >= previous.bottom - 1);
    });
  const fieldsetsNonOverlapping = fieldsetBoxes.every((box, index) =>
    fieldsetBoxes
      .slice(index + 1)
      .every(
        (other) =>
          box.right <= other.left + 1 ||
          other.right <= box.left + 1 ||
          box.bottom <= other.top + 1 ||
          other.bottom <= box.top + 1,
      ),
  );
  const scrollPanel = owner.parentElement;
  if (!scrollPanel) throw new Error("Verbosity Dialog/Sheet scroll owner is missing");
  const beforeTop = scrollPanel.scrollTop;
  const scrollable = scrollPanel.scrollHeight > scrollPanel.clientHeight;
  scrollPanel.scrollTop = scrollPanel.scrollHeight;
  scrollPanel.dispatchEvent(new Event("scroll"));
  await waitForStablePanel(panel);
  const scrolledPanelBox = geometryOf(panel);
  const overlayScroll = {
    connected: panel.isConnected,
    contained:
      scrolledPanelBox.left >= -1 &&
      scrolledPanelBox.right <= window.innerWidth + 1 &&
      scrolledPanelBox.top >= -1 &&
      scrolledPanelBox.bottom <= window.innerHeight + 1,
    scrollable,
    beforeTop,
    afterTop: scrollPanel.scrollTop,
    scrollHeight: scrollPanel.scrollHeight,
    clientHeight: scrollPanel.clientHeight,
  };
  const overflowingElements = horizontalOverflowElements(panel);
  return {
    found: true,
    mobile,
    triggerReachable,
    trigger: triggerBox,
    triggerHitTestable,
    open: true,
    overlayContained:
      panelBox.left >= -1 &&
      panelBox.right <= window.innerWidth + 1 &&
      panelBox.top >= -1 &&
      panelBox.bottom <= window.innerHeight + 1,
    panel: panelBox,
    horizontalOverflowCount: overflowingElements.length,
    targets,
    fieldsetsFound: fieldsets.length,
    effectiveTargets,
    overflowElements: overflowingElements.map(
      (element) =>
        `${element.tagName.toLowerCase()}.${element.className || "(no-class)"} ` +
        `${element.scrollWidth}/${element.clientWidth}x${element.scrollHeight}/${element.clientHeight}`,
    ),
    fieldsetStacked,
    fieldsetsNonOverlapping,
    rootRemPx,
    editorContainerWidth,
    fieldsetColumns,
    sheetBottomAnchored: mobile && panelBox.bottom >= window.innerHeight - 1,
    dialogCentered:
      !mobile &&
      Math.abs((panelBox.left + panelBox.right) / 2 - document.documentElement.clientWidth / 2) <= 1 &&
      Math.abs((panelBox.top + panelBox.bottom) / 2 - document.documentElement.clientHeight / 2) <= 1,
    overlayScroll,
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

interface ChatFocusMeasurement {
  toolsRowFocused: boolean;
  groupFound: boolean;
  groupOpen: boolean;
  summaryIsActive: boolean;
  rationaleIsActive: boolean;
  summaryVisible: boolean;
  activeTag: string | null;
  activeTestId: string | null;
}

async function inspectChatFocus(): Promise<ChatFocusMeasurement> {
  const pane = document.getElementById("oh-pane");
  if (!pane) throw new Error("Chat focus harness pane never mounted");
  const layout = transcriptDisplayStore.getState().viewport;
  const original = transcriptDisplayStore.getState().local[layout];
  const waitFor = async <T,>(read: () => T | null | undefined, label: string): Promise<T> => {
    for (let frame = 0; frame < 180; frame += 1) {
      const value = read();
      if (value !== null && value !== undefined) return value;
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
    }
    throw new Error(`Chat focus harness did not settle: ${label}`);
  };
  try {
    transcriptDisplayStore.getState().setLocal(layout, makeTranscriptDisplayConfig({ kind: "preset", level: "tools" }));
    const toolTrigger = await waitFor(
      () => pane.querySelector<HTMLElement>('[data-view-anchor-id="i3b"] [data-testid="tool-row-trigger"]'),
      "Tools row trigger",
    );
    toolTrigger.focus();
    const toolsRowFocused = document.activeElement === toolTrigger;

    transcriptDisplayStore.getState().setLocal(layout, makeTranscriptDisplayConfig({ kind: "preset", level: "chat" }));
    const rationale = await waitFor(
      () => pane.querySelector<HTMLElement>('[data-view-anchor-id="intent:i3b"]'),
      "Chat intent rationale",
    );
    await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())));
    const group = rationale.closest<HTMLDetailsElement>('details[data-testid="intent-group"]');
    const summary = group?.querySelector<HTMLElement>(":scope > summary");
    const active = document.activeElement;
    return {
      toolsRowFocused,
      groupFound: group !== null,
      groupOpen: group?.open ?? false,
      summaryIsActive: active === summary,
      rationaleIsActive: active === rationale,
      summaryVisible: summary ? isElementVisible(summary) : false,
      activeTag: active?.tagName.toLowerCase() ?? null,
      activeTestId: active instanceof HTMLElement ? (active.dataset.testid ?? null) : null,
    };
  } finally {
    if (original) transcriptDisplayStore.getState().setLocal(layout, original);
    else transcriptDisplayStore.getState().clearLocal(layout);
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
      if (overflowX !== "auto" && overflowX !== "scroll") {
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
  const currentWork = pane.querySelector<HTMLElement>('[data-testid="current-work"]');
  const composerCard = pane.querySelector<HTMLElement>('[data-testid="composer-input-card"]');
  // The fixture has active status and interrupt capability, but no active turn
  // ID, so Composer correctly renders Stop but not the busy-only Steer. These
  // are the actual controls it must render at every width. The card alone is
  // not a controls check: each control is measured below.
  const controlTestIds = ["composer-attach", "composer-stop", "composer-submit"];
  const composeControls = controlTestIds.map((testId) => ({
    testId,
    element: pane.querySelector<HTMLElement>(`[data-testid="${testId}"]`),
  }));
  const subagentCard = pane.querySelector<HTMLElement>('[data-testid="subagent-row"]');
  const subagentQuote = subagentCard?.querySelector<HTMLElement>('[data-testid="subagent-quote"]');
  const subagentStats = subagentCard?.querySelector<HTMLElement>('[data-testid="subagent-stats"]');
  const editors = measureEditors(document);
  const liveEditorElement = Array.from(
    document.querySelectorAll<HTMLElement>('section[aria-label="Transcript detail editor"]'),
  ).find((editor) => !editor.closest('[data-testid^="transcript-display-card-"]'));
  const fieldsets = liveEditorElement
    ? Array.from(liveEditorElement.querySelectorAll<HTMLElement>("fieldset")).map(geometryOf)
    : [];
  const triggerElement = Array.from(document.querySelectorAll<HTMLButtonElement>("button")).find((button) =>
    button.textContent?.includes("Session actions"),
  );
  const triggerBox = triggerElement ? geometryOf(triggerElement) : null;
  const scrollRoots = [pane, ...Array.from(document.querySelectorAll<HTMLElement>('[role="dialog"]'))];
  const scrollContainers = Array.from(new Set(scrollRoots.flatMap((root) => actualScrollContainers(root))));
  const quoteFontSize = subagentQuote ? Number.parseFloat(getComputedStyle(subagentQuote).fontSize) : 0;
  const paneBox = pane.getBoundingClientRect();
  const currentWorkBox = currentWork?.getBoundingClientRect();
  const composerCardBox = composerCard?.getBoundingClientRect();
  const containedInPane = (box: DOMRect | undefined) =>
    !!box && box.left >= paneBox.left - 1 && box.right <= paneBox.right + 1;
  const containedInCard = (box: DOMRect | undefined) =>
    !!box &&
    !!composerCardBox &&
    box.left >= composerCardBox.left - 1 &&
    box.right <= composerCardBox.right + 1 &&
    box.top >= composerCardBox.top - 1 &&
    box.bottom <= composerCardBox.bottom + 1;
  const controls = composeControls.map(({ testId, element }) => {
    const box = element?.getBoundingClientRect();
    return {
      testId,
      present: visible(element),
      containedInCard: containedInCard(box),
      containedInPane: containedInPane(box),
      box,
    };
  });
  const controlsDoNotOverlap = controls.every((control, index) =>
    controls.slice(index + 1).every((other) => {
      if (!control.box || !other.box) return false;
      return (
        control.box.right <= other.box.left + 1 ||
        other.box.right <= control.box.left + 1 ||
        control.box.bottom <= other.box.top + 1 ||
        other.box.bottom <= control.box.top + 1
      );
    }),
  );
  function statusGeometry(element: HTMLElement | null) {
    if (element === null) return null;
    const style = getComputedStyle(element);
    return {
      ...geometryOf(element),
      clientWidth: element.clientWidth,
      scrollWidth: element.scrollWidth,
      display: style.display,
      flex: style.flex,
      minWidth: style.minWidth,
      overflow: style.overflow,
    };
  }
  return {
    width,
    scrollers,
    ignored,
    disclosures,
    editors,
    canvases: [],
    fieldsets,
    trigger: triggerBox
      ? { left: triggerBox.left, right: triggerBox.right, top: triggerBox.top, bottom: triggerBox.bottom }
      : null,
    scrollContainers: scrollContainers.map((element) => ({
      testId: element.dataset.testid ?? (element.className || element.tagName.toLowerCase()),
      scrollWidth: element.scrollWidth,
      clientWidth: element.clientWidth,
      scrollHeight: element.scrollHeight,
      clientHeight: element.clientHeight,
    })),
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
      geometry: {
        status: statusGeometry(status),
        identity: statusGeometry(pane.querySelector<HTMLElement>('[data-testid="status-row-identity"]')),
        model: statusGeometry(model),
        effort: statusGeometry(effort),
        context: statusGeometry(context),
        queue: statusGeometry(queue),
      },
    },
    currentWork: {
      found: visible(currentWork),
      composerCardFound: visible(composerCard),
      controlsFound: controls.every((control) => control.present),
      controlsContained: controls.every((control) => control.containedInCard && control.containedInPane),
      controlsDoNotOverlap,
      controls: controls.map(({ box: _box, ...control }) => control),
      sharedPaneWithoutOverflow:
        !!currentWork &&
        !!composerCard &&
        currentWork.scrollWidth <= currentWork.clientWidth + 1 &&
        composerCard.scrollWidth <= composerCard.clientWidth + 1 &&
        containedInPane(currentWorkBox) &&
        containedInPane(composerCardBox),
      orderedAboveComposer: !!currentWorkBox && !!composerCardBox && currentWorkBox.bottom <= composerCardBox.top + 1,
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
    inspectChatFocus: typeof inspectChatFocus;
    settled: Promise<true>;
  }
}
window.measure = measure;
window.dump = dump;
window.inspectDetail = inspectDetail;
window.inspectChatFocus = inspectChatFocus;
window.settled = settled;
