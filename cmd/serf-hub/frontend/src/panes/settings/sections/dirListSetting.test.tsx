import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import { connectionStore } from "../../../stores/connection";
import { resetExtensionsStoreForTests } from "../../../stores/extensions";
import { getToasts, resetToastStoreForTests } from "../../../widgets/toast/store";
import { DirListSetting, PathListEditor } from "./dirListSetting";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetExtensionsStoreForTests();
  resetToastStoreForTests();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

/** The add row's picker trigger. Its accessible name is the FormRow's own
 * visible label ("New directory"), which labels the trigger the way it labelled
 * the text box before it. */
function picker(): HTMLElement {
  return screen.getByRole("button", { name: "New directory" });
}

/** Opens the add row's path picker and types a literal path, committing it
 * with Enter - which lands it in the add field and closes the panel, exactly
 * as clicking a row would. user.keyboard (not user.type) because the panel's
 * input already holds focus with its pre-filled value selected. */
async function typePath(user: ReturnType<typeof userEvent.setup>, path: string) {
  await user.click(picker());
  await screen.findByRole("combobox", { name: "Path" });
  await user.keyboard(path);
  await user.keyboard("{Enter}");
}

describe("PathListEditor", () => {
  function baseProps(overrides: Partial<Parameters<typeof PathListEditor>[0]> = {}) {
    return {
      label: "Plugin directories",
      addLabel: "New directory",
      kind: "dir" as const,
      items: ["/opt/plugins", "/home/user/.serf/plugins"],
      onAdd: vi.fn(async () => ({ ok: true }) as const),
      onRemove: vi.fn(),
      complete: vi.fn(async () => []),
      emptyMessage: "No plugin directories. Add one below.",
      removeConfirmTitle: "Remove directory",
      removeConfirmBody: (path: string) => `Remove "${path}" from plugin directories?`,
      ...overrides,
    };
  }

  test("renders the list with an accessible name and one row per item", () => {
    render(<PathListEditor {...baseProps()} />);
    expect(screen.getByRole("list", { name: "Plugin directories" })).toBeTruthy();
    expect(screen.getByText("/opt/plugins")).toBeTruthy();
    expect(screen.getByText("/home/user/.serf/plugins")).toBeTruthy();
  });

  test("renders the empty message and no rows when items is empty", () => {
    render(<PathListEditor {...baseProps({ items: [] })} />);
    expect(screen.getByText("No plugin directories. Add one below.")).toBeTruthy();
  });

  test("clicking a row's remove button opens a confirm dialog instead of calling onRemove immediately", async () => {
    const user = userEvent.setup();
    const onRemove = vi.fn();
    render(<PathListEditor {...baseProps({ onRemove })} />);
    await user.click(screen.getByRole("button", { name: "Remove /opt/plugins" }));
    expect(screen.getByRole("dialog", { name: "Remove directory" })).toBeTruthy();
    expect(screen.getByText('Remove "/opt/plugins" from plugin directories?')).toBeTruthy();
    expect(onRemove).not.toHaveBeenCalled();
  });

  test("confirming the dialog calls onRemove with that item and closes the dialog", async () => {
    const user = userEvent.setup();
    const onRemove = vi.fn();
    render(<PathListEditor {...baseProps({ onRemove })} />);
    await user.click(screen.getByRole("button", { name: "Remove /opt/plugins" }));
    await user.click(screen.getByRole("button", { name: "Remove" }));
    expect(onRemove).toHaveBeenCalledWith("/opt/plugins");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  test("cancelling the dialog does not call onRemove", async () => {
    const user = userEvent.setup();
    const onRemove = vi.fn();
    render(<PathListEditor {...baseProps({ onRemove })} />);
    await user.click(screen.getByRole("button", { name: "Remove /opt/plugins" }));
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onRemove).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  test("the confirm dialog's buttons disable while onRemove is in flight", async () => {
    const user = userEvent.setup();
    let resolveRemove: () => void = () => {};
    const onRemove = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          resolveRemove = resolve;
        }),
    );
    render(<PathListEditor {...baseProps({ onRemove })} />);
    await user.click(screen.getByRole("button", { name: "Remove /opt/plugins" }));
    const dialog = screen.getByRole("dialog", { name: "Remove directory" });
    await user.click(within(dialog).getByRole("button", { name: "Remove" }));

    expect((within(dialog).getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(true);
    expect((within(dialog).getByRole("button", { name: "Cancel" }) as HTMLButtonElement).disabled).toBe(true);

    resolveRemove();
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  test("the Add button is disabled while the draft is blank", () => {
    render(<PathListEditor {...baseProps()} />);
    expect((screen.getByRole("button", { name: "Add" }) as HTMLButtonElement).disabled).toBe(true);
  });

  test("picking a path and clicking Add calls onAdd with the trimmed value; ok:true clears the draft", async () => {
    const user = userEvent.setup();
    const onAdd = vi.fn(async () => ({ ok: true }) as const);
    render(<PathListEditor {...baseProps({ onAdd })} />);
    await typePath(user, "/opt/new  ");
    await user.click(screen.getByRole("button", { name: "Add" }));
    expect(onAdd).toHaveBeenCalledWith("/opt/new");
    await waitFor(() => expect(picker().textContent).toMatch(/absolute\/path/));
  });

  test("ok:false shows the inline error and keeps the draft", async () => {
    const user = userEvent.setup();
    const onAdd = vi.fn(async () => ({ ok: false, error: "path does not exist" }) as const);
    render(<PathListEditor {...baseProps({ onAdd })} />);
    await typePath(user, "/nope");
    await user.click(screen.getByRole("button", { name: "Add" }));
    expect(await screen.findByText("path does not exist")).toBeTruthy();
    expect(picker().textContent).toMatch(/\/nope/);
  });

  test("kind:dir browses directories only", async () => {
    const user = userEvent.setup();
    const complete = vi.fn(async () => ["/opt/plugins/serf-lint"]);
    render(<PathListEditor {...baseProps({ kind: "dir", complete })} />);
    await user.click(picker());
    expect(await screen.findByRole("option", { name: /serf-lint/ })).toBeTruthy();
    expect(complete).toHaveBeenCalledWith("", false);
  });

  // The MCP config-files list names files, not directories: its picker has to
  // list them or the final filename is still hand-typed.
  test("kind:file browses files too, and a file row lands in the add field", async () => {
    const user = userEvent.setup();
    const complete = vi.fn(async () => ["/etc/mcp/", "/etc/mcp.json"]);
    render(<PathListEditor {...baseProps({ kind: "file", complete })} />);
    await user.click(picker());
    await user.click(await screen.findByRole("option", { name: /mcp\.json/ }));
    expect(complete).toHaveBeenCalledWith("", true);
    await waitFor(() => expect(picker().textContent).toMatch(/\/etc\/mcp\.json/));
  });
});

describe("DirListSetting", () => {
  test("shows a loading state before the launch layer resolves", async () => {
    const fake = connectFakeClient();
    fake.on("serf/launch/getLayer", () => new Promise(() => {}));
    render(<DirListSetting wireField="pluginDirs" label="Plugin directories" copy="Directories serf scans." />);
    expect(screen.getByRole("status", { name: "Loading" })).toBeTruthy();
  });

  test("renders the label as a heading and the copy as help text", async () => {
    const fake = connectFakeClient();
    fake.on("serf/launch/getLayer", () => ({ pluginDirs: [] }));
    render(
      <DirListSetting
        wireField="pluginDirs"
        label="Plugin directories"
        copy="Directories serf scans for plugins at launch."
      />,
    );
    expect(await screen.findByText("Plugin directories")).toBeTruthy();
    expect(screen.getByText("Directories serf scans for plugins at launch.")).toBeTruthy();
  });

  test("renders the wireField's entries from the fetched launch layer", async () => {
    const fake = connectFakeClient();
    fake.on("serf/launch/getLayer", (params) => {
      expect(params).toEqual({ cwd: "/", layer: "global" });
      return { pluginDirs: ["/opt/plugins"], skillsDirs: ["/opt/skills"] };
    });
    render(<DirListSetting wireField="pluginDirs" label="Plugin directories" copy="c" />);
    expect(await screen.findByText("/opt/plugins")).toBeTruthy();
    expect(screen.queryByText("/opt/skills")).toBeNull();
  });

  test("shows a count header that pluralizes N entries", async () => {
    const fake = connectFakeClient();
    fake.on("serf/launch/getLayer", () => ({ pluginDirs: ["/opt/plugins", "/opt/more"] }));
    render(<DirListSetting wireField="pluginDirs" label="Plugin directories" copy="c" />);
    expect(await screen.findByText("2 entries")).toBeTruthy();
  });

  test("shows a singular count for exactly one entry", async () => {
    const fake = connectFakeClient();
    fake.on("serf/launch/getLayer", () => ({ pluginDirs: ["/opt/plugins"] }));
    render(<DirListSetting wireField="pluginDirs" label="Plugin directories" copy="c" />);
    expect(await screen.findByText("1 entry")).toBeTruthy();
  });

  test("shows a 0 entries count when the list is empty", async () => {
    const fake = connectFakeClient();
    fake.on("serf/launch/getLayer", () => ({ pluginDirs: [] }));
    render(<DirListSetting wireField="pluginDirs" label="Plugin directories" copy="c" />);
    expect(await screen.findByText("0 entries")).toBeTruthy();
  });

  test("does not show a count while the launch layer is still loading", () => {
    const fake = connectFakeClient();
    fake.on("serf/launch/getLayer", () => new Promise(() => {}));
    render(<DirListSetting wireField="pluginDirs" label="Plugin directories" copy="c" />);
    expect(screen.queryByText(/^\d+ entr(y|ies)$/)).toBeNull();
  });

  test("does not show a count when the launch layer fails to load", async () => {
    const fake = connectFakeClient();
    fake.on("serf/launch/getLayer", () => {
      throw new Error("boom");
    });
    render(<DirListSetting wireField="pluginDirs" label="Plugin directories" copy="c" />);
    await screen.findByText("Failed to load");
    expect(screen.queryByText(/^\d+ entr(y|ies)$/)).toBeNull();
  });

  test("shows a failed-to-load error when the launch layer fetch rejects", async () => {
    const fake = connectFakeClient();
    fake.on("serf/launch/getLayer", () => {
      throw new Error("network down");
    });
    render(<DirListSetting wireField="pluginDirs" label="Plugin directories" copy="c" />);
    expect(await screen.findByText("Failed to load")).toBeTruthy();
    expect(screen.getByText("network down")).toBeTruthy();
  });

  test("adding a valid path validates then saves the layer with the new entry appended", async () => {
    const user = userEvent.setup();
    const fake = connectFakeClient();
    fake.on("serf/launch/getLayer", () => ({ pluginDirs: ["/opt/plugins"] }));
    fake.on("serf/path/validate", (params) => {
      expect(params).toEqual({ path: "/opt/new", kind: "dir" });
      return { path: "/opt/new", valid: true };
    });
    fake.on("serf/launch/setLayer", (params) => {
      expect(params).toEqual({ cwd: "/", layer: "global", config: { pluginDirs: ["/opt/plugins", "/opt/new"] } });
      return { effective: {}, layers: {}, provenance: {} };
    });
    fake.on("serf/paths/complete", () => ({ data: [] }));
    render(<DirListSetting wireField="pluginDirs" label="Plugin directories" copy="c" />);
    await screen.findByText("/opt/plugins");
    await typePath(user, "/opt/new");
    await user.click(screen.getByRole("button", { name: "Add" }));
    expect(await screen.findByText("/opt/new")).toBeTruthy();
  });

  test("the add row's picker browses via serf/paths/complete, directories only", async () => {
    const user = userEvent.setup();
    const fake = connectFakeClient();
    fake.on("serf/launch/getLayer", () => ({ pluginDirs: [] }));
    fake.on("serf/paths/complete", (params) => {
      expect(params).toEqual({ prefix: "", includeFiles: false });
      return { data: ["/opt/plugins"] };
    });
    render(<DirListSetting wireField="pluginDirs" label="Plugin directories" copy="c" />);
    await screen.findByText("No plugin directories. Add one below.");
    await user.click(picker());
    expect(await screen.findByRole("option", { name: /plugins/ })).toBeTruthy();
  });

  test("adding an invalid path shows the server's error inline and never calls setLayer", async () => {
    const user = userEvent.setup();
    const fake = connectFakeClient();
    fake.on("serf/launch/getLayer", () => ({ pluginDirs: [] }));
    fake.on("serf/path/validate", () => ({ path: "/nope", valid: false, error: "path does not exist" }));
    const setLayer = vi.fn();
    fake.on("serf/launch/setLayer", setLayer);
    fake.on("serf/paths/complete", () => ({ data: [] }));
    render(<DirListSetting wireField="pluginDirs" label="Plugin directories" copy="c" />);
    await screen.findByText("No plugin directories. Add one below.");
    await typePath(user, "/nope");
    await user.click(screen.getByRole("button", { name: "Add" }));
    expect(await screen.findByText("path does not exist")).toBeTruthy();
    expect(setLayer).not.toHaveBeenCalled();
  });

  test("confirming a row removal saves the layer with that entry filtered out", async () => {
    const user = userEvent.setup();
    const fake = connectFakeClient();
    fake.on("serf/launch/getLayer", () => ({ pluginDirs: ["/opt/plugins", "/opt/keep"] }));
    fake.on("serf/launch/setLayer", (params) => {
      expect(params).toEqual({ cwd: "/", layer: "global", config: { pluginDirs: ["/opt/keep"] } });
      return { effective: {}, layers: {}, provenance: {} };
    });
    render(<DirListSetting wireField="pluginDirs" label="Plugin directories" copy="c" />);
    await screen.findByText("/opt/plugins");
    await user.click(screen.getByRole("button", { name: "Remove /opt/plugins" }));
    await user.click(screen.getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(screen.queryByText("/opt/plugins")).toBeNull());
    expect(screen.getByText("/opt/keep")).toBeTruthy();
  });

  test("a failed removal shows an error toast and leaves the entry in place", async () => {
    const user = userEvent.setup();
    const fake = connectFakeClient();
    fake.on("serf/launch/getLayer", () => ({ pluginDirs: ["/opt/plugins"] }));
    fake.on("serf/launch/setLayer", () => {
      throw new Error("disk full");
    });
    render(<DirListSetting wireField="pluginDirs" label="Plugin directories" copy="c" />);
    await screen.findByText("/opt/plugins");
    await user.click(screen.getByRole("button", { name: "Remove /opt/plugins" }));
    await user.click(screen.getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(getToasts().some((t) => t.kind === "error" && t.text.includes("disk full"))).toBe(true));
    expect(screen.getByText("/opt/plugins")).toBeTruthy();
  });
});
