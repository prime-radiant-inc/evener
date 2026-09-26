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

test("a plugin change on the wired client re-reads a loaded catalog; a replaced client's changes are ignored", async () => {
  const first = catalogClient(["review"]);
  connectionStore.setState({ client: first as never });
  first.emitNotification({ method: "evener/plugin/updated", params: {} });
  await Promise.resolve();
  expect(first.calls).toEqual([]);
  await useCommandCatalog.getState().refresh();
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
  first.emitNotification({ method: "evener/plugin/updated", params: {} });
  await Promise.resolve();
  expect(useCommandCatalog.getState().commands.map((c) => c.name)).toEqual(["review", "ship"]);
  second.emitNotification({ method: "evener/plugin/updated", params: {} });
  await vi.waitFor(() => expect(useCommandCatalog.getState().commands.map((c) => c.name)).toEqual(["release"]));
});
