// Browser-verification harness for the desktop shell's page height.
//
// The bug it exists to find: the rail (sidebar) tree's FULL expanded height
// sets the document's height, so a tree taller than the viewport grows the
// whole page and leaves dead space below the shell - even though the rail's
// own .body is supposed to be the scroll container. jsdom cannot see this
// (it evaluates no cascade and reports zero for every box - kata tzqz), so
// the only honest reproduction is a real browser measuring real boxes.
//
// Renders the REAL AppShell (rail + DockRegion + chrome) against a FakeClient
// advertising a navigation capability, with AppWire navigation reads scripted
// to a tree far taller than any viewport. window.measureShell() returns the
// document's scroll size beside the viewport, the rail body scroll metrics,
// and the boxes (if any) whose bottoms escape the viewport - which is the
// answer the fix has to be aimed at.
import { createRoot } from "react-dom/client";
import { FakeClient } from "../protocol/testing/fakeClient";
import type { NavigationReadParams, NavigationReadResponse } from "../protocol/types.gen";
import "../styles/tokens.css";
import "../styles/global.css";

window.addEventListener("error", (event) => {
  const target = window as typeof window & { __shellGuardErrors?: string[] };
  target.__shellGuardErrors = [...(target.__shellGuardErrors ?? []), event.error?.stack ?? event.message];
});
window.addEventListener("unhandledrejection", (event) => {
  const target = window as typeof window & { __shellGuardErrors?: string[] };
  target.__shellGuardErrors = [...(target.__shellGuardErrors ?? []), event.reason?.stack ?? String(event.reason)];
});

// 12 projects x 10 sessions, every project expanded by default: ~130 tree
// rows, several times taller than the 900px viewport the guard measures at.
const PROJECT_COUNT = 12;
const SESSION_COUNT = 10;
const projectSummaries = Array.from({ length: PROJECT_COUNT }, (_, p) => {
  const key = `proj${p}`;
  const name = `project-${p}`;
  return {
    key,
    name,
    working_dir: `/home/user/${name}`,
    default_expanded: true,
    session_count: SESSION_COUNT,
  };
});
const navigationSessions = (projectKey: string, projectName: string) =>
  Array.from({ length: SESSION_COUNT }, (_, s) => ({
    ref: `local:${projectKey}-s${s}`,
    host_id: "local",
    session_id: `${projectKey}-s${s}`,
    title: `${projectName} session ${s}`,
    project: projectName,
    state: "idle",
    kind: "session",
    live: false,
    children: [],
  }));

const NAVIGATION_MANIFEST = {
  generation_id: "shellguard-generation",
  revision: 1,
  sources: [],
  attentionSummary: { needsYou: 0, error: 0, working: 0 },
  sections: { live: { count: 0 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
  catalogs: { projects: { count: PROJECT_COUNT }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
};
const EMPTY_SECTION = {
  generation_id: "shellguard-generation",
  revision: 1,
  sessions: [],
  remaining: 0,
  truncated: false,
};
const EMPTY_PIN_CATALOG = {
  generation_id: "shellguard-generation",
  revision: 1,
  pin_sections: [],
  remaining: 0,
};
const EMPTY_PROJECT_CATALOG = {
  generation_id: "shellguard-generation",
  revision: 1,
  projects: projectSummaries,
  remaining: 0,
};
const EMPTY_PIN_SECTION = {
  generation_id: "shellguard-generation",
  revision: 1,
  sessions: [],
  remaining: 0,
  truncated: false,
};

function projectResource(key: string) {
  const summary = projectSummaries.find((p) => p.key === key);
  const sessions = summary ? navigationSessions(key, summary.name) : [];
  const tier = { sessions, remaining: 0 };
  return {
    generation_id: "shellguard-generation",
    revision: 1,
    key,
    current: tier,
    recent: { sessions: [], remaining: 0 },
    archived: { sessions: [], remaining: 0 },
    truncated: false,
  };
}

function navigationRead(params: NavigationReadParams): NavigationReadResponse {
  const response = (data: unknown): NavigationReadResponse => ({
    status: "ok",
    generationId: "shellguard-generation",
    revision: 1,
    etag: '"shellguard-etag"',
    data,
  });
  switch (params.resource) {
    case "manifest":
      return response(NAVIGATION_MANIFEST);
    case "section":
      return response(EMPTY_SECTION);
    case "pin_catalog":
      return response(EMPTY_PIN_CATALOG);
    case "pin_section":
      return response(EMPTY_PIN_SECTION);
    case "catalog":
      return response(EMPTY_PROJECT_CATALOG);
    case "project":
      if (params.projectKey === undefined) throw new Error("project navigation read requires projectKey");
      return response(projectResource(params.projectKey));
    case "project_page":
      return response({
        generation_id: "shellguard-generation",
        revision: 1,
        key: params.projectKey,
        tier: params.tier,
        offset: params.offset,
        sessions: [],
        remaining: 0,
        truncated: false,
      });
    case "location":
      return response({
        generation_id: "shellguard-generation",
        revision: 1,
        ref: params.ref,
        top_level_ref: params.ref,
        top_level: true,
      });
  }
  throw new Error(`unsupported navigation resource: ${params.resource}`);
}

const rootEl = document.getElementById("root");
if (!rootEl) throw new Error("shellguard.html is missing #root");
const root = rootEl;

// The guard's own URL (/shellguard.html) resolves to no pane, which would
// render NotFound and no rail. Stand on the welcome route instead - no
// navigation, just the pathname AppShell's router reads at first render.
window.history.replaceState({}, "", "/");

async function boot(): Promise<void> {
  const { AppShell } = await import("../shell/AppShell");
  const fake = new FakeClient("ready");
  fake.on("evener/navigation/read", navigationRead);
  fake.scriptConnect(() => ({
    serverInfo: { name: "fake-evener-hub", version: "0.0.0" },
    protocolVersion: "evener-appwire-v3",
    sourceId: "fake",
    features: {
      threadList: true,
      threadTurnsList: true,
      turnStart: true,
      turnSteer: true,
      threadClear: true,
      threadShutdown: true,
      forkFromTurn: true,
      tasks: true,
      transcriptList: true,
      modelList: true,
      directoryComplete: true,
      auth: true,
    },
    navigation: { version: 1, generationId: "shellguard-generation", sequence: 0 },
  }));
  createRoot(root).render(<AppShell client={fake} />);
}

interface Box {
  left: number;
  top: number;
  right: number;
  bottom: number;
  width: number;
  height: number;
}

function rect(el: Element): Box {
  const r = el.getBoundingClientRect();
  return { left: r.left, top: r.top, right: r.right, bottom: r.bottom, width: r.width, height: r.height };
}

function describe(el: Element): string {
  const cls = typeof el.className === "string" ? el.className : "";
  return `${el.tagName.toLowerCase()}${cls ? `.${cls.trim().split(/\s+/).join(".")}` : ""}`;
}

// The boxes a page-height bug comes from: elements whose painted bottom sits
// below the viewport and that NO scroll container traps (content clipped by an
// ancestor's overflow cannot grow the document, so a rect past the viewport
// inside the rail's own .body is expected and is not a leak). Reported at the
// source: an element whose parent either stays inside the viewport or is
// itself trapped.
function trappedByScroll(el: Element): boolean {
  for (let p = el.parentElement; p; p = p.parentElement) {
    const oy = getComputedStyle(p).overflowY;
    if (oy !== "visible" && oy !== "clip") return true;
  }
  return false;
}

function leakingElements(viewportBottom: number): { selector: string; top: number; bottom: number; height: number }[] {
  const all: { selector: string; top: number; bottom: number; height: number }[] = [];
  const sources: { selector: string; top: number; bottom: number; height: number }[] = [];
  for (const el of document.querySelectorAll("body *")) {
    const r = el.getBoundingClientRect();
    if (r.bottom <= viewportBottom + 1) continue;
    if (trappedByScroll(el)) continue;
    const record = { selector: describe(el), top: r.top, bottom: r.bottom, height: r.height };
    all.push(record);
    const parent = el.parentElement;
    if (parent && parent.getBoundingClientRect().bottom > viewportBottom + 1 && !trappedByScroll(parent)) continue;
    sources.push(record);
  }
  const leaks = sources.length > 0 ? sources : all;
  leaks.sort((a, b) => b.bottom - a.bottom);
  return leaks.slice(0, 10);
}

function measureShell() {
  const de = document.documentElement;
  const shell = root.firstElementChild;
  // The rail's scroll container: the footer owns data-testid="rail-settings";
  // its parent is the rail root, and the rail's .body is the one child whose
  // overflow-y is not visible.
  const settings = document.querySelector("[data-testid='rail-settings']");
  const rail = settings?.parentElement?.parentElement ?? null;
  const railBody =
    rail === null ? null : ([...rail.children].find((el) => getComputedStyle(el).overflowY !== "visible") ?? null);
  // The rail's full ancestor chain up to <body>, with the computed properties
  // the height chain depends on - a page-height bug IS a broken link in this
  // chain, so this names it instead of leaving it to inference.
  const chain: { selector: string; height: number; computed: Record<string, string> }[] = [];
  for (let el = rail; el && el !== document.body; el = el.parentElement) {
    const cs = getComputedStyle(el);
    chain.push({
      selector: describe(el),
      height: el.getBoundingClientRect().height,
      computed: {
        display: cs.display,
        flexDirection: cs.flexDirection,
        height: cs.height,
        minHeight: cs.minHeight,
        flex: cs.flex,
        overflowY: cs.overflowY,
        alignItems: cs.alignItems,
        position: cs.position,
      },
    });
  }
  // Decisive experiment: which single style change collapses the document
  // back to the viewport? Each entry re-measures documentElement.scrollHeight
  // with one rule overridden, then restores it - the contributor is the one
  // whose override drops the height to ~the viewport's.
  const withStyle = (el: Element | null, prop: string, value: string): number | null => {
    if (el === null) return null;
    const htmlEl = el as HTMLElement;
    const prior = htmlEl.style.getPropertyValue(prop);
    htmlEl.style.setProperty(prop, value);
    const measured = de.scrollHeight;
    if (prior === "") htmlEl.style.removeProperty(prop);
    else htmlEl.style.setProperty(prop, prior);
    return measured;
  };
  const experiments = {
    base: de.scrollHeight,
    railDisplayNone: withStyle(rail, "display", "none"),
    railBodyDisplayNone: withStyle(railBody, "display", "none"),
    railBodyOverflowClip: withStyle(railBody, "overflow", "clip"),
    railOverflowHidden: withStyle(rail, "overflow", "hidden"),
    contentOverflowHidden: withStyle(rail?.parentElement ?? null, "overflow", "hidden"),
    shellOverflowHidden: withStyle(shell, "overflow", "hidden"),
  };
  // Every positioned element under the rail, with the box the browser used to
  // place it (offsetParent). An abspos element whose offsetParent sits at or
  // above the rail escapes the .body clip entirely - the one shape of leak
  // that neither "the chain is constrained" nor ".body scrolls" rules out.
  const positioned = rail
    ? [...rail.querySelectorAll("*")]
        .filter((el) => getComputedStyle(el).position !== "static")
        .map((el) => ({
          selector: describe(el),
          position: getComputedStyle(el).position,
          offsetParent: el instanceof HTMLElement && el.offsetParent ? describe(el.offsetParent) : null,
          box: rect(el),
        }))
        .slice(0, 20)
    : [];
  return {
    viewport: { width: window.innerWidth, height: window.innerHeight },
    document: { scrollWidth: de.scrollWidth, scrollHeight: de.scrollHeight },
    scrollingElement: document.scrollingElement?.tagName ?? null,
    htmlOverflowY: getComputedStyle(de).overflowY,
    body: {
      scrollHeight: document.body.scrollHeight,
      box: rect(document.body),
      overflowY: getComputedStyle(document.body).overflowY,
      children: [...document.body.children].map((el) => ({
        selector: describe(el),
        box: rect(el),
        position: getComputedStyle(el).position,
      })),
    },
    shell: shell === null ? null : rect(shell),
    rail: rail === null ? null : rect(rail),
    railBody:
      railBody === null
        ? null
        : {
            box: rect(railBody),
            clientHeight: railBody.clientHeight,
            scrollHeight: railBody.scrollHeight,
            overflowY: getComputedStyle(railBody).overflowY,
          },
    treeRows: document.querySelectorAll('[role="treeitem"]').length,
    leaks: leakingElements(window.innerHeight),
    chain,
    experiments,
    positioned,
    errors: (window as typeof window & { __shellGuardErrors?: string[] }).__shellGuardErrors ?? [],
  };
}

function scrollMetrics(el: Element | null) {
  if (el === null) return null;
  const htmlEl = el as HTMLElement;
  const computed = getComputedStyle(el);
  return {
    clientHeight: htmlEl.clientHeight,
    scrollHeight: htmlEl.scrollHeight,
    overflowX: computed.overflowX,
    overflowY: computed.overflowY,
    box: rect(el),
  };
}

// The mobile Sheet panel hosting the rail, located from the rail's one
// stable anchor (the settings button's testid). Shared by both mobile
// measurements so the ancestor walk lives in exactly one place.
function mobileRailPanel(): Element | null {
  const settings = document.querySelector("[data-testid='rail-settings']");
  const footer = settings?.parentElement ?? null;
  const rail = footer?.parentElement ?? null;
  const panelBody = rail?.parentElement ?? null;
  return panelBody?.parentElement ?? null;
}

function measureMobileSidebar() {
  const settings = document.querySelector("[data-testid='rail-settings']");
  const footer = settings?.parentElement ?? null;
  const rail = footer?.parentElement ?? null;
  const railBody = footer?.previousElementSibling ?? null;
  const panelBody = rail?.parentElement ?? null;
  const panel = mobileRailPanel();
  const panelText = panel?.textContent ?? "";
  const errors = (window as typeof window & { __shellGuardErrors?: string[] }).__shellGuardErrors ?? [];

  return {
    viewport: { width: window.innerWidth, height: window.innerHeight },
    document: {
      scrollWidth: document.documentElement.scrollWidth,
      scrollHeight: document.documentElement.scrollHeight,
    },
    panel: scrollMetrics(panel),
    panelBody: scrollMetrics(panelBody),
    rail: scrollMetrics(rail),
    railBody: scrollMetrics(railBody),
    searchBox: panel?.querySelector('input[type="search"]') !== null,
    resume: panelText.includes("Jump back in"),
    hints:
      panelText.includes("command palette") ||
      panelText.includes("focus the composer") ||
      panelText.includes("next session needing you") ||
      panelText.includes("shows all shortcuts"),
    orientation: panelText.includes("read and edit the repository"),
    errors,
  };
}

const booted = boot();

// Interactive elements inside the MOBILE rail panel (the session list a phone
// user taps through) whose rendered box falls under the platform tap floor.
// Zero-box elements (display:none / visibility:hidden at measure time) are not
// targets and are excluded. Both dimensions are reported; a target fails when
// EITHER is under the floor - a 44px-tall row whose "⋯" button is 20px wide is
// still a miss (Apple HIG/WCAG 2.5.5's 44x44).
const TAP_TARGET_MIN = 44;

function measureTapTargets() {
  const panel = mobileRailPanel();
  const interactive = panel
    ? [...panel.querySelectorAll('button, a[href], input, [role="treeitem"], [role="button"]')]
    : [];
  const offenders = interactive
    .filter((el) => {
      const r = el.getBoundingClientRect();
      if (r.width === 0 || r.height === 0) return false;
      return r.width < TAP_TARGET_MIN || r.height < TAP_TARGET_MIN;
    })
    .map((el) => ({ selector: describe(el), box: rect(el) }));
  return { min: TAP_TARGET_MIN, measured: interactive.length, offenders };
}

const target = window as typeof window & {
  settledShell: Promise<unknown>;
  measureShell: typeof measureShell;
  measureMobileSidebar: typeof measureMobileSidebar;
  measureTapTargets: typeof measureTapTargets;
};
target.measureShell = measureShell;
target.measureMobileSidebar = measureMobileSidebar;
target.measureTapTargets = measureTapTargets;
target.settledShell = (async () => {
  await booted;
  const deadline = performance.now() + 15_000;
  const expectedRows = PROJECT_COUNT * (1 + SESSION_COUNT); // every project row plus every expanded session row
  for (;;) {
    // Every project row plus every session row is in the accessibility tree
    // only once the rail has rendered the full expanded tree.
    if (document.querySelectorAll('[role="treeitem"]').length >= expectedRows) return true;
    const errors = (window as typeof window & { __shellGuardErrors?: string[] }).__shellGuardErrors;
    if (errors && errors.length > 0) throw new Error(`shell harness page errors: ${errors.join("\n")}`);
    if (performance.now() > deadline) {
      throw new Error(
        `shell harness: expected at least ${expectedRows} tree rows, found ${document.querySelectorAll('[role="treeitem"]').length} ` +
          `(rail settings button: ${document.querySelector("[data-testid='rail-settings']") !== null}, ` +
          `body children: ${[...document.body.children].map((el) => el.tagName).join(",")}, ` +
          `root children: ${root.children.length}, pathname: ${window.location.pathname})`,
      );
    }
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
})();
