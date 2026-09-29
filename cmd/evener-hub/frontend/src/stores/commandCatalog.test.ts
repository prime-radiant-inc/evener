// @vitest-environment node

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
