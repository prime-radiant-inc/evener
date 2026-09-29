// Browser-verification harness for the inline mermaid diagram (Task 4's
// MermaidDiagram, rendered through the two-layer sanitize in
// widgets/mermaid/security.ts).
//
// WHAT THIS PROVES THAT jsdom CANNOT: the widget's suite renders mermaid in
// jsdom, which has no layout and - more to the point - does not fetch a
// src'd <img> the way a real browser does. Here a real headless Chrome renders
// two diagrams: a benign flowchart whose labels must survive, and a hostile one
// whose model-authored <img src> label, authored <a href>, and click directive
// must never reach the document. The guard records every attempt to fetch a
// cross-origin resource and asserts there were none - the pixel URL is the
// probe for whether the sanitizer layers actually held.
//
// The instrumentation is installed at MODULE scope, before the first React
// render and long before mermaid's lazy import resolves, so nothing it is meant
// to observe can slip past it. mermaid itself renders into a temporary element
// it appends to document.body; a surviving <img> there starts a fetch the
// moment it is inserted, which the PerformanceObserver (resource entries) and
// the img-src MutationObserver both see. new Image()/fetch() are wrapped too, so
// a programmatic fetch attempt is recorded even when it never produces a
// resource timing entry.
import { createRoot } from "react-dom/client";
import { MermaidDiagram } from "../widgets/mermaid";
import { MERMAID_DIAGRAM_ATTR } from "../widgets/mermaid/markers";
import "../styles/tokens.css";
import "../styles/global.css";

// The handle Chrome needs to report that this page booted (see navigateTo's
// boot seam in scripts/browserGuardCdp.mjs): a page whose load event fired but
// where this global is still missing never ran the entry module.
declare global {
  interface Window {
    waitForMermaidGuardSettled: typeof waitForMermaidGuardSettled;
  }
}

// The benign diagram's labels, pinned here so the entry and the runner share
// one source of truth for what "rendered with labels" means.
const BENIGN_LABELS = ["Guard label one", "Guard label two", "Guard label three"];

const BENIGN_SOURCE = ["graph TD", "  A[Guard label one] --> B[Guard label two]", "  B --> C[Guard label three]"].join(
  "\n",
);

// The hostile diagram: a resource-bearing <img> in a node label, an authored
// <a href> in another, and a click directive. Each is a distinct way a model
// could try to make the rendered diagram reach the network or navigate.
const HOSTILE_SOURCE = [
  "graph TD",
  "  H[\"<img src='https://invalid.example/pixel.png'>Hostile node\"]",
  "  H --> S[\"<a href='https://invalid.example/click'>authored anchor</a>\"]",
  '  click H href "https://invalid.example/click"',
].join("\n");

// --- external-fetch instrumentation (installed before any render) ----------

const externalAttempts = new Set<string>();

// Record a URL only when it would leave the guard's own origin. Same-origin
// Vite module/font traffic is every legitimate request this page makes, so
// anything else is an attempt the sanitizer was supposed to prevent.
function noteExternal(url: unknown): void {
  if (typeof url !== "string" || url.length === 0) return;
  let resolved: URL;
  try {
    resolved = new URL(url, window.location.href);
  } catch {
    externalAttempts.add(url);
    return;
  }
  if (resolved.protocol === "data:" || resolved.protocol === "blob:") return;
  if (resolved.origin === window.location.origin) return;
  externalAttempts.add(resolved.href);
}

function installResourceObserver(): void {
  try {
    const observer = new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) noteExternal(entry.name);
    });
    // buffered: true picks up entries created before this observer existed;
    // installExternalFetchInstrumentation runs first, at module scope.
    observer.observe({ type: "resource", buffered: true });
  } catch {
    // A browser without PerformanceObserver cannot run this guard; the runner's
    // other assertions still hold and the missing observer would only ever make
    // externalAttempts under-report. Nothing to do here.
  }
}

function installImageWrapping(): void {
  const descriptor = Object.getOwnPropertyDescriptor(HTMLImageElement.prototype, "src");
  if (descriptor?.set) {
    Object.defineProperty(HTMLImageElement.prototype, "src", {
      ...descriptor,
      set(value: string) {
        noteExternal(value);
        descriptor.set?.call(this, value);
      },
    });
  }
  // mermaid builds its SVG as a string and inserts it with innerHTML, so the
  // src setter above never runs for it: a MutationObserver catches the <img>
  // element as it enters the document, which is the moment the browser would
  // start the fetch. Anchors are deliberately NOT recorded here - a click is
  // not an automatic fetch, and the guard asserts on them separately.
  const observer = new MutationObserver((records) => {
    for (const record of records) {
      for (const node of record.addedNodes) {
        if (!(node instanceof Element)) continue;
        if (node.tagName === "IMG") noteExternal(node.getAttribute("src"));
        for (const image of node.querySelectorAll("img")) noteExternal(image.getAttribute("src"));
      }
    }
  });
  observer.observe(document.documentElement, { childList: true, subtree: true });
}

function installFetchWrapping(): void {
  const nativeFetch = window.fetch?.bind(window);
  if (!nativeFetch) return;
  window.fetch = (input: RequestInfo | URL, init?: RequestInit) => {
    if (typeof input === "string") noteExternal(input);
    else if (input instanceof URL) noteExternal(input.href);
    else noteExternal(input.url);
    return nativeFetch(input, init);
  };
}

if (typeof PerformanceObserver !== "undefined") installResourceObserver();
installImageWrapping();
installFetchWrapping();

// --- probes the runner drives over CDP -------------------------------------

function mermaidGuardResult(): {
  labelsPresent: string[];
  missingLabels: string[];
  externalAttempts: string[];
  anchors: number;
} {
  const containers = [...document.querySelectorAll(`[${MERMAID_DIAGRAM_ATTR}]`)];
  const text = containers.map((container) => container.textContent ?? "").join("\n");
  return {
    labelsPresent: BENIGN_LABELS.filter((label) => text.includes(label)),
    missingLabels: BENIGN_LABELS.filter((label) => !text.includes(label)),
    externalAttempts: [...externalAttempts],
    anchors: document.querySelectorAll(`[${MERMAID_DIAGRAM_ATTR}] a`).length,
  };
}

// Resolves once both diagrams have rendered (each container holds either an
// <svg> or the error fallback), then gives a surviving external <img> a short
// window to be observed. A diagram stuck on the loading placeholder trips the
// deadline instead of hanging the runner's evaluate().
async function waitForMermaidGuardSettled(): Promise<ReturnType<typeof mermaidGuardResult>> {
  const deadline = performance.now() + 20_000;
  for (;;) {
    const containers = [...document.querySelectorAll(`[${MERMAID_DIAGRAM_ATTR}]`)];
    const settled =
      containers.length === 2 &&
      containers.every(
        (container) => container.querySelector("svg") !== null || (container.textContent ?? "").includes("render"),
      );
    if (settled) break;
    if (performance.now() > deadline) {
      throw new Error(
        `mermaidguard harness: diagrams never settled (containers=${containers.length}, svgs=${document.querySelectorAll(`[${MERMAID_DIAGRAM_ATTR}] svg`).length})`,
      );
    }
    await new Promise((resolve) => requestAnimationFrame(resolve));
  }
  // The mermaid render DOM is appended and its <img> fetch kicked off before it
  // is torn down; this grace lets the PerformanceObserver deliver that entry.
  await new Promise((resolve) => setTimeout(resolve, 300));
  return mermaidGuardResult();
}

const rootEl = document.getElementById("root");
if (!rootEl) throw new Error("mermaidguard.html is missing #root");

createRoot(rootEl).render(
  <main>
    <MermaidDiagram source={BENIGN_SOURCE} />
    <MermaidDiagram source={HOSTILE_SOURCE} />
  </main>,
);

window.waitForMermaidGuardSettled = waitForMermaidGuardSettled;
