// @vitest-environment node

import type { CommandListResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { useCommandCatalog } from "./commandCatalog";
import { connectionStore } from "./connection";

function reset() {
  useCommandCatalog.setState(useCommandCatalog.getInitialState());
  connectionStore.setState({ client: null });
}

beforeEach(reset);
afterEach(reset);

function catalogClient(names: string[]) {
  const fake = new FakeClient();
  fake.on("evener/command/list", () => ({
    commands: names.map((name) => ({ name, description: `${name} cmd`, source: "user" })),
  }));
  return fake;
}

test("a client arriving ready loads the catalog without waiting for a palette open", async () => {
  connectionStore.getState().connect(catalogClient(["par"]) as never);
  await vi.waitFor(() => expect(useCommandCatalog.getState().commands.map((c) => c.name)).toEqual(["par"]));
});

test("a replaced client's catalog is read on the swap, not only on its later notifications", async () => {
  connectionStore.getState().connect(catalogClient(["review"]) as never);
  await vi.waitFor(() => expect(useCommandCatalog.getState().commands.map((c) => c.name)).toEqual(["review"]));
  connectionStore.getState().connect(catalogClient(["release"]) as never);
  await vi.waitFor(() => expect(useCommandCatalog.getState().commands.map((c) => c.name)).toEqual(["release"]));
});

// RoboRev #3022's reconciliation: a swap to a client that is not ready yet
// triggers no re-read (no ready transition), so a response from the OUTGOING
// client that lands in that window must not publish its catalog over the
// connection that replaced it. The supersede is silent: the last catalog
// (here the initial empty one) stays with NO error state - a superseded read
// is an expected internal cancellation, not a failure to surface.
test("a response landing after a swap to a not-yet-ready client is dropped, not published", async () => {
  const first = new FakeClient("ready");
  let answerFirst!: (value: CommandListResponse) => void;
  let firstReads = 0;
  first.on("evener/command/list", () => {
    firstReads += 1;
    if (firstReads === 1) return { commands: [{ name: "review", source: "user" }] };
    return new Promise<CommandListResponse>((resolve) => {
      answerFirst = resolve;
    });
  });
  connectionStore.getState().connect(first as never);
  await vi.waitFor(() => expect(useCommandCatalog.getState().commands.map((c) => c.name)).toEqual(["review"]));
  // A plugin change starts a second read that hangs past the swap below.
  first.emitNotification({ method: "evener/plugin/updated", params: {} });
  await vi.waitFor(() => expect(first.calls).toHaveLength(2));

  const second = new FakeClient("connecting");
  second.on("evener/command/list", () => ({ commands: [{ name: "release", source: "user" }] }));
  connectionStore.getState().connect(second as never);

  answerFirst({ commands: [{ name: "stale-from-first", source: "user" }] });
  // Let the superseded read settle, then assert: the LAST catalog is kept -
  // not the stale payload, and not a reset to the empty initial one - and no
  // error flashed for what is an expected cancellation.
  await vi.waitFor(() => expect(useCommandCatalog.getState().loading).toBe(false));
  expect(useCommandCatalog.getState()).toMatchObject({ commands: [{ name: "review", source: "user" }], error: null });

  second.emitStateChange("ready");
  await vi.waitFor(() => expect(useCommandCatalog.getState().commands.map((c) => c.name)).toEqual(["release"]));
  expect(useCommandCatalog.getState().error).toBeNull();
});

// The rejection arm of the case above: when the outgoing client's in-flight
// read FAILS after the swap (its socket closed, failing the request), the
// same silent-supersede contract holds - no error publishes for an expected
// internal cancellation. A failure on the CURRENT connection still surfaces.
test("a rejection landing after a swap to a not-yet-ready client stays silent too", async () => {
  const first = new FakeClient("ready");
  let failFirst!: (error: unknown) => void;
  let firstReads = 0;
  first.on("evener/command/list", () => {
    firstReads += 1;
    if (firstReads === 1) return { commands: [{ name: "review", source: "user" }] };
    return new Promise<CommandListResponse>((_resolve, reject) => {
      failFirst = reject;
    });
  });
  connectionStore.getState().connect(first as never);
  await vi.waitFor(() => expect(useCommandCatalog.getState().commands.map((c) => c.name)).toEqual(["review"]));
  first.emitNotification({ method: "evener/plugin/updated", params: {} });
  await vi.waitFor(() => expect(first.calls).toHaveLength(2));

  const second = new FakeClient("connecting");
  second.on("evener/command/list", () => ({ commands: [{ name: "release", source: "user" }] }));
  connectionStore.getState().connect(second as never);

  failFirst(new Error("socket closed"));
  await vi.waitFor(() => expect(useCommandCatalog.getState().loading).toBe(false));
  expect(useCommandCatalog.getState()).toMatchObject({ commands: [{ name: "review", source: "user" }], error: null });

  second.emitStateChange("ready");
  await vi.waitFor(() => expect(useCommandCatalog.getState().commands.map((c) => c.name)).toEqual(["release"]));
  expect(useCommandCatalog.getState().error).toBeNull();
});

test("refresh reads the catalog through the connection's client, and a failed re-read keeps the last one", async () => {
  connectionStore.setState({ client: catalogClient(["review", "standup"]) as never });
  await useCommandCatalog.getState().refresh();
  expect(useCommandCatalog.getState().commands).toHaveLength(2);
  expect(useCommandCatalog.getState()).toMatchObject({ loading: false, error: null });

  const failing = new FakeClient();
  failing.on("evener/command/list", () => Promise.reject(new Error("down")));
  connectionStore.setState({ client: failing as never });
  await useCommandCatalog.getState().refresh();
  expect(useCommandCatalog.getState().commands).toHaveLength(2);
  expect(useCommandCatalog.getState().error).toContain("down");
});

test("a refresh with no client wired is a named failure, not a hang or a throw", async () => {
  await useCommandCatalog.getState().refresh();
  expect(useCommandCatalog.getState()).toMatchObject({ commands: [], loading: false });
  expect(useCommandCatalog.getState().error).toContain("Not connected");
});

test("a plugin change on the wired client re-reads the catalog; a replaced client's changes are ignored", async () => {
  const first = catalogClient(["review"]);
  connectionStore.setState({ client: first as never });
  await vi.waitFor(() => expect(useCommandCatalog.getState().commands.map((c) => c.name)).toEqual(["review"]));

  first.on("evener/command/list", () => ({
    commands: [
      { name: "review", source: "user" },
      { name: "ship", source: "user" },
    ],
  }));
  first.emitNotification({ method: "evener/plugin/updated", params: {} });
  await vi.waitFor(() => expect(useCommandCatalog.getState().commands.map((c) => c.name)).toEqual(["review", "ship"]));

  const second = catalogClient(["release"]);
  connectionStore.setState({ client: second as never });
  await vi.waitFor(() => expect(useCommandCatalog.getState().commands.map((c) => c.name)).toEqual(["release"]));

  // The replaced client's notifications reach nothing: first's call log is
  // frozen at the two reads above (the initial load's and its plugin change's).
  first.emitNotification({ method: "evener/plugin/updated", params: {} });
  await Promise.resolve();
  expect(first.calls).toHaveLength(2);
  expect(useCommandCatalog.getState().commands.map((c) => c.name)).toEqual(["release"]);

  second.on("evener/command/list", () => ({
    commands: [
      { name: "release", source: "user" },
      { name: "tag", source: "user" },
    ],
  }));
  second.emitNotification({ method: "evener/plugin/updated", params: {} });
  await vi.waitFor(() => expect(useCommandCatalog.getState().commands.map((c) => c.name)).toEqual(["release", "tag"]));
});
