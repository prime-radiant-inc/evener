// @vitest-environment node
import { describe, expect, test } from "vitest";
import type { RailProject, RailSession } from "./railNodes";
import type { RailResources } from "./railPending";
import { applyPending, buildPinSourceIndex, type PendingOp } from "./railPending";

function session(overrides: Partial<RailSession> = {}): RailSession {
  return {
    row_id: "navigation:a",
    ref: "a",
    host_id: "local",
    session_id: "a",
    title: "A",
    project: "P",
    state: "idle",
    kind: "session",
    live: true,
    children: [],
    ...overrides,
  };
}
function project(overrides: Partial<RailProject> = {}): RailProject {
  return { key: "p", name: "P", sessions: [], ...overrides };
}
function resources(overrides: Partial<RailResources> = {}): RailResources {
  return { live: [], needsYou: [], pinSections: [], projects: [], archivedProjects: [], testRuns: [], ...overrides };
}
const section = (id: string, rows: RailSession[] = []) => ({ id, name: id, member_count: rows.length, sessions: rows });

describe("resource-local optimistic rail projection", () => {
  test("does not mutate source resources and preserves identity when empty", () => {
    const source = resources({
      projects: [project({ sessions: [session({ children: [session({ ref: "child", row_id: "child" })] })] })],
    });
    const snapshot = JSON.stringify(source);
    expect(applyPending(source, [])).toBe(source);
    applyPending(source, [{ kind: "sessionTitle", ref: "a", title: "renamed" }]);
    expect(JSON.stringify(source)).toBe(snapshot);
  });
  test("hides duplicate session representations across global, pin, and project slices", () => {
    const row = session();
    const source = resources({
      live: [row],
      pinSections: [section("pin", [row])],
      projects: [project({ sessions: [row] })],
    });
    const got = applyPending(source, [{ kind: "hideSession", ref: "a" }]);
    expect(got.live).toEqual([]);
    expect(got.pinSections[0]?.sessions).toEqual([]);
    expect(got.projects[0]?.sessions).toEqual([]);
  });
  test("projects a selected authoritative source into a named pin exactly once", () => {
    const row = session({ title: "authoritative" });
    const source = resources({
      projects: [project({ sessions: [row] })],
      pinSections: [section("old", [row]), section("target", [session({ ref: "keep" })])],
    });
    const got = applyPending(source, [{ kind: "sessionPin", ref: "a", source: row, section: section("target") }]);
    expect(got.pinSections.find((s) => s.id === "old")).toBeUndefined();
    expect(got.pinSections.find((s) => s.id === "target")?.sessions.map((s) => s.ref)).toEqual(["keep", "a"]);
    expect(got.pinSections.find((s) => s.id === "target")?.sessions[1]?.title).toBe("authoritative");
  });
  test("rejects stale or disappeared project rows as pin sources", () => {
    const cached = session({ ref: "missing" });
    const source = resources();
    const index = buildPinSourceIndex(source, {
      generationID: "new",
      projectDetails: [{ projectKey: "gone", generationID: "old", rows: [cached] }],
    });
    const op: PendingOp = { kind: "sessionPin", ref: "missing", source: cached, section: section("target") };
    expect(applyPending(source, [op], { pinSources: index }).pinSections).toEqual([]);
  });
  test("unpins and clears annotations without removing unrelated sections", () => {
    const row = session({ pin_section_id: "old" });
    const source = resources({
      live: [row],
      pinSections: [section("old", [row]), section("keep", [session({ ref: "b" })])],
    });
    const got = applyPending(source, [{ kind: "sessionUnpin", ref: "a" }]);
    expect(got.live[0]?.pin_section_id).toBeUndefined();
    expect(got.pinSections.map((s) => s.id)).toEqual(["keep"]);
  });
  test("renames/deletes pin sections and projects without cross-resource mutation", () => {
    const row = session({ pin_section_id: "old" });
    const source = resources({
      pinSections: [section("old", [row]), section("keep")],
      projects: [project({ favorite: false })],
    });
    const renamed = applyPending(source, [
      { kind: "pinSectionRename", id: "old", name: "Renamed" },
      { kind: "projectFavorite", key: "p", value: true },
    ]);
    expect(renamed.pinSections[0]?.name).toBe("Renamed");
    expect(renamed.projects[0]?.favorite).toBe(true);
    const deleted = applyPending(renamed, [
      { kind: "pinSectionDelete", id: "old" },
      { kind: "hideProject", key: "p" },
    ]);
    expect(deleted.pinSections.map((s) => s.id)).toEqual(["keep"]);
    expect(deleted.projects).toEqual([]);
  });
});
