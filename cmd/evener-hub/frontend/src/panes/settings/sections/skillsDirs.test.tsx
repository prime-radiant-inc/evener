import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import { connectionStore } from "../../../stores/connection";
import { resetExtensionsStoreForTests } from "../../../stores/extensions";
import { SkillsDirsSection } from "./skillsDirs";

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

test("renders the Skill directories heading, copy, and the skillsDirs entries", async () => {
  const fake = connectFakeClient();
  fake.on("serf/launch/getLayer", (params) => {
    expect(params).toEqual({ cwd: "/", layer: "global" });
    return { pluginDirs: ["/opt/plugins"], skillsDirs: ["/opt/skills"] };
  });
  render(<SkillsDirsSection />);
  expect(await screen.findByText("Skill directories")).toBeTruthy();
  expect(screen.getByText("Directories serf scans for skills at launch. Applied to every spawn.")).toBeTruthy();
  expect(await screen.findByText("/opt/skills")).toBeTruthy();
  expect(screen.queryByText("/opt/plugins")).toBeNull();
});

test("saving a new entry writes it to the skillsDirs field, not pluginDirs", async () => {
  const fake = connectFakeClient();
  fake.on("serf/launch/getLayer", () => ({ pluginDirs: ["/existing-plugin"], skillsDirs: [] }));
  fake.on("serf/path/validate", () => ({ path: "/opt/new-skill", valid: true }));
  fake.on("serf/launch/setLayer", (params) => {
    expect(params).toEqual({
      cwd: "/",
      layer: "global",
      config: { pluginDirs: ["/existing-plugin"], skillsDirs: ["/opt/new-skill"] },
    });
    return { effective: {}, layers: {}, provenance: {} };
  });
  fake.on("serf/paths/complete", () => ({ data: [] }));
  const user = userEvent.setup();
  render(<SkillsDirsSection />);
  await screen.findByText("No skill directories. Add one below.");
  await user.click(screen.getByRole("button", { name: "New directory" }));
  await screen.findByRole("combobox", { name: "Path" });
  await user.keyboard("/opt/new-skill");
  await user.keyboard("{Enter}");
  await user.click(screen.getByRole("button", { name: "Add" }));
  expect(await screen.findByText("/opt/new-skill")).toBeTruthy();
});
