import { hydrateThread, type SessionJobsResponse } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { connectionStore } from "../../../stores/connection";
import {
  activityClient,
  activityContext,
  activityThread,
  activityWatch,
} from "../../../stores/sessionActivityTestUtils";
import { ActivityPanelBody } from "./ActivityPanel";

const ref = "remote:owner",
  model = () => hydrateThread(activityThread(ref), ref, 0);
afterEach(() => {
  cleanup();
  connectionStore.setState({ client: null, state: "idle" });
});

test("typed receiver watches remain visible while an independent job page is pending", async () => {
  const client = activityClient(),
    pending = deferred<SessionJobsResponse>();
  client.on("evener/thread/jobs/list", () => pending.promise);
  client.on("evener/thread/watches/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    watches: [activityWatch({ note: "receiver heartbeat", cadence: [{ kind: "every", seconds: 60 }] }, ref)],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  render(<ActivityPanelBody sessionRef={ref} model={model()} />);
  expect(await screen.findByRole("treeitem", { name: "Watch: receiver heartbeat" })).toBeTruthy();
  expect(screen.queryByText("No retained activity yet")).toBeNull();
  await act(async () =>
    pending.resolve({ context: activityContext(), scope: "subtree", jobs: [], page: { complete: true, issues: [] } }),
  );
});

test("ended retained source still renders supplied watch cadence, note and deliveries", async () => {
  const client = activityClient();
  client.on("evener/thread/watches/list", ({ ref, scope }) => ({
    context: { ...activityContext(ref), availability: "retained" },
    scope: scope ?? "session",
    watches: [
      activityWatch({ note: "retained delivery", active: false, deliveries: 2, endReason: "runtime_lost" }, ref),
    ],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  render(<ActivityPanelBody sessionRef={ref} model={model()} />);
  expect(await screen.findByRole("treeitem", { name: "Watch: retained delivery" })).toBeTruthy();
  expect(screen.getByTestId("watch-facts").textContent).toContain("2 deliveries");
});

test("authoritative empty requires completed collection reads", async () => {
  const client = activityClient();
  client.on("evener/thread/delegates/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    delegates: [],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  render(<ActivityPanelBody sessionRef={ref} model={model()} />);
  await waitFor(() => expect(screen.getByText("No retained activity yet")).toBeTruthy());
  expect(client.calls.filter((c) => c.method === "evener/thread/watches/list")).toHaveLength(1);
});
