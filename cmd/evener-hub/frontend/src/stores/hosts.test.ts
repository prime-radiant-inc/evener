// hosts.test.ts — store-level unit tests for the Hosts settings section's
// store (stores/hosts.ts). Component-level rendering and polling tests live
// in panes/settings/sections/hosts.test.tsx.
//
// Pattern mirrors daemonResidents.test.ts: each test resets store +
// connection in beforeEach.

import { AppwireClient, type HostListResponse, type HostRow, WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { FakeSocket } from "@evener/appwire-client/testing/fakeSocket";
import { beforeEach, describe, expect, test, vi } from "vitest";
import { connectionStore } from "./connection";
import {
  currentHostRegistration,
  hostRegistrationChanged,
  hostsStore,
  registrySaysHostGone,
  sameHostRegistration,
} from "./hosts";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

function row(name: string): HostRow {
  return {
    name,
    generation: 1,
    incarnationId: "inc-1",
    origin: "hub.toml",
    attached: false,
    midAttach: false,
    removed: false,
  };
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  hostsStore.getState().resetForTests();
});

describe("client replacement", () => {
  test("a different client wired in after the store was cleared clears the published marker", async () => {
    const a = connectFakeClient();
    a.on("evener/host/list", () => ({ hosts: [row("alpha")] }));
    await hostsStore.getState().fetch();
    expect(hostsStore.getState().publishedRevision).not.toBeNull();
    const revision = hostsStore.getState().revision;

    // The store is cleared (a disconnect), then a DIFFERENT client is wired in.
    // That is a replacement even though the two clients were never both present:
    // the new hub's registry has published nothing.
    connectionStore.setState({ client: null });
    const b = new FakeClient("ready");
    b.on("evener/host/list", () => ({ hosts: [row("alpha")] }));
    connectionStore.getState().connect(b);

    expect(hostsStore.getState().publishedRevision).toBeNull();
    expect(hostsStore.getState().load.phase).toBe("loading");
    expect(hostsStore.getState().revision).toBeGreaterThan(revision);

    // Its own registry answer is what republishes.
    await hostsStore.getState().fetch();
    expect(hostsStore.getState().publishedRevision).not.toBeNull();
  });

  test("a reconnect of the same client does not clear the published marker", async () => {
    const a = connectFakeClient();
    a.on("evener/host/list", () => ({ hosts: [row("alpha")] }));
    await hostsStore.getState().fetch();
    const revision = hostsStore.getState().revision;

    // Same client object, disconnected and wired back: not a replacement, so the
    // rows it read still describe it and nothing is invalidated.
    connectionStore.setState({ client: null });
    connectionStore.getState().connect(a);

    expect(hostsStore.getState().publishedRevision).not.toBeNull();
    expect(hostsStore.getState().revision).toBe(revision);
  });

  test("a replacement drops the old quiet-read slot without disturbing the new connection's reads", async () => {
    const a = connectFakeClient();
    let settleOld!: (value: HostListResponse) => void;
    const oldRead = new Promise<HostListResponse>((resolve) => (settleOld = resolve));
    a.on("evener/host/list", () => oldRead);
    void hostsStore.getState().refresh();
    await Promise.resolve();
    expect(hostsStore.getState().reading).toBe(1);

    // The replacement drops the old connection's quiet-read slot, so a refresh
    // issues its OWN request on the new client rather than joining the old one.
    const b = new FakeClient("ready");
    let settleNew!: (value: HostListResponse) => void;
    const newRead = new Promise<HostListResponse>((resolve) => (settleNew = resolve));
    b.on("evener/host/list", () => newRead);
    connectionStore.getState().connect(b);
    const nextRefresh = hostsStore.getState().refresh();
    await Promise.resolve();
    expect(b.calls.filter((call) => call.method === "evener/host/list")).toHaveLength(1);
    expect(hostsStore.getState().reading).toBe(2);

    // The stale old read settles AFTER the new request started: it releases only
    // its own slot, so the new connection's read still owns the gate.
    settleOld({ hosts: [row("alpha")] });
    for (let i = 0; i < 5; i++) await Promise.resolve();
    expect(hostsStore.getState().reading).toBe(1);

    settleNew({ hosts: [row("alpha")] });
    await nextRefresh;
    expect(hostsStore.getState().reading).toBe(0);
    expect(hostsStore.getState().publishedRevision).not.toBeNull();
  });
});

describe("refresh", () => {
  test("refresh without a connected client does not wedge the in-flight gate", async () => {
    // The quiet refresh swallows the no-client refusal (rows keep their last
    // snapshot; the next tick retries) — and must not leave its settled
    // promise cached as the in-flight refresh. When the IIFE's cleanup ran
    // before the outer assignment stored the already-settled promise
    // (round-4 M1), refreshInflight stayed non-null forever and every later
    // refresh() returned without ever issuing a request.
    await hostsStore.getState().refresh();

    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({ hosts: [row("beta")] }));
    await hostsStore.getState().refresh();

    expect(fake.calls.filter((c) => c.method === "evener/host/list")).toHaveLength(1);
  });

  test("a stale background refresh response cannot overwrite a newer fetch list", async () => {
    const fake = connectFakeClient();
    let resolveBackground!: (data: HostListResponse) => void;
    fake.on("evener/host/list", () => new Promise<HostListResponse>((resolve) => (resolveBackground = resolve)));
    const background = hostsStore.getState().refresh();

    // The mutation path (add/remove/connect) re-reads with the quiet list
    // read, which is independent of the in-flight background refresh: the
    // two requests race, and the background one can land after the
    // mutation's. fetch() stands in for it here — same generation-guarded
    // publish, same independence from the gate.
    fake.on("evener/host/list", () => ({ hosts: [row("post-mutation")] }));
    await hostsStore.getState().fetch();

    let load = hostsStore.getState().load;
    expect(load.phase).toBe("ready");
    if (load.phase !== "ready") throw new Error("unreachable");
    expect(load.hosts).toEqual([row("post-mutation")]);

    // The pre-mutation background response lands late and must be discarded
    // (round-4 M4): a latest-wins guard, or the older list would overwrite
    // the rows the mutation just changed.
    resolveBackground({ hosts: [row("pre-mutation")] });
    await background;

    load = hostsStore.getState().load;
    expect(load.phase).toBe("ready");
    if (load.phase !== "ready") throw new Error("unreachable");
    expect(load.hosts).toEqual([row("post-mutation")]);
  });

  test("a snapshot that changes only the remnant fence fields is still published", async () => {
    // S16: the row's repair affordance keys off HostRow's `openRemnantId` /
    // `escalationAgeSec` (registry spec 08 §11), so the equal-snapshot skip
    // must compare them: a remnant that opens, escalates, or clears without
    // any other row change is a real change the pane has to render — the
    // repair affordance would otherwise appear late or never clear.
    const fake = connectFakeClient();
    let remnant: string | undefined;
    let escalation: number | undefined;
    fake.on("evener/host/list", () => ({
      hosts: [
        {
          ...row("beta"),
          ...(remnant === undefined ? {} : { openRemnantId: remnant }),
          ...(escalation === undefined ? {} : { escalationAgeSec: escalation }),
        },
      ],
    }));
    await hostsStore.getState().fetch();

    remnant = "remnant-7";
    await hostsStore.getState().refresh();
    let load = hostsStore.getState().load;
    if (load.phase !== "ready") throw new Error("unreachable");
    expect(load.hosts[0]?.openRemnantId).toBe("remnant-7");

    // Past the escalation bound: the recover affordance's own field.
    escalation = 7200;
    await hostsStore.getState().refresh();
    load = hostsStore.getState().load;
    if (load.phase !== "ready") throw new Error("unreachable");
    expect(load.hosts[0]?.escalationAgeSec).toBe(7200);

    // Resolved: both fields go away together and the affordance must clear.
    remnant = undefined;
    escalation = undefined;
    await hostsStore.getState().refresh();
    load = hostsStore.getState().load;
    if (load.phase !== "ready") throw new Error("unreachable");
    expect(load.hosts[0]?.openRemnantId).toBeUndefined();
    expect(load.hosts[0]?.escalationAgeSec).toBeUndefined();
  });

  test("a re-read with a larger escalation age is not republished", async () => {
    // The age is the whole-second age of the remnant commit instant, so it
    // changes on every poll once past the bound. Comparing the integer would
    // republish the section (and advance the revision its caches key on) every
    // HOST_POLL_MS; presence is what the affordance keys off.
    const fake = connectFakeClient();
    let escalation: number | undefined = 7200;
    fake.on("evener/host/list", () => ({
      hosts: [
        {
          ...row("beta"),
          openRemnantId: "remnant-7",
          ...(escalation === undefined ? {} : { escalationAgeSec: escalation }),
        },
      ],
    }));
    await hostsStore.getState().fetch();
    const first = hostsStore.getState().load;
    if (first.phase !== "ready") throw new Error("unreachable");

    escalation = 7201;
    await hostsStore.getState().refresh();
    expect(hostsStore.getState().load).toBe(first);

    // The undefined <-> defined transition is still a real change.
    escalation = undefined;
    await hostsStore.getState().refresh();
    const after = hostsStore.getState().load;
    if (after.phase !== "ready") throw new Error("unreachable");
    expect(after).not.toBe(first);
    expect(after.hosts[0]?.escalationAgeSec).toBeUndefined();
  });
});

describe("fetch", () => {
  test("a failed background refresh must not strand fetch's fetched list", async () => {
    // fetch races the background poll (round-5 LOW): fetch issues its list
    // read and flips the section to the loading skeleton, the poll's own —
    // newer — request fails, and only then does fetch's earlier response
    // arrive with the real rows. The generation guard used to compare
    // against a counter the failed poll had already advanced, so the fetched
    // list was discarded and the skeleton never cleared. A response may only
    // be discarded when a newer response actually PUBLISHED: a failed request
    // publishes nothing and invalidates nothing.
    const fake = connectFakeClient();
    let resolveFetch!: (data: HostListResponse) => void;
    fake.on("evener/host/list", () => new Promise<HostListResponse>((resolve) => (resolveFetch = resolve)));
    const fetched = hostsStore.getState().fetch();

    // The background poll's request fails while fetch's is still in flight.
    fake.on("evener/host/list", () => Promise.reject(new Error("poll failed")));
    await hostsStore.getState().refresh();
    expect(hostsStore.getState().load.phase).toBe("loading");

    // fetch's own response lands last, with the older generation: it is the
    // only response that ever published, so the section must show its rows.
    resolveFetch({ hosts: [row("m4")] });
    await fetched;

    const load = hostsStore.getState().load;
    expect(load.phase).toBe("ready");
    if (load.phase !== "ready") throw new Error("unreachable");
    expect(load.hosts).toEqual([row("m4")]);
  });
});

describe("mutations", () => {
  test("update remains pending past the ordinary RPC deadline and ultimately resolves", async () => {
    vi.useFakeTimers();
    const socket = new FakeSocket({ autoInitialize: true });
    const client = new AppwireClient({ url: "ws://test/rpc", socketFactory: () => socket });
    try {
      const ready = client.connect();
      socket.open();
      await ready;
      // Prove the transport remains live independently of the mutation under
      // test while the hub is legitimately waiting on the host gate.
      await client.request("ping", {});
      expect(client.state).toBe("ready");
      connectionStore.getState().connect(client);

      const updatedRow = { ...row("side"), address: "edited.example" };
      let outcome: { status: "resolved"; row: HostRow } | { status: "rejected"; error: unknown } | undefined;
      const done = hostsStore
        .getState()
        .update({
          name: "side",
          entry: { address: "edited.example" },
          expected: { generation: 1, incarnationId: "inc-1" },
        })
        .then(
          (result) => {
            outcome = { status: "resolved", row: result };
          },
          (error: unknown) => {
            outcome = { status: "rejected", error };
          },
        );
      // The update carries the pair its dialog opened on, so it goes out
      // without reading the list first.
      const listFrames = () =>
        socket.sent
          .map((frame) => JSON.parse(frame) as { id?: number; method?: string })
          .filter((frame) => frame.method === "evener/host/list");
      await vi.advanceTimersByTimeAsync(0);
      expect(listFrames()).toEqual([]);
      const updateRequest = socket.sent
        .map((frame) => JSON.parse(frame) as { id?: number; method?: string })
        .find((frame) => frame.method === "evener/host/update");
      if (typeof updateRequest?.id !== "number") throw new Error("missing evener/host/update request id");

      await vi.advanceTimersByTimeAsync(31_000);
      expect(client.state).toBe("ready");
      expect(outcome, "Update must keep waiting under the same minutes-long budget as Connect").toBeUndefined();

      // The held update now succeeds, then its quiet list re-read succeeds too:
      // the store promise must resolve rather than merely avoiding the 30s error.
      socket.receive({ id: updateRequest.id, result: { outcome: "committed", host: updatedRow } });
      await vi.advanceTimersByTimeAsync(0);
      const listRequest = listFrames().at(-1);
      if (typeof listRequest?.id !== "number") throw new Error("missing post-update evener/host/list request id");
      socket.receive({ id: listRequest.id, result: { hosts: [updatedRow] } });
      await done;
      expect(outcome).toEqual({ status: "resolved", row: updatedRow });
    } finally {
      client.close();
      connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
      vi.useRealTimers();
    }
  });

  test("remove remains pending past the ordinary RPC deadline and ultimately resolves", async () => {
    vi.useFakeTimers();
    const socket = new FakeSocket({ autoInitialize: true });
    const client = new AppwireClient({ url: "ws://test/rpc", socketFactory: () => socket });
    try {
      const ready = client.connect();
      socket.open();
      await ready;
      // Prove the transport remains live independently of the mutation under
      // test while the hub is legitimately waiting on the host gate.
      await client.request("ping", {});
      expect(client.state).toBe("ready");
      connectionStore.getState().connect(client);

      let outcome: { status: "resolved" } | { status: "rejected"; error: unknown } | undefined;
      const done = hostsStore
        .getState()
        .remove("side")
        .then(
          () => {
            outcome = { status: "resolved" };
          },
          (error: unknown) => {
            outcome = { status: "rejected", error };
          },
        );
      // The guarded removal needs the row's pair and none is held, so the store
      // lists first: that read must answer before the remove frame exists.
      const listFrames = () =>
        socket.sent
          .map((frame) => JSON.parse(frame) as { id?: number; method?: string })
          .filter((frame) => frame.method === "evener/host/list");
      const pairRequest = listFrames()[0];
      if (typeof pairRequest?.id !== "number") throw new Error("missing pair-read evener/host/list request id");
      socket.receive({ id: pairRequest.id, result: { hosts: [row("side")] } });
      await vi.advanceTimersByTimeAsync(0);
      const removeRequest = socket.sent
        .map((frame) => JSON.parse(frame) as { id?: number; method?: string })
        .find((frame) => frame.method === "evener/host/remove");
      if (typeof removeRequest?.id !== "number") throw new Error("missing evener/host/remove request id");

      await vi.advanceTimersByTimeAsync(31_000);
      expect(client.state).toBe("ready");
      expect(outcome, "Remove must keep waiting under the same minutes-long host-gate budget").toBeUndefined();

      // The held removal now succeeds, then its quiet list re-read succeeds too:
      // the store promise must resolve rather than merely avoiding the 30s error.
      socket.receive({
        id: removeRequest.id,
        result: { outcome: "committed", host: { ...row("side"), removed: true, attached: false, midEnsure: false } },
      });
      await vi.advanceTimersByTimeAsync(0);
      const rereadFrames = listFrames().filter((frame) => frame.id !== pairRequest.id);
      const listRequest = rereadFrames[rereadFrames.length - 1];
      if (typeof listRequest?.id !== "number") throw new Error("missing post-remove evener/host/list request id");
      socket.receive({ id: listRequest.id, result: { hosts: [] } });
      await done;
      expect(outcome).toEqual({ status: "resolved" });
    } finally {
      client.close();
      connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
      vi.useRealTimers();
    }
  });

  test("update sends the target name and the entry, then re-reads quietly", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({ hosts: [row("alpha")] }));
    await hostsStore.getState().fetch();

    fake.on("evener/host/update", () => ({ outcome: "committed", host: { ...row("alpha"), address: "a2.example" } }));
    fake.on("evener/host/list", () => ({ hosts: [{ ...row("alpha"), address: "a2.example" }] }));
    const updated = await hostsStore.getState().update({
      name: "alpha",
      entry: { address: "a2.example", user: "operator" },
      expected: { generation: 1, incarnationId: "inc-1" },
    });

    expect(updated.address).toBe("a2.example");
    const updateParams = fake.calls.find((c) => c.method === "evener/host/update")?.params as {
      name: string;
      entry: unknown;
      mutationId: string;
      expectedGeneration: number;
      expectedIncarnationId: string;
    };
    expect(updateParams.name).toBe("alpha");
    expect(updateParams.entry).toEqual({ address: "a2.example", user: "operator" });
    // The guarded-mutation fields the hub now requires (registry spec 08 §4):
    // a fresh opaque key and the pair of the row the edit opened on.
    expect(typeof updateParams.mutationId).toBe("string");
    expect(updateParams.mutationId.length).toBeGreaterThan(0);
    expect(updateParams.expectedGeneration).toBe(1);
    expect(updateParams.expectedIncarnationId).toBe("inc-1");
    const load = hostsStore.getState().load;
    expect(load.phase).toBe("ready");
    if (load.phase !== "ready") throw new Error("unreachable");
    expect(load.hosts[0]?.address).toBe("a2.example");
  });

  test("a rejected update reaches the caller and keeps the rows", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({ hosts: [row("alpha")] }));
    await hostsStore.getState().fetch();

    fake.on("evener/host/update", () => Promise.reject(new Error('host "alpha": missing ssh destination')));
    await expect(
      hostsStore
        .getState()
        .update({ name: "alpha", entry: { address: "" }, expected: { generation: 1, incarnationId: "inc-1" } }),
    ).rejects.toThrowError(/missing ssh destination/);

    const load = hostsStore.getState().load;
    expect(load.phase).toBe("ready");
    if (load.phase !== "ready") throw new Error("unreachable");
    expect(load.hosts).toEqual([row("alpha")]);
  });

  test("an add's non-commit arm rejects but still re-reads, so the row the hub reports appears", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({ hosts: [] }));
    fake.on("evener/host/add", () => ({ outcome: "ambiguous", observedRow: row("beta") }));
    // The re-read after the ambiguous arm shows the row the hub says already
    // exists, even though the add rejects.
    fake.on("evener/host/list", () => ({ hosts: [row("beta")] }));

    await expect(hostsStore.getState().add({ name: "beta", address: "b.example" })).rejects.toThrowError(
      /cannot tell whether it committed/,
    );

    const load = hostsStore.getState().load;
    expect(load.phase).toBe("ready");
    if (load.phase !== "ready") throw new Error("unreachable");
    expect(load.hosts).toEqual([row("beta")]);
  });

  test("an update's teardown-failure arm rejects but still re-reads, so the committed row appears", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({ hosts: [row("alpha")] }));
    await hostsStore.getState().fetch();

    fake.on("evener/host/update", () => ({
      outcome: "committed-with-teardown-failure",
      seam: "rebind",
      remnantId: "r1",
      host: { ...row("alpha"), address: "a2.example" },
    }));
    fake.on("evener/host/list", () => ({
      hosts: [{ ...row("alpha"), address: "a2.example", openRemnantId: "r1" }],
    }));

    await expect(
      hostsStore.getState().update({
        name: "alpha",
        entry: { address: "a2.example" },
        expected: { generation: 1, incarnationId: "inc-1" },
      }),
    ).rejects.toThrowError(/remnantId r1/);

    const load = hostsStore.getState().load;
    expect(load.phase).toBe("ready");
    if (load.phase !== "ready") throw new Error("unreachable");
    expect(load.hosts).toEqual([{ ...row("alpha"), address: "a2.example", openRemnantId: "r1" }]);
  });

  test("a remove's teardown-failure arm rejects but still re-reads, so the committed removal appears", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({ hosts: [row("alpha")] }));
    await hostsStore.getState().fetch();

    fake.on("evener/host/remove", () => ({
      outcome: "committed-with-teardown-failure",
      seam: "rebind",
      remnantId: "r1",
      host: { ...row("alpha"), removed: true },
    }));
    fake.on("evener/host/list", () => ({ hosts: [{ ...row("alpha"), removed: true, openRemnantId: "r1" }] }));

    await expect(hostsStore.getState().remove("alpha")).rejects.toThrowError(/remnantId r1/);

    const load = hostsStore.getState().load;
    expect(load.phase).toBe("ready");
    if (load.phase !== "ready") throw new Error("unreachable");
    expect(load.hosts).toEqual([{ ...row("alpha"), removed: true, openRemnantId: "r1" }]);
  });

  test("add sends a minted mutationId so a retried add cannot double-add", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({ hosts: [] }));
    fake.on("evener/host/add", () => ({ outcome: "committed", host: row("beta") }));
    await hostsStore.getState().add({ name: "beta", address: "b.example" });

    const addParams = fake.calls.find((c) => c.method === "evener/host/add")?.params as {
      entry: unknown;
      mutationId: string;
    };
    expect(addParams.entry).toEqual({ name: "beta", address: "b.example" });
    expect(typeof addParams.mutationId).toBe("string");
    expect(addParams.mutationId.length).toBeGreaterThan(0);
  });

  test("remove sends the pair the held row carries", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({ hosts: [{ ...row("alpha"), generation: 7, incarnationId: "inc-7" }] }));
    await hostsStore.getState().fetch();
    fake.on("evener/host/remove", () => ({ outcome: "committed", host: { ...row("alpha"), removed: true } }));
    fake.on("evener/host/list", () => ({ hosts: [] }));
    await hostsStore.getState().remove("alpha");

    const removeParams = fake.calls.find((c) => c.method === "evener/host/remove")?.params as {
      name: string;
      mutationId: string;
      expectedGeneration: number;
      expectedIncarnationId: string;
    };
    expect(removeParams.name).toBe("alpha");
    expect(removeParams.mutationId.length).toBeGreaterThan(0);
    expect(removeParams.expectedGeneration).toBe(7);
    expect(removeParams.expectedIncarnationId).toBe("inc-7");
  });

  test("a remove with no held row lists first instead of fabricating a pair", async () => {
    const fake = connectFakeClient();
    // The store never fetched, so no row is held; the first list read answers
    // the pair, and the remove echoes that — never a defaulted pair.
    fake.on("evener/host/list", () => ({ hosts: [{ ...row("alpha"), generation: 3, incarnationId: "inc-3" }] }));
    fake.on("evener/host/remove", () => ({ outcome: "committed", host: { ...row("alpha"), removed: true } }));
    await hostsStore.getState().remove("alpha");

    const removeParams = fake.calls.find((c) => c.method === "evener/host/remove")?.params as {
      expectedGeneration: number;
      expectedIncarnationId: string;
    };
    expect(removeParams.expectedGeneration).toBe(3);
    expect(removeParams.expectedIncarnationId).toBe("inc-3");
  });

  test("a guarded mutation for a name the registry does not list refuses locally", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({ hosts: [{ ...row("beta"), generation: 1, incarnationId: "inc-b" }] }));
    await expect(hostsStore.getState().remove("alpha")).rejects.toThrowError(/not listed/);
    expect(fake.calls.some((c) => c.method === "evener/host/remove")).toBe(false);
  });

  test("an edit sends the pair its dialog opened on, and a stale refusal after a refresh surfaces unretried", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({ hosts: [{ ...row("alpha"), generation: 1, incarnationId: "inc-1" }] }));
    await hostsStore.getState().fetch();
    // The dialog opens on generation 1. Someone else edits the host, and the
    // background poll brings generation 2 while the dialog is still open.
    fake.on("evener/host/list", () => ({ hosts: [{ ...row("alpha"), generation: 2, incarnationId: "inc-1" }] }));
    await hostsStore.getState().refresh();

    const sent: Array<{ expectedGeneration: number }> = [];
    fake.on("evener/host/update", (params: { expectedGeneration: number }) => {
      sent.push(params);
      throw new WireError('host "alpha": the entry moved', -32013, {
        evenerErrorInfo: "stale-entry",
        binding: "generation",
      });
    });
    await expect(
      hostsStore.getState().update({
        name: "alpha",
        entry: { address: "a2.example" },
        expected: { generation: 1, incarnationId: "inc-1" },
      }),
    ).rejects.toThrowError(/the entry moved/);
    // One attempt, against the row the person saw: never a retry that would
    // overwrite the other edit.
    expect(sent.map((params) => params.expectedGeneration)).toEqual([1]);
  });

  test("a remove refused as stale re-reads and retries once with a new mutationId", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({ hosts: [{ ...row("alpha"), generation: 1, incarnationId: "inc-1" }] }));
    await hostsStore.getState().fetch();

    // The first attempt carries the held pair; the hub refuses it stale and
    // the row is now generation 2. The retry must re-read and send the fresh
    // pair with a NEW key, then land.
    const sent: Array<{ mutationId: string; expectedGeneration: number; expectedIncarnationId: string }> = [];
    fake.on(
      "evener/host/remove",
      (params: { mutationId: string; expectedGeneration: number; expectedIncarnationId: string }) => {
        sent.push(params);
        if (sent.length === 1) {
          throw new WireError('host "alpha": the entry moved', -32013, {
            evenerErrorInfo: "stale-entry",
            binding: "generation",
          });
        }
        return { outcome: "committed", host: { ...row("alpha"), removed: true } };
      },
    );
    fake.on("evener/host/list", () => ({ hosts: [{ ...row("alpha"), generation: 2, incarnationId: "inc-1" }] }));

    await hostsStore.getState().remove("alpha");
    expect(sent).toHaveLength(2);
    expect(sent[0]?.expectedGeneration).toBe(1);
    expect(sent[1]?.expectedGeneration).toBe(2);
    expect(sent[1]?.expectedIncarnationId).toBe("inc-1");
    expect(sent[1]?.mutationId).not.toBe(sent[0]?.mutationId);
  });

  test("a second stale-entry refusal of a remove surfaces to the caller", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({ hosts: [{ ...row("alpha"), generation: 1, incarnationId: "inc-1" }] }));
    await hostsStore.getState().fetch();

    let attempts = 0;
    fake.on("evener/host/remove", () => {
      attempts++;
      throw new WireError('host "alpha": the entry moved', -32013, {
        evenerErrorInfo: "stale-entry",
        binding: "generation",
      });
    });
    fake.on("evener/host/list", () => ({ hosts: [{ ...row("alpha"), generation: 2, incarnationId: "inc-1" }] }));

    await expect(hostsStore.getState().remove("alpha")).rejects.toThrowError(/the entry moved/);
    expect(attempts).toBe(2);
  });

  test("a non-stale refusal is not retried", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({ hosts: [{ ...row("alpha"), generation: 1, incarnationId: "inc-1" }] }));
    await hostsStore.getState().fetch();

    let attempts = 0;
    fake.on("evener/host/update", () => {
      attempts++;
      throw new WireError('host "alpha": missing ssh destination', -32602, {
        evenerErrorInfo: "invalidHostField",
        field: "address",
      });
    });
    await expect(
      hostsStore
        .getState()
        .update({ name: "alpha", entry: { address: "" }, expected: { generation: 1, incarnationId: "inc-1" } }),
    ).rejects.toThrowError(/missing ssh destination/);
    expect(attempts).toBe(1);
  });

  test("the publish guard does not swallow an edit that changes an entry field only", async () => {
    // hostRowEqual's field list is what decides whether the quiet re-read's rows
    // replace the rendered ones; a comparator that ignores user/evenerPath/
    // configPath/addr/roots would leave the pane rendering the pre-edit row.
    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({ hosts: [row("alpha")] }));
    await hostsStore.getState().fetch();

    const changes: Partial<HostRow>[] = [
      { user: "operator" },
      { evenerPath: "/opt/evener" },
      { configPath: "/etc/evener/hub.toml" },
      { addr: "127.0.0.1:9180" },
      { roots: ["/srv/one"] },
    ];
    const accumulated: Partial<HostRow> = {};
    for (const change of changes) {
      Object.assign(accumulated, change);
      // Keep every previous change in both the published row and the next
      // response, so the field added by this iteration is their only difference.
      fake.on("evener/host/list", () => ({ hosts: [{ ...row("alpha"), ...accumulated }] }));
      await hostsStore.getState().refresh();
      const load = hostsStore.getState().load;
      expect(load.phase).toBe("ready");
      if (load.phase !== "ready") throw new Error("unreachable");
      expect(load.hosts[0]).toMatchObject(change);
    }
  });

  test("a successful add whose list re-read fails keeps the rows and does not error the section", async () => {
    // The mutation's own response already proved the add landed, so the
    // re-read behind it must stay quiet: a failed list read flips neither
    // the section to the error state nor the rows away — the rows stay
    // rendered, and the next poll or fetch converges them. Pre-fix the
    // mutation path re-read through fetch(), which parked the section on
    // the error state over a mutation that succeeded.
    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({ hosts: [row("alpha")] }));
    await hostsStore.getState().fetch();

    fake.on("evener/host/add", () => ({ outcome: "committed", host: row("beta") }));
    fake.on("evener/host/list", () => Promise.reject(new Error("list failed")));
    const added = await hostsStore.getState().add({ name: "beta", address: "b.example" });
    expect(added.name).toBe("beta");

    const load = hostsStore.getState().load;
    expect(load.phase).toBe("ready");
    if (load.phase !== "ready") throw new Error("unreachable");
    expect(load.hosts).toEqual([row("alpha")]);
  });

  test("a successful remove whose list re-read fails keeps the rows and does not error the section", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({ hosts: [row("alpha"), row("beta")] }));
    await hostsStore.getState().fetch();

    fake.on("evener/host/remove", () => ({ outcome: "committed", host: row("alpha") }));
    fake.on("evener/host/list", () => Promise.reject(new Error("list failed")));
    await hostsStore.getState().remove("alpha");

    // The removal succeeded; the failed re-read keeps the last rows instead
    // of erroring the section, until the next successful read.
    const load = hostsStore.getState().load;
    expect(load.phase).toBe("ready");
    if (load.phase !== "ready") throw new Error("unreachable");
    expect(load.hosts).toEqual([row("alpha"), row("beta")]);
  });

  test("a pre-mutation list response cannot publish after an add whose re-read failed", async () => {
    // The background poll's list read goes out before the mutation and stays
    // in flight; the add then lands, its quiet re-read fails, and nothing
    // newer ever publishes. The response still in flight carries the snapshot
    // from before the add, so publishing it now would show rows the server has
    // already left behind — and a failed quiet read advances no marker, so
    // before the mutation fence it could still land. The landed add fences it
    // out: the rows the section was rendering stay, and the next poll or fetch
    // converges them.
    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({ hosts: [row("alpha")] }));
    await hostsStore.getState().fetch();

    let resolvePoll!: (data: HostListResponse) => void;
    fake.on("evener/host/list", () => new Promise<HostListResponse>((resolve) => (resolvePoll = resolve)));
    const poll = hostsStore.getState().refresh();

    fake.on("evener/host/add", () => ({ outcome: "committed", host: row("beta") }));
    fake.on("evener/host/list", () => Promise.reject(new Error("list failed")));
    await hostsStore.getState().add({ name: "beta", address: "b.example" });

    // Distinct contents make the fence observable: these are the rows as of
    // the poll's issue, not the rows the section is showing.
    resolvePoll({ hosts: [row("alpha"), row("stale")] });
    await poll;

    const load = hostsStore.getState().load;
    expect(load.phase).toBe("ready");
    if (load.phase !== "ready") throw new Error("unreachable");
    expect(load.hosts).toEqual([row("alpha")]);
  });

  test("a pre-mutation list response cannot publish after a remove whose re-read failed", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({ hosts: [row("alpha"), row("beta")] }));
    await hostsStore.getState().fetch();

    let resolvePoll!: (data: HostListResponse) => void;
    fake.on("evener/host/list", () => new Promise<HostListResponse>((resolve) => (resolvePoll = resolve)));
    const poll = hostsStore.getState().refresh();

    fake.on("evener/host/remove", () => ({ outcome: "committed", host: row("beta") }));
    fake.on("evener/host/list", () => Promise.reject(new Error("list failed")));
    await hostsStore.getState().remove("beta");

    resolvePoll({ hosts: [row("alpha"), row("beta"), row("stale")] });
    await poll;

    const load = hostsStore.getState().load;
    expect(load.phase).toBe("ready");
    if (load.phase !== "ready") throw new Error("unreachable");
    expect(load.hosts).toEqual([row("alpha"), row("beta")]);
  });

  test("a failed post-mutation re-read does not strand an in-flight fetch on the skeleton", async () => {
    // A foreground fetch parks the section on the loading skeleton and drops
    // the rows, and stays in flight. The add then lands and its quiet re-read
    // fails, so nothing newer publishes — but the parked fetch's response is
    // the one read left that can clear the skeleton, and the mutation fence
    // must stand down for it: it protects rows that are rendering, and here
    // there are none to protect.
    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({ hosts: [row("alpha")] }));
    await hostsStore.getState().fetch();

    let resolveFetch!: (data: HostListResponse) => void;
    fake.on("evener/host/list", () => new Promise<HostListResponse>((resolve) => (resolveFetch = resolve)));
    const fetched = hostsStore.getState().fetch();
    expect(hostsStore.getState().load.phase).toBe("loading");

    fake.on("evener/host/add", () => ({ outcome: "committed", host: row("beta") }));
    fake.on("evener/host/list", () => Promise.reject(new Error("list failed")));
    await hostsStore.getState().add({ name: "beta", address: "b.example" });

    resolveFetch({ hosts: [row("alpha")] });
    await fetched;

    const load = hostsStore.getState().load;
    expect(load.phase).toBe("ready");
    if (load.phase !== "ready") throw new Error("unreachable");
    expect(load.hosts).toEqual([row("alpha")]);
  });
});

describe("reading", () => {
  // The flag answers "is a registry read in flight" for consumers that must not
  // read a remote listing under a snapshot about to be replaced. Two callers can
  // overlap (a foreground fetch and the poll), so one of them settling must not
  // clear it while the other request is still out.
  test("stays set while any list request is still in flight", async () => {
    const fake = connectFakeClient();
    const gates: Array<(value: HostListResponse) => void> = [];
    fake.on(
      "evener/host/list",
      () =>
        new Promise<HostListResponse>((resolve) => {
          gates.push(resolve);
        }),
    );

    const first = hostsStore.getState().fetch();
    await Promise.resolve();
    const second = hostsStore.getState().fetch();
    await Promise.resolve();
    expect(gates).toHaveLength(2);
    expect(hostsStore.getState().reading).toBe(2);

    gates[0]?.({ hosts: [] });
    await first;
    expect(hostsStore.getState().reading).toBe(1);

    gates[1]?.({ hosts: [] });
    await second;
    expect(hostsStore.getState().reading).toBe(0);
  });
});

// --- host registration identity (component 07b, round 8) ---------------------
//
// A per-host store instance caches that host's own data (schema, catalogs,
// document). Keying the cache by NAME alone means a host removed and re-added
// under the same name with a different registration is served the previous
// registration's cache, and an in-flight response from the old registration
// can land in the new one. The registry's own row is the identity those caches
// are built against: `currentHostRegistration` reads it, and
// `hostRegistrationChanged` answers whether it still describes the instance.
//
// The live session fields (attached, the reported versions, os/arch, the
// attach error, midAttach) move without the registration changing, so they are
// deliberately NOT part of the identity: a store rebuilt on one of those would
// re-read everything on every attach, which is the churn the registry's own
// unchanged-snapshot rule exists to avoid.

describe("host registration identity", () => {
  test("currentHostRegistration reads the registry's own row for the host", () => {
    hostsStore.setState({ load: { phase: "ready", hosts: [row("beta")] } });
    expect(currentHostRegistration("beta")).toEqual(row("beta"));
    expect(currentHostRegistration("gamma")).toBeNull();
  });

  test("currentHostRegistration is undefined while the registry has no answer", () => {
    // Never fetched, still reading, or failed: no evidence either way, so
    // nothing may be invalidated from it.
    expect(currentHostRegistration("beta")).toBeUndefined();
    hostsStore.setState({ load: { phase: "error", message: "nope" } });
    expect(currentHostRegistration("beta")).toBeUndefined();
  });

  test("sameHostRegistration compares the registration, not the attach lifecycle", () => {
    const base = row("beta");
    const registered = { ...base, address: "beta.example:22", user: "u", roots: ["/a", "/b"] };
    expect(sameHostRegistration(registered, { ...registered })).toBe(true);
    // Fields the hub reports about a LIVE connection, which move without any
    // re-registration.
    expect(
      sameHostRegistration(registered, {
        ...registered,
        attached: true,
        serverName: "hub",
        serverVersion: "1.2.3",
        hubVersion: "9",
        os: "linux",
        arch: "arm64",
        lastAttachError: "handshake failed",
        midAttach: true,
      }),
    ).toBe(true);
    // A different registration.
    expect(sameHostRegistration(registered, { ...registered, address: "elsewhere:22" })).toBe(false);
    expect(sameHostRegistration(registered, { ...registered, removed: true })).toBe(false);
    // An absent and an empty root list describe the same host (the wire omits
    // empty optional arrays).
    expect(sameHostRegistration({ ...registered, roots: ["/a", "/b"] }, { ...registered, roots: undefined })).toBe(
      false,
    );
    expect(sameHostRegistration({ ...registered, roots: [] }, { ...registered, roots: undefined })).toBe(true);
  });

  test("hostRegistrationChanged treats an unread registry as no evidence", () => {
    const registered = row("beta");
    // Built before the registry ever answered: the first answer identifies the
    // instance rather than invalidating it.
    expect(hostRegistrationChanged(undefined, registered)).toBe(false);
    // No answer now.
    expect(hostRegistrationChanged(registered, undefined)).toBe(false);
    // The same registration, and the same absence of one.
    expect(hostRegistrationChanged(registered, registered)).toBe(false);
    expect(hostRegistrationChanged(null, null)).toBe(false);
    // A registration appearing, changing, or leaving.
    expect(hostRegistrationChanged(null, registered)).toBe(true);
    expect(hostRegistrationChanged(registered, { ...registered, address: "elsewhere:22" })).toBe(true);
    expect(hostRegistrationChanged(registered, null)).toBe(true);
  });
});

describe("the attach epoch", () => {
  // What a host-scoped pane re-reads on when a host that was away comes back
  // (panes/settings/sections/useConnectedEffect.ts's useHostScopedLoad). It has
  // to advance for that transition and for NOTHING else: panes re-read on it,
  // and at the panes that blank for different content an epoch that moved for a
  // poll or an attach-to-detach would take a form's draft with it.
  test("advances when a host comes back, and for nothing else", () => {
    const load = (host: HostRow) => hostsStore.setState({ load: { phase: "ready", hosts: [host] } });
    const epoch = () => hostsStore.getState().attachEpochs.beta;

    // Away, and still away (a mid-attach report included): nothing has come back.
    load(row("beta"));
    expect(epoch()).toBeUndefined();
    load({ ...row("beta"), midAttach: true });
    expect(epoch()).toBeUndefined();

    // Back: the one transition this exists for.
    load({ ...row("beta"), attached: true });
    expect(epoch()).toBe(1);

    // Still attached, however much the live state moves: no further advance.
    load({ ...row("beta"), attached: true, serverVersion: "2.0.0", hubVersion: "9" });
    expect(epoch()).toBe(1);

    // Away again, and back again: the second reconnect.
    load(row("beta"));
    load({ ...row("beta"), attached: true });
    expect(epoch()).toBe(2);
  });

  test("a section's foreground fetch does not hide the flip that follows it", () => {
    const load = (host: HostRow) => hostsStore.setState({ load: { phase: "ready", hosts: [host] } });

    load(row("beta"));
    // fetch() passes through "loading" with NO rows at all, and the answer that
    // follows reports the host back: the flip is measured against the last host
    // we saw, not against the previous snapshot (which describes none).
    hostsStore.setState({ load: { phase: "loading" } });
    load({ ...row("beta"), attached: true });

    expect(hostsStore.getState().attachEpochs.beta).toBe(1);
  });

  test("a host first seen attached has not come back from anywhere", () => {
    hostsStore.setState({ load: { phase: "ready", hosts: [{ ...row("beta"), attached: true }] } });

    expect(hostsStore.getState().attachEpochs.beta).toBeUndefined();
  });

  test("resetForTests forgets what was attached", () => {
    hostsStore.setState({ load: { phase: "ready", hosts: [row("beta")] } });
    hostsStore.setState({ load: { phase: "ready", hosts: [{ ...row("beta"), attached: true }] } });
    expect(hostsStore.getState().attachEpochs.beta).toBe(1);

    hostsStore.getState().resetForTests();

    hostsStore.setState({ load: { phase: "ready", hosts: [{ ...row("beta"), attached: true }] } });
    expect(hostsStore.getState().attachEpochs).toEqual({});
  });
});

describe("registrySaysHostGone", () => {
  // The predicate the frame's refusal and the per-host registries' eviction
  // share (see its own doc comment). What matters most is what it does NOT call
  // gone: an unanswered registry, and a host that is merely unattached.
  test("answers only for an answered registry, and never for an unattached host", () => {
    const gone = () => registrySaysHostGone(hostsStore.getState().load, "beta");

    // No answer yet, and a failed read: no evidence either way.
    expect(gone()).toBe(false);
    hostsStore.setState({ load: { phase: "error", message: "the list read failed" } });
    expect(gone()).toBe(false);

    // Answered, and beta is in it - attached or not.
    hostsStore.setState({ load: { phase: "ready", hosts: [row("beta")] } });
    expect(gone()).toBe(false);

    // Answered and beta is not listed: gone.
    hostsStore.setState({ load: { phase: "ready", hosts: [] } });
    expect(gone()).toBe(true);

    // Answered and beta is listed as removed: gone (the registry's own removal
    // answer, which the frame's refusal has always honoured).
    hostsStore.setState({ load: { phase: "ready", hosts: [{ ...row("beta"), removed: true }] } });
    expect(gone()).toBe(true);
  });
});
