import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { resetConnectionStoreForTests } from "../../../stores/connection";
import { resetExtensionsStoreForTests } from "../../../stores/extensions";
import { connectReadyFakeClient } from "../../../testing/connectionTestUtils";
import { SkillsDirsSection } from "./skillsDirs";

beforeEach(() => {
  resetConnectionStoreForTests();
  resetExtensionsStoreForTests();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

test("renders the full discovery order and the extra skillsDirs entries, not pluginDirs", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/launch/getLayer", (params) => {
    expect(params).toEqual({ cwd: "/", layer: "global" });
    return { pluginDirs: ["/opt/plugins"], skillsDirs: ["/opt/skills"] };
  });
  fake.on("evener/settings/skillDirs/get", () => ({
    homeSkillsDir: "/host/home/.agents/skills",
    userSkillsDir: "/host/config/evener/skills",
  }));
  render(<SkillsDirsSection />);
  expect(await screen.findByRole("heading", { level: 2 })).toBeTruthy();
  expect(await screen.findByText("/host/home/.agents/skills")).toBeTruthy();
  expect(await screen.findByText("/host/config/evener/skills")).toBeTruthy();
  expect(await screen.findByText("/opt/skills")).toBeTruthy();
  expect(document.querySelector("[data-count]")?.getAttribute("data-count")).toBe("1");
  expect(screen.queryByText("/opt/plugins")).toBeNull();
});

test("a failed built-in read offers a retry that reads the roots again", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/launch/getLayer", () => ({ skillsDirs: [] }));
  let reads = 0;
  fake.on("evener/settings/skillDirs/get", () => {
    reads += 1;
    if (reads === 1) throw new Error("temporary read failure");
    return {
      homeSkillsDir: "/recovered/home/.agents/skills",
      userSkillsDir: "/recovered/config/evener/skills",
    };
  });
  const user = userEvent.setup();
  render(<SkillsDirsSection />);
  await user.click(await screen.findByRole("button", { name: "Retry" }));
  expect(await screen.findByText("/recovered/home/.agents/skills")).toBeTruthy();
  expect(reads).toBe(2);
});

test("saving a new entry writes it to the skillsDirs field, not pluginDirs", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/launch/getLayer", () => ({ pluginDirs: ["/existing-plugin"], skillsDirs: [] }));
  fake.on("evener/settings/skillDirs/get", () => ({
    homeSkillsDir: "/host/home/.agents/skills",
    userSkillsDir: "/host/config/evener/skills",
  }));
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
  await user.click(await screen.findByRole("button", { name: /^New directory:/ }));
  const input = await screen.findByRole("textbox", { name: "Path" });
  await user.clear(input);
  await user.type(input, "/opt/new-skill");
  await user.keyboard("{Enter}");
  await user.click(screen.getByRole("button", { name: "Use this folder" }));
  await user.click(screen.getByRole("button", { name: "Add" }));
  expect(await screen.findByText("/opt/new-skill")).toBeTruthy();
});
