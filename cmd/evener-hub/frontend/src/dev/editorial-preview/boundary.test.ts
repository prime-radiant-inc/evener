import { expect, test } from "vitest";
import { CHILD, createEditorialClient, PARENT, QUESTION, RESUMED } from "./fixture";

test("distinct child reads and owner projections never derive current lifecycle from receipts", async () => {
  const client = createEditorialClient();
  const parent = await client.request("thread/read", { includeTurns: true, ref: PARENT });
  const child = await client.request("thread/read", { includeTurns: true, ref: CHILD });
  const resumed = await client.request("thread/read", { includeTurns: true, ref: RESUMED });
  expect(child.thread.evener.parentRef).toBe(PARENT);
  expect(child.thread.id).not.toBe(parent.thread.id);
  expect(child.thread.turns?.[0]?.items?.[0]?.text).toBe(
    "Child report: independent transcript, not the parent snapshot.",
  );
  expect(resumed.thread.turns?.[0]?.items?.[0]?.text).toContain("Previous report:");
  const projections = parent.thread.evener.diagnostics?.delegates;
  expect(projections).toEqual(
    expect.arrayContaining([
      expect.objectContaining({
        delegateId: "dlg_editorial_report",
        ownerSessionId: "editorial-parent",
        status: "idle",
        outcome: "completed",
        resumable: true,
      }),
      expect.objectContaining({
        delegateId: "dlg_editorial_resumed",
        ownerSessionId: "editorial-parent",
        status: "running",
        outcome: "completed",
        terminal: false,
      }),
    ]),
  );
  const items = parent.thread.turns?.[0]?.items ?? [];
  expect(JSON.parse(items.find((item) => item.id === "reported")?.output ?? "{}").status).toBe("running");
  expect(JSON.parse(items.find((item) => item.id === "resumed")?.output ?? "{}").status).toBe("completed");
  expect(projections?.some((row) => row.delegateId === "dlg_editorial_unknown")).toBe(false);
  expect(items.find((item) => item.id === "failure")).toMatchObject({ status: "failed" });
  expect(items.find((item) => item.id === "active")).toMatchObject({ status: "inProgress" });
  const native = items.find((item) => item.id === "long")?.output ?? "";
  expect(native.split("\n")).toHaveLength(120);
  expect(native).toContain("120\tconst fixtureLine120");
});

test("fixture answer is reflected once, clears attention, and publishes real notifications", async () => {
  const client = createEditorialClient();
  const notifications: string[] = [];
  client.onNotification((notification) => notifications.push(notification.method));
  const snapshot = await client.request("thread/read", { includeTurns: true, ref: QUESTION, subscribe: true });
  const params = {
    ref: QUESTION,
    clientMutationId: "test-answer",
    expectedInstanceId: snapshot.thread.evener.instanceId ?? "",
    input: [{ type: "text", text: "Tool evidence" }],
  };
  const result = await client.request("turn/steer", params);
  expect(result.receipt).toMatchObject({
    clientMutationId: "test-answer",
    disposition: "applied",
    projectionState: "reflected",
  });
  expect((await client.request("turn/steer", params)).receipt.disposition).toBe("replayed");
  const after = await client.request("thread/read", { includeTurns: true, ref: QUESTION });
  expect(after.thread.evener.askPending).toBe(false);
  expect(after.thread.turns).toHaveLength(2);
  expect(after.thread.turns?.[1]?.items).toEqual(
    expect.arrayContaining([
      expect.objectContaining({ text: "Fixture acknowledged: Tool evidence" }),
      expect.objectContaining({ clientMutationId: "test-answer" }),
    ]),
  );
  expect(notifications.filter((method) => method === "turn/started")).toHaveLength(1);
  expect(notifications).toContain("turn/completed");
  const manifest = await client.request("evener/navigation/read", { resource: "manifest", representationVersion: 2 });
  expect(manifest.data).toMatchObject({ metadata: { attentionSummary: { needsYou: 0 } } });
  const section = await client.request("evener/navigation/read", {
    resource: "section",
    section: "needs_you",
    representationVersion: 2,
  });
  expect(section.data).toMatchObject({ entities: [] });
  await client.request("thread/unsubscribe", { ref: QUESTION });
  expect(client.rejectedRequests).toEqual([]);
});

test("unexpected requests and unknown refs fail loudly; client instances do not share mutations", async () => {
  const client = createEditorialClient();
  await expect(client.request("thread/read", { includeTurns: true, ref: "local:not-a-fixture" })).rejects.toThrow(
    "Unknown fixture thread",
  );
  await expect(client.request("evener/auth/test", { provider: "fixture-provider" })).rejects.toThrow(
    "no handler scripted",
  );
  expect(client.rejectedRequests).toHaveLength(2);
  const other = createEditorialClient();
  expect((await other.request("thread/read", { includeTurns: true, ref: QUESTION })).thread.evener.askPending).toBe(
    true,
  );
  expect(other.rejectedRequests).toEqual([]);
});
