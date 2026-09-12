// @vitest-environment node
import { describe, expect, test } from "vitest";
import type { RailPinSection, RailProject, RailSession } from "./railNodes";
import {
  archivedCount,
  archivedProjectNodes,
  archivedSessionGroups,
  displayState,
  needsYouDescendantCount,
  overrideLookup,
  pinSectionDisclosureID,
  projectNodeIdForSessionRef,
  projectNodes,
  sessionNodes,
  topLevelAncestorRef,
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
