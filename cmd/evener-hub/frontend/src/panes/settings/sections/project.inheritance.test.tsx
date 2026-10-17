import type { LaunchConfigLayer, LaunchConfigResolved, LaunchOptionSchemaResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test } from "vitest";
import { connectionStore } from "../../../stores/connection";
import { resetLaunchConfigStoreForTests } from "../../../stores/launchConfig";
import { ProjectSection } from "./project";

const schema: LaunchOptionSchemaResponse = {
  options: [
    {
      field: "agent",
      wireField: "agent",
      label: "Agent",
      group: "Agent",
      kind: "text",
      perLaunch: true,
      defaultableLayers: ["global", "project"],
    },
    {
      field: "model_fallbacks",
      wireField: "modelFallbacks",
      label: "Fallbacks",
      group: "Agent",
      kind: "modelList",
      perLaunch: true,
      defaultableLayers: ["global", "project"],
    },
  ],
};
const global: LaunchConfigLayer = { agent: "global-sentinel", modelFallbacks: ["global/model"] };
const repo: LaunchConfigLayer = { agent: "repo-sentinel", modelFallbacks: ["repo/model"] };
const project: LaunchConfigLayer = { agent: "local-sentinel", modelFallbacks: [] };

beforeEach(() => {
  connectionStore.setState({ state: "idle", client: null, serverInfo: undefined });
  resetLaunchConfigStoreForTests();
  window.history.replaceState({}, "", "/settings/project?cwd=%2Frepo%2Fnested");
});
afterEach(() => {
  cleanup();
  connectionStore.setState({ state: "idle", client: null, serverInfo: undefined });
  window.history.replaceState({}, "", "/");
});

test.each([true, false])(
  "clearing a local scalar previews actual lower layers before saving, trusted=%s",
  async (trusted) => {
    const client = new FakeClient("ready");
    const saved: LaunchConfigLayer[] = [];
    const result = (current: LaunchConfigLayer): LaunchConfigResolved => ({
      effective: { ...global, ...(trusted ? repo : {}), ...current },
      layers: { global, ...(trusted ? { repo } : {}), project: current },
      provenance: { agent: "project", model_fallbacks: "project" },
      repo: {
        path: "/repo/.evener/launch.toml",
        hash: "reviewed-hash",
        trust: trusted ? "trusted" : "untrusted",
        preview: 'agent = "repo-sentinel"',
      },
    });
    client.on("evener/launch/schema", () => schema);
    client.on("evener/launch/getLayer", (params) => (params.layer === "global" ? global : project));
    client.on("evener/launch/resolve", () => result(project));
    client.on("evener/launch/setLayer", (params) => {
      saved.push(params.config);
      return result(params.config);
    });
    connectionStore.getState().connect(client);
    render(<ProjectSection sectionId="project" />);
    const input = (await screen.findByRole("textbox", { name: "Agent" })) as HTMLInputElement;
    const user = userEvent.setup();
    await user.clear(input);
    expect(input.value).toBe("");
    expect(input.placeholder).toContain(trusted ? repo.agent : global.agent);
    expect(input.placeholder).not.toContain(project.agent);
    expect(saved).toEqual([]);
    // An explicit empty chain stays explicit when a different field is cleared.
    expect(screen.getByRole("switch").getAttribute("aria-checked")).toBe("true");
    const fallbacks = screen.getByRole("list", { name: "Fallbacks" });
    expect(fallbacks.querySelectorAll('[data-kind="inherited"]')).toHaveLength(0);
    await user.click(screen.getByRole("switch"));
    expect(fallbacks.querySelectorAll('[data-kind="inherited"]')).toHaveLength(1);
    expect(fallbacks.textContent).toContain(trusted ? "repo/model" : "global/model");
    await user.click(screen.getByRole("switch"));
    await user.click(screen.getByRole("button", { name: "Save local overrides" }));
    await waitFor(() => expect(saved).toEqual([{ modelFallbacks: [] }]));
    expect(input.placeholder).toContain(trusted ? repo.agent : global.agent);
    await user.type(input, "next-local-sentinel");
    await user.click(screen.getByRole("button", { name: "Save local overrides" }));
    await waitFor(() => expect(saved).toHaveLength(2));
    await user.clear(input);
    expect(input.placeholder).toContain(trusted ? repo.agent : global.agent);
    expect(input.placeholder).not.toContain("next-local-sentinel");
    expect(client.calls.filter((call) => call.method === "evener/launch/trustRepo")).toEqual([]);
    for (const call of client.calls.filter((call) =>
      ["evener/launch/resolve", "evener/launch/getLayer", "evener/launch/setLayer"].includes(call.method),
    )) {
      expect(call.params).toMatchObject({ cwd: "/repo/nested" });
    }
  },
);

test("local collection removal reveals lower-layer data, not the removed saved entry", async () => {
  const client = new FakeClient("ready");
  const options: LaunchOptionSchemaResponse = {
    options: [
      {
        field: "skills_dirs",
        wireField: "skillsDirs",
        label: "Skill paths",
        group: "test",
        kind: "pathList",
        perLaunch: true,
        defaultableLayers: ["project"],
      },
      {
        field: "env",
        wireField: "env",
        label: "Environment",
        group: "test",
        kind: "envMap",
        perLaunch: true,
        defaultableLayers: ["project"],
      },
    ],
  };
  const local = { skillsDirs: ["/repo/shared", "/local/only"], env: { MODE: "local-value" } };
  client.on("evener/launch/schema", () => options);
  client.on("evener/launch/getLayer", () => local);
  client.on("evener/launch/resolve", () => ({
    effective: { skillsDirs: ["/global", "/repo/shared", ...local.skillsDirs], env: local.env },
    provenance: { skills_dirs: "project", env: "project" },
    layers: {
      global: { skillsDirs: ["/global"], env: { MODE: "global-value" } },
      repo: { skillsDirs: ["/repo/shared"], env: { MODE: "repo-value" } },
      project: local,
    },
  }));
  connectionStore.getState().connect(client);
  render(<ProjectSection sectionId="project" />);
  const paths = await screen.findByRole("list", { name: "Skill paths" });
  expect(paths.querySelectorAll('[data-kind="inherited"]')).toHaveLength(2);
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Remove /local/only" }));
  expect(paths.textContent).not.toContain("/local/only");
  await user.click(screen.getByRole("button", { name: "Remove MODE=local-value" }));
  const env = screen.getByRole("list", { name: "Environment" });
  expect(env.querySelectorAll('[data-kind="inherited"]')).toHaveLength(1);
  expect(env.textContent).toContain("repo-value");
  expect(env.textContent).not.toContain("local-value");
  expect(client.calls.filter((call) => call.method === "evener/launch/setLayer")).toEqual([]);
});
