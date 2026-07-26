import { describe, expect, test } from "vitest";
import type { TreeNode as ApiTreeNode, TreeProject as ApiTreeProject } from "../../stores/tree";
import {
  archivedCount,
  archivedProjectNodes,
  archivedSessionGroups,
  needsYouDescendantCount,
  overrideLookup,
  projectNodeIdForSessionRef,
  projectNodes,
  sessionNodes,
  topLevelAncestorRef,
} from "./railNodes";

function node(overrides: Partial<ApiTreeNode> = {}): ApiTreeNode {
  return {
    row_id: "project:p1:local:a",
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

function project(overrides: Partial<ApiTreeProject> = {}): ApiTreeProject {
  return {
    key: "p1",
    name: "Proj",
    sessions: [],
    ...overrides,
  };
}

// Always-false lookup: every node renders collapsed unless a test overrides
// the callback directly - keeps each test's intent about expand state
// explicit rather than riding a shared default.
const NEVER_EXPANDED = () => false;

describe("sessionNodes", () => {
  test("maps each session's row_id to the rail node id and carries the source node", () => {
    const [rail] = sessionNodes([node({ row_id: "r1", title: "A" })], NEVER_EXPANDED);
    expect(rail).toMatchObject({ id: "r1", kind: "session" });
    expect(rail && "session" in rail && rail.session.title).toBe("A");
  });

  test("a session with no children renders as a leaf (empty children array)", () => {
    const [rail] = sessionNodes([node({ children: [] })], NEVER_EXPANDED);
    expect(rail?.children).toEqual([]);
  });

  test("a session's nested children (subagent clusters) recurse into session rail nodes", () => {
    const [rail] = sessionNodes(
      [node({ row_id: "parent", children: [node({ row_id: "child", title: "Subagent" })] })],
      NEVER_EXPANDED,
    );
    expect(rail?.children).toHaveLength(1);
    expect(rail?.children?.[0]).toMatchObject({ id: "child", kind: "session" });
  });

  test("expanded state for a branch session comes from the isExpanded callback, keyed by row_id, defaulting to false", () => {
    const isExpanded = overrideLookup(new Map([["parent", true]]));
    const [rail] = sessionNodes([node({ row_id: "parent", children: [node({ row_id: "child" })] })], isExpanded);
    expect(rail?.expanded).toBe(true);
  });

  test("multiple top-level sessions preserve input order", () => {
    const rails = sessionNodes([node({ row_id: "r1" }), node({ row_id: "r2" })], NEVER_EXPANDED);
    expect(rails.map((r) => r.id)).toEqual(["r1", "r2"]);
  });
});

// parity-m3-sidebar-tree.md §3: a subagent that has finished is not a thing
// you are supervising, and a parent that spawned twenty of them buried its own
// live children under the wreckage. Current children stay inline; terminal ones
// fold behind the parent's own disclosure, collapsed until asked for.
describe("inactive-subagent fold", () => {
  const CURRENT_STATES = ["active", "awaiting", "idle", "warning", "notLoaded"];
  const INACTIVE_STATES = ["ended", "closed", "errored"];

  function parentWith(...childStates: string[]) {
    return node({
      row_id: "parent",
      children: childStates.map((state, i) => node({ row_id: `child${i}`, ref: `ref${i}`, state })),
    });
  }

  for (const state of CURRENT_STATES) {
    test(`a "${state}" subagent renders inline as a session row`, () => {
      const [rail] = sessionNodes([parentWith(state)], NEVER_EXPANDED);
      expect(rail?.children.map((c) => c.kind)).toEqual(["session"]);
    });
  }

  for (const state of INACTIVE_STATES) {
    test(`a "${state}" subagent renders only inside the fold, never inline`, () => {
      const [rail] = sessionNodes([parentWith(state)], NEVER_EXPANDED);
      expect(rail?.children).toHaveLength(1);
      const [fold] = rail?.children ?? [];
      expect(fold?.kind).toBe("inactiveFold");
      expect(fold?.children?.map((c) => c.id)).toEqual(["child0"]);
    });
  }

  test("the fold sits after every inline child, so live work stays at the top", () => {
    const [rail] = sessionNodes([parentWith("ended", "active", "ended")], NEVER_EXPANDED);
    expect(rail?.children.map((c) => c.kind)).toEqual(["session", "inactiveFold"]);
  });

  test("inline children keep their incoming order", () => {
    const [rail] = sessionNodes([parentWith("active", "ended", "awaiting")], NEVER_EXPANDED);
    const inline = rail?.children.filter((c) => c.kind === "session") ?? [];
    expect(inline.map((c) => c.id)).toEqual(["child0", "child2"]);
  });

  test("folded children keep their incoming order", () => {
    const [rail] = sessionNodes([parentWith("ended", "active", "closed")], NEVER_EXPANDED);
    const fold = rail?.children.find((c) => c.kind === "inactiveFold");
    expect(fold?.children?.map((c) => c.id)).toEqual(["child0", "child2"]);
  });

  // Contrast with clustering's clusterMin=3 (hubcore/tree.go), an unrelated
  // mechanism - and with design-system.md's "past ~3" language, which describes
  // neither. One finished subagent folds.
  test("a single inactive subagent still folds - there is no minimum count", () => {
    const [rail] = sessionNodes([parentWith("ended")], NEVER_EXPANDED);
    expect(rail?.children.map((c) => c.kind)).toEqual(["inactiveFold"]);
  });

  test("a parent whose children are all current gets no fold node at all", () => {
    const [rail] = sessionNodes([parentWith("active", "idle")], NEVER_EXPANDED);
    expect(rail?.children.every((c) => c.kind === "session")).toBe(true);
  });

  test("the fold carries how many it hides, so the row can say so without expanding", () => {
    const [rail] = sessionNodes([parentWith("ended", "closed", "errored", "active")], NEVER_EXPANDED);
    const fold = rail?.children.find((c) => c.kind === "inactiveFold");
    expect(fold && "count" in fold && fold.count).toBe(3);
  });

  test("the fold starts collapsed", () => {
    const [rail] = sessionNodes([parentWith("ended")], NEVER_EXPANDED);
    const fold = rail?.children.find((c) => c.kind === "inactiveFold");
    expect(fold?.expanded).toBe(false);
  });

  test("the fold opens when the caller's lookup says its key is expanded", () => {
    const [rail] = sessionNodes([parentWith("ended")], overrideLookup(new Map([["inactive:parent", true]])));
    const fold = rail?.children.find((c) => c.kind === "inactiveFold");
    expect(fold?.expanded).toBe(true);
  });

  test("the fold's id is derived from its parent's row_id, so each parent's fold is its own", () => {
    const rails = sessionNodes(
      [
        node({ row_id: "p1", children: [node({ row_id: "a", ref: "ra", state: "ended" })] }),
        node({ row_id: "p2", children: [node({ row_id: "b", ref: "rb", state: "ended" })] }),
      ],
      overrideLookup(new Map([["inactive:p1", true]])),
    );
    expect(rails[0]?.children[0]?.expanded).toBe(true);
    expect(rails[1]?.children[0]?.expanded).toBe(false);
  });

  test("a nested subagent's own inactive children fold independently of its parent's", () => {
    const [rail] = sessionNodes(
      [
        node({
          row_id: "top",
          children: [
            node({
              row_id: "mid",
              ref: "rmid",
              state: "active",
              children: [node({ row_id: "leaf", ref: "rleaf", state: "ended" })],
            }),
            node({ row_id: "sib", ref: "rsib", state: "ended" }),
          ],
        }),
      ],
      NEVER_EXPANDED,
    );
    const mid = rail?.children.find((c) => c.id === "mid");
    expect(mid?.children?.map((c) => c.id)).toEqual(["inactive:mid"]);
    expect(rail?.children.find((c) => c.kind === "inactiveFold")?.id).toBe("inactive:top");
  });
});

describe("needsYouDescendantCount", () => {
  test("counts needs-you nodes in the subtree, not the node itself", () => {
    const root = node({
      row_id: "s1",
      ref: "r1",
      state: "active",
      children: [
        node({ row_id: "s1a", ref: "r1a", state: "awaiting" }),
        node({
          row_id: "s1b",
          ref: "r1b",
          state: "warning",
          children: [node({ row_id: "s1b1", ref: "r1b1", state: "active" })],
        }),
      ],
    });
    expect(needsYouDescendantCount(root)).toBe(2); // s1a + s1b, not the active root or grandchild
  });

  test("a leaf node (no children) counts zero", () => {
    expect(needsYouDescendantCount(node({ row_id: "leaf", state: "awaiting" }))).toBe(0);
  });
});

// parity-m3-sidebar-tree.md §2.3, §8: the wire hands an active project every
// tier at once (web_api_tree.go's projectSessions concatenates Current +
// Recent + Archived), and a live project's header must never list the archived
// ones. They surface only through the bottom "Archived sessions" section.
describe("archived-tier sessions divert out of their project's inline list", () => {
  test("projectNodes drops a session whose tier is archived", () => {
    const [rail] = projectNodes(
      [
        project({
          sessions: [node({ row_id: "keep", tier: "current" }), node({ row_id: "drop", ref: "rd", tier: "archived" })],
        }),
      ],
      NEVER_EXPANDED,
    );
    expect(rail?.children.map((c) => c.id)).toEqual(["keep"]);
  });

  test("projectNodes keeps every other tier, including an absent one", () => {
    const [rail] = projectNodes(
      [
        project({
          sessions: [
            node({ row_id: "cur", tier: "current" }),
            node({ row_id: "rec", ref: "r2", tier: "recent" }),
            node({ row_id: "none", ref: "r3" }),
          ],
        }),
      ],
      NEVER_EXPANDED,
    );
    expect(rail?.children.map((c) => c.id)).toEqual(["cur", "rec", "none"]);
  });

  test("a project whose sessions are all archived still renders, as an empty branch", () => {
    const [rail] = projectNodes([project({ sessions: [node({ tier: "archived" })] })], NEVER_EXPANDED);
    expect(rail?.children).toEqual([]);
  });
});

describe("archivedSessionGroups", () => {
  test("gives each project with archived sessions a group holding only those", () => {
    const groups = archivedSessionGroups(
      [
        project({
          key: "p1",
          sessions: [node({ row_id: "live", tier: "current" }), node({ row_id: "old", ref: "ro", tier: "archived" })],
        }),
      ],
      NEVER_EXPANDED,
    );
    expect(groups).toHaveLength(1);
    expect(groups[0]?.children.map((c) => c.id)).toEqual(["old"]);
  });

  test("skips a project with nothing archived", () => {
    expect(archivedSessionGroups([project({ sessions: [node({ tier: "current" })] })], NEVER_EXPANDED)).toEqual([]);
  });

  test("the group's id differs from the same project's own node id, so both can render at once", () => {
    const [group] = archivedSessionGroups(
      [project({ key: "p1", sessions: [node({ tier: "archived" })] })],
      NEVER_EXPANDED,
    );
    const [inline] = projectNodes([project({ key: "p1" })], NEVER_EXPANDED);
    expect(group?.id).not.toBe(inline?.id);
  });

  test("carries the real project, so the row's menu acts on it rather than a synthetic stand-in", () => {
    const p = project({ key: "p1", name: "Proj", working_dir: "/w", sessions: [node({ tier: "archived" })] });
    const [group] = archivedSessionGroups([p], NEVER_EXPANDED);
    expect(group?.project).toBe(p);
  });

  test("starts collapsed, and opens when the caller's lookup says so", () => {
    const p = project({ key: "p1", sessions: [node({ tier: "archived" })] });
    expect(archivedSessionGroups([p], NEVER_EXPANDED)[0]?.expanded).toBe(false);
    const [open] = archivedSessionGroups([p], overrideLookup(new Map([["archivedgroup:p1", true]])));
    expect(open?.expanded).toBe(true);
  });
});

describe("archivedCount", () => {
  test("counts a stub archived project by its session_count, which is all the wire sends", () => {
    expect(archivedCount([project({ session_count: 7, sessions: [] })], [])).toBe(7);
  });

  test("counts a hydrated archived project by its real sessions", () => {
    expect(archivedCount([project({ sessions: [node(), node({ ref: "r2" })] })], [])).toBe(2);
  });

  test("adds the archived-tier sessions living inside active projects", () => {
    const active = project({ sessions: [node({ tier: "current" }), node({ ref: "r2", tier: "archived" })] });
    expect(archivedCount([], [active])).toBe(1);
  });
});

describe("projectNodes", () => {
  test("ids are namespaced separately from any session row_id so they never collide within a section", () => {
    const [rail] = projectNodes([project({ key: "p1" })], NEVER_EXPANDED);
    expect(rail?.id).not.toBe("p1");
    expect(rail?.kind).toBe("project");
  });

  test("children come directly from project.sessions, mapped to session rail nodes", () => {
    const [rail] = projectNodes(
      [project({ sessions: [node({ row_id: "r1" }), node({ row_id: "r2" })] })],
      NEVER_EXPANDED,
    );
    expect(rail?.children?.map((c) => c.id)).toEqual(["r1", "r2"]);
  });

  test("a project with no sessions renders as a leaf", () => {
    const [rail] = projectNodes([project({ sessions: [] })], NEVER_EXPANDED);
    expect(rail?.children).toEqual([]);
  });

  test("sorts a project's sessions needs-you-first, preserving relative order otherwise (vbh8)", () => {
    const [rail] = projectNodes(
      [
        project({
          sessions: [
            node({ row_id: "idle1", ref: "r1", state: "idle" }),
            node({ row_id: "needsYou1", ref: "r2", state: "awaiting" }),
            node({ row_id: "idle2", ref: "r3", state: "idle" }),
            // Needs-you via a descendant, not its own state - still sorts first.
            node({
              row_id: "hasNeedsYouChild",
              ref: "r4",
              state: "active",
              children: [node({ ref: "r4a", state: "warning" })],
            }),
          ],
        }),
      ],
      NEVER_EXPANDED,
    );
    expect(rail?.children?.map((c) => c.id)).toEqual(["needsYou1", "hasNeedsYouChild", "idle1", "idle2"]);
  });

  test("expanded defaults to the project's own default_expanded wire field", () => {
    // An empty override map falls back to each call's own default (see
    // overrideLookup) - NEVER_EXPANDED would ignore the default entirely,
    // which is the wrong double to prove a default is actually being read.
    const noOverrides = overrideLookup(new Map());
    const [expandedByDefault] = projectNodes([project({ key: "p1", default_expanded: true })], noOverrides);
    expect(expandedByDefault?.expanded).toBe(true);

    const [collapsedByDefault] = projectNodes([project({ key: "p2", default_expanded: false })], noOverrides);
    expect(collapsedByDefault?.expanded).toBe(false);
  });

  test("an explicit isExpanded override wins over default_expanded", () => {
    const rails = projectNodes([project({ key: "p1", default_expanded: true })], () => false);
    expect(rails[0]?.expanded).toBe(false);
  });
});

describe("archivedProjectNodes", () => {
  test("a project not yet in projectDetails, with sessions to load, gets one loading placeholder child", () => {
    const [rail] = archivedProjectNodes([project({ key: "p1", session_count: 3 })], new Map(), NEVER_EXPANDED);
    expect(rail?.children).toHaveLength(1);
    expect(rail?.children?.[0]?.kind).toBe("loading");
  });

  test("a project not yet in projectDetails with zero sessions gets no children at all", () => {
    const [rail] = archivedProjectNodes([project({ key: "p1", session_count: 0 })], new Map(), NEVER_EXPANDED);
    expect(rail?.children).toEqual([]);
  });

  test("a project present in projectDetails renders its real, hydrated sessions instead of a placeholder", () => {
    const detail = project({ key: "p1", session_count: 2, sessions: [node({ row_id: "r1" }), node({ row_id: "r2" })] });
    const [rail] = archivedProjectNodes(
      [project({ key: "p1", session_count: 2 })],
      new Map([["p1", detail]]),
      NEVER_EXPANDED,
    );
    expect(rail?.children?.map((c) => c.id)).toEqual(["r1", "r2"]);
  });

  test("an archived project ignores default_expanded and starts collapsed regardless", () => {
    const [rail] = archivedProjectNodes([project({ key: "p1", default_expanded: true })], new Map(), NEVER_EXPANDED);
    expect(rail?.expanded).toBe(false);
  });

  test("an explicit isExpanded override still applies to an archived project", () => {
    // The rail node id is namespaced (see the projectNodes id test above)
    // and private to this module - discover it via an unrelated call first
    // rather than hardcoding the private id format in the test.
    const [unexpanded] = archivedProjectNodes([project({ key: "p1" })], new Map(), NEVER_EXPANDED);
    const isExpanded = overrideLookup(new Map([[unexpanded!.id, true]]));
    const [rail] = archivedProjectNodes([project({ key: "p1" })], new Map(), isExpanded);
    expect(unexpanded?.id).toBe(rail?.id);
    expect(rail?.expanded).toBe(true);
  });
});

describe("overrideLookup", () => {
  test("returns the override when present, otherwise the given default", () => {
    const isExpanded = overrideLookup(
      new Map([
        ["a", true],
        ["b", false],
      ]),
    );
    expect(isExpanded("a", false)).toBe(true);
    expect(isExpanded("b", true)).toBe(false);
    expect(isExpanded("c", true)).toBe(true);
    expect(isExpanded("c", false)).toBe(false);
  });
});

describe("projectNodeIdForSessionRef", () => {
  test("returns the projectnode: id matching what projectNodes assigns for a session under a project", () => {
    const projects = [project({ key: "p1", sessions: [node({ ref: "local:a" })] })];
    // Same id projectNodes would put on the branch, so Rail's reveal expands
    // exactly that node's override entry.
    expect(projectNodeIdForSessionRef(projects, "local:a")).toBe("projectnode:p1");
    const [rail] = projectNodes(projects, () => false);
    expect(projectNodeIdForSessionRef(projects, "local:a")).toBe(rail?.id);
  });

  test("finds a session nested in a subagent cluster under the project", () => {
    const projects = [
      project({ key: "p2", sessions: [node({ ref: "local:parent", children: [node({ ref: "local:child" })] })] }),
    ];
    expect(projectNodeIdForSessionRef(projects, "local:child")).toBe("projectnode:p2");
  });

  test("returns null for a ref that belongs to no project (a top-level tier entry)", () => {
    const projects = [project({ key: "p1", sessions: [node({ ref: "local:a" })] })];
    expect(projectNodeIdForSessionRef(projects, "local:elsewhere")).toBeNull();
  });

  test("returns the FIRST matching project's id and does not match a different project", () => {
    const projects = [
      project({ key: "p1", sessions: [node({ ref: "local:a" })] }),
      project({ key: "p2", sessions: [node({ ref: "local:b" })] }),
    ];
    expect(projectNodeIdForSessionRef(projects, "local:b")).toBe("projectnode:p2");
  });
});

// A cluster row (hubcore/tree.go's repeated-title fold, Kind "cluster") is
// ALREADY a disclosure, and its members are ordinary top-level sessions -
// never subagents. Clustering only ever folds idle/ended sessions
// (tree.go's clusterable), so every member is terminal by construction: a
// state-based split applied here would put EVERY cluster's members behind a
// second, wrongly-named "Inactive subagents" fold inside it. Caught on real
// hub data, where every cluster looked empty until you expanded twice.
describe("cluster rows are not subject to the subagent fold", () => {
  test("an ended cluster member renders directly under its cluster", () => {
    const cluster = node({
      row_id: "cluster:abc",
      kind: "cluster",
      cluster_count: 3,
      children: [node({ row_id: "m1", ref: "r1", state: "ended" }), node({ row_id: "m2", ref: "r2", state: "ended" })],
    });
    const [rail] = sessionNodes([cluster], NEVER_EXPANDED);
    expect(rail?.children.map((c) => c.id)).toEqual(["m1", "m2"]);
    expect(rail?.children.every((c) => c.kind === "session")).toBe(true);
  });
});

// parity §1/§2.3: the server caps each tier at 50 rows
// (maxSidebarSessionsPerTier) and ships the remainder as more_current /
// more_recent / more_archived. Nothing read those fields, so on a project with
// more than 50 sessions in a tier the extras simply were not there and nothing
// said so. hubcore's own doc comment describes the intended affordance: "the
// sidebar shows '+N older' for the rest".
describe("per-tier overflow", () => {
  test("projectNodes appends an overflow row summing the two tiers it renders", () => {
    const [rail] = projectNodes([project({ sessions: [node()], more_current: 7, more_recent: 5 })], NEVER_EXPANDED);
    const last = rail?.children.at(-1);
    expect(last?.kind).toBe("overflow");
    expect(last && "count" in last && last.count).toBe(12);
  });

  test("the overflow row sits last, after every session", () => {
    const [rail] = projectNodes(
      [project({ sessions: [node({ row_id: "a" }), node({ row_id: "b", ref: "rb" })], more_current: 1 })],
      NEVER_EXPANDED,
    );
    expect(rail?.children.map((c) => c.kind)).toEqual(["session", "session", "overflow"]);
  });

  test("no overflow row when nothing was capped", () => {
    const [rail] = projectNodes([project({ sessions: [node()] })], NEVER_EXPANDED);
    expect(rail?.children.every((c) => c.kind === "session")).toBe(true);
  });

  test("more_archived does not leak into the project's inline list - that tier is not rendered there", () => {
    const [rail] = projectNodes([project({ sessions: [node()], more_archived: 9 })], NEVER_EXPANDED);
    expect(rail?.children.every((c) => c.kind === "session")).toBe(true);
  });

  test("the archived sub-branch reports its own tier's overflow", () => {
    const [group] = archivedSessionGroups(
      [project({ sessions: [node({ tier: "archived" })], more_archived: 4, more_current: 99 })],
      NEVER_EXPANDED,
    );
    const last = group?.children.at(-1);
    expect(last?.kind).toBe("overflow");
    expect(last && "count" in last && last.count).toBe(4);
  });

  test("a hydrated archived project reports every tier's overflow, since it renders them all", () => {
    const stub = project({ key: "p1", session_count: 3, sessions: [] });
    const detail = project({ key: "p1", sessions: [node()], more_current: 1, more_recent: 2, more_archived: 3 });
    const [rail] = archivedProjectNodes([stub], new Map([["p1", detail]]), NEVER_EXPANDED);
    const last = rail?.children.at(-1);
    expect(last?.kind).toBe("overflow");
    expect(last && "count" in last && last.count).toBe(6);
  });

  test("an un-hydrated archived stub shows its loading row, not an overflow row", () => {
    const [rail] = archivedProjectNodes(
      [project({ key: "p1", session_count: 3, sessions: [], more_current: 5 })],
      new Map(),
      NEVER_EXPANDED,
    );
    expect(rail?.children.map((c) => c.kind)).toEqual(["loading"]);
  });
});

// A nested session opens BESIDE the top-level session that spawned it, so the
// rail has to answer "which top-level row does this ref belong under?". The
// wire tree is already nested, so this is a walk rather than a new index.
describe("topLevelAncestorRef", () => {
  const tiers = (projects: ApiTreeProject[]) => projects;

  test("a direct child resolves to its parent", () => {
    const p = project({
      sessions: [node({ ref: "local:parent", children: [node({ row_id: "c", ref: "local:kid" })] })],
    });
    expect(topLevelAncestorRef(tiers([p]), "local:kid")).toBe("local:parent");
  });

  test("a deeply nested child resolves to the TOP-level row, not its immediate parent", () => {
    const p = project({
      sessions: [
        node({
          ref: "local:top",
          children: [node({ row_id: "m", ref: "local:mid", children: [node({ row_id: "l", ref: "local:leaf" })] })],
        }),
      ],
    });
    expect(topLevelAncestorRef(tiers([p]), "local:leaf")).toBe("local:top");
  });

  test("a top-level session is its own ancestor", () => {
    const p = project({ sessions: [node({ ref: "local:solo" })] });
    expect(topLevelAncestorRef(tiers([p]), "local:solo")).toBe("local:solo");
  });

  test("a ref that is nowhere in the given projects resolves to null", () => {
    const p = project({ sessions: [node({ ref: "local:a" })] });
    expect(topLevelAncestorRef(tiers([p]), "local:missing")).toBeNull();
  });

  test("searches across every project it is given", () => {
    const a = project({ key: "p1", sessions: [node({ ref: "local:a" })] });
    const b = project({
      key: "p2",
      sessions: [node({ row_id: "t2", ref: "local:top2", children: [node({ row_id: "k2", ref: "local:kid2" })] })],
    });
    expect(topLevelAncestorRef([a, b], "local:kid2")).toBe("local:top2");
  });
});
