// Browser-verification harness for the automatic memory refresh renderer.
//
// WHAT THIS PROVES THAT jsdom CANNOT: jsdom runs no cascade and reports zero
// for every box, and it does not run the browser's native <summary> keyboard
// activation. This guard mounts the REAL production path - hydrateThread (the
// reducer) -> projectThread (the shared projector) -> TurnBlock -> the memory
// renderer - in a real headless Chrome and measures real geometry at desktop
// and phone widths, drives a trusted pointer click and a trusted Space key,
// and reads the formatted body and the folded literal Source.
//
// The fixture is the frozen wire contract: type systemMessage, eventKind
// "memory-context", id item_memory_context_0, Text the exact recorded section,
// raw.memoryContext the decoded observation. It carries a long unbroken token
// inside the decoded content so the horizontal-overflow check has something to
// catch, and a Markdown task list whose checkbox markers the sanitizer strips -
// the Source must keep them verbatim.
import {
  hydrateThread,
  makeTranscriptDisplayConfig,
  projectThread,
  type Thread,
  type ThreadItem,
} from "@evener/appwire-client";
import { createRoot } from "react-dom/client";
import { TurnBlock } from "../panes/session/transcript/TurnBlock";
import { TranscriptRenderProvider } from "../transcriptDisplay/renderContext";
import "../styles/tokens.css";
import "../styles/global.css";

const params = new URLSearchParams(window.location.search);
const width = Number(params.get("w") ?? "1024");
const level = params.get("level") === "full" ? "full" : "tools";

const LONG_TOKEN = "T".repeat(400);
const CONTENT = `## Personal memory\n\n- [x] guard checked\n- [ ] guard unchecked\n\n${LONG_TOKEN}\n\n![diagram](guard.png "guard title")`;
const RECORDED_TEXT = `Personal memory index: "# Personal memory\\n\\n- [x] guard checked\\n- [ ] guard unchecked\\n\\n${LONG_TOKEN}\\n\\n![diagram](guard.png \\"guard title\\")"`;

const item = {
  id: "item_memory_context_0",
  turnId: "turn_1",
  type: "systemMessage",
  text: RECORDED_TEXT,
  status: "completed",
  eventKind: "memory-context",
  raw: { memoryContext: { scope: "personal", state: "current", truncated: false, content: CONTENT } },
} as unknown as ThreadItem;

const thread = {
  id: "thread-1",
  sessionId: "session-1",
  preview: "",
  ephemeral: false,
  modelProvider: "anthropic",
  createdAt: 0,
  updatedAt: 0,
  status: { type: "ready" },
  cwd: "/tmp",
  cliVersion: "1.0.0",
  source: "local",
  turns: [{ id: "turn_1", itemsView: "full", status: "completed", items: [item] }],
  evener: { ref: "memoryguard", queue: { revision: 0 } },
} as unknown as Thread;

const model = hydrateThread({ thread }, "memoryguard", 0);
const config = makeTranscriptDisplayConfig({ kind: "preset", level }, level === "full" ? { systemEvents: true } : {});
const projected = projectThread(model, config);
const turn = projected.turns[0];
if (!turn) throw new Error("memoryguard fixture projected no turn");

function summaryEl(): HTMLElement {
  const details = document.querySelector<HTMLDetailsElement>('[data-testid="memory-context-item"]');
  const summary = details?.querySelector("summary");
  if (!summary) throw new Error("memoryguard: no memory-context summary in the DOM");
  return summary;
}

function detailsEl(): HTMLDetailsElement {
  const details = document.querySelector<HTMLDetailsElement>('[data-testid="memory-context-item"]');
  if (!details) throw new Error("memoryguard: no memory-context item in the DOM");
  return details;
}

function text(testId: string): string | null {
  return document.querySelector<HTMLElement>(`[data-testid="${testId}"]`)?.textContent ?? null;
}

// The guard's read surface: geometry, the formatted body, the literal Source,
// and the disclosure's open state. All values are plain JSON so CDP can carry
// them back.
function probe() {
  const details = detailsEl();
  return {
    found: true,
    open: details.open,
    label: text("memory-context-label"),
    meta: text("memory-context-meta"),
    state: text("memory-context-state"),
    contentText: text("memory-context-content"),
    fallbackText: text("memory-context-fallback"),
    sourceText: text("memory-context-source-text"),
    detailsScrollWidth: details.scrollWidth,
    detailsClientWidth: details.clientWidth,
    docScrollWidth: document.documentElement.scrollWidth,
    docClientWidth: document.documentElement.clientWidth,
  };
}

declare global {
  interface Window {
    memoryGuard: {
      settled: Promise<true>;
      probe: typeof probe;
      focusSummary: () => boolean;
      summaryPoint: () => { x: number; y: number };
      isOpen: () => boolean;
      sourceOpen: () => boolean;
    };
  }
}

const rootEl = document.getElementById("root");
if (!rootEl) throw new Error("memoryguard.html is missing #root");
document.body.style.margin = "0";
document.body.style.background = "var(--surface-0)";

createRoot(rootEl).render(
  <div id="mg-pane" style={{ width, minHeight: 900 }}>
    <TranscriptRenderProvider config={config} surface="readOnly" disclosureScope="memoryguard" sessionRef="memoryguard">
      <TurnBlock turn={turn} sessionRef="memoryguard" />
    </TranscriptRenderProvider>
  </div>,
);

const settled = new Promise<true>((resolve) => {
  requestAnimationFrame(() =>
    requestAnimationFrame(() =>
      setTimeout(() => {
        const finite = document
          .getAnimations()
          .filter((a) => a.effect?.getTiming().iterations !== Number.POSITIVE_INFINITY);
        Promise.all(finite.map((a) => a.finished.catch(() => undefined))).then(() => resolve(true));
      }, 0),
    ),
  );
});

window.memoryGuard = {
  settled,
  probe,
  focusSummary: () => {
    const summary = summaryEl();
    summary.focus();
    return document.activeElement === summary;
  },
  summaryPoint: () => {
    const box = summaryEl().getBoundingClientRect();
    return { x: box.left + box.width / 2, y: box.top + box.height / 2 };
  },
  isOpen: () => detailsEl().open,
  sourceOpen: () => Boolean(document.querySelector<HTMLDetailsElement>('[data-testid="memory-context-source"]')?.open),
};
