// @vitest-environment node
import { describe, expect, it } from "vitest";
import { countSearchMatches, type GroupedSearchRow, groupSearchResults } from "./searchGrouping";
import type { SearchResult } from "./types.gen";

function liveRow(id: string, title = id): SearchResult {
  return { id, ref: `local:${id}`, title, project: "proj", state: "active", age: "now" };
}

function pastRow(id: string, title = id): SearchResult {
  return { id, ref: `local:${id}`, title, project: "proj", state: "ended", age: "2h" };
}

/** A matching subagent row naming its top-level session, as the hub
 * annotates past results (SearchResult.topLevel). */
function childOf(parent: { id: string; title: string; state: string; age: string }, id: string): SearchResult {
  return {
    ...pastRow(id),
    topLevel: { id: parent.id, ref: `local:${parent.id}`, title: parent.title, state: parent.state, age: parent.age },
  };
}

function refs(rows: GroupedSearchRow[]): string[] {
  return rows.map((row) => row.result.ref);
}

describe("groupSearchResults", () => {
  it("nests a matching subagent under its real past anchor, contiguously at the first member's position", () => {
    const parent = pastRow("root", "Alpha root");
    const child = childOf(parent, "sub");
    const standalone = pastRow("other", "Other work");
    // Past order is newest-first: the child, an unrelated match, then the
    // parent — the group must open at the child's position, anchor first.
    const grouped = groupSearchResults([], [child, standalone, parent]);

    expect(refs(grouped.past)).toEqual([parent.ref, child.ref, standalone.ref]);
    expect(grouped.past[0]).toMatchObject({ nested: false, indirect: false });
    expect(grouped.past[1]).toMatchObject({ nested: true, indirect: false });
    expect(grouped.past[2]).toMatchObject({ nested: false, indirect: false });
  });

  it("opens the group at the anchor's position when the anchor is the newest member", () => {
    const parent = pastRow("root", "Alpha root");
    const child = childOf(parent, "sub");
    const grouped = groupSearchResults([], [parent, child]);

    expect(refs(grouped.past)).toEqual([parent.ref, child.ref]);
    expect(grouped.past[0]).toMatchObject({ nested: false, indirect: false });
    expect(grouped.past[1]).toMatchObject({ nested: true, indirect: false });
  });

  it("synthesizes a greyed anchor when the top-level session has no row of its own", () => {
    const child = childOf({ id: "gone", title: "Gone root", state: "ended", age: "3h" }, "sub");
    const grouped = groupSearchResults([], [child]);

    expect(grouped.past).toHaveLength(2);
    const [anchor, nested] = grouped.past;
    expect(anchor).toMatchObject({
      nested: false,
      indirect: true,
    });
    expect(anchor?.result).toMatchObject({
      id: "gone",
      ref: "local:gone",
      title: "Gone root",
      state: "ended",
      age: "3h",
      project: "",
    });
    expect(nested).toMatchObject({ nested: true, indirect: false });
    expect(nested?.result.ref).toBe(child.ref);
  });

  it("synthesizes a live-state anchor in the past section for an unmatched live parent", () => {
    const child = childOf({ id: "running", title: "Live root", state: "active", age: "now" }, "sub");
    const grouped = groupSearchResults([], [child]);

    expect(grouped.live).toEqual([]);
    const [anchor, nested] = grouped.past;
    expect(anchor?.indirect).toBe(true);
    expect(anchor?.result.state).toBe("active");
    expect(anchor?.result.age).toBe("now");
    expect(nested?.nested).toBe(true);
  });

  it("nests past children under a matched live anchor in the live section, keeping them out of past", () => {
    const parent = liveRow("root", "Alpha root");
    const child = childOf(parent, "sub");
    const grouped = groupSearchResults([parent], [child]);

    expect(refs(grouped.live)).toEqual([parent.ref, child.ref]);
    expect(grouped.live[1]).toMatchObject({ nested: true, indirect: false });
    expect(grouped.past).toEqual([]);
  });

  it("keeps multiple children in match order under one anchor", () => {
    const parent = pastRow("root", "Alpha root");
    const first = childOf(parent, "sub1");
    const second = childOf(parent, "sub2");
    const grouped = groupSearchResults([], [first, second, parent]);

    expect(refs(grouped.past)).toEqual([parent.ref, first.ref, second.ref]);
    expect(grouped.past.slice(1).every((row) => row.nested)).toBe(true);
  });

  it("leaves an unannotated row flat, exactly as today", () => {
    const orphanSubagent = pastRow("sub", "Unannotated delegate");
    const plain = pastRow("plain", "Plain match");
    const grouped = groupSearchResults([], [orphanSubagent, plain]);

    expect(refs(grouped.past)).toEqual([orphanSubagent.ref, plain.ref]);
    expect(grouped.past.every((row) => !row.nested && !row.indirect)).toBe(true);
  });

  it("returns a topLevel-less response unchanged, adding no dedup of its own", () => {
    // An older hub: no topLevel anywhere, and a past row duplicating a
    // live ref stays in the answer — the web palette renders it verbatim
    // today, and the helper must not change old-hub rendering.
    const live = [liveRow("dup", "Live one")];
    const past = [pastRow("dup", "Live one again"), pastRow("plain", "Plain match")];
    const grouped = groupSearchResults(live, past);

    expect(refs(grouped.live)).toEqual(["local:dup"]);
    expect(refs(grouped.past)).toEqual(["local:dup", "local:plain"]);
    expect(grouped.past.every((row) => !row.nested && !row.indirect)).toBe(true);
  });

  it("keeps live rows without children as plain rows", () => {
    const only = liveRow("solo", "Solo session");
    const grouped = groupSearchResults([only], [pastRow("plain", "Plain match")]);

    expect(grouped.live).toEqual([{ result: only, nested: false, indirect: false }]);
  });
});

describe("countSearchMatches", () => {
  it("counts every row but the indirect anchors, whose matches are their subagents'", () => {
    const parent = pastRow("root", "Alpha root");
    const child = childOf(parent, "sub");
    const goneSub = childOf({ id: "gone", title: "Gone root", state: "ended", age: "3h" }, "goneSub");
    const grouped = groupSearchResults([], [child, goneSub, parent]);
    const flat = groupSearchResults([], [pastRow("plain", "Plain match")]).past;

    // The greyed synthesized anchor is context for its subagent's match,
    // not a match of its own; the real anchor and both children are.
    expect(countSearchMatches(grouped.past)).toBe(3);
    expect(countSearchMatches(flat)).toBe(1);
  });
});
