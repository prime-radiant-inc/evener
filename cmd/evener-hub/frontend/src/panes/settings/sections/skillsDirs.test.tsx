import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { connectionStore } from "../../../stores/connection";
import { resetExtensionsStoreForTests } from "../../../stores/extensions";
import { connectReadyFakeClient } from "../../../testing/connectionTestUtils";
import { SkillsDirsSection } from "./skillsDirs";

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetExtensionsStoreForTests();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

test("renders a heading and the skillsDirs entries, not pluginDirs", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/launch/getLayer", (params) => {
    expect(params).toEqual({ cwd: "/", layer: "global" });
    return { pluginDirs: ["/opt/plugins"], skillsDirs: ["/opt/skills"] };
  });
  render(<SkillsDirsSection />);
  expect(await screen.findByRole("heading", { level: 2 })).toBeTruthy();
  expect(await screen.findByText("/opt/skills")).toBeTruthy();
  expect(screen.queryByText("/opt/plugins")).toBeNull();
});

test("saving a new entry writes it to the skillsDirs field, not pluginDirs", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/launch/getLayer", () => ({ pluginDirs: ["/existing-plugin"], skillsDirs: [] }));
  fake.on("evener/path/validate", () => ({ path: "/opt/new-skill", valid: true }));
  fake.on("evener/launch/setLayer", (params) => {
    expect(params).toEqual({
      cwd: "/",
      layer: "global",
      config: { pluginDirs: ["/existing-plugin"], skillsDirs: ["/opt/new-skill"] },
    });
    return { effective: {}, layers: {}, provenance: {} };
  });
  fake.on("evener/paths/complete", () => ({ data: [] }));
  const user = userEvent.setup();
  render(<SkillsDirsSection />);
  await screen.findByText("No skill directories. Add one below.");
  await user.click(screen.getByRole("button", { name: /^New directory:/ }));
  const input = await screen.findByRole("textbox", { name: "Path" });
  await user.clear(input);
  await user.type(input, "/opt/new-skill");
  await user.keyboard("{Enter}");
  await user.click(screen.getByRole("button", { name: "Use this folder" }));
  await user.click(screen.getByRole("button", { name: "Add" }));
  expect(await screen.findByText("/opt/new-skill")).toBeTruthy();
});
