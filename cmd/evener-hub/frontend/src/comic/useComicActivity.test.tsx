import { hydrateThread, type SessionDelegatesResponse, type ThreadModel } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import { notificationWireItem } from "@evener/appwire-client/testing/notificationWireFixtures";
import {
  subagentOutcomesDelegatesResponse,
  subagentResumedDelegatesResponse,
} from "@evener/appwire-client/testing/subagentWireFixtures";
import { act, cleanup, render, waitFor } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { connectionStore } from "../stores/connection";
import { acquireSessionActivity } from "../stores/sessionActivity";
import { projectComicConversation } from "./projection";
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
  client.on("thread/read", () => ({ thread: wireThread(ref, { sessionId: fixture.context.sessionId }) }));
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
  const model: ThreadModel = hydrateThread(
    { thread: wireThread(ref, { sessionId: fixture.context.sessionId }) },
    ref,
    0,
  );
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

test.each(["local", "remote"])(
  "keeps %s routing aliases separate from canonical activity owners across Clear publication orders",
  async (host) => {
    const fixture = subagentOutcomesDelegatesResponse();
    const ref = `${host}:workspace-alias`;
    const client = new FakeClient("ready");
    const contextFor = (generation: string) => ({
      ...fixture.context,
      ref: `${host}:canonical-${generation}`,
      sessionId: `session-${generation}`,
      rootRef: `${host}:ancestor`,
      parentRef: `${host}:ancestor`,
      delegateId: `dlg-${generation}`,
      delegateName: `Named ${generation}`,
      ancestors: [{ ref: `${host}:ancestor`, sessionId: "ancestor", title: "Ancestor" }],
    });
    let context = contextFor("old");
    const modelFor = (generation: string) =>
      hydrateThread(
        {
          thread: wireThread(ref, {
            id: `wire-${generation}`,
            sessionId: `session-${generation}`,
            turns: [
              {
                id: "turn",
                status: "completed",
                itemsView: "full",
                items: [
                  {
                    id: "answer",
                    type: "agentMessage",
                    text: "Answer without authorship",
                  },
                  { ...notificationWireItem("delegate-failed-unnamed"), steeringKind: "notification" },
                ],
              },
            ],
          }),
        },
        ref,
        0,
      );
    let model = modelFor("old");
    client.on("thread/read", () => ({
      thread: wireThread(ref, { id: model.threadId, sessionId: model.imageSessionId }),
    }));
    client.on("thread/unsubscribe", () => ({}));
    const counts = { known: true, total: 1, active: 0, failed: 0, completed: 1 };
    client.on("evener/thread/activity/read", () => ({
      context,
      scope: "session",
      delegates: counts,
      jobs: counts,
      watches: counts,
    }));
    client.on("evener/thread/delegates/list", () => ({
      ...fixture,
      context,
      scope: "session",
      delegates: fixture.delegates.slice(0, 1).map((row) => ({
        ...row,
        delegateId: "dlg_2",
        name: "Canonical worker",
        ownerRef: context.ref,
        rootRef: context.rootRef,
        childRef: `${host}:child`,
      })),
    }));
    connectionStore.getState().connect(client);
    let observed!: ReturnType<typeof useComicActivity>;
    function Harness() {
      observed = useComicActivity(ref, model);
      return null;
    }
    const mounted = render(<Harness />);
    const lease = acquireSessionActivity(client, ref);
    try {
      await waitFor(() => expect(observed.snapshot?.delegates.rows).toHaveLength(1));
      expect(observed.strips.map((strip) => strip.ownerRef)).toEqual([context.ref]);
      expect(observed.context.assistant?.name).toBe("Named old");
      expect(
        projectComicConversation(model, observed.context).find((entry) => entry.sourceItemId === "item_steering_4")
          ?.persona?.name,
      ).toBe("Canonical worker");
      // Model first: the old activity must not lend the new transcript its cast.
      model = modelFor("new");
      mounted.rerender(<Harness />);
      expect(observed.strips).toEqual([]);
      expect(observed.snapshot).toBeNull();
      expect(observed.entities.size).toBe(0);
      expect(observed.context.assistant).toBeUndefined();
      context = contextFor("new");
      await act(async () => {
        await lease.store.refresh("delegates");
      });
      expect(observed.strips.map((strip) => strip.ownerRef)).toEqual([context.ref]);
      expect(observed.context.assistant?.name).toBe("Named new");
      // Activity first: even a complete replacement collection cannot rename the old model.
      context = contextFor("next");
      await act(async () => {
        await lease.store.refresh("delegates");
      });
      expect(observed.strips).toEqual([]);
      expect(observed.snapshot).toBeNull();
      expect(observed.entities.size).toBe(0);
      expect(observed.context.assistant).toBeUndefined();
      model = modelFor("next");
      mounted.rerender(<Harness />);
      expect(observed.strips.map((strip) => strip.ownerRef)).toEqual([context.ref]);
      expect(observed.context.assistant?.name).toBe("Named next");
      expect(
        client.calls
          .filter((call) => call.method !== "thread/unsubscribe")
          .every((call) => (call.params as { ref?: string }).ref === ref),
      ).toBe(true);
    } finally {
      lease.release();
      mounted.unmount();
    }
  },
);
