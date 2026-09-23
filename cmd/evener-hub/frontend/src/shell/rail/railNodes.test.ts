// @vitest-environment node

import type { NavigationWatchSummary, Source } from "@evener/appwire-client";
import { describe, expect, test } from "vitest";
import type { OverflowRailNode, RailNode, RailPinSection, RailProject, RailSession } from "./railNodes";
import {
  activeWatchCount,
  archivedCount,
  archivedProjectNodes,
  archivedSessionGroups,
  displayState,
  hostProjectNodes,
  liveNodesGroupedByHost,
  needsYouDescendantCount,
  overrideLookup,
  pinSectionDisclosureID,
  projectNodeIdForSessionRef,
  projectNodes,
  projectNodesWithHostBranches,
  revealExpansionIds,
  sessionNodes,
  topLevelAncestorRef,
  watchCountLabel,
  workingDescendantCount,
} from "./railNodes";

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
function watch(overrides: Partial<NavigationWatchSummary> = {}): NavigationWatchSummary {
  return {
    id: "w1",
    source: "self",
    deliveries: 0,
    created_at: "2026-09-12T19:00:00Z",
    active: true,
    ...overrides,
  };
}
const closed = () => false;

test("adapts resource-local session summaries into stable recursive rail rows", () => {
  const child = session({ row_id: "navigation:child", ref: "child", kind: "subagent", state: "active" });
  const rows = sessionNodes([session({ row_id: "navigation:parent", ref: "parent", children: [child] })], closed);
  expect(rows[0]).toMatchObject({ id: "navigation:parent", kind: "session", session: { ref: "parent" } });
  expect(rows[0]?.children[0]).toMatchObject({ id: "navigation:child", kind: "session" });
});

test("session node memo dependencies are complete and bottom-up", () => {
  const unchangedSibling = session({ row_id: "sibling", ref: "sibling", title: "Sibling", state: "active" });
  const grandchild = session({ row_id: "grandchild", ref: "grandchild", title: "Before", state: "active" });
  const child = session({ row_id: "child", ref: "child", state: "active", children: [grandchild] });
  const parent = session({ row_id: "parent", ref: "parent", state: "active", children: [child] });
  const before = sessionNodes([parent, unchangedSibling], closed);
  const repeated = sessionNodes([parent, unchangedSibling], closed);

  expect(repeated[0]).toBe(before[0]);
  expect(repeated[0]?.children).toBe(before[0]?.children);
  expect(repeated[1]).toBe(before[1]);

  const changedGrandchild = { ...grandchild, title: "After" };
  const changedChild = { ...child, children: [changedGrandchild] };
  const changedParent = { ...parent, children: [changedChild] };
  const after = sessionNodes([changedParent, unchangedSibling], closed);

  expect(after[0]).not.toBe(before[0]);
  expect(after[0]?.children).not.toBe(before[0]?.children);
  expect(after[0]?.children[0]).not.toBe(before[0]?.children[0]);
  expect(after[0]?.children[0]).toMatchObject({ children: [{ session: { title: "After" } }] });
  expect(after[1]).toBe(before[1]);
  expect(after[1]?.children).toBe(before[1]?.children);

  const expandedChild = (_id: string, defaultExpanded: boolean) => defaultExpanded;
  const lookupBefore = sessionNodes([changedParent], expandedChild)[0];
  const lookupAfter = sessionNodes([changedParent], (id, defaultExpanded) =>
    id === changedChild.row_id ? true : defaultExpanded,
  )[0];
  expect(lookupAfter).not.toBe(lookupBefore);
  expect(lookupAfter?.children[0]).toMatchObject({ id: changedChild.row_id, expanded: true });
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
  test("folds inactive descendants while retaining current order and counts", () => {
    const root = session({
      ref: "root",
      row_id: "root",
      state: "active",
      children: [
        session({ ref: "done", row_id: "done", state: "ended" }),
        session({ ref: "working", row_id: "working", state: "active" }),
      ],
    });
    const [node] = sessionNodes([root], closed);
    expect(node?.children.map((child) => child.kind)).toEqual(["session", "inactiveFold"]);
    expect(node?.children[1]).toMatchObject({ count: 1, expanded: false });
  });

  test("keeps active jobs inline and puts idle subagents and completed jobs in separate folds", () => {
    const root = session({
      ref: "root",
      row_id: "root",
      state: "idle",
      children: [
        session({ ref: "idle-child", row_id: "idle-child", kind: "subagent", state: "idle" }),
        session({ ref: "active-child", row_id: "active-child", kind: "subagent", state: "active" }),
      ],
    });
    Object.assign(root, {
      running_jobs: [{ job_id: "job-running", job_type: "shell", status: "running" }],
      completed_jobs: [{ job_id: "job-completed", job_type: "shell", status: "completed" }],
    });
    const [node] = sessionNodes([root], closed);
    expect(node?.children.map((child) => child.kind)).toEqual(["session", "job", "inactiveFold", "completedJobsFold"]);
    expect(node?.children[1]).toMatchObject({ kind: "job", job: { job_id: "job-running" } });
    expect(node?.children[2]).toMatchObject({ kind: "inactiveFold", count: 1 });
    expect(node?.children[3]).toMatchObject({ kind: "completedJobsFold", count: 1 });
  });

  test("keeps an idle subagent with active work in the inline list", () => {
    const child = session({ ref: "child", row_id: "child", kind: "subagent", state: "idle" });
    Object.assign(child, { running_jobs: [{ job_id: "job-child", job_type: "shell", status: "running" }] });
    const [node] = sessionNodes([session({ ref: "root", row_id: "root", children: [child] })], closed);
    expect(node?.children.map((entry) => entry.kind)).toEqual(["session"]);
    expect(node?.children[0]).toMatchObject({ children: [{ kind: "job", job: { job_id: "job-child" } }] });
  });

  test("keeps a session's own watch rows inline, after its running jobs", () => {
    const root = session({ ref: "root", row_id: "root", state: "idle" });
    Object.assign(root, {
      running_jobs: [{ job_id: "job-1", job_type: "shell", status: "running" }],
      watches: [watch({ id: "w1" }), watch({ id: "w2" })],
    });
    const [node] = sessionNodes([root], closed);
    expect(node?.children.map((child) => child.kind)).toEqual(["job", "watch", "watch"]);
    // The row id is namespaced off the parent, the way job rows are, so two
    // sessions carrying a same-id watch (each its own receiver) get distinct
    // tree ids.
    expect(node?.children[1]).toMatchObject({ id: "watch:root:w1", kind: "watch", watch: { id: "w1" } });
  });

  test("renders no watch rows for a session whose wire list is absent or empty", () => {
    const [node] = sessionNodes([session({ ref: "root", row_id: "root" })], closed);
    expect(node?.children).toEqual([]);
  });

  test("caps the inline watch rows and notes the remainder in the overflow grammar", () => {
    const root = session({
      ref: "root",
      row_id: "root",
      watches: ["w1", "w2", "w3", "w4", "w5"].map((id) => watch({ id })),
    });
    const [node] = sessionNodes([root], closed);
    expect(node?.children.map((child) => child.kind)).toEqual(["watch", "watch", "watch", "overflow"]);
    expect(node?.children.at(-1)).toMatchObject({ kind: "overflow", count: 2, suffix: "more watches", pages: [] });
  });

  test("marks the watch overflow row passive: it can reveal nothing by activating", () => {
    // The inline cap is local to the rail and the wire already carried every
    // watch, so there is no page behind "+N more watches". The row must be an
    // honest count, not a control that looks actionable and no-ops.
    const root = session({
      ref: "root",
      row_id: "root",
      watches: ["w1", "w2", "w3", "w4", "w5"].map((id) => watch({ id })),
    });
    const [node] = sessionNodes([root], closed);
    expect(node?.children.at(-1)).toMatchObject({ kind: "overflow", suffix: "more watches", pages: [], passive: true });
  });

  test("counts watches the projector omitted in the fold-out overflow, even under the inline cap", () => {
    // The hub caps its per-session watch list and reports the rows it dropped
    // as omitted_watches; the summary line already says "+4 more". A fold-out
    // that showed only the one retained row would silently contradict it, so
    // the omitted rows are part of the fold-out's hidden count.
    const root = session({
      ref: "root",
      row_id: "root",
      watches: [watch({ id: "w1" })],
      omitted_watches: 4,
    });
    const [node] = sessionNodes([root], closed);
    expect(node?.children.map((child) => child.kind)).toEqual(["watch", "overflow"]);
    expect(node?.children.at(-1)).toMatchObject({
      kind: "overflow",
      count: 4,
      suffix: "more watches",
      pages: [],
      passive: true,
    });
  });

  test("the summary line's watch count and the fold-out's hidden count agree", () => {
    // One total per session: retained rows plus the projector's omitted count.
    // The fold-out shows the inline head of the retained rows and counts
    // everything else - retained beyond the cap plus the omitted rows - so the
    // two surfaces cannot disagree about how many watches the session holds.
    const cases = [
      { retained: 1, omitted: 4, label: "1 watch · 1 armed total · +4 more" },
      { retained: 3, omitted: 0, label: "3 watches" },
      { retained: 5, omitted: 3, label: "5 watches · 5 armed total · +3 more" },
      { retained: 2, omitted: 1, label: "2 watches · 2 armed total · +1 more" },
    ];
    for (const c of cases) {
      const watches = Array.from({ length: c.retained }, (_, i) => watch({ id: `w${i}` }));
      const root = session({ ref: "root", row_id: "root", watches, omitted_watches: c.omitted });
      const [node] = sessionNodes([root], closed);
      const children = node?.children ?? [];
      const shown = children.filter((child) => child.kind === "watch").length;
      const overflow = children.find((child) => child.kind === "overflow") as OverflowRailNode | undefined;
      const hidden = overflow?.count ?? 0;
      // Every retained row is armed in this matrix, so the summary line counts
      // all of them; the fold-out plus its overflow must add up to the same
      // number the line's total implies.
      expect(shown + hidden).toBe(c.retained + c.omitted);
      expect(shown).toBe(Math.min(c.retained, 3));
      const label = watchCountLabel(activeWatchCount(root), c.retained, c.omitted);
      // An all-armed list keeps the bare count when nothing was omitted; once
      // rows were omitted the figure is labelled a total covering them.
      expect(label).toBe(c.label);
    }
  });

  test("the summary line's total includes retained-but-inactive watches", () => {
    // A fired one-shot whose teardown is still pending projects inactive, so a
    // session can hold retained rows that are not armed. The summary line used
    // to count only the armed rows (`2 watches`) while the fold-out listed all
    // five, so the two surfaces disagreed. One total now: the retained count,
    // with the armed count beside it.
    const inactive = (id: string) => watch({ id, active: false });
    const cases = [
      // 2 armed + 3 inactive, nothing omitted: 5 total, 2 armed.
      {
        watches: [watch({ id: "w1" }), watch({ id: "w2" }), inactive("w3"), inactive("w4"), inactive("w5")],
        omitted: 0,
        label: "5 watches · 2 armed",
      },
      // Same, plus 3 rows the projector omitted.
      {
        watches: [watch({ id: "w1" }), watch({ id: "w2" }), inactive("w3"), inactive("w4"), inactive("w5")],
        omitted: 3,
        label: "5 watches · 2 armed total · +3 more",
      },
      // All-inactive: 3 retained, 0 armed - still one total of 3.
      {
        watches: [inactive("w1"), inactive("w2"), inactive("w3")],
        omitted: 0,
        label: "3 watches · 0 armed",
      },
    ];
    for (const c of cases) {
      const root = session({ ref: "root", row_id: "root", watches: c.watches, omitted_watches: c.omitted });
      const [node] = sessionNodes([root], closed);
      const children = node?.children ?? [];
      const shown = children.filter((child) => child.kind === "watch").length;
      const overflow = children.find((child) => child.kind === "overflow") as OverflowRailNode | undefined;
      const hidden = overflow?.count ?? 0;
      const label = watchCountLabel(activeWatchCount(root), root.watches?.length ?? 0, c.omitted);
      expect(label).toBe(c.label);
      // The summary's total - the retained base plus the omitted "+N more" -
      // is exactly the fold-out's shown-plus-hidden total.
      expect((root.watches?.length ?? 0) + c.omitted).toBe(shown + hidden);
    }
  });

  test("counts a session's own armed watches once, never a descendant's", () => {
    const child = session({
      ref: "child",
      row_id: "child",
      watches: [watch({ id: "c1" }), watch({ id: "c2" })],
    });
    const parent = session({
      ref: "parent",
      row_id: "parent",
      children: [child],
      watches: [watch({ id: "p1" }), watch({ id: "p2", active: false })],
    });
    // The parent counts only what its own summary carries - a receiver watch
    // belongs to the session whose summary carries it - and only while armed.
    expect(activeWatchCount(parent)).toBe(1);
    expect(activeWatchCount(child)).toBe(2);
    expect(activeWatchCount(session({ ref: "none", row_id: "none" }))).toBe(0);
  });

  test("adds omitted armed rows to the armed total and labels what the number covers", () => {
    // A session with more armed watches than the hub's per-session cap: 32
    // retained, 8 more omitted and all of them armed. The retained rows alone
    // would report 32, understating the session's armed total of 40.
    const retained = Array.from({ length: 32 }, (_, i) => watch({ id: `w${i}` }));
    const root = session({
      ref: "root",
      row_id: "root",
      watches: retained,
      omitted_watches: 8,
      omitted_armed_watches: 8,
    });
    expect(activeWatchCount(root)).toBe(40);
    expect(watchCountLabel(activeWatchCount(root), retained.length, 8)).toBe("32 watches · 40 armed total · +8 more");
    // Even when none of the omitted rows were armed, the figure is still
    // labelled a total: the panel cannot show which of the omitted rows were
    // armed, so the number must say what it covers.
    const mixed = session({
      ref: "mixed",
      row_id: "mixed",
      watches: [watch({ id: "a" }), watch({ id: "b", active: false })],
      omitted_watches: 3,
      omitted_armed_watches: 0,
    });
    expect(watchCountLabel(activeWatchCount(mixed), 2, 3)).toBe("2 watches · 1 armed total · +3 more");

    // The byte fitter can shed every retained row: the label then drops the base
    // count rather than leading with "0 watches" beside a nonzero armed total.
    expect(watchCountLabel(40, 0, 8)).toBe("40 armed total · +8 more");
    expect(watchCountLabel(0, 0, 8)).toBe("0 armed total · +8 more");
  });

  test("handles cluster disclosure without a second inactive fold", () => {
    const cluster = session({
      kind: "cluster",
      row_id: "cluster",
      ref: "cluster",
      children: [session({ row_id: "member", ref: "member", state: "ended" })],
    });
    expect(sessionNodes([cluster], closed)[0]?.children.map((child) => child.id)).toEqual(["member"]);
  });
  test("derives attention and working counts from recursive summaries", () => {
    const root = session({
      state: "active",
      children: [
        session({ ref: "ask", state: "awaiting" }),
        session({ ref: "worker", state: "active", children: [session({ ref: "worker2", state: "active" })] }),
      ],
    });
    expect(needsYouDescendantCount(root)).toBe(1);
    expect(workingDescendantCount(root)).toBe(2);
    expect(displayState(session({ kind: "subagent", state: "awaiting" }))).toBe("idle");
    expect(displayState(session({ kind: "subagent", state: "awaiting", ask_pending: true }))).toBe("awaiting");
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
    expect(archivedSessionGroups([a], closed)[0]?.children[0]).toMatchObject({ id: "navigation:old-a" });
  });
  test("counts archived rows plus archived remainder in active and test projects", () => {
    const active = project({ sessions: [session({ tier: "archived" })], more_archived: 4 });
    expect(archivedCount([], [active])).toBe(5);
  });
  test("keeps archived stubs visible with a loading child and hydrated roots projectable", () => {
    const stub = project({ key: "archived", is_archived: true, session_count: 3 });
    expect(archivedProjectNodes([stub], new Map(), closed)[0]?.children[0]).toMatchObject({ kind: "loading" });
    const hydrated = project({
      ...stub,
      sessions: [session({ ref: "old", row_id: "navigation:old" })],
      more_archived: 2,
    });
    expect(
      archivedProjectNodes([stub], new Map([["archived", hydrated]]), closed)[0]?.children.map((row) => row.id),
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

    const groupBefore = archivedSessionGroups([archivedInActive], closed)[0];
    const groupRepeated = archivedSessionGroups([archivedInActive], closed)[0];
    expect(groupRepeated).toBe(groupBefore);
    expect(groupRepeated?.children).toBe(groupBefore?.children);

    const archivedBefore = archivedProjectNodes([archivedStub, archivedSibling], details, closed);
    const archivedRepeated = archivedProjectNodes([archivedStub, archivedSibling], details, closed);
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
    );
    expect(archivedAfter[0]).not.toBe(archivedBefore[0]);
    expect(archivedAfter[0]?.children[0]).toMatchObject({ session: { title: "After" } });
    expect(archivedAfter[1]).toBe(archivedBefore[1]);
    expect(archivedAfter[1]?.children).toBe(archivedBefore[1]?.children);
  });
  test("uses persisted overrides and location membership for deep-link reveal", () => {
    const p = project({
      key: "p1",
      sessions: [session({ ref: "top", row_id: "top", children: [session({ ref: "child", row_id: "child" })] })],
    });
    expect(overrideLookup(new Map([["x", true]]))("x", false)).toBe(true);
    expect(projectNodeIdForSessionRef([p], "child")).toBe("projectnode:p1");
    expect(topLevelAncestorRef([p], "child")).toBe("top");
    expect(topLevelAncestorRef([p], "missing")).toBeNull();
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

  test("hostProjectNodes re-ids each copy's overflow so two copies of one project never share a node id", () => {
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
    const devboxOverflow = devboxCopy?.kind === "project" ? devboxCopy.children.at(-1) : undefined;
    expect(localOverflow).toMatchObject({ id: "projectnode:evener@local:overflow", kind: "overflow", count: 7 });
    expect(devboxOverflow).toMatchObject({ id: "projectnode:evener@devbox:overflow", kind: "overflow", count: 7 });
    // Both copies page the same project resource, so their page descriptors match.
    expect(devboxOverflow && "pages" in devboxOverflow ? devboxOverflow.pages : []).toEqual(
      localOverflow && "pages" in localOverflow ? localOverflow.pages : [],
    );
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

  test("projectNodesWithHostBranches hides a host branch with no loaded rows, while host-first keeps the copy for its overflow", () => {
    const evener = project({
      key: "evener",
      sources: ["local", "devbox"],
      sessions: [on("local", "l1")],
      more_current: 7,
    });
    // Project-first: the devbox branch would hold nothing, so it does not
    // render; the project-level overflow still does.
    const [branchNode] = projectNodesWithHostBranches([evener], sources, closed);
    expect(branchNode?.children.map((child) => child.id)).toEqual([
      "projectnode:evener@host:local",
      "projectnode:evener:overflow",
    ]);
    // Host-first: the devbox copy stays (its rows may be beyond the loaded
    // window) and carries the overflow that can reveal them.
    const hosts = hostProjectNodes([evener], sources, closed);
    const devboxCopy = hosts.find((node) => node.id === "host:devbox")?.children[0];
    expect(childIds(devboxCopy)).toEqual(["projectnode:evener@devbox:overflow"]);
  });

  test("liveNodesGroupedByHost groups rows under hosts only while more than one host is in play", () => {
    const rows = sessionNodes([on("local", "l1"), on("devbox", "d1")], closed);
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
    const evener = project({ key: "evener", sources: ["local", "devbox"], sessions: [on("devbox", "d1")] });
    expect(revealExpansionIds([evener], [], "devbox:d1", "project-host")).toEqual([
      "projectnode:evener",
      "projectnode:evener@host:devbox",
    ]);
  });

  test("revealExpansionIds routes a nested row through its top-level carrier's host", () => {
    const nested = project({
      key: "evener",
      sources: ["local"],
      sessions: [session({ ref: "root", row_id: "root", host_id: "local", children: [on("devbox", "child")] })],
    });
    expect(revealExpansionIds([nested], [], "devbox:child", "host-project")).toEqual([
      "host:local",
      "projectnode:evener@local",
    ]);
  });

  test("revealExpansionIds opens a Live host subheader exactly while live rows span hosts", () => {
    const twoHosts = [on("local", "l1"), on("devbox", "d1")];
    expect(revealExpansionIds([], twoHosts, "devbox:d1", "project-host")).toEqual(["livehost:devbox"]);
    expect(revealExpansionIds([], [on("local", "l1")], "local:l1", "project-host")).toEqual([]);
  });

  test("revealExpansionIds is empty for a ref nothing loaded holds (the location path owns it)", () => {
    expect(revealExpansionIds([], [], "missing", "host-project")).toEqual([]);
    // A single host (or none) keeps today's flat list, byte for byte.
    const single = sessionNodes([on("local", "l1"), on("local", "l2")], closed);
    expect(liveNodesGroupedByHost(single, sources, closed)).toBe(single);
    expect(liveNodesGroupedByHost([], sources, closed)).toEqual([]);
  });

  test("host-first copies carry the host they nest under; flat project rows carry none", () => {
    const evener = project({
      key: "evener",
      sources: ["local", "devbox"],
      sessions: [on("local", "l1"), on("devbox", "d1")],
    });
    const hosts = hostProjectNodes([evener], sources, closed);
    const localCopy = hosts.find((node) => node.id === "host:local")?.children[0];
    const devboxCopy = hosts.find((node) => node.id === "host:devbox")?.children[0];
    expect(localCopy).toMatchObject({ kind: "project", spawnHost: "local" });
    expect(devboxCopy).toMatchObject({ kind: "project", spawnHost: "devbox" });
    const [flat] = projectNodes([evener], closed);
    expect((flat as { spawnHost?: string } | undefined)?.spawnHost).toBeUndefined();
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
});
