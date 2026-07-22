import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import { connectionStore } from "../../../stores/connection";
import { resetExtensionsStoreForTests } from "../../../stores/extensions";
import { PluginsDirsSection } from "./pluginsDirs";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetExtensionsStoreForTests();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

test("renders the Plugin directories heading, copy, and the pluginDirs entries", async () => {
  const fake = connectFakeClient();
  fake.on("serf/launch/getLayer", (params) => {
    expect(params).toEqual({ cwd: "/", layer: "global" });
    return { pluginDirs: ["/opt/plugins"], skillsDirs: ["/opt/skills"] };
  });
  render(<PluginsDirsSection />);
  expect(await screen.findByText("Plugin directories")).toBeTruthy();
  expect(screen.getByText("Directories serf scans for plugins at launch. Applied to every spawn.")).toBeTruthy();
  expect(await screen.findByText("/opt/plugins")).toBeTruthy();
  expect(screen.queryByText("/opt/skills")).toBeNull();
});

test("saving a new entry writes it to the pluginDirs field, not skillsDirs", async () => {
  const fake = connectFakeClient();
  fake.on("serf/launch/getLayer", () => ({ pluginDirs: [], skillsDirs: ["/existing-skill"] }));
  fake.on("serf/path/validate", () => ({ path: "/opt/new", valid: true }));
  fake.on("serf/launch/setLayer", (params) => {
    expect(params).toEqual({
      cwd: "/",
      layer: "global",
      config: { pluginDirs: ["/opt/new"], skillsDirs: ["/existing-skill"] },
    });
    return { effective: {}, layers: {}, provenance: {} };
  });
  const user = userEvent.setup();
  render(<PluginsDirsSection />);
  await screen.findByText("No plugin directories. Add one below.");
  await user.type(screen.getByPlaceholderText("/absolute/path"), "/opt/new");
  await user.click(screen.getByRole("button", { name: "Add" }));
  expect(await screen.findByText("/opt/new")).toBeTruthy();
});
