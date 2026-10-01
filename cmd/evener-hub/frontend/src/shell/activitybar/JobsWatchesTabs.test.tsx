import { createNavigationStore } from "@evener/appwire-client/state/navigation";
import { memoryNavigationPersistence } from "@evener/appwire-client/testing/navigationPersistence";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { connectionStore } from "../../stores/connection";
import { activityClient, activityContext, activityJob, activityWatch } from "../../stores/sessionActivityTestUtils";
import { deriveScope } from "../statusbar/statusScope";
import { JobsTab } from "./JobsTab";
import { WatchesTab } from "./WatchesTab";

const scope = () =>
  deriveScope(createNavigationStore({ persistence: memoryNavigationPersistence() }).getState(), "remote:owner");
afterEach(() => {
  cleanup();
  connectionStore.setState({ client: null, state: "idle" });
});
test("jobs show supplied terminal status and have no invented transcript action", async () => {
  const client = activityClient();
  client.on("evener/thread/jobs/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    jobs: [
      activityJob({
        transcriptRef: undefined,
        command: "real status",
        status: "command_exited_nonzero",
        terminal: true,
        reason: "exit 2",
      }),
    ],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  render(<JobsTab scope={scope()} />);
  expect(await screen.findByText("real status")).toBeTruthy();
  expect(screen.getByText(/Command failed/)).toBeTruthy();
  expect(screen.queryByRole("button", { name: /real status/ })).toBeNull();
});
test("watch tab reads typed receiver state and preserves cadence/delivery vocabulary", async () => {
  const client = activityClient();
  client.on("evener/thread/watches/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    watches: [
      activityWatch({ note: "heartbeat", cadence: [{ kind: "every", seconds: 300 }] }),
      activityWatch({
        id: "fired",
        note: "one shot",
        active: false,
        deliveries: 1,
        cadence: [{ kind: "after", seconds: 600 }],
      }),
    ],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  render(<WatchesTab scope={scope()} />);
  expect(await screen.findByText("heartbeat")).toBeTruthy();
  expect(screen.getByText("every 5m · armed")).toBeTruthy();
  expect(screen.getByText("after 10m · 1 delivery · not armed")).toBeTruthy();
  expect(client.calls.filter((c) => c.method === "evener/thread/jobs/list")).toHaveLength(0);
});
