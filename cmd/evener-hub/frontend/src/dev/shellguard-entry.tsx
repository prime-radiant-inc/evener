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
import { navigationInvalidatedNotification } from "../protocol/testing/notifications";
import type { NavigationReadBase, NavigationReadParams, NavigationReadResponse } from "../protocol/types.gen";
import railStyles from "../shell/rail/Rail.module.css";
import { RailRenderObserver } from "../shell/rail/railRenderObserver";
import {
  navigationOwnedContainerKey,
  navigationRootContainerKey,
  navigationViewScope,
  type ResourceKey,
} from "../stores/navigation/types";
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
  const key = `p${p}`;
  const name = `project-${p}`;
  return {
    key,
    name,
    working_dir: `/home/user/${name}`,
    default_expanded: true,
    session_count: SESSION_COUNT,
  };
});
const NAVIGATION_MANIFEST = {
  generation_id: "shellguard-generation",
  revision: 1,
  sources: [],
  attentionSummary: { needsYou: 0, error: 0, working: 0 },
  sections: { live: { count: 0 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
  catalogs: { projects: { count: PROJECT_COUNT }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
};

let changedTitle = "project-0 session 0";
let mutationRevision = 1;
const renderCounts = new Map<string, number>();
let shellClient: FakeClient | null = null;
let visibleRowIDs: string[] = [];
let changedObserverRowID: string | null = null;

function sessionValue(projectKey: string, projectName: string, index: number) {
  return {
    ref: `local:${projectKey}-s${index}`,
    host_id: "local",
    session_id: `${projectKey}-s${index}`,
    title: projectKey === "p0" && index === 0 ? changedTitle : `${projectName} session ${index}`,
    project: projectName,
    state: "idle",
    kind: "session",
    live: false,
    children: [],
  };
}

function snapshot(metadata: unknown, entities: unknown[], containers: unknown[]) {
  return { metadata, entities, containers };
}

async function navigationEntityKey(key: ResourceKey, kind: string, logicalIdentity: string): Promise<string> {
  const source = new TextEncoder().encode(`${kind}\0${logicalIdentity}`);
  const digest = await crypto.subtle.digest("SHA-256", source);
  const hex = [...new Uint8Array(digest)].map((byte) => byte.toString(16).padStart(2, "0")).join("");
  return `${navigationViewScope(key)}/entity/${hex}`;
}

function effectiveLimit(key: ResourceKey): number {
  if (!("limit" in key)) return 0;
  const maximum = key.kind === "pin_catalog" || key.kind === "catalog" ? 100 : 50;
  return key.limit === 0 || key.limit > maximum ? maximum : key.limit;
}

function resourceKey(params: NavigationReadParams): ResourceKey {
  const offset = params.offset ?? 0;
  const limit = params.limit ?? 0;
  switch (params.resource) {
    case "manifest":
      return { kind: "manifest" };
    case "section":
      if (params.section !== "live" && params.section !== "needs_you")
        throw new Error("section navigation read requires section");
      return { kind: "section", section: params.section, offset, limit };
    case "pin_catalog":
      return { kind: "pin_catalog", offset, limit };
    case "pin_section":
      if (!params.sectionId) throw new Error("pin-section navigation read requires sectionId");
      return { kind: "pin_section", sectionId: params.sectionId, offset, limit };
    case "catalog":
      if (params.catalog !== "projects" && params.catalog !== "archived_projects" && params.catalog !== "test_runs")
        throw new Error("catalog navigation read requires catalog");
      return { kind: "catalog", catalog: params.catalog, offset, limit };
    case "project":
      if (!params.projectKey) throw new Error("project navigation read requires projectKey");
      return { kind: "project", projectKey: params.projectKey };
    case "project_page":
      if (!params.projectKey || (params.tier !== "current" && params.tier !== "recent" && params.tier !== "archived"))
        throw new Error("project-page navigation read requires projectKey and tier");
      return { kind: "project_page", projectKey: params.projectKey, tier: params.tier, offset, limit };
    case "location":
      if (!params.ref) throw new Error("location navigation read requires ref");
      return { kind: "location", ref: params.ref };
    default:
      throw new Error(`unsupported navigation resource: ${params.resource}`);
  }
}

async function sessionSnapshot(key: Extract<ResourceKey, { kind: "project" }>, projectName: string) {
  const projectEntityKey = await navigationEntityKey(key, "project", key.projectKey);
  const entities = await Promise.all(
    Array.from({ length: SESSION_COUNT }, async (_, index) => ({
      key: await navigationEntityKey(key, "session", `local:${key.projectKey}-s${index}`),
      kind: "session",
      value: sessionValue(key.projectKey, projectName, index),
    })),
  );
  return snapshot(
    {
      generation_id: "shellguard-generation",
      revision: mutationRevision,
      key: key.projectKey,
      current_remaining: 0,
      recent_remaining: 0,
      archived_remaining: 0,
      truncated: false,
    },
    [{ key: projectEntityKey, kind: "project", value: { key: key.projectKey } }, ...entities],
    [
      {
        key: navigationOwnedContainerKey(projectEntityKey, "current"),
        owner: { kind: "entity", entityKey: projectEntityKey, slot: "current" },
        children: entities.map((entity) => entity.key),
      },
      {
        key: navigationOwnedContainerKey(projectEntityKey, "recent"),
        owner: { kind: "entity", entityKey: projectEntityKey, slot: "recent" },
        children: [],
      },
      {
        key: navigationOwnedContainerKey(projectEntityKey, "archived"),
        owner: { kind: "entity", entityKey: projectEntityKey, slot: "archived" },
        children: [],
      },
      ...entities.map((entity) => ({
        key: navigationOwnedContainerKey(entity.key, "children"),
        owner: { kind: "entity", entityKey: entity.key, slot: "children" },
        children: [],
      })),
    ],
  );
}

async function catalogSnapshot(key: Extract<ResourceKey, { kind: "catalog" }>) {
  const projects = key.catalog === "projects" ? projectSummaries : [];
  const entities = await Promise.all(
    projects.map(async (project) => ({
      key: await navigationEntityKey(key, "project", project.key),
      kind: "project",
      value: project,
    })),
  );
  return snapshot(
    {
      generation_id: "shellguard-generation",
      revision: mutationRevision,
      offset: key.offset,
      limit: effectiveLimit(key),
      remaining: 0,
    },
    entities,
    [
      {
        key: navigationRootContainerKey(key, "projects"),
        owner: { kind: "resource_root", slot: "projects" },
        children: entities.map((entity) => entity.key),
      },
    ],
  );
}

function emptySnapshot(key: ResourceKey) {
  let metadata: Record<string, unknown>;
  let slot: string;
  switch (key.kind) {
    case "section":
    case "pin_section":
      metadata = {
        generation_id: "shellguard-generation",
        revision: mutationRevision,
        offset: key.offset,
        limit: effectiveLimit(key),
        remaining: 0,
        truncated: false,
      };
      slot = "sessions";
      break;
    case "pin_catalog":
      metadata = {
        generation_id: "shellguard-generation",
        revision: mutationRevision,
        offset: key.offset,
        limit: effectiveLimit(key),
        remaining: 0,
      };
      slot = "pin_sections";
      break;
    case "project_page":
      metadata = {
        generation_id: "shellguard-generation",
        revision: mutationRevision,
        key: key.projectKey,
        tier: key.tier,
        offset: key.offset,
        limit: effectiveLimit(key),
        remaining: 0,
        truncated: false,
      };
      slot = "sessions";
      break;
    case "location":
      metadata = {
        generation_id: "shellguard-generation",
        revision: mutationRevision,
        ref: key.ref,
        top_level_ref: key.ref,
        top_level: true,
      };
      slot = "session";
      break;
    default:
      throw new Error(`shellguard has no empty snapshot for ${key.kind}`);
  }
  return snapshot(
    metadata,
    [],
    [
      {
        key: navigationRootContainerKey(key, slot),
        owner: { kind: "resource_root", slot },
        children: [],
      },
    ],
  );
}

async function projectDelta(key: Extract<ResourceKey, { kind: "project" }>) {
  return {
    metadata: {
      generation_id: "shellguard-generation",
      revision: mutationRevision,
      key: key.projectKey,
      current_remaining: 0,
      recent_remaining: 0,
      archived_remaining: 0,
      truncated: false,
    },
    upsertedEntities: [
      {
        key: await navigationEntityKey(key, "session", "local:p0-s0"),
        kind: "session",
        value: sessionValue("p0", "project-0", 0),
      },
    ],
    removedEntityKeys: [],
    upsertedContainers: [],
    removedContainerKeys: [],
  };
}
async function navigationRead(params: NavigationReadParams): Promise<NavigationReadResponse> {
  if (params.representationVersion !== 2) throw new Error("shellguard expected v2 navigation reads");
  const key = resourceKey(params);
  const v2 = (
    data: unknown,
    representation: "snapshot" | "delta" = "snapshot",
    base?: NavigationReadBase,
  ): NavigationReadResponse => ({
    status: "ok",
    representation,
    generationId: "shellguard-generation",
    revision: mutationRevision,
    etag: `"shellguard-${mutationRevision}"`,
    data,
    ...(representation === "delta" ? { base } : {}),
  });
  switch (key.kind) {
    case "manifest":
      return v2(
        snapshot(
          { ...NAVIGATION_MANIFEST, revision: mutationRevision },
          [],
          [
            {
              key: navigationRootContainerKey(key, "manifest"),
              owner: { kind: "resource_root", slot: "manifest" },
              children: [],
            },
          ],
        ),
      );
    case "section":
      return v2(emptySnapshot(key));
    case "pin_catalog":
      return v2(emptySnapshot(key));
    case "pin_section":
      return v2(emptySnapshot(key));
    case "catalog":
      return v2(await catalogSnapshot(key));
    case "project": {
      if (mutationRevision > 1 && params.base?.revision === 1 && key.projectKey === "p0")
        return v2(await projectDelta(key), "delta", params.base);
      const summary = projectSummaries.find((project) => project.key === key.projectKey);
      return v2(await sessionSnapshot(key, summary?.name ?? key.projectKey));
    }
    case "project_page":
      return v2(emptySnapshot(key));
    case "location":
      return v2(emptySnapshot(key));
  }
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
  shellClient = fake;
  fake.scriptConnect(() => ({
    serverInfo: { name: "fake-evener-hub", version: "0.0.0" },
    protocolVersion: "evener-appwire-v4",
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
    navigation: { version: 1, readVersions: [2], generationId: "shellguard-generation", sequence: 0 },
  }));
  createRoot(root).render(
    <RailRenderObserver value={(id) => renderCounts.set(id, (renderCounts.get(id) ?? 0) + 1)}>
      <AppShell client={fake} />
    </RailRenderObserver>,
  );
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
  // Select the rail directly, independently of header control placement.
  const rail = document.querySelector(`.${railStyles.rail}`);
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

// The mobile Sheet panel hosting the rail. Shared by both mobile
// measurements so the ancestor walk lives in exactly one place.
function mobileRailPanel(): Element | null {
  const rail = document.querySelector(`.${railStyles.rail}`);
  const panelBody = rail?.parentElement ?? null;
  return panelBody?.parentElement ?? null;
}

function measureMobileSidebar() {
  const rail = document.querySelector(`.${railStyles.rail}`);
  const railBody = rail?.querySelector(`.${railStyles.body}`) ?? null;
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
  applyShellNavigationDelta: () => Promise<unknown>;
  measureRailRenderCounts: () => {
    counts: Record<string, number>;
    changedRowID: string | null;
    visibleRowIDs: string[];
    document: { scrollHeight: number; viewportHeight: number };
  };
};
target.measureShell = measureShell;
target.measureMobileSidebar = measureMobileSidebar;
target.measureTapTargets = measureTapTargets;
target.measureRailRenderCounts = () => ({
  counts: Object.fromEntries(renderCounts),
  changedRowID: changedObserverRowID,
  visibleRowIDs,
  document: { scrollHeight: document.documentElement.scrollHeight, viewportHeight: window.innerHeight },
});
target.applyShellNavigationDelta = async () => {
  if (!shellClient) throw new Error("shellguard client is not ready");
  visibleRowIDs = [...renderCounts.keys()];
  const expectedChangedRowID = await navigationEntityKey(
    { kind: "project", projectKey: "p0" },
    "session",
    "local:p0-s0",
  );
  changedObserverRowID = visibleRowIDs.find((id) => id === expectedChangedRowID) ?? null;
  if (changedObserverRowID === null) {
    throw new Error(`shellguard changed row was not observed before delta: ${expectedChangedRowID}`);
  }
  changedTitle = "project-0 session 0 changed";
  mutationRevision = 2;
  // Preserve the visible observer IDs above; clear only invocation counts and
  // do it immediately before publishing the one-entity delta.
  renderCounts.clear();
  shellClient.emitNotification(
    navigationInvalidatedNotification({
      generationId: "shellguard-generation",
      sequence: 1,
      targets: [{ kind: "project", projectKey: "p0", revision: 2 }],
    }),
  );
  const deadline = performance.now() + 15_000;
  for (;;) {
    if (document.body.textContent?.includes(changedTitle)) return true;
    if (performance.now() > deadline) throw new Error("shellguard delta did not converge");
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
};
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
