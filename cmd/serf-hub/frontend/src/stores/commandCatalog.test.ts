import { afterEach, beforeEach, expect, test } from "vitest";
import { FakeClient } from "../protocol/testing/fakeClient";
import { useCommandCatalog } from "./commandCatalog";
import { connectionStore } from "./connection";

beforeEach(() => {
  useCommandCatalog.setState({ commands: [], loaded: false });
  connectionStore.setState({ client: null });
});

afterEach(() => {
  useCommandCatalog.setState({ commands: [], loaded: false });
  connectionStore.setState({ client: null });
});

test("refresh populates catalog entries and tolerates failure", async () => {
  const fake = new FakeClient();
  fake.on("serf/command/list", () => ({
    commands: [
      { name: "review", pluginName: "p", description: "plugin cmd", source: "plugin" },
      { name: "standup", description: "user cmd", source: "user" },
    ],
  }));
  connectionStore.setState({ client: fake as never });
  await useCommandCatalog.getState().refresh();
  expect(useCommandCatalog.getState().commands).toHaveLength(2);
  expect(useCommandCatalog.getState().loaded).toBe(true);

  const failing = new FakeClient();
  failing.on("serf/command/list", () => Promise.reject(new Error("down")));
  connectionStore.setState({ client: failing as never });
  await useCommandCatalog.getState().refresh();
  expect(useCommandCatalog.getState().commands).toEqual([]);
  expect(useCommandCatalog.getState().loaded).toBe(true);
});
