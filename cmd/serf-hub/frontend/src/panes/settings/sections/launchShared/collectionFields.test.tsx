import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { LaunchOption } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { resetExtensionsStoreForTests } from "../../../../stores/extensions";
import * as catalogClientModule from "../../../../widgets/modelCatalog/catalogClient";
import { EnvMapField, McpServerListField, ModelListField, PathListField } from "./collectionFields";

// modelList adds come from the shared searchable ModelCatalog picker, which
// fetches /api/models; the wire loader is mocked so these stay hermetic.
//
// vi.spyOn, not vi.mock: see ModelField.test.tsx's own comment on this exact
// pattern - under a shared module registry (isolate:false), a vi.mock()
// factory registered here only replaces what THIS file's own import
// resolves to, not what an already-loaded importer calls internally.
// Spying on the real module's own export patches the one binding every
// importer actually shares, regardless of import order. No test in this
// file overrides the resolved value afterward, so the spy is installed
// fresh each time without keeping a reference to it.
beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetExtensionsStoreForTests();
  vi.spyOn(catalogClientModule, "fetchModelCatalog").mockResolvedValue({
    models: [
      { provider: "openai", model: "gpt-5-mini", displayName: "GPT-5 Mini" },
      { provider: "anthropic", model: "claude-haiku-4-5", displayName: "Claude Haiku 4.5" },
    ],
    recent: [],
    diagnostics: [],
  });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

/** Scripts serf/paths/complete off a prefix -> entries table, the way the
 * pathList add row's picker asks for it (the store passes the prefix through
 * verbatim, and directory entries carry a trailing slash once includeFiles is
 * on). Returns the recorded (prefix, includeFiles) pairs. */
function connectPathLister(table: Record<string, string[]>): { calls: Array<[string, boolean]> } {
  const fake = new FakeClient("ready");
  const calls: Array<[string, boolean]> = [];
  fake.on("serf/paths/complete", (params) => {
    calls.push([params.prefix, params.includeFiles === true]);
    return { data: table[params.prefix] ?? [] };
  });
  connectionStore.getState().connect(fake);
  return { calls };
}

/** Opens the modelList add row's picker and clicks one model by display name,
 * which lands its qualified id in the add field. */
async function pickModel(user: ReturnType<typeof userEvent.setup>, displayName: string) {
  await user.click(screen.getByRole("button", { name: /change model/i }));
  await user.click(await screen.findByText(displayName));
}

/** Opens the pathList add row's path picker and types a literal path, then
 * commits it with Enter - which lands it in CollectionEditor's own draft and
 * closes the panel, exactly like clicking a file row would. Typing (not
 * user.type) because the panel's input is already focused with its pre-filled
 * value selected. */
async function typePath(user: ReturnType<typeof userEvent.setup>, path: string) {
  await user.click(screen.getByRole("button", { name: /browse/i }));
  await screen.findByRole("combobox", { name: "Path" });
  await user.keyboard(path);
  await user.keyboard("{Enter}");
}

function pathListOption(overrides: Partial<LaunchOption> = {}): LaunchOption {
  return {
    field: "skills_dirs",
    wireField: "skillsDirs",
    label: "Skill directories",
    description: "Extra directories serf scans for skills.",
    group: "Resources",
    kind: "pathList",
    pathKind: "dir",
    perLaunch: true,
    ...overrides,
  };
}

describe("PathListField", () => {
  test("renders existing items and the section label/help", () => {
    render(
      <PathListField
        option={pathListOption()}
        items={["/opt/skills"]}
        onChange={() => {}}
        validatePath={async () => ({ path: "/opt/skills", valid: true })}
      />,
    );
    expect(screen.getAllByText("Skill directories").length).toBeGreaterThan(0);
    expect(screen.getByText("Extra directories serf scans for skills.")).toBeTruthy();
    expect(screen.getByText("/opt/skills")).toBeTruthy();
  });

  test("the add row is the shared path picker, not a hand-typed text box", () => {
    connectPathLister({});
    render(
      <PathListField
        option={pathListOption()}
        items={[]}
        onChange={() => {}}
        validatePath={async () => ({ path: "", valid: true })}
      />,
    );
    expect(screen.queryByPlaceholderText("/path/to/directory")).toBeNull();
    const browse = screen.getByRole("button", { name: /browse/i });
    expect(browse.textContent).toMatch(/\/path\/to\/directory/);
  });

  // LaunchConfigForm puts skillsDirs/pluginDirs/mcpConfigs in one "Resources"
  // group, so their three add triggers render on the same page. Named only by
  // their (identical, empty) value text they'd be indistinguishable.
  test("two sibling pathList add triggers have distinct accessible names", () => {
    connectPathLister({});
    render(
      <>
        <PathListField
          option={pathListOption()}
          items={[]}
          onChange={() => {}}
          validatePath={async () => ({ path: "", valid: true })}
        />
        <PathListField
          option={pathListOption({ field: "plugin_dirs", wireField: "pluginDirs", label: "Plugin directories" })}
          items={[]}
          onChange={() => {}}
          validatePath={async () => ({ path: "", valid: true })}
        />
      </>,
    );
    // Two exact-name lookups, each of which throws if it matches zero or more
    // than one button - so this fails both when the names collide and when
    // either one loses its field name.
    expect(screen.getByRole("button", { name: "Skill directories: /path/to/directory — browse" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Plugin directories: /path/to/directory — browse" })).toBeTruthy();
  });

  test("a dir pathKind browses directories only", async () => {
    const user = userEvent.setup();
    const lister = connectPathLister({ "": ["/opt/plugins", "/opt/skills"] });
    render(
      <PathListField
        option={pathListOption()}
        items={[]}
        onChange={() => {}}
        validatePath={async () => ({ path: "", valid: true })}
      />,
    );
    await user.click(screen.getByRole("button", { name: /browse/i }));
    expect(await screen.findByRole("option", { name: /plugins/ })).toBeTruthy();
    expect(lister.calls).toEqual([["", false]]);
  });

  test("a file pathKind (mcpConfigs) browses files too, and a file row lands in the draft", async () => {
    const user = userEvent.setup();
    const lister = connectPathLister({ "": ["/etc/mcp/", "/etc/mcp.json"] });
    render(
      <PathListField
        option={pathListOption({
          field: "mcp_configs",
          wireField: "mcpConfigs",
          label: "MCP config files",
          pathKind: "file",
        })}
        items={[]}
        onChange={() => {}}
        validatePath={async () => ({ path: "", valid: true })}
      />,
    );
    // The closed trigger IS the field now, so its resting text has to name a
    // file: this list holds .json config files, not directories.
    const browse = screen.getByRole("button", { name: /browse/i });
    expect(browse.textContent).toMatch(/\/path\/to\/file/);
    await user.click(browse);
    await user.click(await screen.findByRole("option", { name: /mcp\.json/ }));
    expect(lister.calls[0]).toEqual(["", true]);
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /browse/i }).textContent).toMatch(/\/etc\/mcp\.json/),
    );
  });

  test("adding a value validates it via the injected validatePath(path, schemaPathKind) before accepting", async () => {
    const user = userEvent.setup();
    connectPathLister({});
    const validatePath = vi.fn().mockResolvedValue({ path: "/opt/plugins", valid: true });
    const onChange = vi.fn();
    render(<PathListField option={pathListOption()} items={[]} onChange={onChange} validatePath={validatePath} />);
    await typePath(user, "/opt/plugins");
    await user.click(screen.getByRole("button", { name: "Add" }));
    await waitFor(() => expect(onChange).toHaveBeenCalledWith(["/opt/plugins"]));
    expect(validatePath).toHaveBeenCalledWith("/opt/plugins", "dir");
  });

  // Enter in the picker must not ALSO submit the add row. Two independent
  // things stop it: Popover portals the panel to document.body, so the input
  // is not in CollectionEditor's add <form> at all, and the panel's own keydown
  // handler preventDefaults Enter. Descending into a directory row is the case
  // that can observe a regression in either, since the panel stays OPEN - after
  // a commit the panel unmounts and there's no input left to submit from.
  test("Enter on a directory row descends without submitting the add row", async () => {
    const user = userEvent.setup();
    connectPathLister({ "": ["/opt/plugins"] });
    const validatePath = vi.fn().mockResolvedValue({ path: "/opt/plugins", valid: true });
    const onChange = vi.fn();
    render(<PathListField option={pathListOption()} items={[]} onChange={onChange} validatePath={validatePath} />);
    await user.click(screen.getByRole("button", { name: /browse/i }));
    await screen.findByRole("combobox", { name: "Path" });
    await user.click(await screen.findByRole("option", { name: /plugins/ }));
    // Still browsing: nothing has been added, and no path has been validated.
    expect(screen.getByRole("combobox", { name: "Path" })).toBeTruthy();
    expect(validatePath).not.toHaveBeenCalled();
    expect(onChange).not.toHaveBeenCalled();

    await user.keyboard("{ArrowDown}");
    await user.keyboard("{Enter}");
    expect(screen.getByRole("combobox", { name: "Path" })).toBeTruthy();
    expect(validatePath).not.toHaveBeenCalled();
    expect(onChange).not.toHaveBeenCalled();
  });

  // The add fires exactly once, and only from the Add click - a picker Enter
  // that also submitted would double it.
  test("committing a typed path in the panel adds the row exactly once, on the Add click", async () => {
    const user = userEvent.setup();
    connectPathLister({});
    const validatePath = vi.fn().mockResolvedValue({ path: "/opt/plugins", valid: true });
    const onChange = vi.fn();
    render(<PathListField option={pathListOption()} items={[]} onChange={onChange} validatePath={validatePath} />);
    await typePath(user, "/opt/plugins");
    expect(validatePath).not.toHaveBeenCalled();
    expect(onChange).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: "Add" }));
    await waitFor(() => expect(onChange).toHaveBeenCalledTimes(1));
    expect(onChange).toHaveBeenCalledWith(["/opt/plugins"]);
    expect(validatePath).toHaveBeenCalledTimes(1);
  });

  test("outputFile pathKind is translated to 'output-file' for the RPC call", async () => {
    const user = userEvent.setup();
    connectPathLister({});
    const validatePath = vi.fn().mockResolvedValue({ path: "/tmp/trace.json", valid: true });
    render(
      <PathListField
        option={pathListOption({
          field: "mcp_configs",
          wireField: "mcpConfigs",
          label: "MCP config files",
          pathKind: "file",
        })}
        items={[]}
        onChange={() => {}}
        validatePath={validatePath}
      />,
    );
    await typePath(user, "/tmp/mcp.json");
    await user.click(screen.getByRole("button", { name: "Add" }));
    await waitFor(() => expect(validatePath).toHaveBeenCalledWith("/tmp/mcp.json", "file"));
  });

  test("an invalid path shows an inline error and does not add the row", async () => {
    const user = userEvent.setup();
    connectPathLister({});
    const validatePath = vi.fn().mockResolvedValue({ path: "", valid: false, error: "path does not exist" });
    const onChange = vi.fn();
    render(<PathListField option={pathListOption()} items={[]} onChange={onChange} validatePath={validatePath} />);
    await typePath(user, "/nope");
    await user.click(screen.getByRole("button", { name: "Add" }));
    await waitFor(() => expect(screen.getByText("path does not exist")).toBeTruthy());
    expect(onChange).not.toHaveBeenCalled();
  });

  test("accepts the server-canonicalized path when the server rewrites it", async () => {
    const user = userEvent.setup();
    connectPathLister({});
    const validatePath = vi.fn().mockResolvedValue({ path: "/opt/plugins/canonical", valid: true });
    const onChange = vi.fn();
    render(<PathListField option={pathListOption()} items={[]} onChange={onChange} validatePath={validatePath} />);
    await typePath(user, "/opt/plugins/../plugins");
    await user.click(screen.getByRole("button", { name: "Add" }));
    await waitFor(() => expect(onChange).toHaveBeenCalledWith(["/opt/plugins/canonical"]));
  });

  // Shared with the spawn pane's own pathList control (validatePathListAdd): a
  // duplicate never reaches the RPC, and a broken RPC never wedges the add row.
  test("a path already in the list is rejected without spending an RPC", async () => {
    const user = userEvent.setup();
    connectPathLister({});
    const validatePath = vi.fn().mockResolvedValue({ path: "/opt/skills", valid: true });
    const onChange = vi.fn();
    render(
      <PathListField
        option={pathListOption()}
        items={["/opt/skills"]}
        onChange={onChange}
        validatePath={validatePath}
      />,
    );
    await typePath(user, "/opt/skills");
    await user.click(screen.getByRole("button", { name: "Add" }));
    expect(await screen.findByText("Already added.")).toBeTruthy();
    expect(validatePath).not.toHaveBeenCalled();
    expect(onChange).not.toHaveBeenCalled();
  });

  test("a validate RPC that fails outright does not block the add", async () => {
    const user = userEvent.setup();
    connectPathLister({});
    const validatePath = vi.fn().mockRejectedValue(new Error("socket closed"));
    const onChange = vi.fn();
    render(<PathListField option={pathListOption()} items={[]} onChange={onChange} validatePath={validatePath} />);
    await typePath(user, "/opt/skills");
    await user.click(screen.getByRole("button", { name: "Add" }));
    await waitFor(() => expect(onChange).toHaveBeenCalledWith(["/opt/skills"]));
  });

  test("remove drops the item from the list", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <PathListField
        option={pathListOption()}
        items={["/opt/a", "/opt/b"]}
        onChange={onChange}
        validatePath={async () => ({ path: "", valid: true })}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Remove /opt/a" }));
    expect(onChange).toHaveBeenCalledWith(["/opt/b"]);
  });

  test("empty state message", () => {
    render(
      <PathListField
        option={pathListOption()}
        items={[]}
        onChange={() => {}}
        validatePath={async () => ({ path: "", valid: true })}
      />,
    );
    expect(screen.getByText(/No skill directories configured/i)).toBeTruthy();
  });
});

describe("ModelListField", () => {
  const modelFallbacksOption: LaunchOption = {
    field: "model_fallbacks",
    wireField: "modelFallbacks",
    label: "Model fallbacks",
    description: "Ordered list of alternative models.",
    group: "Resources",
    kind: "modelList",
    perLaunch: true,
  };

  test("adds come from the shared model picker, not a free-text provider/model box", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <ModelListField
        option={modelFallbacksOption}
        items={[]}
        onChange={onChange}
        explicitEmpty={false}
        onExplicitEmptyChange={() => {}}
      />,
    );

    // The hand-typed box is gone; the picker's trigger is the add affordance.
    expect(screen.queryByPlaceholderText("provider/model")).toBeNull();
    await pickModel(user, "GPT-5 Mini");
    await user.click(screen.getByRole("button", { name: "Add" }));

    await waitFor(() => expect(onChange).toHaveBeenCalledWith(["openai/gpt-5-mini"]));
  });

  test("a model already in the list is rejected rather than duplicated", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <ModelListField
        option={modelFallbacksOption}
        items={["openai/gpt-5-mini"]}
        onChange={onChange}
        explicitEmpty={false}
        onExplicitEmptyChange={() => {}}
      />,
    );

    await pickModel(user, "GPT-5 Mini");
    await user.click(screen.getByRole("button", { name: "Add" }));

    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(onChange).not.toHaveBeenCalled();
  });

  test("the explicit-empty toggle is the only way to send an explicit []", async () => {
    const user = userEvent.setup();
    const onExplicitEmptyChange = vi.fn();
    render(
      <ModelListField
        option={modelFallbacksOption}
        items={[]}
        onChange={() => {}}
        explicitEmpty={false}
        onExplicitEmptyChange={onExplicitEmptyChange}
      />,
    );
    await user.click(screen.getByRole("switch", { name: /no model fallbacks/i }));
    expect(onExplicitEmptyChange).toHaveBeenCalledWith(true);
  });

  test("adding a row while explicit-empty is on turns the toggle back off", async () => {
    const user = userEvent.setup();
    const onExplicitEmptyChange = vi.fn();
    render(
      <ModelListField
        option={modelFallbacksOption}
        items={[]}
        onChange={() => {}}
        explicitEmpty={true}
        onExplicitEmptyChange={onExplicitEmptyChange}
      />,
    );
    await pickModel(user, "GPT-5 Mini");
    await user.click(screen.getByRole("button", { name: "Add" }));
    await waitFor(() => expect(onExplicitEmptyChange).toHaveBeenCalledWith(false));
  });
});

describe("EnvMapField", () => {
  const envOption: LaunchOption = {
    field: "env",
    wireField: "env",
    label: "Environment variables",
    group: "Environment",
    kind: "envMap",
    perLaunch: true,
  };

  test("renders each entry as NAME=value", () => {
    render(<EnvMapField option={envOption} value={{ FOO: "bar" }} onChange={() => {}} />);
    expect(screen.getByText("FOO=bar")).toBeTruthy();
  });

  test("the add row is two structured fields (name, value), not one combined NAME=value box", () => {
    render(<EnvMapField option={envOption} value={{}} onChange={() => {}} />);
    expect(screen.getByRole("textbox", { name: "Variable name" })).toBeTruthy();
    expect(screen.getByRole("textbox", { name: "Variable value" })).toBeTruthy();
    expect(screen.queryByPlaceholderText("NAME=value")).toBeNull();
  });

  test("typing a name and a value and submitting adds that entry", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<EnvMapField option={envOption} value={{}} onChange={onChange} />);
    await user.type(screen.getByRole("textbox", { name: "Variable name" }), "TOKEN");
    await user.type(screen.getByRole("textbox", { name: "Variable value" }), "abc=def");
    await user.click(screen.getByRole("button", { name: "Add" }));
    expect(onChange).toHaveBeenCalledWith({ TOKEN: "abc=def" });
  });

  test("a value typed before any name still round-trips intact once a name is added (the '=' join point is code-owned, never user-typed)", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<EnvMapField option={envOption} value={{}} onChange={onChange} />);
    await user.type(screen.getByRole("textbox", { name: "Variable value" }), "abc=def=ghi");
    await user.type(screen.getByRole("textbox", { name: "Variable name" }), "TOKEN");
    await user.click(screen.getByRole("button", { name: "Add" }));
    expect(onChange).toHaveBeenCalledWith({ TOKEN: "abc=def=ghi" });
  });

  test("a blank name is still rejected with the same inline error as before (typing only a value)", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<EnvMapField option={envOption} value={{}} onChange={onChange} />);
    await user.type(screen.getByRole("textbox", { name: "Variable value" }), "bar");
    await user.click(screen.getByRole("button", { name: "Add" }));
    expect(onChange).not.toHaveBeenCalled();
    expect(screen.getByText(/use NAME=value/i)).toBeTruthy();
  });

  test("typing '=' into the name field does not leak into the value field", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<EnvMapField option={envOption} value={{}} onChange={onChange} />);
    await user.type(screen.getByRole("textbox", { name: "Variable value" }), "bar");
    await user.type(screen.getByRole("textbox", { name: "Variable name" }), "FOO=BAZ");
    await user.click(screen.getByRole("button", { name: "Add" }));
    expect(onChange).toHaveBeenCalledWith({ FOOBAZ: "bar" });
  });

  test("remove drops just that key", () => {
    const onChange = vi.fn();
    render(<EnvMapField option={envOption} value={{ FOO: "bar", BAZ: "qux" }} onChange={onChange} />);
    screen.getByRole("button", { name: "Remove FOO=bar" }).click();
    expect(onChange).toHaveBeenCalledWith({ BAZ: "qux" });
  });
});

describe("McpServerListField", () => {
  const mcpOption: LaunchOption = {
    field: "mcps",
    wireField: "mcps",
    label: "MCP servers",
    group: "Resources",
    kind: "mcpServerList",
    perLaunch: true,
  };

  test("renders 'name → command args' per row", () => {
    render(
      <McpServerListField
        option={mcpOption}
        items={[{ name: "fs", command: "mcp-fs", args: ["--root", "/"] }]}
        onChange={() => {}}
        validateCommand={async () => ({ path: "mcp-fs", valid: true })}
      />,
    );
    expect(screen.getByText("fs → mcp-fs --root /")).toBeTruthy();
  });

  test("adding 'name command args...' validates the command and pushes a parsed spec", async () => {
    const user = userEvent.setup();
    const validateCommand = vi.fn().mockResolvedValue({ path: "mcp-fs", valid: true });
    const onChange = vi.fn();
    render(<McpServerListField option={mcpOption} items={[]} onChange={onChange} validateCommand={validateCommand} />);
    await user.type(screen.getByPlaceholderText("name command args..."), "fs mcp-fs --root /");
    await user.click(screen.getByRole("button", { name: "Add" }));
    await waitFor(() =>
      expect(onChange).toHaveBeenCalledWith([{ name: "fs", command: "mcp-fs", args: ["--root", "/"] }]),
    );
    expect(validateCommand).toHaveBeenCalledWith("mcp-fs");
  });

  test("an invalid command is rejected inline and not added", async () => {
    const user = userEvent.setup();
    const validateCommand = vi.fn().mockResolvedValue({ path: "", valid: false, error: "invalid command" });
    const onChange = vi.fn();
    render(<McpServerListField option={mcpOption} items={[]} onChange={onChange} validateCommand={validateCommand} />);
    await user.type(screen.getByPlaceholderText("name command args..."), "fs not-a-real-binary");
    await user.click(screen.getByRole("button", { name: "Add" }));
    await waitFor(() => expect(screen.getByText("invalid command")).toBeTruthy());
    expect(onChange).not.toHaveBeenCalled();
  });

  test("missing a command token is rejected without calling validateCommand at all", async () => {
    const user = userEvent.setup();
    const validateCommand = vi.fn();
    const onChange = vi.fn();
    render(<McpServerListField option={mcpOption} items={[]} onChange={onChange} validateCommand={validateCommand} />);
    await user.type(screen.getByPlaceholderText("name command args..."), "justonetoken");
    await user.click(screen.getByRole("button", { name: "Add" }));
    expect(validateCommand).not.toHaveBeenCalled();
    expect(onChange).not.toHaveBeenCalled();
  });
});
