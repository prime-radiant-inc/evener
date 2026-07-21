import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { LaunchConfigLayer, LaunchOptionSchemaResponse } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
import { resetLaunchConfigStoreForTests } from "../../../stores/launchConfig";
import { Toast } from "../../../widgets";
import { ProjectSection } from "./project";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

function setQueryCwd(cwd: string | null): void {
  const url = new URL(window.location.href);
  if (cwd === null) url.searchParams.delete("cwd");
  else url.searchParams.set("cwd", cwd);
  window.history.replaceState({}, "", `${url.pathname}${url.search}`);
}

const SCHEMA: LaunchOptionSchemaResponse = {
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
  ],
};

const PROJECT_LAYER: LaunchConfigLayer = {};
const GLOBAL_LAYER: LaunchConfigLayer = { agent: "serf" };

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetLaunchConfigStoreForTests();
  setQueryCwd(null);
});

afterEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  setQueryCwd(null);
  cleanup();
});

describe("no cwd in the query string", () => {
  test("shows an instructional message instead of fetching anything", () => {
    render(<ProjectSection sectionId="project" />);
    expect(screen.getByText(/No project selected/i)).toBeTruthy();
  });
});

describe("3-state loaded contract", () => {
  test("loading, then ready: fetches schema + project layer + global layer (global read-only, for hints)", async () => {
    setQueryCwd("/repo");
    const fake = connectFakeClient();
    const calls: string[] = [];
    fake.on("serf/launch/schema", () => {
      calls.push("schema");
      return SCHEMA;
    });
    fake.on("serf/launch/getLayer", (params) => {
      calls.push(`getLayer:${params.layer}`);
      return params.layer === "project" ? PROJECT_LAYER : GLOBAL_LAYER;
    });
    render(<ProjectSection sectionId="project" />);
    expect(screen.getByText(/Loading project launch settings/i)).toBeTruthy();
    await screen.findByLabelText("Agent");
    expect(calls).toEqual(["schema", "getLayer:project", "getLayer:global"]);
    // The global layer is read-only context for the "default: X" hint, never itself editable here.
    expect(screen.getByText("default: serf")).toBeTruthy();
  });

  test("error is a distinct third state (not '2-state, stays loading forever')", async () => {
    setQueryCwd("/repo");
    const fake = connectFakeClient();
    fake.on("serf/launch/schema", () => {
      throw new Error("boom");
    });
    render(<ProjectSection sectionId="project" />);
    await screen.findByText(/Failed to load project launch settings/i);
    expect(screen.getByText(/boom/)).toBeTruthy();
  });

  test("no diagnostics panel anywhere on this page", async () => {
    setQueryCwd("/repo");
    const fake = connectFakeClient();
    fake.on("serf/launch/schema", () => SCHEMA);
    fake.on("serf/launch/getLayer", (params) => (params.layer === "project" ? PROJECT_LAYER : GLOBAL_LAYER));
    render(<ProjectSection sectionId="project" />);
    await screen.findByLabelText("Agent");
    expect(screen.queryByText("Warnings")).toBeNull();
  });
});

describe("save", () => {
  test("saves to the project layer (not global) and toasts the project-specific success message", async () => {
    setQueryCwd("/repo");
    const fake = connectFakeClient();
    fake.on("serf/launch/schema", () => SCHEMA);
    fake.on("serf/launch/getLayer", (params) => (params.layer === "project" ? PROJECT_LAYER : GLOBAL_LAYER));
    fake.on("serf/launch/setLayer", (params) => {
      expect(params.cwd).toBe("/repo");
      expect(params.layer).toBe("project");
      return { effective: {}, layers: {}, provenance: {} };
    });
    render(
      <>
        <ProjectSection sectionId="project" />
        <Toast />
      </>,
    );
    await screen.findByLabelText("Agent");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Save launch defaults" }));
    await screen.findByText(/^Saved at /);
    expect(screen.getAllByText("Project launch settings saved").length).toBeGreaterThan(0);
  });
});
