// @vitest-environment node

import type { EvenerWatchInfo, Source } from "@evener/appwire-client";
import { describe, expect, test } from "vitest";
import type { HostRailNode, RailNode, RailPinSection, RailProject, RailSession, SessionRailNode } from "./railNodes";
import {
  activeWatchCount,
  archivedCount,
  archivedProjectNodes,
  archivedSessionGroups,
  displayState,
  hostProjectNodes,
  liveNodesGroupedByHost,
  overrideLookup,
  pinSectionDisclosureID,
  projectNodes,
  projectNodesWithHostBranches,
  revealExpansionIds,
  sessionNodes,
  subagentIsCurrent,
  watchCountLabel,
} from "./railNodes";

describe("subagentIsCurrent", () => {
  test("a descendant in any current state makes the subagent current, not only an active one", () => {
    // The direct level accepts the whole current set (awaiting counts); the
    // subtree must read the same set, or the Agents tab folds an idle
    // subagent whose own child is waiting on the user.
    const grandchild = session({ ref: "local:grandchild", state: "awaiting" });
    const child = session({ ref: "local:child", state: "idle", children: [grandchild] });
    expect(subagentIsCurrent(child)).toBe(true);
  });

  test("a descendant's running job makes the subagent current", () => {
    const grandchild = session({
      ref: "local:grandchild",
      state: "idle",
      running_job_count: 1,
    });
    const child = session({ ref: "local:child", state: "idle", children: [grandchild] });
    expect(subagentIsCurrent(child)).toBe(true);
  });

  test("an idle subagent with a quiet subtree is not current", () => {
    const grandchild = session({ ref: "local:grandchild", state: "idle" });
    const child = session({ ref: "local:child", state: "idle", children: [grandchild] });
    expect(subagentIsCurrent(child)).toBe(false);
  });

  test("approval_pending makes an idle subagent current (it displays as awaiting)", () => {
    // displayState folds approval_pending into "awaiting" - the rail's badge
    // counts such a node as needs-you, so the activity surfaces must not
    // fold it as inactive about the same node.
    expect(subagentIsCurrent(session({ state: "idle", approval_pending: true }))).toBe(true);
  });

  test("a descendant's approval_pending makes the subagent current", () => {
    const grandchild = session({ ref: "local:grandchild", state: "idle", approval_pending: true });
    const child = session({ ref: "local:child", state: "idle", children: [grandchild] });
    expect(subagentIsCurrent(child)).toBe(true);
  });

  test("an errored subagent is current (a failure needs the user more than a warning does)", () => {
    // The fold hides work that is done; an errored subagent is not done in
    // that sense - its row paints danger, and warning (the less severe
    // signal) is already current.
    expect(subagentIsCurrent(session({ state: "errored" }))).toBe(true);
  });

  test("a descendant's errored state makes the subagent current", () => {
    const grandchild = session({ ref: "local:grandchild", state: "errored" });
    const child = session({ ref: "local:child", state: "idle", children: [grandchild] });
    expect(subagentIsCurrent(child)).toBe(true);
  });
});

function session(overrides: Partial<RailSession> = {}): RailSession {
  return {
    row_id: "navigation:local:a",
    ref: "local:a",
    host_id: "local",
    session_id: "a",
    title: "Session A",
    project: "Proj",
    state: "idle",
    kind: "session",
    live: true,
    children: [],
    ...overrides,
  };
}
function project(overrides: Partial<RailProject> = {}): RailProject {
  return { key: "p1", name: "Proj", sessions: [], ...overrides };
}
function watch(overrides: Partial<EvenerWatchInfo> = {}): EvenerWatchInfo {
  return {
    id: "w1",
    source: "self",
    deliveries: 0,
    createdAt: "2026-09-12T19:00:00Z",
    active: true,
    ...overrides,
  };
}
const closed = () => false;
// The archived builders' launch-host resolution needs the manifest's
// sources; the cache-and-children tests below don't care about hosts, so
// they pass the empty manifest shape.
const NO_SOURCES: readonly Source[] = [];

test("adapts resource-local session summaries into stable flat rail rows", () => {
  const rows = sessionNodes([session({ row_id: "navigation:parent", ref: "parent" })]);
  expect(rows[0]).toMatchObject({ id: "navigation:parent", kind: "session", session: { ref: "parent" } });
});

test("a summary's nested sessions, jobs, and watches build no rail rows", () => {
  // The rail's rows are flat top-level rows with gloss and chips: a session's
  // subagents live in the activity sidebar's Agents tab and its jobs and
  // watches on the row's own summary figures, so nothing nests anymore.
  const root = session({
    ref: "root",
    row_id: "root",
    state: "active",
    children: [
      session({ ref: "done", row_id: "done", kind: "fork", state: "ended" }),
      session({ ref: "working", row_id: "working", kind: "subagent", state: "active" }),
    ],
    running_job_count: 1,
    watch_count: [watch({ id: "w1" })].length + 0,
    armed_watch_count: [watch({ id: "w1" })].filter((w) => w.active).length + 0,
  });
  const [node] = sessionNodes([root]);
  expect(node?.children).toEqual([]);
});

test("session node identity tracks the session object, per tier shape", () => {
  const unchangedSibling = session({ row_id: "sibling", ref: "sibling", title: "Sibling", state: "active" });
  const parent = session({ row_id: "parent", ref: "parent", state: "active" });
  const before = sessionNodes([parent, unchangedSibling]);
  const repeated = sessionNodes([parent, unchangedSibling]);

  expect(repeated[0]).toBe(before[0]);
  expect(repeated[0]?.children).toBe(before[0]?.children);
  expect(repeated[1]).toBe(before[1]);

  const changedParent = { ...parent, title: "After" };
  const after = sessionNodes([changedParent, unchangedSibling]);

  expect(after[0]).not.toBe(before[0]);
  expect(after[0]).toMatchObject({ session: { title: "After" } });
  expect(after[1]).toBe(before[1]);
});

test("sessionNodes marks its rows as cross-project tier roots; projects do not", () => {
  const root = session({ ref: "root", row_id: "root" });
  const [live] = sessionNodes([root]);
  expect(live?.crossProjectTier).toBe(true);
  // A Projects-tier session always nests under its own ProjectRow, which
  // already names the project - no cross-project line there. The mark splits
  // the node cache too: one session renders as both shapes without either
  // serving the other's cached node.
  const [projectRow] = projectNodes([project({ key: "p", sessions: [root] })], closed);
  const nested = projectRow?.children.find((child): child is SessionRailNode => child.kind === "session");
  expect(nested?.crossProjectTier).toBeUndefined();
  expect(nested).not.toBe(live);
});

describe("resource projection semantics", () => {
  test("preserves authoritative global/pin order and pin disclosure identity", () => {
    const section: RailPinSection = {
      id: "opaque",
      name: "Research",
      member_count: 2,
      sessions: [session({ ref: "a" }), session({ ref: "b" })],
    };
    expect(section.sessions.map((row) => row.ref)).toEqual(["a", "b"]);
    expect(pinSectionDisclosureID(section.id)).toBe("pinsection:opaque");
  });

  test("displayState passes a plain awaiting session through as needing you", () => {
    expect(displayState(session({ state: "awaiting" }))).toBe("awaiting");
  });
  // An approval blocks its turn mid-tool, so the row's wire state stays
  // "active"; approval_pending is the only thing saying a person is needed.
  test("an approval presents as needing you on any state but a failure", () => {
    expect(displayState(session({ state: "active", approval_pending: true }))).toBe("awaiting");
    expect(displayState(session({ state: "idle", approval_pending: true }))).toBe("awaiting");
    expect(displayState(session({ state: "errored", approval_pending: true }))).toBe("errored");
  });
  test("a project's session waiting on an approval sorts ahead of its working sibling", () => {
    const working = session({ row_id: "working", ref: "working", state: "active" });
    const approval = session({ row_id: "approval", ref: "approval", state: "active", approval_pending: true });
    const [node] = projectNodes([project({ sessions: [working, approval] })], closed);
    expect(node?.children.map((child) => child.id)).toEqual(["approval", "working"]);
  });
  test("projects current/recent rows and deterministic bounded overflow pages", () => {
    const rows = [
      session({ row_id: "current", ref: "current", tier: "current" }),
      session({ row_id: "recent", ref: "recent", tier: "recent" }),
    ];
    const [node] = projectNodes([project({ sessions: rows, more_current: 7, more_recent: 5 })], closed);
    const overflow = node?.children.at(-1);
    expect(node?.children.map((child) => child.id)).toEqual(["current", "recent", "projectnode:p1:overflow"]);
    expect(overflow).toMatchObject({ kind: "overflow", count: 12 });
    expect(overflow && "pages" in overflow ? overflow.pages[0] : undefined).toMatchObject({ offset: 1, limit: 7 });
  });
  test("gives an unloaded nonempty project a branch while its root loads", () => {
    const [node] = projectNodes([project({ session_count: 2 })], closed);
    expect(node?.children).toEqual([{ id: "projectnode:p1:loading", kind: "loading" }]);
  });
  test("diverts archived sessions and preserves same-name project labels", () => {
    const a = project({
      key: "a",
      name: "frontend",
      working_dir: "/repoA/frontend",
      sessions: [session({ tier: "archived", ref: "old-a", row_id: "navigation:old-a" })],
    });
    const b = project({ key: "b", name: "frontend", working_dir: "/repoB/frontend" });
    expect(projectNodes([a, b], closed).map((row) => row.displayName)).toEqual([
      "frontend (repoA)",
      "frontend (repoB)",
    ]);
    expect(archivedSessionGroups([a], closed, NO_SOURCES)[0]?.children[0]).toMatchObject({ id: "navigation:old-a" });
  });
  test("counts archived rows plus archived remainder in active and test projects", () => {
    const active = project({ sessions: [session({ tier: "archived" })], more_archived: 4 });
    expect(archivedCount([], [active])).toBe(5);
  });
  test("keeps archived stubs visible with a loading child and hydrated roots projectable", () => {
    const stub = project({ key: "archived", is_archived: true, session_count: 3 });
    expect(archivedProjectNodes([stub], new Map(), closed, NO_SOURCES)[0]?.children[0]).toMatchObject({
      kind: "loading",
    });
    const hydrated = project({
      ...stub,
      sessions: [session({ ref: "old", row_id: "navigation:old" })],
      more_archived: 2,
    });
    expect(
      archivedProjectNodes([stub], new Map([["archived", hydrated]]), closed, NO_SOURCES)[0]?.children.map(
        (row) => row.id,
      ),
    ).toEqual(["navigation:old", "projectnode:archived:overflow"]);
    expect(archivedCount([stub], [])).toBe(3);
    expect(archivedCount([hydrated], [])).toBe(3);
  });
  test("memoizes project and archived builders from complete child dependencies", () => {
    const active = project({
      key: "active",
      sessions: [session({ ref: "current", row_id: "current", state: "active" })],
    });
    const archivedInActive = project({
      key: "mixed",
      sessions: [session({ ref: "old", row_id: "old", tier: "archived" })],
    });
    const archivedStub = project({ key: "archived", is_archived: true, session_count: 1 });
    const archivedSibling = project({ key: "archived-sibling", is_archived: true, session_count: 1 });
    const archivedDetail = project({
      key: "archived",
      is_archived: true,
      sessions: [session({ ref: "detail", row_id: "detail", title: "Before" })],
    });
    const siblingDetail = project({
      key: "archived-sibling",
      is_archived: true,
      sessions: [session({ ref: "sibling-detail", row_id: "sibling-detail" })],
    });
    const details = new Map([
      [archivedStub.key, archivedDetail],
      [archivedSibling.key, siblingDetail],
    ]);

    const activeBefore = projectNodes([active], closed)[0];
    const activeRepeated = projectNodes([active], closed)[0];
    expect(activeRepeated).toBe(activeBefore);
    expect(activeRepeated?.children).toBe(activeBefore?.children);

    const groupBefore = archivedSessionGroups([archivedInActive], closed, NO_SOURCES)[0];
    const groupRepeated = archivedSessionGroups([archivedInActive], closed, NO_SOURCES)[0];
    expect(groupRepeated).toBe(groupBefore);
    expect(groupRepeated?.children).toBe(groupBefore?.children);

    const archivedBefore = archivedProjectNodes([archivedStub, archivedSibling], details, closed, NO_SOURCES);
    const archivedRepeated = archivedProjectNodes([archivedStub, archivedSibling], details, closed, NO_SOURCES);
    expect(archivedRepeated[0]).toBe(archivedBefore[0]);
    expect(archivedRepeated[0]?.children).toBe(archivedBefore[0]?.children);
    expect(archivedRepeated[1]).toBe(archivedBefore[1]);

    const changedDetail = {
      ...archivedDetail,
      sessions: [{ ...archivedDetail.sessions[0], title: "After" } as RailSession],
    };
    const archivedAfter = archivedProjectNodes(
      [archivedStub, archivedSibling],
      new Map([
        [archivedStub.key, changedDetail],
        [archivedSibling.key, siblingDetail],
      ]),
      closed,
      NO_SOURCES,
    );
    expect(archivedAfter[0]).not.toBe(archivedBefore[0]);
    expect(archivedAfter[0]?.children[0]).toMatchObject({ session: { title: "After" } });
    expect(archivedAfter[1]).toBe(archivedBefore[1]);
    expect(archivedAfter[1]?.children).toBe(archivedBefore[1]?.children);
  });
  test("uses persisted overrides and flat membership for deep-link reveal", () => {
    const p = project({ key: "p1", sessions: [session({ ref: "top", row_id: "top" })] });
    expect(overrideLookup(new Map([["x", true]]))("x", false)).toBe(true);
    expect(revealExpansionIds([p], [], "top", "flat")).toEqual(["projectnode:p1"]);
    expect(revealExpansionIds([p], [], "missing", "flat")).toEqual([]);
  });
});

// Host grouping (the rail's organize-by setting): the pure reshaping of the
// same project/session nodes over the manifest's sources. Host-first makes
// hosts the top groups; project-first inserts host branches inside each
// project; the Live section groups under host subheaders whenever its rows
// span more than one host. No wire data beyond session host_id and each
// project's sources.
describe("host grouping (organize by)", () => {
  const sources: Source[] = [
    { id: "local", label: "this host", kind: "local", online: true },
    { id: "devbox", label: "devbox", kind: "appwire", online: true },
    { id: "render-farm", label: "render-farm", kind: "appwire", online: true },
    { id: "ci-runner", label: "ci-runner", kind: "appwire", online: false },
  ];
  const on = (host: string, ref: string, overrides: Partial<RailSession> = {}) =>
    session({
      row_id: `navigation:${host}:${ref}`,
      ref: `${host}:${ref}`,
      session_id: ref,
      host_id: host,
      ...overrides,
    });
  // The grouped-row assertions read children ids through kind guards (a
  // plain "children" in check cannot narrow the base type's optional
  // property), so the guard itself lives here once.
  const childIds = (node: RailNode | undefined): string[] =>
    (node?.kind === "project" || node?.kind === "host" ? node.children : []).map((child) => child.id);

  test("hostProjectNodes orders hosts this host first, then online alphabetically, offline last, and hides empty hosts", () => {
    const render = project({ key: "render", sources: ["render-farm"], sessions: [on("render-farm", "r1")] });
    const ci = project({ key: "ci", sources: ["ci-runner"], sessions: [on("ci-runner", "c1")] });
    const dev = project({ key: "dev", sources: ["devbox"], sessions: [on("devbox", "d1")] });
    const home = project({ key: "home", sources: ["local"], sessions: [on("local", "h1")] });
    const nodes = hostProjectNodes([render, ci, dev, home], sources, closed);
    expect(nodes.map((node) => node.id)).toEqual(["host:local", "host:devbox", "host:render-farm", "host:ci-runner"]);
  });

  test("hostProjectNodes nests a copy of each project under every owning host, holding only that host's sessions", () => {
    const evener = project({
      key: "evener",
      sources: ["local", "devbox"],
      sessions: [on("local", "l1"), on("devbox", "d1"), on("devbox", "d2", { state: "awaiting" })],
    });
    const nodes = hostProjectNodes([evener], sources, closed);
    const local = nodes.find((node) => node.id === "host:local");
    const devbox = nodes.find((node) => node.id === "host:devbox");
    expect(local?.kind).toBe("host");
    expect(local?.children.map((child) => child.id)).toEqual(["projectnode:evener@local"]);
    const localCopy = local?.children[0];
    expect(localCopy).toMatchObject({ kind: "project", project: { key: "evener" } });
    expect(childIds(localCopy)).toEqual(["navigation:local:l1"]);
    // The needs-you-first sort survives the per-host filter.
    expect(devbox?.children.map((child) => child.id)).toEqual(["projectnode:evener@devbox"]);
    const devboxCopy = devbox?.children[0];
    expect(childIds(devboxCopy)).toEqual(["navigation:devbox:d2", "navigation:devbox:d1"]);
  });

  test("hostProjectNodes re-ids the project's overflow under the one copy that carries it", () => {
    const evener = project({
      key: "evener",
      sources: ["local", "devbox"],
      sessions: [on("local", "l1"), on("devbox", "d1")],
      more_current: 7,
    });
    const nodes = hostProjectNodes([evener], sources, closed);
    const localCopy = nodes.find((node) => node.id === "host:local")?.children[0];
    const devboxCopy = nodes.find((node) => node.id === "host:devbox")?.children[0];
    const localOverflow = localCopy?.kind === "project" ? localCopy.children.at(-1) : undefined;
    expect(localOverflow).toMatchObject({ id: "projectnode:evener@local:overflow", kind: "overflow", count: 7 });
    // The overflow is the project's, so exactly one copy - the first in rail
    // order - carries it; the other host's copy keeps only its own rows.
    expect(devboxCopy?.kind === "project" ? childIds(devboxCopy) : []).toEqual(["navigation:devbox:d1"]);
  });

  test("hostProjectNodes keeps the loading placeholder on an unloaded project's copies and places projects missing sources by their sessions", () => {
    const unloaded = project({ key: "unloaded", sources: ["local", "devbox"], session_count: 2 });
    const noSources = project({ key: "bare", sessions: [on("devbox", "d1")] });
    const nodes = hostProjectNodes([unloaded, noSources], sources, closed);
    const local = nodes.find((node) => node.id === "host:local");
    const devbox = nodes.find((node) => node.id === "host:devbox");
    const unloadedUnderLocal = local?.children.find((child) => child.id === "projectnode:unloaded@local");
    expect(unloadedUnderLocal?.kind === "project" ? unloadedUnderLocal.children : []).toEqual([
      { id: "projectnode:unloaded@local:loading", kind: "loading" },
    ]);
    const unloadedUnderDevbox = devbox?.children.find((child) => child.id === "projectnode:unloaded@devbox");
    expect(unloadedUnderDevbox?.kind === "project" ? unloadedUnderDevbox.children : []).toEqual([
      { id: "projectnode:unloaded@devbox:loading", kind: "loading" },
    ]);
    // No sources field: placement falls back to the hosts its own rows name.
    expect(local?.children.some((child) => child.id === "projectnode:bare@local")).toBe(false);
    expect(devbox?.children.map((child) => child.id)).toContain("projectnode:bare@devbox");
  });

  test("projectNodesWithHostBranches inserts host branches inside a loaded project, overflow stays at the project level", () => {
    const evener = project({
      key: "evener",
      sources: ["local", "devbox"],
      sessions: [on("local", "l1"), on("devbox", "d1")],
      more_current: 7,
    });
    const [node] = projectNodesWithHostBranches([evener], sources, closed);
    expect(node?.children.map((child) => child.id)).toEqual([
      "projectnode:evener@host:local",
      "projectnode:evener@host:devbox",
      "projectnode:evener:overflow",
    ]);
    const localBranch = node?.children[0];
    expect(localBranch).toMatchObject({ kind: "host", host: { id: "local" } });
    expect(childIds(localBranch)).toEqual(["navigation:local:l1"]);
  });

  test("projectNodesWithHostBranches leaves an unloaded project's loading placeholder alone", () => {
    const [node] = projectNodesWithHostBranches([project({ key: "p1", session_count: 2 })], sources, closed);
    expect(node?.children).toEqual([{ id: "projectnode:p1:loading", kind: "loading" }]);
  });

  test("project-first keeps single-host rows flat, and host-first renders the project overflow on one copy only", () => {
    const evener = project({
      key: "evener",
      sources: ["local", "devbox"],
      sessions: [on("local", "l1")],
      more_current: 7,
    });
    // Project-first: only one host has loaded rows, so the project keeps
    // today's flat children - a lone "this host" branch would bury every
    // session one expansion deeper for no grouping gained.
    const [branchNode] = projectNodesWithHostBranches([evener], sources, closed);
    expect(childIds(branchNode)).toEqual(["navigation:local:l1", "projectnode:evener:overflow"]);
    // Host-first: the overflow is the PROJECT's, so it renders once - on the
    // first copy in rail order - instead of claiming "+7" under every host.
    // The empty devbox copy stays: revealed rows land under their hosts, and
    // a deep link still expands it.
    const hosts = hostProjectNodes([evener], sources, closed);
    const localCopy = hosts.find((node) => node.id === "host:local")?.children[0];
    const devboxCopy = hosts.find((node) => node.id === "host:devbox")?.children[0];
    expect(childIds(localCopy)).toEqual(["navigation:local:l1", "projectnode:evener@local:overflow"]);
    expect(childIds(devboxCopy)).toEqual([]);
  });

  test("liveNodesGroupedByHost groups rows under hosts only while more than one host is in play", () => {
    const rows = sessionNodes([on("local", "l1"), on("devbox", "d1")]);
    const grouped = liveNodesGroupedByHost(rows, sources, overrideLookup(new Map()));
    expect(grouped.map((node) => node.id)).toEqual(["livehost:local", "livehost:devbox"]);
    const local = grouped[0];
    expect(local).toMatchObject({ kind: "host", host: { id: "local", online: true }, expanded: true });
    expect(childIds(local)).toEqual(["navigation:local:l1"]);
  });

  // A deep-link reveal has to walk the same ids the grouped builders mint,
  // or the row it targets stays hidden behind a group that never opens.
  test("revealExpansionIds walks the host-first chain: the host group, then the project's copy", () => {
    const evener = project({
      key: "evener",
      sources: ["local", "devbox"],
      sessions: [on("devbox", "d1"), on("local", "l1")],
    });
    expect(revealExpansionIds([evener], [], "devbox:d1", "host-project")).toEqual([
      "host:devbox",
      "projectnode:evener@devbox",
    ]);
    expect(revealExpansionIds([evener], [], "devbox:d1", "flat")).toEqual(["projectnode:evener"]);
  });

  test("revealExpansionIds walks the project-first chain: the project, then its per-host branch", () => {
    const evener = project({
      key: "evener",
      sources: ["local", "devbox"],
      sessions: [on("local", "l1"), on("devbox", "d1")],
    });
    expect(revealExpansionIds([evener], [], "devbox:d1", "project-host")).toEqual([
      "projectnode:evener",
      "projectnode:evener@host:devbox",
    ]);
    // Single-host rows render flat children, so the chain stops at the
    // project fold - a branch id would name a fold that does not exist.
    const solo = project({ key: "solo", sources: ["local", "devbox"], sessions: [on("devbox", "d1")] });
    expect(revealExpansionIds([solo], [], "devbox:d1", "project-host")).toEqual(["projectnode:solo"]);
  });

  test("revealExpansionIds does not walk nested summaries: a subagent ref names no rail row", () => {
    // The rail's lists carry top-level rows only, so a ref that exists only as
    // a nested summary (a subagent, a fork original) finds nothing here. Its
    // reveal resolves through the location lookup instead, which names the
    // top-level carrier (top_level_ref) whose row renders.
    const nested = project({
      key: "evener",
      sources: ["local"],
      sessions: [session({ ref: "root", row_id: "root", host_id: "local", children: [on("devbox", "child")] })],
    });
    expect(revealExpansionIds([nested], [], "devbox:child", "host-project")).toEqual([]);
    expect(revealExpansionIds([nested], [], "devbox:child", "flat")).toEqual([]);
  });

  test("revealExpansionIds opens the project fold for a top-level row, adding the host branch only when rows span hosts", () => {
    const carrier = on("local", "root");
    const solo = project({ key: "evener", sources: ["local"], sessions: [carrier] });
    expect(revealExpansionIds([solo], [], "local:root", "flat")).toEqual(["projectnode:evener"]);
    expect(revealExpansionIds([solo], [], "local:root", "project-host")).toEqual(["projectnode:evener"]);
    // A project whose rows span hosts adds its per-host branch first.
    const spread = project({
      key: "spread",
      sources: ["local", "devbox"],
      sessions: [on("local", "l1"), on("devbox", "d2"), carrier],
    });
    expect(revealExpansionIds([spread], [], "local:root", "project-host")).toEqual([
      "projectnode:spread",
      "projectnode:spread@host:local",
    ]);
  });

  test("revealExpansionIds routes an archived-tier row to the archived group fold", () => {
    const archived = project({ key: "old", sessions: [on("local", "achild", { tier: "archived" })] });
    expect(revealExpansionIds([archived], [], "local:achild", "host-project")).toEqual(["archivedgroup:old"]);
    expect(revealExpansionIds([archived], [], "local:achild", "flat", { rowsUnderProjectNode: true })).toEqual([
      "projectnode:old",
    ]);
  });

  test("revealExpansionIds opens a Live host subheader only while Live renders grouped", () => {
    const twoHosts = [on("local", "l1"), on("devbox", "d1")];
    expect(revealExpansionIds([], twoHosts, "devbox:d1", "project-host")).toEqual(["livehost:devbox"]);
    expect(revealExpansionIds([], [on("local", "l1")], "local:l1", "project-host")).toEqual([]);
    // Flat mode renders Live ungrouped, so no subheader exists to expand.
    expect(revealExpansionIds([], twoHosts, "devbox:d1", "flat")).toEqual([]);
  });

  test("revealExpansionIds is empty for a ref nothing loaded holds (the location path owns it)", () => {
    expect(revealExpansionIds([], [], "missing", "host-project")).toEqual([]);
    // A single host (or none) keeps today's flat list, byte for byte.
    const single = sessionNodes([on("local", "l1"), on("local", "l2")]);
    expect(liveNodesGroupedByHost(single, sources, closed)).toBe(single);
    expect(liveNodesGroupedByHost([], sources, closed)).toEqual([]);
  });

  test("host-first copies carry the host they nest under; the first speaks for the project", () => {
    const evener = project({
      key: "evener",
      sources: ["local", "devbox"],
      sessions: [on("local", "l1"), on("devbox", "d1")],
    });
    const hosts = hostProjectNodes([evener], sources, closed);
    const localCopy = hosts.find((node) => node.id === "host:local")?.children[0];
    const devboxCopy = hosts.find((node) => node.id === "host:devbox")?.children[0];
    // The first copy in rail order is the project's canonical copy: the
    // one that renders the project's aggregate facts (overflow, rollup)
    // instead of duplicating them under every host.
    expect(localCopy).toMatchObject({ kind: "project", spawnHost: "local", canonicalCopy: true });
    expect(devboxCopy).toMatchObject({ kind: "project", spawnHost: "devbox" });
    expect(devboxCopy).not.toHaveProperty("canonicalCopy");
    const [flat] = projectNodes([evener], closed);
    expect((flat as { spawnHost?: string } | undefined)?.spawnHost).toBeUndefined();
  });

  test("the canonical copy is the first host in rail order that has loaded rows", () => {
    // The hub sorts first in rail order but owns no loaded rows here: the
    // project's overflow must not render inside an empty host group, where
    // collapsing the group hides the project's only "+N older".
    const evener = project({
      key: "evener",
      sources: ["local", "devbox"],
      sessions: [on("devbox", "d1"), on("devbox", "d2")],
    });
    const hosts = hostProjectNodes([evener], sources, closed);
    const emptyCopy = hosts.find((node) => node.id === "host:local")?.children[0];
    const rowsCopy = hosts.find((node) => node.id === "host:devbox")?.children[0];
    expect(emptyCopy).not.toHaveProperty("canonicalCopy");
    expect(rowsCopy).toMatchObject({ kind: "project", spawnHost: "devbox", canonicalCopy: true });
    // A project with no loaded rows anywhere (a stub) keeps the first host
    // in rail order, so the anchor is stable until rows land somewhere.
    const stub = project({ key: "stub", sources: ["local", "devbox"], sessions: [] });
    const stubHosts = hostProjectNodes([stub], sources, closed);
    expect(stubHosts.find((node) => node.id === "host:local")?.children[0]).toMatchObject({
      kind: "project",
      canonicalCopy: true,
    });
  });

  test("project-first rows name the host a launch targets, so a remote directory cannot fall back to this hub", () => {
    const hubbed = project({
      key: "hubbed",
      sources: ["local", "devbox"],
      sessions: [on("local", "l1"), on("devbox", "d1")],
    });
    const remote = project({ key: "remote", sources: ["devbox"], sessions: [on("devbox", "d1")] });
    const rows = projectNodesWithHostBranches([hubbed, remote], sources, closed);
    // The hub owns the merged record, so its row launches here explicitly -
    // a remembered remote host cannot survive the click. The row stays the
    // project's ONE aggregate row, rollup and overflow included.
    expect(rows.find((node) => node.id === "projectnode:hubbed")).toMatchObject({
      spawnHost: "local",
      canonicalCopy: true,
    });
    // A remote-owned project names its host: the spawn draft prefills it
    // instead of silently launching a remote working_dir on this hub.
    expect(rows.find((node) => node.id === "projectnode:remote")).toMatchObject({ spawnHost: "devbox" });
    // The no-sources call stays hostless - the builder's contract for
    // callers with no manifest to read; the rail's flat tier passes its
    // display sources, so its rows do name a host (Rail.test.tsx pins
    // the launch URL).
    const [flat] = projectNodes([remote], closed);
    expect((flat as { spawnHost?: string } | undefined)?.spawnHost).toBeUndefined();
  });

  test("archived and test-run rows name their launch host, whatever the grouping", () => {
    const remote = project({
      key: "remote",
      sources: ["devbox"],
      sessions: [on("devbox", "a1", { tier: "archived" })],
    });
    // The Archived sessions section's group row and the whole-archived
    // project row both name the host: a remote-owned directory must not
    // fall back to this hub from an archived row either.
    expect(archivedSessionGroups([remote], closed, sources)[0]).toMatchObject({
      spawnHost: "devbox",
      canonicalCopy: true,
    });
    const whole = project({ key: "old", sources: ["devbox"], session_count: 3 });
    expect(archivedProjectNodes([whole], new Map(), closed, sources)[0]).toMatchObject({
      spawnHost: "devbox",
      canonicalCopy: true,
    });
    // Test-run project rows render flat but still name the host.
    const run = project({ key: "run", sources: ["devbox"], sessions: [on("devbox", "t1")] });
    expect(projectNodes([run], closed, sources)[0]).toMatchObject({
      spawnHost: "devbox",
      canonicalCopy: true,
    });
    // The no-sources call keeps the hostless contract even though no rail
    // tier uses it anymore: every tier passes its display sources.
    expect(projectNodes([run], closed)[0]).not.toHaveProperty("spawnHost");
  });

  test("an archived row re-renders its launch host when the project's ownership changes in place", () => {
    const owned = project({
      key: "remote",
      sources: ["devbox"],
      sessions: [on("devbox", "a1", { tier: "archived" })],
    });
    const before = archivedSessionGroups([owned], closed, sources)[0];
    expect(before).toMatchObject({ spawnHost: "devbox" });
    // Ownership flips while the global sources and the row's children stay
    // identical: the cache must not serve the old node's host - the same
    // compare cachedProjectNode does for the other tiers.
    owned.sources = ["local", "devbox"];
    const after = archivedSessionGroups([owned], closed, sources)[0];
    expect(after).toMatchObject({ spawnHost: "local" });
    expect(after).not.toBe(before);

    const stub = project({ key: "old", sources: ["devbox"], session_count: 3 });
    const stubBefore = archivedProjectNodes([stub], new Map(), closed, sources)[0];
    stub.sources = ["local", "devbox"];
    const stubAfter = archivedProjectNodes([stub], new Map(), closed, sources)[0];
    expect(stubAfter).toMatchObject({ spawnHost: "local" });
    expect(stubAfter).not.toBe(stubBefore);
  });

  test("a reveal routes an archived-tier row to the archived group fold, whatever the grouping", () => {
    const evener = project({
      key: "evener",
      sources: ["devbox"],
      sessions: [on("devbox", "a1", { tier: "archived" })],
    });
    expect(revealExpansionIds([evener], [], "devbox:a1", "flat")).toEqual(["archivedgroup:evener"]);
    expect(revealExpansionIds([evener], [], "devbox:a1", "host-project")).toEqual(["archivedgroup:evener"]);
  });

  test("a whole-archived project's rows reveal under its own node, not the archived group", () => {
    // archivedProjectNodes renders every row (whatever its tier) under the
    // project's own node, so the reveal must not route to the group fold.
    const old = project({ key: "old", sessions: [on("devbox", "a1", { tier: "archived" })] });
    expect(revealExpansionIds([old], [], "devbox:a1", "flat", { rowsUnderProjectNode: true })).toEqual([
      "projectnode:old",
    ]);
  });

  test("hosts order by their display labels, ties broken by id", () => {
    const labeled: Source[] = [
      { id: "local", label: "this host", kind: "local", online: true },
      { id: "zz-host", label: "Alpha box", kind: "appwire", online: true },
      { id: "mm-host", label: "Alpha box", kind: "appwire", online: true },
      { id: "aa-host", label: "Zeta farm", kind: "appwire", online: true },
    ];
    const alpha = project({ key: "alpha", sources: ["zz-host"], sessions: [on("zz-host", "z1")] });
    const twin = project({ key: "twin", sources: ["mm-host"], sessions: [on("mm-host", "m1")] });
    const zeta = project({ key: "zeta", sources: ["aa-host"], sessions: [on("aa-host", "a1")] });
    expect(hostProjectNodes([zeta, alpha, twin], labeled, closed).map((node) => node.id)).toEqual([
      "host:mm-host",
      "host:zz-host",
      "host:aa-host",
    ]);
  });

  test("a project-first host branch rebuilds when the manifest's source facts change", () => {
    const evener = project({
      key: "evener",
      sources: ["local", "devbox"],
      sessions: [on("local", "l1"), on("devbox", "d1")],
    });
    const devboxBranch = (nodes: readonly RailNode[]): HostRailNode | undefined =>
      nodes.find((child): child is HostRailNode => child.kind === "host" && child.host.id === "devbox");
    const local = (label: string): Source => ({ id: "local", label, kind: "local", online: true });
    const devbox = (label: string): Source => ({ id: "devbox", label, kind: "appwire", online: true });
    const before = projectNodesWithHostBranches([evener], [local("this host"), devbox("devbox")], closed);
    expect(devboxBranch(before[0]?.children ?? [])).toMatchObject({ host: { id: "devbox", label: "devbox" } });
    const after = projectNodesWithHostBranches([evener], [local("this host"), devbox("renamed box")], closed);
    expect(devboxBranch(after[0]?.children ?? [])).toMatchObject({ host: { id: "devbox", label: "renamed box" } });
  });

  test("an unchanged manifest refresh reuses the branch children it already built", () => {
    const evener = project({
      key: "evener",
      sources: ["local", "devbox"],
      sessions: [on("local", "l1"), on("devbox", "d1")],
    });
    const local = (): Source => ({ id: "local", label: "this host", kind: "local", online: true });
    const devbox = (): Source => ({ id: "devbox", label: "devbox", kind: "appwire", online: true });
    const first = projectNodesWithHostBranches([evener], [local(), devbox()], closed);
    // A revalidation hands back a NEW array carrying the SAME facts; the
    // cache must reuse the children, not mint a variant per refresh.
    const second = projectNodesWithHostBranches([evener], [local(), devbox()], closed);
    expect(second[0]?.children).toBe(first[0]?.children);
  });

  test("the overflow follows the first host in rail order when that order changes", () => {
    const evener = project({
      key: "evener",
      sources: ["devbox", "render-farm"],
      sessions: [on("devbox", "d1"), on("render-farm", "r1")],
      more_current: 7,
    });
    const online = (): Source[] => [
      { id: "devbox", label: "devbox", kind: "appwire", online: true },
      { id: "render-farm", label: "Alpha box", kind: "appwire", online: true },
    ];
    const offline = (): Source[] => [
      { id: "devbox", label: "devbox", kind: "appwire", online: true },
      { id: "render-farm", label: "Alpha box", kind: "appwire", online: false },
    ];
    // "Alpha box" sorts ahead of "devbox", so it carries the overflow.
    const first = hostProjectNodes([evener], online(), closed);
    const alphaCopy = first.find((node) => node.id === "host:render-farm")?.children[0];
    const devboxCopy = first.find((node) => node.id === "host:devbox")?.children[0];
    expect(childIds(alphaCopy)).toEqual(["navigation:render-farm:r1", "projectnode:evener@render-farm:overflow"]);
    expect(childIds(devboxCopy)).toEqual(["navigation:devbox:d1"]);
    // The farm drops offline and behind devbox; the overflow must move
    // with the new first host instead of duplicating or disappearing.
    const second = hostProjectNodes([evener], offline(), closed);
    const devboxAfter = second.find((node) => node.id === "host:devbox")?.children[0];
    const alphaAfter = second.find((node) => node.id === "host:render-farm")?.children[0];
    expect(childIds(devboxAfter)).toEqual(["navigation:devbox:d1", "projectnode:evener@devbox:overflow"]);
    expect(childIds(alphaAfter)).toEqual(["navigation:render-farm:r1"]);
  });
});

test("compact receiver counts include all watches without loading descendant detail", () => {
  const child = session({ ref: "child", watch_count: 40, armed_watch_count: 40 });
  const root = session({ ref: "parent", watch_count: 5, armed_watch_count: 2, children: [child] });
  expect(activeWatchCount(root)).toBe(2);
  expect(activeWatchCount(child)).toBe(40);
  expect(watchCountLabel(2, 5)).toBe("5 watches · 2 armed");
  expect(watchCountLabel(40, 40)).toBe("40 watches");
});
