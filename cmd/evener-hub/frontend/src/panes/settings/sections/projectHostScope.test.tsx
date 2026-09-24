import type { HostRow, LaunchOptionSchemaResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test } from "vitest";
import { connectionStore } from "../../../stores/connection";
import { hostsStore } from "../../../stores/hosts";
import { resetLaunchConfigHostStoresForTests, resetLaunchConfigStoreForTests } from "../../../stores/launchConfig";
import { resetSettingsHostForTests, setSettingsHost } from "../../../stores/settingsHost";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import { ProjectHostScope } from "./project";

// The Per-project launch overrides surface's host scope (component 07b): the
// shared HostPicker over the selected host, whose own project layer both shows
// and changes from here. The pane is reached at /settings/project?cwd=<dir> -
// the cwd stays in the query string, the host comes from the settings route
// (the frame's own selection). Local is today's plain-call section; a remote
// selection routes every read and write through evener/host/request and never
// touches the controller's launch config.

function hostRow(overrides: Partial<HostRow> & Pick<HostRow, "name">): HostRow {
  return { origin: "sidecar", attached: false, midAttach: false, removed: false, ...overrides };
}

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

/** A schema with one plain text option and one path option, so a save exercises
 * both the layer write and the path validation the pane hands the form. */
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
    {
      field: "systemPromptPath",
      wireField: "systemPromptPath",
      label: "System prompt file",
      group: "Prompt",
      kind: "path",
      pathKind: "file",
      perLaunch: true,
      defaultableLayers: ["global", "project"],
    },
  ],
};

/** The calls this section makes about the launch config and the path helpers it
 * uses - the controller's own, i.e. NOT via the proxy. */
function controllerLaunchCalls(fake: FakeClient): string[] {
  return fake.calls
    .map((call) => call.method)
    .filter((method) => method.startsWith("evener/launch/") || method.startsWith("evener/path"));
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetLaunchConfigStoreForTests();
  resetLaunchConfigHostStoresForTests();
  resetToastStoreForTests();
  hostsStore.getState().resetForTests();
  resetSettingsHostForTests();
  setQueryCwd(null);
  window.history.pushState({}, "", "/settings/project");
  setSettingsHost("local");
});

afterEach(() => {
  cleanup();
  resetLaunchConfigHostStoresForTests();
  setQueryCwd(null);
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  window.history.pushState({}, "", "/");
});

test("with this hub selected the section issues the plain launch calls and never the proxy", async () => {
  setQueryCwd("/repo");
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/launch/schema", () => SCHEMA);
  fake.on("evener/launch/getLayer", (params) =>
    params.layer === "project" ? { systemPromptPath: "/repo/prompt.md" } : { agent: "evener" },
  );
  fake.on("evener/launch/resolve", () => ({ effective: { agent: "evener" }, layers: {}, provenance: {} }));
  fake.on("evener/path/validate", ({ path }) => ({ valid: true, path }));
  fake.on("evener/launch/setLayer", () => ({ effective: {}, layers: {}, provenance: {} }));
  fake.on("evener/host/request", () => {
    throw new Error("the local hub must never route through the proxy");
  });

  render(<ProjectHostScope sectionId="project" />);
  const user = userEvent.setup();
  await screen.findByLabelText("Agent");
  await user.click(screen.getByRole("button", { name: "Save launch defaults" }));

  await waitFor(() => expect(fake.calls.some((call) => call.method === "evener/launch/setLayer")).toBe(true));
  expect(fake.calls.some((call) => call.method === "evener/path/validate")).toBe(true);
  expect(fake.calls.some((call) => call.method === "evener/host/request")).toBe(false);
});

test("selecting a remote host shows THAT host's project layer, never the controller's", async () => {
  setQueryCwd("/repo");
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  // The controller's own handlers are booby-trapped: reaching any of them while
  // beta is selected is a host-scoping bug.
  fake.on("evener/launch/schema", () => {
    throw new Error("a remote selection must not read this hub's schema");
  });
  fake.on("evener/launch/getLayer", () => {
    throw new Error("a remote selection must not read this hub's layer");
  });
  fake.on("evener/launch/resolve", () => {
    throw new Error("a remote selection must not resolve against this hub");
  });
  fake.on("evener/host/request", (params) => {
    const forwarded = params as { host: string; method: string; params: { layer?: string } };
    expect(forwarded.host).toBe("beta");
    if (forwarded.method === "evener/launch/schema") return SCHEMA as never;
    if (forwarded.method === "evener/launch/getLayer")
      return (forwarded.params.layer === "project" ? {} : { agent: "beta-agent" }) as never;
    if (forwarded.method === "evener/launch/resolve")
      return { effective: { agent: "beta-agent" }, layers: {}, provenance: {} } as never;
    throw new Error(`unexpected forwarded method ${forwarded.method}`);
  });

  setSettingsHost("beta");
  render(<ProjectHostScope sectionId="project" />);

  await screen.findByText("default: beta-agent");
  expect(controllerLaunchCalls(fake)).toEqual([]);
});

test("a remote save writes that host's project layer through the proxy, never this hub's", async () => {
  setQueryCwd("/repo");
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/launch/setLayer", () => {
    throw new Error("a remote selection must not write this hub's layer");
  });
  fake.on("evener/path/validate", () => {
    throw new Error("a remote selection must not validate a path on this hub");
  });
  fake.on("evener/host/request", (params) => {
    const forwarded = params as { method: string; params: { layer?: string } };
    if (forwarded.method === "evener/launch/schema") return SCHEMA as never;
    if (forwarded.method === "evener/launch/getLayer")
      return (forwarded.params.layer === "project" ? { systemPromptPath: "/beta/prompt.md" } : {}) as never;
    if (forwarded.method === "evener/launch/resolve") return { effective: {}, layers: {}, provenance: {} } as never;
    if (forwarded.method === "evener/path/validate") return { valid: true, path: "/beta/prompt.md" } as never;
    if (forwarded.method === "evener/launch/setLayer") return { effective: {}, layers: {}, provenance: {} } as never;
    throw new Error(`unexpected forwarded method ${forwarded.method}`);
  });

  setSettingsHost("beta");
  render(<ProjectHostScope sectionId="project" />);
  const user = userEvent.setup();
  await screen.findByLabelText("Agent");
  await user.click(screen.getByRole("button", { name: "Save launch defaults" }));

  await waitFor(() =>
    expect(
      fake.calls.some(
        (call) =>
          call.method === "evener/host/request" &&
          (call.params as { method: string }).method === "evener/launch/setLayer",
      ),
    ).toBe(true),
  );
  expect(fake.calls.some((call) => call.method === "evener/launch/setLayer")).toBe(false);
  expect(fake.calls.some((call) => call.method === "evener/path/validate")).toBe(false);
});

test("browsing a path field on a remote host completes against THAT host, never the controller", async () => {
  setQueryCwd("/repo");
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  // The form's browse helpers must follow the selected host's store, not the
  // controller singleton: reaching the controller's own path handlers here is
  // the bug this test pins.
  fake.on("evener/path/validate", () => {
    throw new Error("a remote selection must not validate a path on this hub");
  });
  fake.on("evener/paths/complete", () => {
    throw new Error("a remote selection must not complete a path on this hub");
  });
  fake.on("evener/host/request", (params) => {
    const forwarded = params as { method: string; params: { layer?: string } };
    if (forwarded.method === "evener/launch/schema") return SCHEMA as never;
    if (forwarded.method === "evener/launch/getLayer")
      return (forwarded.params.layer === "project" ? { systemPromptPath: "/beta/prompt.md" } : {}) as never;
    if (forwarded.method === "evener/launch/resolve") return { effective: {}, layers: {}, provenance: {} } as never;
    if (forwarded.method === "evener/path/validate") return { valid: true, path: "/beta" } as never;
    if (forwarded.method === "evener/paths/complete") return { data: [] } as never;
    throw new Error(`unexpected forwarded method ${forwarded.method}`);
  });

  setSettingsHost("beta");
  render(<ProjectHostScope sectionId="project" />);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: /system prompt file/i }));

  await waitFor(() =>
    expect(
      fake.calls.some(
        (call) =>
          call.method === "evener/host/request" &&
          (call.params as { method: string }).method === "evener/paths/complete",
      ),
    ).toBe(true),
  );
  expect(fake.calls.some((call) => call.method === "evener/path/validate")).toBe(false);
  expect(fake.calls.some((call) => call.method === "evener/paths/complete")).toBe(false);
});

// The pane is reached as /settings/project?cwd=<dir>: the SECTION is the
// project. Switching hosts re-scopes that same project to the new host, so the
// host change has to carry the route's query - dropping it leaves the pane with
// no cwd at all, rendering "No project selected" over the project the user was
// editing.
test("switching hosts keeps the route's ?cwd=, re-scoping the same project to the new host", async () => {
  setQueryCwd("/repo");
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/launch/schema", () => SCHEMA);
  fake.on("evener/launch/getLayer", (params) =>
    params.layer === "project" ? { systemPromptPath: "/repo/prompt.md" } : { agent: "evener" },
  );
  fake.on("evener/launch/resolve", () => ({ effective: { agent: "evener" }, layers: {}, provenance: {} }));
  fake.on("evener/host/request", (params) => {
    const forwarded = params as { method: string; params: { layer?: string } };
    if (forwarded.method === "evener/launch/schema") return SCHEMA as never;
    if (forwarded.method === "evener/launch/getLayer")
      return (forwarded.params.layer === "project" ? {} : { agent: "beta-agent" }) as never;
    if (forwarded.method === "evener/launch/resolve")
      return { effective: { agent: "beta-agent" }, layers: {}, provenance: {} } as never;
    throw new Error(`unexpected forwarded method ${forwarded.method}`);
  });

  render(<ProjectHostScope sectionId="project" />);
  const user = userEvent.setup();
  await screen.findByLabelText("Agent");
  expect(screen.getByText("/repo")).toBeTruthy();

  const select = screen.getByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");

  expect(new URL(window.location.href).searchParams.get("cwd")).toBe("/repo");
  expect(new URL(window.location.href).searchParams.get("host")).toBe("beta");
  expect(screen.queryByText(/No project selected/)).toBeNull();
  // And the same project's layer now comes from beta.
  await screen.findByText("default: beta-agent");
  expect(screen.getByText("/repo")).toBeTruthy();
});
