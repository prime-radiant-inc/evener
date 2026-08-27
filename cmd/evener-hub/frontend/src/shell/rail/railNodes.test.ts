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
