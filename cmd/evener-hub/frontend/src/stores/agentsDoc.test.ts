import { beforeEach, describe, expect, test } from "vitest";
import { FakeClient } from "../protocol/testing/fakeClient";
import type { AgentsDocResponse, AnyNotification } from "../protocol/types.gen";
import { agentsDocStore, resetAgentsDocStoreForTests } from "./agentsDoc";
import { connectionStore } from "./connection";

const DOC: AgentsDocResponse = { path: "/home/u/.config/evener/AGENTS.md", exists: true, content: "# hi\n" };

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetAgentsDocStoreForTests();
});

describe("fetch", () => {
  test("loads the document from evener/settings/agentsDoc/get", async () => {
    const fake = connectFakeClient();
    fake.on("evener/settings/agentsDoc/get", () => DOC);
    await agentsDocStore.getState().fetch();
    expect(agentsDocStore.getState().doc).toEqual(DOC);
    expect(agentsDocStore.getState().loading).toBe(false);
    expect(agentsDocStore.getState().error).toBeNull();
  });

  // `loading` is what the section draws its Skeleton from, and it is only
  // observable while a request is outstanding - a handler that never resolves
  // is the only way to stand in that window.
  test("loading is true while the request is in flight", async () => {
    const fake = connectFakeClient();
    let land: ((doc: AgentsDocResponse) => void) | undefined;
    const pending = new Promise<AgentsDocResponse>((resolve) => {
      land = resolve;
    });
    fake.on("evener/settings/agentsDoc/get", () => pending);

    const inFlight = agentsDocStore.getState().fetch();
    expect(agentsDocStore.getState().loading).toBe(true);
    expect(agentsDocStore.getState().doc).toBeNull();

    land?.(DOC);
    await inFlight;
    expect(agentsDocStore.getState().loading).toBe(false);
    expect(agentsDocStore.getState().doc).toEqual(DOC);
  });

  test("records a failed fetch as error text and keeps doc null", async () => {
    const fake = connectFakeClient();
    fake.on("evener/settings/agentsDoc/get", () => {
      throw new Error("disk on fire");
    });
    await agentsDocStore.getState().fetch();
    expect(agentsDocStore.getState().doc).toBeNull();
    expect(agentsDocStore.getState().error).toContain("disk on fire");
    expect(agentsDocStore.getState().loading).toBe(false);
  });

  test("throws when no client is connected", async () => {
    await expect(agentsDocStore.getState().fetch()).rejects.toThrow(/no client connected/);
  });
});

describe("save", () => {
  test("sends the content to evener/settings/agentsDoc/set and adopts the response", async () => {
    const fake = connectFakeClient();
    fake.on("evener/settings/agentsDoc/set", (params) => ({ ...DOC, content: params.content }));
    const saved = await agentsDocStore.getState().save("# new\n");
    expect(saved.content).toBe("# new\n");
    expect(agentsDocStore.getState().doc?.content).toBe("# new\n");
    expect(fake.calls.map((c) => c.method)).toEqual(["evener/settings/agentsDoc/set"]);
    expect(fake.calls[0]?.params).toEqual({ content: "# new\n" });
  });

  test("a rejected save propagates and leaves doc untouched", async () => {
    const fake = connectFakeClient();
    fake.on("evener/settings/agentsDoc/get", () => DOC);
    await agentsDocStore.getState().fetch();
    fake.on("evener/settings/agentsDoc/set", () => {
      throw new Error("read-only");
    });
    await expect(agentsDocStore.getState().save("x")).rejects.toThrow("read-only");
    expect(agentsDocStore.getState().doc).toEqual(DOC);
  });
});

describe("changed notification", () => {
  test("replaces doc with the broadcast payload", async () => {
    const fake = connectFakeClient();
    fake.on("evener/settings/agentsDoc/get", () => DOC);
    await agentsDocStore.getState().fetch();
    const pushed: AgentsDocResponse = { ...DOC, content: "# elsewhere\n" };
    fake.emitNotification({ method: "evener/settings/agentsDoc/changed", params: pushed } as AnyNotification);
    expect(agentsDocStore.getState().doc).toEqual(pushed);
  });

  test("a replaced client is re-wired: the old client's notifications stop landing", async () => {
    const first = connectFakeClient();
    const second = connectFakeClient();
    second.on("evener/settings/agentsDoc/get", () => DOC);
    await agentsDocStore.getState().fetch();
    first.emitNotification({
      method: "evener/settings/agentsDoc/changed",
      params: { ...DOC, content: "stale" },
    } as AnyNotification);
    expect(agentsDocStore.getState().doc).toEqual(DOC);
    second.emitNotification({
      method: "evener/settings/agentsDoc/changed",
      params: { ...DOC, content: "live" },
    } as AnyNotification);
    expect(agentsDocStore.getState().doc?.content).toBe("live");
  });
});
