import { beforeEach, describe, expect, test } from "vitest";
import { WireError } from "../protocol/errors";
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

  // Only the most recently started read may land, exactly as
  // stores/credentials.ts fences its own: an overlapping older response
  // carries a view of the file that is already out of date, and the section
  // syncs its draft off whatever `doc` holds.
  test("an older fetch that resolves last does not overwrite the newer one", async () => {
    const fake = connectFakeClient();
    let finishOlder!: (doc: AgentsDocResponse) => void;
    fake.on(
      "evener/settings/agentsDoc/get",
      () =>
        new Promise<AgentsDocResponse>((resolve) => {
          finishOlder = resolve;
        }),
    );
    const older = agentsDocStore.getState().fetch();
    await Promise.resolve();

    fake.on("evener/settings/agentsDoc/get", () => DOC);
    await agentsDocStore.getState().fetch();

    finishOlder({ ...DOC, content: "# older\n" });
    await older;
    expect(agentsDocStore.getState().doc).toEqual(DOC);
  });

  test("a late response from a replaced client is dropped", async () => {
    const first = connectFakeClient();
    let finishFirst!: (doc: AgentsDocResponse) => void;
    first.on(
      "evener/settings/agentsDoc/get",
      () =>
        new Promise<AgentsDocResponse>((resolve) => {
          finishFirst = resolve;
        }),
    );
    const interrupted = agentsDocStore.getState().fetch();
    await Promise.resolve();

    // Scripted before connecting: the replacement's own reconnect refetch
    // fires the moment it becomes the store's client.
    const second = new FakeClient("ready");
    second.on("evener/settings/agentsDoc/get", () => DOC);
    connectionStore.getState().connect(second);
    await Promise.resolve();
    await Promise.resolve();
    expect(agentsDocStore.getState().doc).toEqual(DOC);

    finishFirst({ ...DOC, content: "# from the dead socket\n" });
    await interrupted;
    expect(agentsDocStore.getState().doc).toEqual(DOC);
  });
});

// An automatic reconnect reuses the same AppwireClient, so a socket drop and
// recovery is a state transition and nothing else. The broadcasts that landed
// while it was down are gone, so the store has to re-read the file itself.
describe("reconnect", () => {
  test("a reconnect on the same client refetches", async () => {
    const fake = connectFakeClient();
    const away: AgentsDocResponse = { ...DOC, content: "# while we were away\n" };
    let calls = 0;
    fake.on("evener/settings/agentsDoc/get", () => {
      calls += 1;
      return calls === 1 ? DOC : away;
    });
    await agentsDocStore.getState().fetch();
    expect(agentsDocStore.getState().doc).toEqual(DOC);

    fake.emitStateChange("reconnecting");
    fake.emitReady();
    await Promise.resolve();
    await Promise.resolve();
    expect(calls).toBe(2);
    expect(agentsDocStore.getState().doc).toEqual(away);
  });

  // A drop the fence catches mid-fetch drops that response, so nothing is
  // left to turn `loading` off - and a drop that never recovers would leave
  // the section drawing its Skeleton over a document it will never get.
  test("a drop mid-fetch clears loading", async () => {
    const fake = connectFakeClient();
    let land!: (doc: AgentsDocResponse) => void;
    fake.on(
      "evener/settings/agentsDoc/get",
      () =>
        new Promise<AgentsDocResponse>((resolve) => {
          land = resolve;
        }),
    );
    const interrupted = agentsDocStore.getState().fetch();
    await Promise.resolve(); // the fake defers its handler by a microtask
    expect(agentsDocStore.getState().loading).toBe(true);

    fake.emitStateChange("reconnecting");
    expect(agentsDocStore.getState().loading).toBe(false);

    land(DOC);
    await interrupted;
    expect(agentsDocStore.getState().doc).toBeNull();
    expect(agentsDocStore.getState().loading).toBe(false);
  });

  test("a reconnect before anything was requested fetches nothing", async () => {
    const fake = connectFakeClient();
    fake.on("evener/settings/agentsDoc/get", () => DOC);

    fake.emitStateChange("reconnecting");
    fake.emitReady();
    await Promise.resolve();
    await Promise.resolve();
    expect(fake.calls).toEqual([]);
    expect(agentsDocStore.getState().doc).toBeNull();
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

  // A reload can fail while the socket stays up, and the section's notice for
  // that ("saving would overwrite anything changed on disk since") is false
  // the moment a write lands, so a save has to take the error with it.
  test("a successful save clears a stale fetch error", async () => {
    const fake = connectFakeClient();
    fake.on("evener/settings/agentsDoc/get", () => {
      throw new WireError("hub unreachable", -1);
    });
    await agentsDocStore.getState().fetch();
    expect(agentsDocStore.getState().error).toContain("hub unreachable");

    fake.on("evener/settings/agentsDoc/set", (params) => ({ ...DOC, content: params.content }));
    await agentsDocStore.getState().save("x");
    expect(agentsDocStore.getState().error).toBeNull();
    expect(agentsDocStore.getState().doc?.content).toBe("x");
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
