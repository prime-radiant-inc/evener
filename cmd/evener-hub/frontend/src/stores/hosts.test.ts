// hosts.test.ts — store-level unit tests for the Hosts settings section's
// store (stores/hosts.ts). Component-level rendering and polling tests live
// in panes/settings/sections/hosts.test.tsx.
//
// Pattern mirrors daemonResidents.test.ts: each test resets store +
// connection in beforeEach.

import type { HostListResponse, HostRow } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { beforeEach, describe, expect, test } from "vitest";
import { connectionStore } from "./connection";
import { hostsStore } from "./hosts";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

function row(name: string): HostRow {
  return { name, origin: "sidecar", attached: false, midAttach: false, removed: false };
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  hostsStore.getState().resetForTests();
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

    // The mutation path (add/remove/connect) re-reads with fetch(), which is
    // independent of the in-flight background refresh: the two requests race,
    // and the background one can land after the mutation's.
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
});
