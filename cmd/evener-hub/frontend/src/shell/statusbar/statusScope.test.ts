import { SessionActivityStore } from "@evener/appwire-client";
import { createNavigationStore } from "@evener/appwire-client/state/navigation";
import { memoryNavigationPersistence } from "@evener/appwire-client/testing/navigationPersistence";
import { describe, expect, it } from "vitest";
import { activityClient, activityContext, activitySummary } from "../../stores/sessionActivityTestUtils";
import { deriveScope } from "./statusScope";

const navigation = () => createNavigationStore({ persistence: memoryNavigationPersistence() }).getState();
describe("typed activity scope", () => {
  it("keeps an exact deep child before navigation or activity context is loaded", () => {
    const scope = deriveScope(navigation(), "remote:deep", null, null);
    expect(scope?.leaf.ref).toBe("remote:deep");
    expect(scope?.ancestryKnown).toBe(false);
    expect(scope?.counts).toMatchObject({
      activeSubagents: null,
      runningJobs: null,
      armedWatches: null,
      delegatesTotal: null,
      jobsTotal: null,
      watchesTotal: null,
    });
  });
  it("takes ancestry and authoritative counts from context and summary, not loaded rows", async () => {
    const client = activityClient();
    const summary = activitySummary("remote:deep");
    summary.context = {
      ...activityContext("remote:deep"),
      rootRef: "remote:root",
      ancestors: [
        { ref: "remote:root", sessionId: "root", title: "Root" },
        { ref: "remote:parent", sessionId: "parent", title: "Parent" },
      ],
    };
    client.on("evener/thread/activity/read", ({ scope }) => summary);
    const store = new SessionActivityStore(client, "remote:deep");
    await store.refresh();
    const scope = deriveScope(navigation(), "remote:deep", store.getSnapshot(), null);
    expect(scope?.path.map((crumb) => crumb.ref)).toEqual(["remote:root", "remote:parent", "remote:deep"]);
    expect(scope?.counts.runningJobs).toBe(2);
    expect(scope?.counts.jobsTotal).toBe(201);
    expect(scope?.counts.delegatesTotal).toBeNull();
    expect(scope?.counts.activeSubagents).toBeNull();
    // Subagents are counted at every depth, from the subtree count given,
    // never from the session's own summary.
    const subtree = { known: true, total: 4, active: 3, failed: 0, completed: 1 };
    const counted = deriveScope(navigation(), "remote:deep", store.getSnapshot(), subtree);
    expect(counted.counts).toMatchObject({ activeSubagents: 3, delegatesTotal: 4, runningJobs: 2 });
    store.dispose();
  });
});
