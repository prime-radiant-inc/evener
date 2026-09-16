// The seam between the panel store and the summary store. activitySummary.ts
// imports activityPanel.ts; the panel must not import back, so what a
// continuation page owes the badge crosses through the link the summary
// store registers, and the tests here drive that link directly.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { ActivityTree } from "@evener/appwire-client";
import { describe, expect, test, vi } from "vitest";
import {
  type ActivitySummaryLink,
  activityPanelStore,
  type ContinuationSettlement,
  linkActivitySummary,
  resetActivityPanelStoreForTests,
} from "./activityPanel";

const HERE = dirname(fileURLToPath(import.meta.url));

function tree(revision = 1): ActivityTree {
  return {
    revision,
    root: {
      kind: "session",
      sessionId: "sess_a",
      ref: "ref_a",
      label: "A",
      aggregate: "running",
      counts: { active: 1, failed: 0, completed: 0, complete: true },
      entries: [],
      branch: {},
    },
  };
}

function linkWithGeneration(generation: number | undefined) {
  const settled = vi.fn<(ref: string, settlement: ContinuationSettlement) => void>();
  const link: ActivitySummaryLink = {
    summaryGeneration: () => generation,
    onContinuationSettled: settled,
  };
  linkActivitySummary(link);
  return settled;
}

function readyRoot(): void {
  const root = activityPanelStore.getState().beginFetch("ref_a");
  activityPanelStore.getState().publishFetch("ref_a", root, { kind: "ready", tree: tree() });
}

describe("activityPanel does not depend on activitySummary", () => {
  test("the panel store imports nothing from ./activitySummary", () => {
    const source = readFileSync(join(HERE, "activityPanel.ts"), "utf8");
    expect(source).not.toMatch(/["']\.\/activitySummary["']/);
  });
});

describe("the summary link", () => {
  test("a merged page settles with the generation it began under and the merged counts", () => {
    resetActivityPanelStoreForTests();
    const settled = linkWithGeneration(7);
    readyRoot();
    const page = activityPanelStore.getState().beginFetch("ref_a", { nodeID: "session:sess_a" });
    expect(settled).not.toHaveBeenCalled();
    activityPanelStore.getState().publishFetch("ref_a", page, { kind: "ready", tree: tree(2) });
    const merged = activityPanelStore.getState().entries.get("ref_a")?.load;
    if (merged?.kind !== "ready") throw new Error("expected the page to merge");
    expect(settled).toHaveBeenCalledTimes(1);
    expect(settled).toHaveBeenCalledWith("ref_a", {
      summaryRequestID: 7,
      debt: { kind: "counts", counts: merged.tree.root.counts },
    });
  });

  test("a failed page settles as a failure debt", () => {
    resetActivityPanelStoreForTests();
    const settled = linkWithGeneration(7);
    readyRoot();
    const page = activityPanelStore.getState().beginFetch("ref_a", { nodeID: "session:sess_a" });
    activityPanelStore.getState().publishFetch("ref_a", page, {
      kind: "continuation-failed",
      nodeID: "session:sess_a",
      message: "branch failed",
    });
    expect(settled).toHaveBeenCalledWith("ref_a", { summaryRequestID: 7, debt: { kind: "failure" } });
  });

  test("a page begun with no summary entry still settles, so a queued root can be issued", () => {
    resetActivityPanelStoreForTests();
    const settled = linkWithGeneration(undefined);
    readyRoot();
    const page = activityPanelStore.getState().beginFetch("ref_a", { nodeID: "session:sess_a" });
    activityPanelStore.getState().publishFetch("ref_a", page, { kind: "ready", tree: tree(2) });
    expect(settled).toHaveBeenCalledTimes(1);
    expect(settled.mock.calls[0]?.[1].summaryRequestID).toBeUndefined();
  });

  test("a root publication and a dropped stale page settle nothing", () => {
    resetActivityPanelStoreForTests();
    const settled = linkWithGeneration(7);
    readyRoot();
    const stale = activityPanelStore.getState().beginFetch("ref_a", { nodeID: "session:sess_a" });
    activityPanelStore.getState().beginFetch("ref_a");
    activityPanelStore.getState().publishFetch("ref_a", stale, { kind: "ready", tree: tree(2) });
    expect(settled).not.toHaveBeenCalled();
  });
});
