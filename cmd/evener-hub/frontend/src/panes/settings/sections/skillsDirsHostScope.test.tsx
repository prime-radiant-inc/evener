import type { HostRow } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test } from "vitest";
import { connectionStore } from "../../../stores/connection";
import { resetExtensionsStoreForTests } from "../../../stores/extensions";
import { hostsStore } from "../../../stores/hosts";
import { resetSettingsHostForTests, setSettingsHost } from "../../../stores/settingsHost";
import { Toast } from "../../../widgets";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import { SkillsDirsHostScope } from "./skillsDirs";

// The Skills (dirs) settings surface's host scope (component 07b): a remote
// selection shows that host's own skill directories and writes them back
// through evener/host/request, never this hub's launch layer.

function hostRow(overrides: Partial<HostRow> & Pick<HostRow, "name">): HostRow {
  return { origin: "sidecar", attached: false, midAttach: false, removed: false, ...overrides };
}

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetExtensionsStoreForTests();
  resetToastStoreForTests();
  hostsStore.getState().resetForTests();
  resetSettingsHostForTests();
  window.history.pushState({}, "", "/settings/skills");
  setSettingsHost("local");
});

afterEach(() => {
  cleanup();
  resetExtensionsStoreForTests();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  window.history.pushState({}, "", "/");
});

test("with this hub selected the section issues the plain launch calls and never the proxy", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/launch/getLayer", () => ({ skillsDirs: ["/controller/skills"] }));
  fake.on("evener/host/request", () => {
    throw new Error("the local hub must never route through the proxy");
  });

  render(<SkillsDirsHostScope />);

  expect(await screen.findByText("/controller/skills")).toBeTruthy();
  expect(fake.calls.some((call) => call.method === "evener/host/request")).toBe(false);
});

test("a remote selection shows that host's own skill directories, never the controller's", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/launch/getLayer", () => {
    throw new Error("a remote selection must not read this hub's launch layer");
  });
  fake.on("evener/host/request", (params) => {
    const forwarded = params as { host: string; method: string };
    expect(forwarded.host).toBe("beta");
    if (forwarded.method === "evener/launch/getLayer") return { skillsDirs: ["/beta/skills"] } as never;
    throw new Error(`unexpected forwarded method ${forwarded.method}`);
  });

  setSettingsHost("beta");
  render(<SkillsDirsHostScope />);

  expect(await screen.findByText("/beta/skills")).toBeTruthy();
  expect(fake.calls.filter((call) => call.method.startsWith("evener/launch/"))).toEqual([]);
});

test("adding a skill directory writes that host's layer through the proxy, never this hub's", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/launch/setLayer", () => {
    throw new Error("a remote selection must not write this hub's launch layer");
  });
  fake.on("evener/host/request", (params) => {
    const forwarded = params as { method: string; params: { config?: { skillsDirs?: string[] } } };
    if (forwarded.method === "evener/launch/getLayer") return { skillsDirs: [] } as never;
    if (forwarded.method === "evener/path/validate") return { path: "/opt/new-skill", valid: true } as never;
    if (forwarded.method === "evener/launch/setLayer") return { effective: {}, layers: {}, provenance: {} } as never;
    throw new Error(`unexpected forwarded method ${forwarded.method}`);
  });

  setSettingsHost("beta");
  render(
    <>
      <Toast />
      <SkillsDirsHostScope />
    </>,
  );

  expect(await screen.findByText("No skill directories. Add one below.")).toBeTruthy();
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: /^New directory:/ }));
  const input = await screen.findByRole("textbox", { name: "Path" });
  await user.clear(input);
  await user.type(input, "/opt/new-skill");
  await user.keyboard("{Enter}");
  await user.click(screen.getByRole("button", { name: "Use this folder" }));
  await user.click(screen.getByRole("button", { name: "Add" }));

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
});
