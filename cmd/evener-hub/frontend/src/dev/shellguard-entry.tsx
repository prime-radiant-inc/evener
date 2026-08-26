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
// with /api/tree stubbed to a tree far taller than any viewport, the same
// shape AppShell.test.tsx's own fixtures use. window.measureShell() returns
// the document's scroll size beside the viewport, the rail body scroll
// metrics, and the boxes (if any) whose bottoms escape the viewport - which
// is the answer the fix has to be aimed at.
import { createRoot } from "react-dom/client";
import { FakeClient } from "../protocol/testing/fakeClient";
import { type TreeNode, type TreeProject, type TreeResponse, treeStore } from "../stores/tree";
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

function session(projectKey: string, projectName: string, index: number): TreeNode {
  return {
    row_id: `project:${projectKey}:local:s${index}`,
    ref: `local:${projectKey}-s${index}`,
    host_id: "local",
    session_id: `${projectKey}-s${index}`,
    title: `${projectName} session ${index}`,
    project: projectName,
    state: "idle",
    kind: "session",
    live: true,
    children: [],
  };
}

// 12 projects x 10 sessions, every project expanded by default: ~130 tree
// rows, several times taller than the 900px viewport the guard measures at.
const PROJECT_COUNT = 12;
const SESSION_COUNT = 10;
const projects: TreeProject[] = Array.from({ length: PROJECT_COUNT }, (_, p) => {
  const key = `proj${p}`;
  const name = `project-${p}`;
  return {
    key,
    name,
    working_dir: `/home/user/${name}`,
    default_expanded: true,
    sessions: Array.from({ length: SESSION_COUNT }, (_, s) => session(key, name, s)),
  };
});

const TALL_TREE: TreeResponse = {
  generated_at: "2026-01-01T00:00:00Z",
  sources: [],
  live: [],
  needs_you: [],
  pin_sections: [],
  projects,
  archived_projects: [],
  test_runs: [],
  attentionSummary: { needsYou: 0, error: 0, working: 0 },
};

// treeStore reads /api/tree over plain fetch (not the appwire socket), so the
// stub lives on window.fetch. Installed at module evaluation, before the
// AppShell render below can fire its first refresh. The navigation store reads
// /api/navigation/* the same way; stubbing those routes lets the harness
// exercise the v1 bounded-resource path rather than falling back to legacy
// /api/tree mode (store.ts: enterLegacyMode when the client reports no
// navigation capability).
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
  projects: projects.map((p) => ({
    key: p.key,
    name: p.name,
    working_dir: p.working_dir,
    default_expanded: p.default_expanded,
    session_count: SESSION_COUNT,
  })),
  remaining: 0,
};

function navigationResponse(url: string): Response | null {
  if (url.startsWith("/api/navigation/catalogs/projects")) {
    return new Response(JSON.stringify(EMPTY_PROJECT_CATALOG), {
      status: 200,
      headers: { "content-type": "application/json" },
    });
  }
  if (url.startsWith("/api/navigation/sections/live") || url.startsWith("/api/navigation/sections/needs-you")) {
    return new Response(JSON.stringify(EMPTY_SECTION), {
      status: 200,
      headers: { "content-type": "application/json" },
    });
  }
  if (url.startsWith("/api/navigation/pin-sections")) {
    return new Response(JSON.stringify(EMPTY_PIN_CATALOG), {
      status: 200,
      headers: { "content-type": "application/json" },
    });
  }
  if (url === "/api/navigation" || url === "/api/navigation/") {
    return new Response(JSON.stringify(NAVIGATION_MANIFEST), {
      status: 200,
      headers: { "content-type": "application/json", etag: '"shellguard-etag"' },
    });
  }
  return null;
}

const realFetch = window.fetch.bind(window);
window.fetch = (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
  const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
  const nav = navigationResponse(url);
  if (nav) return Promise.resolve(nav);
  if (url.startsWith("/api/tree")) {
    return Promise.resolve(
      new Response(JSON.stringify(TALL_TREE), { status: 200, headers: { "content-type": "application/json" } }),
    );
  }
  if (url.startsWith("/api/")) {
    return Promise.resolve(
      new Response(JSON.stringify({}), { status: 200, headers: { "content-type": "application/json" } }),
    );
  }
  return realFetch(input, init);
};

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

const booted = boot();

const target = window as typeof window & {
  settledShell: Promise<unknown>;
  measureShell: typeof measureShell;
};
target.measureShell = measureShell;
target.settledShell = (async () => {
  await booted;
  const deadline = performance.now() + 15_000;
  const expectedRows = PROJECT_COUNT * SESSION_COUNT;
  for (;;) {
    // Every project row plus every session row is in the accessibility tree
    // only once the rail has rendered the full expanded tree.
    if (document.querySelectorAll('[role="treeitem"]').length >= expectedRows) return true;
    const errors = (window as typeof window & { __shellGuardErrors?: string[] }).__shellGuardErrors;
    if (errors && errors.length > 0) throw new Error(`shell harness page errors: ${errors.join("\n")}`);
    if (performance.now() > deadline) {
      const treeState = treeStore.getState();
      throw new Error(
        `shell harness: expected at least ${expectedRows} tree rows, found ${document.querySelectorAll('[role="treeitem"]').length} ` +
          `(tree loaded: ${treeState.tree !== null}, error: ${treeState.error ?? "none"}, ` +
          `rail settings button: ${document.querySelector("[data-testid='rail-settings']") !== null}, ` +
          `body children: ${[...document.body.children].map((el) => el.tagName).join(",")}, ` +
          `root children: ${root.children.length}, pathname: ${window.location.pathname})`,
      );
    }
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
})();
