import type { LaunchConfigResolved, LaunchOption } from "@evener/appwire-client";
import { expect, test } from "vitest";
import { projectInheritedDefaults } from "./projectInheritedDefaults";

test("projects lower layers with scalar, append, replacement, and keyed-map semantics", () => {
  const resolved: LaunchConfigResolved = {
    effective: { agent: "local", env: { A: "local", B: "local" } },
    provenance: { agent: "project", env: "project" },
    layers: {
      global: {
        agent: "global",
        verbose: true,
        maxRounds: 40,
        skillsDirs: ["/global"],
        modelFallbacks: ["global/model"],
        enabledPlugins: ["global/plugin"],
        mcps: [{ name: "global-mcp", command: "global-bin" }],
        env: { A: "global", B: "global", API_KEY: "blocked-sentinel" },
      },
      repo: {
        agent: "repo",
        verbose: false,
        maxRounds: 0,
        skillsDirs: ["/repo"],
        modelFallbacks: [],
        enabledPlugins: [],
        mcps: [{ name: "repo-mcp", command: "repo-bin" }],
        env: { B: "repo" },
      },
      project: {
        agent: "local",
        skillsDirs: ["/local"],
        modelFallbacks: ["local/model"],
        env: { A: "local", B: "local" },
      },
      launch: { agent: "launch" },
    },
  };
  const before = structuredClone(resolved);
  expect(projectInheritedDefaults(resolved, [])).toEqual({
    agent: "repo",
    verbose: false,
    maxRounds: 0,
    skillsDirs: ["/global", "/repo"],
    modelFallbacks: [],
    enabledPlugins: [],
    mcps: [
      { name: "global-mcp", command: "global-bin" },
      { name: "repo-mcp", command: "repo-bin" },
    ],
    env: { A: "global", B: "repo" },
  });
  expect(resolved).toEqual(before);
});

test("changing prompt mode removes inactive lower-layer payloads, including explicit empty inline text", () => {
  expect(
    projectInheritedDefaults(
      {
        effective: {},
        provenance: {},
        layers: {
          global: {
            systemPromptMode: "file",
            systemPromptFile: "/global/prompt",
            systemPromptAppendMode: "inline",
            systemPromptAppendText: "global-append",
          },
          repo: {
            systemPromptMode: "inline",
            systemPromptText: "",
            systemPromptAppend: ["/repo/append", "/ignored/legacy"],
          },
          project: { systemPromptMode: "inline", systemPromptText: "local-prompt" },
        },
      },
      [],
    ),
  ).toEqual({
    systemPromptMode: "inline",
    systemPromptText: "",
    systemPromptAppendMode: "file",
    systemPromptAppendFile: "/repo/append",
  });
});

test("uses only provenance-confirmed runtime floors, never a hidden local or launch value", () => {
  const options: LaunchOption[] = ["agent", "sandbox_net", "verbose", "model", "max_rounds"].map((field, index) => ({
    field,
    wireField: ["agent", "sandboxNet", "verbose", "model", "maxRounds"][index] ?? field,
    label: field,
    group: "test",
    kind: "text",
    perLaunch: true,
  }));
  expect(
    projectInheritedDefaults(
      {
        effective: { agent: "builtin-agent", sandboxNet: true, verbose: false, model: "local-model", maxRounds: 100 },
        provenance: { agent: "builtin", sandbox_net: "env", verbose: "hub", model: "project", max_rounds: "launch" },
        layers: { project: { model: "local-model" }, launch: { maxRounds: 100 } },
      },
      options,
    ),
  ).toEqual({ agent: "builtin-agent", sandboxNet: true, verbose: false });
});

test("ignores prompt payloads from a layer that does not select a prompt mode", () => {
  expect(
    projectInheritedDefaults(
      {
        effective: {},
        provenance: {},
        layers: {
          global: { systemPromptMode: "inline", systemPromptText: "active-global" },
          repo: { systemPromptText: "inactive-repo", systemPromptFile: "/inactive/file" },
        },
      },
      [],
    ),
  ).toEqual({ systemPromptMode: "inline", systemPromptText: "active-global" });
});
