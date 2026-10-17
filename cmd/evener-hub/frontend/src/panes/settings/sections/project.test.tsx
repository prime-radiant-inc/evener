import type { LaunchConfigLayer, LaunchOptionSchemaResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { connectionStore } from "../../../stores/connection";
import { resetLaunchConfigStoreForTests } from "../../../stores/launchConfig";
import { Toast } from "../../../widgets";
import { getToasts } from "../../../widgets/toast/store";
import { ProjectSection } from "./project";
import { shownReasons } from "./reasonTestUtils";

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
const GLOBAL_LAYER: LaunchConfigLayer = { agent: "evener" };

test("project settings review repository trust without replacing an unsaved local override", async () => {
  setQueryCwd("/repo");
  const fake = connectFakeClient();
  let trusted = false;
  const approvals: unknown[] = [];
  fake.on("evener/launch/schema", () => SCHEMA);
  fake.on("evener/launch/getLayer", () => ({}));
  fake.on("evener/launch/resolve", () => ({
    effective: {},
    layers: {},
    provenance: {},
    repo: {
      path: "/repo/.evener/launch.toml",
      trust: trusted ? "trusted" : "untrusted",
      hash: "reviewed-hash",
      preview: 'agent = "repo-agent"',
    },
  }));
  fake.on("evener/launch/trustRepo", (params) => {
    approvals.push(params);
    trusted = true;
    return { effective: {}, layers: {}, provenance: {} };
  });
  render(<ProjectSection sectionId="project" />);
  const agent = await screen.findByLabelText("Agent");
  const user = userEvent.setup();
  await user.type(agent, "local-draft");
  expect(approvals).toEqual([]);
  await user.click(await screen.findByRole("button", { name: /trust this file/i }));
  await waitFor(() => expect(screen.getByTestId("inrepo-status").getAttribute("data-state")).toBe("trusted"));
  expect(approvals).toEqual([{ cwd: "/repo", hash: "reviewed-hash" }]);
  expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("local-draft");
});

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
    expect(shownReasons()).toEqual(["no-project"]);
  });
});

describe("3-state loaded contract", () => {
  test("loading, then ready: fetches schema + project layer + global layer (global read-only, for hints)", async () => {
    setQueryCwd("/repo");
    const fake = connectFakeClient();
    const calls: string[] = [];
    fake.on("evener/launch/schema", () => {
      calls.push("schema");
      return SCHEMA;
    });
    fake.on("evener/launch/getLayer", (params) => {
      calls.push(`getLayer:${params.layer}`);
      return params.layer === "project" ? PROJECT_LAYER : GLOBAL_LAYER;
    });
    render(<ProjectSection sectionId="project" />);
    expect(screen.getByRole("status", { name: "Loading project launch settings" })).toBeTruthy();
    await screen.findByLabelText("Agent");
    expect(calls).toEqual(["schema", "getLayer:project", "getLayer:global"]);
    // The global layer is read-only context for the "default: X" hint, never itself editable here.
    expect(screen.getByText("default: evener")).toBeTruthy();
  });

  test("error is a distinct third state (not '2-state, stays loading forever')", async () => {
    setQueryCwd("/repo");
    const fake = connectFakeClient();
    fake.on("evener/launch/schema", () => {
      throw new Error("boom");
    });
    render(<ProjectSection sectionId="project" />);
    await waitFor(() => expect(shownReasons()).toContain("load-failed"));
    // error is converted via friendlyErrorMessage: the raw string never appears
    expect(screen.queryByText(/boom/)).toBeNull();
  });

  test("no diagnostics panel anywhere on this page", async () => {
    setQueryCwd("/repo");
    const fake = connectFakeClient();
    fake.on("evener/launch/schema", () => SCHEMA);
    fake.on("evener/launch/getLayer", (params) => (params.layer === "project" ? PROJECT_LAYER : GLOBAL_LAYER));
    render(<ProjectSection sectionId="project" />);
    await screen.findByLabelText("Agent");
    expect(screen.queryByTestId("diagnostics")).toBeNull();
  });
});

describe("save", () => {
  test("repository inspection failure leaves local overrides editable and saveable", async () => {
    setQueryCwd("/repo");
    const fake = connectFakeClient();
    fake.on("evener/launch/schema", () => SCHEMA);
    fake.on("evener/launch/getLayer", () => ({}));
    fake.on("evener/launch/resolve", () => {
      throw new Error("inspection unavailable");
    });
    const saved: unknown[] = [];
    fake.on("evener/launch/setLayer", (params) => {
      saved.push(params);
      return { effective: {}, layers: {}, provenance: {} };
    });
    render(<ProjectSection sectionId="project" />);
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Agent"), "local-agent");
    await waitFor(() => expect(screen.getByTestId("inrepo-status").getAttribute("data-state")).toBe("error"));
    await user.click(screen.getByRole("button", { name: "Save launch defaults" }));
    await waitFor(() => expect(saved).toHaveLength(1));
    expect(saved[0]).toMatchObject({ cwd: "/repo", layer: "project", config: { agent: "local-agent" } });
  });

  test("saves to the project layer (not global) and toasts the project-specific success message", async () => {
    setQueryCwd("/repo");
    const fake = connectFakeClient();
    fake.on("evener/launch/schema", () => SCHEMA);
    fake.on("evener/launch/getLayer", (params) => (params.layer === "project" ? PROJECT_LAYER : GLOBAL_LAYER));
    fake.on("evener/launch/setLayer", (params) => {
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
    await waitFor(() => expect(screen.getByTestId("launch-config-status").getAttribute("data-state")).toBe("saved"));
    expect(getToasts().at(-1)?.kind).toBe("success");
  });
});

describe("resolved-default labels", () => {
  // A boolean field is the readable case: on the project layer its unset
  // marker is "(use global default)", which the resolve's effective layer
  // turns into "true (use global default)".
  const BOOL_SCHEMA: LaunchOptionSchemaResponse = {
    options: [
      {
        field: "sandbox_net",
        wireField: "sandboxNet",
        label: "Sandbox network egress",
        group: "Sandbox",
        kind: "boolean",
        perLaunch: true,
        defaultableLayers: ["global", "project"],
      },
    ],
  };

  function boolOptionLabels(): (string | null)[] {
    const select = screen.getByLabelText("Sandbox network egress") as HTMLSelectElement;
    return Array.from(select.options).map((o) => o.textContent);
  }

  test("an unset field names the effective layer's value once resolve(cwd) lands", async () => {
    setQueryCwd("/repo");
    const fake = connectFakeClient();
    fake.on("evener/launch/schema", () => BOOL_SCHEMA);
    fake.on("evener/launch/getLayer", () => ({}));
    fake.on("evener/launch/resolve", (params) => {
      expect(params.cwd).toBe("/repo");
      return { effective: { sandboxNet: true }, layers: {}, provenance: {} };
    });
    render(<ProjectSection sectionId="project" />);

    await screen.findByLabelText("Sandbox network egress");
    await waitFor(() => expect(boolOptionLabels()).toEqual(["true (use global default)", "true", "false"]));
  });

  test("a failed resolve leaves the plain (use global default) marker", async () => {
    setQueryCwd("/repo");
    const fake = connectFakeClient();
    fake.on("evener/launch/schema", () => BOOL_SCHEMA);
    fake.on("evener/launch/getLayer", () => ({}));
    fake.on("evener/launch/resolve", () => {
      throw new Error("resolve failed");
    });
    render(<ProjectSection sectionId="project" />);

    await screen.findByLabelText("Sandbox network egress");
    expect(boolOptionLabels()).toEqual(["(use global default)", "true", "false"]);
  });

  test("saving refreshes the labels from setLayer's own returned resolved config", async () => {
    setQueryCwd("/repo");
    const fake = connectFakeClient();
    fake.on("evener/launch/schema", () => BOOL_SCHEMA);
    fake.on("evener/launch/getLayer", () => ({}));
    fake.on("evener/launch/resolve", () => ({ effective: { sandboxNet: true }, layers: {}, provenance: {} }));
    fake.on("evener/launch/setLayer", () => ({ effective: { sandboxNet: false }, layers: {}, provenance: {} }));
    render(<ProjectSection sectionId="project" />);

    await screen.findByLabelText("Sandbox network egress");
    await waitFor(() => expect(boolOptionLabels()).toEqual(["true (use global default)", "true", "false"]));

    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Save launch defaults" }));
    await waitFor(() => expect(boolOptionLabels()).toEqual(["false (use global default)", "true", "false"]));
  });
});
