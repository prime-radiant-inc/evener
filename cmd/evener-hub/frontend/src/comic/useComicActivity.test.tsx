import { hydrateThread, type SessionDelegatesResponse, type ThreadModel } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import {
  subagentOutcomesDelegatesResponse,
  subagentResumedDelegatesResponse,
} from "@evener/appwire-client/testing/subagentWireFixtures";
import { act, cleanup, render, waitFor } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { connectionStore } from "../stores/connection";
import { useComicActivity } from "./useComicActivity";

afterEach(() => {
  cleanup();
  connectionStore.setState({ client: null, state: "idle" });
});
test("pages only bounded session delegates and keeps unavailable evidence with its chosen generation", async () => {
  const fixture = subagentOutcomesDelegatesResponse();
  const resumed = subagentResumedDelegatesResponse();
  const ref = fixture.context.ref;
  const client = new FakeClient("ready");
  client.on("thread/read", () => ({ thread: wireThread(ref) }));
  client.on("thread/unsubscribe", () => ({}));
  const counts = { known: false, total: 0, active: 0, failed: 0, completed: 0 };
  client.on("evener/thread/activity/read", () => ({
    context: fixture.context,
    scope: "session",
    delegates: counts,
    jobs: counts,
    watches: counts,
  }));
  let page: SessionDelegatesResponse = {
    ...fixture,
    scope: "session",
    delegates: fixture.delegates.slice(0, 1),
    page: { complete: false, nextCursor: "bounded-next", issues: [{ ref, code: "actionUnavailable" }] },
  };
  client.on("evener/thread/delegates/list", () => page);
  connectionStore.getState().connect(client);
  let observed!: ReturnType<typeof useComicActivity>;
  const model: ThreadModel = hydrateThread({ thread: wireThread(ref) }, ref, 0);
  function Harness() {
    observed = useComicActivity(ref, model);
    return null;
  }
  const mounted = render(<Harness />);
  await waitFor(() => expect(observed.strips).toHaveLength(1));
  expect(observed.snapshot?.delegates.complete).toBe(false);
  expect(observed.snapshot?.delegates.issues).toEqual([{ ref, code: "actionUnavailable" }]);
  page = { ...resumed, scope: "session", delegates: resumed.delegates.slice(1) };
  await act(async () => {
    await observed.loadMore();
  });
  expect(observed.strips).toHaveLength(3);
  const report = observed.strips.find((s) => s.delegateId === "dlg_reported")!.entity;
  expect(report.row?.delegate).toMatchObject({ runGeneration: 2, reportPreview: "Second run report." });
  const calls = client.calls.filter((c) => c.method === "evener/thread/delegates/list");
  expect(calls).toHaveLength(2);
  expect(calls[1]?.params).toMatchObject({ ref, scope: "session", cursor: "bounded-next" });
  expect(
    client.calls.some(
      (c) =>
        c.method === "evener/thread/jobs/list" ||
        c.method === "evener/thread/watches/list" ||
        c.method === "evener/jobs/list" ||
        c.method === "thread/resume" ||
        c.method === "turn/start",
    ),
  ).toBe(false);
  mounted.unmount();
});
