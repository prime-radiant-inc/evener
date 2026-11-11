import {
  type PluginComponents,
  type PluginLaunchCandidate,
  SCOPED_HOST_READ_DEBOUNCE_MS,
  WireError,
} from "@evener/appwire-client";
import { deferRequest, FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, render, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { useTestDrivenFakeTimers } from "../../testing/fakeTimers";
import { LauncherPluginDetails } from "./LauncherPluginDetails";

// A directory-source candidate: no marketplace, so the preview resolved it
// by path - the same path the body's read must target.
const DIRECTORY_CANDIDATE: PluginLaunchCandidate = {
  name: "beta",
  version: "0.9.0",
  description: "Beta helpers",
  source: "directory",
  path: "/tmp/beta",
  selected: false,
  skillCount: 0,
  agentCount: 0,
  commandCount: 0,
  hookCount: 1,
  mcpCount: 1,
};

const COMPONENTS: PluginComponents = { skills: ["run"], warnings: ["launcher-warning-sentinel"] };

function componentsClient(): FakeClient {
  const fake = new FakeClient();
  fake.on("evener/plugin/components", (params) => ({
    results: params.targets.map((target) => ({ ...target, components: COMPONENTS })),
  }));
  return fake;
}

function renderDetails(overrides: Partial<React.ComponentProps<typeof LauncherPluginDetails>> = {}) {
  const onToggleSelected = vi.fn();
  render(
    <LauncherPluginDetails
      candidate={DIRECTORY_CANDIDATE}
      selected={false}
      onCollapse={() => {}}
      onToggleSelected={onToggleSelected}
      client={componentsClient()}
      {...overrides}
    />,
  );
  return { onToggleSelected };
}

/** The detail body element the panel mounts under a row. */
function body(): HTMLElement {
  const el = document.querySelector('[data-kind="plugin-detail"]');
  if (!(el instanceof HTMLElement)) throw new Error("no detail body renders");
  return el;
}

async function settleRead(): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(SCOPED_HOST_READ_DEBOUNCE_MS);
    await Promise.resolve();
  });
}

afterEach(() => {
  vi.useRealTimers();
});

test("a directory-source candidate reads by its path and renders the answer as Contents", async () => {
  useTestDrivenFakeTimers();
  const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
  const fake = new FakeClient();
  fake.on("evener/plugin/components", (params) => {
    expect(params).toEqual({ targets: [{ path: "/tmp/beta" }] });
    return { results: [{ path: "/tmp/beta", components: COMPONENTS }] };
  });
  const { onToggleSelected } = renderDetails({ client: fake });
  await settleRead();
  const detail = body();
  // The one-target batch named the candidate by path alone.
  expect(fake.calls).toEqual([{ method: "evener/plugin/components", params: { targets: [{ path: "/tmp/beta" }] } }]);
  expect(detail.querySelector('[data-kind="plugin-contents"]')).toBeTruthy();
  expect(detail.querySelector('[data-kind="skills"]')).toBeTruthy();
  expect(within(detail).getByRole("alert").getAttribute("data-reason")).toBe("load-warnings");
  expect(within(detail).getByRole("alert").textContent).toContain(COMPONENTS.warnings?.[0]);
  expect(detail.querySelector('[data-reason="manifest-only"]')).toBeNull();
  // Metadata: Version when the candidate carries one, no Marketplace row for
  // a directory source, and the candidate's source kind.
  expect(within(detail).getByText("v0.9.0")).toBeTruthy();
  expect(detail.querySelector('[data-kind="plugin-marketplace"]')).toBeNull();
  expect(detail.getAttribute("data-mode")).toBe("launcher");
  expect(within(detail).getByText("directory")).toBeTruthy();
  // Launcher mode has no editor: one selection switch, no action buttons.
  const included = within(detail).getByRole("switch", { name: "Included in this session" });
  expect(included.getAttribute("aria-checked")).toBe("false");
  await user.click(included);
  expect(onToggleSelected).toHaveBeenCalledWith(true);
  expect(within(detail).queryByRole("button", { name: "Install" })).toBeNull();
  expect(within(detail).queryByRole("button", { name: "Remove" })).toBeNull();
});

test("an installed winner reads its resolved path when its manifest name differs from its catalog name", async () => {
  useTestDrivenFakeTimers();
  const path = "/plugins/cache/m/zzz-enabled/1.0.0";
  const fake = new FakeClient();
  fake.on("evener/plugin/components", (params) => ({
    results: params.targets.map((target) =>
      target.path === path
        ? { ...target, components: { commands: ["winner-command"] } }
        : { ...target, error: "unresolved-pair-sentinel" },
    ),
  }));
  renderDetails({
    client: fake,
    candidate: { ...DIRECTORY_CANDIDATE, name: "shared", source: "installed", marketplace: "m", path },
  });
  await settleRead();

  expect(fake.calls).toEqual([{ method: "evener/plugin/components", params: { targets: [{ path }] } }]);
  expect(body().querySelector('[data-kind="commands"]')?.textContent).toContain("winner-command");
  expect(body().querySelector('[data-reason="plugin-broken"]')).toBeNull();
});

test("an erroring read degrades with the quiet notice where Contents would be, and retry re-reads", async () => {
  useTestDrivenFakeTimers();
  const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
  const fake = new FakeClient();
  fake.on("evener/plugin/components", () => {
    throw new WireError("method not found: evener/plugin/components", -32601);
  });
  renderDetails({ client: fake });
  await settleRead();
  const detail = body();
  // The read failed for the whole method, not for this plugin: a quiet
  // degrade notice where Contents would be, never the failure alert.
  const notice = within(detail).getByRole("status");
  expect(notice.getAttribute("data-reason")).toBe("components-degraded");
  expect(within(detail).queryByRole("alert")).toBeNull();
  expect(detail.querySelector('[data-kind="plugin-contents"]')).toBeNull();
  expect(document.querySelector('[data-reason="manifest-only"]')).toBeNull();

  // The hub learns the method: the retry's fresh read now answers.
  fake.on("evener/plugin/components", () => ({ results: [{ path: "/tmp/beta", components: COMPONENTS }] }));
  await user.click(within(detail).getByRole("button", { name: "Retry" }));
  await settleRead();
  expect(fake.calls.filter((call) => call.method === "evener/plugin/components")).toHaveLength(2);
  expect(detail.querySelector('[data-kind="plugin-contents"]')).toBeTruthy();
  expect(within(detail).queryByRole("status")).toBeNull();
});

test("keyboard Retry keeps focus inside the details body while loading", async () => {
  useTestDrivenFakeTimers();
  const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
  const fake = new FakeClient();
  fake.on("evener/plugin/components", () => {
    throw new WireError("unavailable-method-sentinel", -32601);
  });
  render(
    <LauncherPluginDetails
      candidate={DIRECTORY_CANDIDATE}
      selected={false}
      onCollapse={() => {}}
      onToggleSelected={() => {}}
      client={fake}
    />,
  );
  await settleRead();
  const detail = body();
  const retry = within(within(detail).getByRole("status")).getByRole("button");
  for (let i = 0; i < 5 && document.activeElement !== retry; i++) await user.tab();
  expect(document.activeElement).toBe(retry);
  const release = deferRequest<{ results: [] }>(fake, "evener/plugin/components");
  await user.keyboard("{Enter}");
  await settleRead();
  expect(fake.calls.filter((call) => call.method === "evener/plugin/components")).toHaveLength(2);
  expect(retry.isConnected).toBe(false);
  // The unmounted Retry rehomes into the body, not to the document body.
  expect(detail.contains(document.activeElement)).toBe(true);
  await act(async () => release({ results: [] }));
});

test("a remove-only inclusion toggle rehomes focus when its own activation disables it", async () => {
  useTestDrivenFakeTimers();
  const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
  const fake = componentsClient();
  function Harness() {
    const [selected, setSelected] = useState(true);
    return (
      <LauncherPluginDetails
        candidate={DIRECTORY_CANDIDATE}
        selected={selected}
        selectionDisabled={!selected}
        onCollapse={() => {}}
        onToggleSelected={setSelected}
        client={fake}
      />
    );
  }
  render(<Harness />);
  await settleRead();
  const detail = body();
  const included = within(detail).getByRole("switch");
  for (let i = 0; i < 5 && document.activeElement !== included; i++) await user.tab();
  expect(document.activeElement).toBe(included);
  await user.keyboard("{Enter}");
  expect(included.getAttribute("aria-checked")).toBe("false");
  expect((included as HTMLButtonElement).disabled).toBe(true);
  expect(document.activeElement).not.toBe(included);
  expect(detail.contains(document.activeElement)).toBe(true);
});

test("a plugin whose own load fails shows the failure where Contents would be, never manifest-only", async () => {
  vi.useFakeTimers();
  const fake = new FakeClient();
  fake.on("evener/plugin/components", () => ({
    results: [{ path: "/tmp/beta", error: "target-load-error-sentinel" }],
  }));
  renderDetails({ client: fake });
  await settleRead();
  const detail = body();
  // The body carries the plugin's own load failure: the failure alert, with
  // no retry (the plugin is the problem, not the read) and no Contents.
  const failure = within(detail).getByRole("alert");
  expect(failure.getAttribute("data-reason")).toBe("plugin-broken");
  expect(within(detail).queryByRole("status")).toBeNull();
  expect(within(detail).queryByRole("button", { name: "Retry" })).toBeNull();
  expect(detail.querySelector('[data-kind="plugin-contents"]')).toBeNull();
  expect(document.querySelector('[data-reason="manifest-only"]')).toBeNull();
});
