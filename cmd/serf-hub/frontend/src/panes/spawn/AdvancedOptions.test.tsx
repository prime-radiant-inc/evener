import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";
import type { LaunchConfigResolved, LaunchOption } from "../../protocol/types.gen";
import type { ModelCatalog as ModelCatalogEnvelope } from "../../widgets";
import { AdvancedOptions } from "./AdvancedOptions";

afterEach(() => cleanup());

function option(partial: Partial<LaunchOption> & { wireField: string; kind: string; label: string }): LaunchOption {
  return { field: partial.wireField, group: "general", perLaunch: true, ...partial };
}

// Every model-valued advanced field renders the shared ModelCatalog picker, so
// the panel needs a catalog loader; this is the smallest real envelope.
const CATALOG: ModelCatalogEnvelope = {
  models: [
    { provider: "openai", model: "gpt-5", displayName: "GPT-5" },
    { provider: "anthropic", model: "claude-sonnet-4-5", displayName: "Claude Sonnet 4.5" },
  ],
  recent: [],
};

const RESOLVED: LaunchConfigResolved = {
  effective: { sandbox: "off", maxRounds: 5 },
  layers: {},
  provenance: {},
};

// Every path-valued field renders the shared PathField browse widget, so the
// panel needs a completion loader; entries are keyed by the listing prefix the
// widget sends, with directory entries carrying a trailing slash the way
// serf/paths/complete's includeFiles mode does. An empty-valued field opens on
// the empty prefix, which the hub resolves to $HOME - here, /opt's contents.
const HOME_ENTRIES = ["/opt/skills/", "/opt/prompt.md"];
const TREE: Record<string, string[]> = {
  "": HOME_ENTRIES,
  "/opt/": HOME_ENTRIES,
};

function renderPanel(
  options: LaunchOption[],
  over: Partial<Parameters<typeof AdvancedOptions>[0]> = {},
  children?: ReactNode,
) {
  const onOverridesChange = over.onOverridesChange ?? vi.fn();
  const validatePath = over.validatePath ?? vi.fn().mockResolvedValue({ valid: true });
  const resolveConfig = over.resolveConfig ?? vi.fn().mockResolvedValue(RESOLVED);
  const loadCatalog = over.loadCatalog ?? vi.fn().mockResolvedValue(CATALOG);
  // Mirrors the hub's two modes: a dirs-only response is unsuffixed, and a
  // files-included one keeps the directory slashes.
  const complete =
    over.complete ??
    vi.fn((prefix: string, includeFiles: boolean) => {
      const entries = TREE[prefix] ?? [];
      if (includeFiles) return Promise.resolve(entries);
      return Promise.resolve(entries.filter((e) => e.endsWith("/")).map((e) => e.replace(/\/+$/, "")));
    });
  render(
    <AdvancedOptions
      options={options}
      onOverridesChange={onOverridesChange as (o: unknown) => void}
      validatePath={validatePath as (p: string, k: string) => Promise<{ valid: boolean; error?: string }>}
      resolveConfig={resolveConfig as (o: unknown) => Promise<LaunchConfigResolved>}
      loadCatalog={loadCatalog}
      complete={complete}
    >
      {children}
    </AdvancedOptions>,
  );
  return { onOverridesChange, validatePath, resolveConfig, loadCatalog, complete };
}

/** A PathField's closed trigger. A scalar field's trigger is labeled by its
 * FormRow; an add-row picker inside a CollectionEditor has no label, so its
 * name falls back to its own contents (the value plus a "browse" hint). */
function pathTrigger(name: string | RegExp = /browse/i): HTMLElement {
  return screen.getByRole("button", { name });
}

/** Opens a PathField's browse panel and types a literal path into it, which
 * Enter commits (nothing is highlighted, so the typed text IS the answer). The
 * browse panel is portaled to document.body, so it is queried from `screen`. */
async function typePath(user: ReturnType<typeof userEvent.setup>, trigger: HTMLElement, path: string): Promise<void> {
  await user.click(trigger);
  await screen.findByRole("combobox", { name: "Path" });
  // keyboard, not type: the panel input is already focused with its pre-filled
  // value selected, and clicking it would collapse that selection.
  await user.keyboard(`${path}{Enter}`);
}

test("is collapsed by default and reveals the panel on toggle", async () => {
  const user = userEvent.setup();
  renderPanel([option({ wireField: "maxRounds", kind: "integer", label: "Max rounds" })]);

  expect(screen.queryByLabelText("Max rounds")).toBeNull();
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  expect(screen.getByLabelText("Max rounds")).toBeTruthy();
});

test("a boolean control collects true/false and drops the (default)", async () => {
  const user = userEvent.setup();
  const { onOverridesChange } = renderPanel([
    option({ wireField: "noProjectPrompts", kind: "boolean", label: "No project prompts" }),
  ]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.selectOptions(screen.getByLabelText("No project prompts"), "On");

  expect(onOverridesChange).toHaveBeenLastCalledWith({ noProjectPrompts: true });
});

test("an integer control collects a parsed number", async () => {
  const user = userEvent.setup();
  const { onOverridesChange } = renderPanel([option({ wireField: "maxRounds", kind: "integer", label: "Max rounds" })]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.type(screen.getByLabelText("Max rounds"), "7");

  await waitFor(() => expect(onOverridesChange).toHaveBeenLastCalledWith({ maxRounds: 7 }));
});

test("a failing path validation flags the field invalid so it is dropped from the overrides", async () => {
  const user = userEvent.setup();
  const validatePath = vi.fn().mockResolvedValue({ valid: false, error: "path is a directory" });
  const { onOverridesChange } = renderPanel(
    [option({ wireField: "systemPromptFile", kind: "path", label: "System prompt file", pathKind: "file" })],
    { validatePath },
  );

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await typePath(user, pathTrigger("System prompt file"), "/etc");

  expect(await screen.findByText("path is a directory")).toBeTruthy();
  await waitFor(() => expect(validatePath).toHaveBeenCalledWith("/etc", "file"));
  // Collected overrides must not include the invalid field.
  await waitFor(() => expect(onOverridesChange).toHaveBeenLastCalledWith({}));
});

// serf/path/validate spells the output-file kind "output-file"; the schema
// spells it "outputFile". Sending the schema spelling falls through the RPC's
// switch to a plain stat, which rejects the not-yet-existing file an
// outputFile field exists to name (spec 3.4).
test.each([
  ["file", "file"],
  ["dir", "dir"],
  ["outputFile", "output-file"],
])("a %s-kind path field validates with the RPC's own kind %s", async (pathKind, wireKind) => {
  const user = userEvent.setup();
  const { validatePath } = renderPanel([
    option({ wireField: "traceFile", kind: "path", label: "Trace file", pathKind }),
  ]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await typePath(user, pathTrigger("Trace file"), "/opt/new.log");

  await waitFor(() => expect(validatePath).toHaveBeenCalledWith("/opt/new.log", wireKind));
});

test("show resolved config previews the effective launch config", async () => {
  const user = userEvent.setup();
  renderPanel([option({ wireField: "maxRounds", kind: "integer", label: "Max rounds" })]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.click(screen.getByRole("button", { name: "Show resolved config" }));

  const pre = await screen.findByLabelText("Resolved config");
  expect(pre.textContent).toContain('"sandbox": "off"');
  expect(pre.textContent).toContain('"maxRounds": 5');
});

test("renders children inside the expanded panel, before any schema control (9ct0)", async () => {
  const user = userEvent.setup();
  renderPanel(
    [option({ wireField: "maxRounds", kind: "integer", label: "Max rounds" })],
    {},
    <div data-testid="child-slot">hi</div>,
  );

  expect(screen.queryByTestId("child-slot")).toBeNull(); // panel is collapsed by default
  await user.click(screen.getByRole("button", { name: "Advanced options" }));

  const toggleButton = screen.getByRole("button", { name: "Advanced options" });
  const panelId = toggleButton.getAttribute("aria-controls");
  if (!panelId) throw new Error("expected aria-controls on the toggle");
  const panel = document.getElementById(panelId);
  if (!panel) throw new Error("expected the expanded panel to be in the document");

  // The child slot is the panel's first child, ahead of the schema controls.
  expect(panel.firstElementChild).toBe(screen.getByTestId("child-slot"));
});

// --- model-valued fields all use the shared searchable picker ---------------

test("a modelPicker field renders the shared model picker, not a free-text box", async () => {
  const user = userEvent.setup();
  const { onOverridesChange } = renderPanel([
    option({ wireField: "fastCheapModel", kind: "modelPicker", label: "Fast cheap model" }),
  ]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));

  expect(screen.getByText("Fast cheap model")).toBeTruthy();
  expect(screen.queryByRole("textbox")).toBeNull();
  await user.click(screen.getByRole("button", { name: /change model/i }));
  await user.click(await screen.findByText("GPT-5"));

  await waitFor(() => expect(onOverridesChange).toHaveBeenCalledWith({ fastCheapModel: "openai/gpt-5" }));
});

test("a modelList field adds from the picker instead of a hand-typed provider/model", async () => {
  const user = userEvent.setup();
  const { onOverridesChange } = renderPanel([
    option({ wireField: "modelFallbacks", kind: "modelList", label: "Model fallbacks" }),
  ]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.click(screen.getByRole("button", { name: /change model/i }));
  await user.click(await screen.findByText("Claude Sonnet 4.5"));
  await user.click(screen.getByRole("button", { name: "Add" }));

  await waitFor(() =>
    expect(onOverridesChange).toHaveBeenCalledWith({ modelFallbacks: ["anthropic/claude-sonnet-4-5"] }),
  );
});

test("a modelList field rejects a model already in the list", async () => {
  const user = userEvent.setup();
  const { onOverridesChange } = renderPanel([
    option({ wireField: "modelFallbacks", kind: "modelList", label: "Model fallbacks" }),
  ]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  async function addSonnet() {
    await user.click(screen.getByRole("button", { name: /change model/i }));
    await user.click(await screen.findByText("Claude Sonnet 4.5"));
    await user.click(screen.getByRole("button", { name: "Add" }));
  }
  await addSonnet();
  await waitFor(() => expect(onOverridesChange).toHaveBeenCalled());
  vi.mocked(onOverridesChange as (o: unknown) => void).mockClear();
  await addSonnet();

  expect(await screen.findByRole("alert")).toBeTruthy();
  expect(onOverridesChange).not.toHaveBeenCalled();
});

// --- path-valued fields all browse, rather than being typed blind -----------

test("a path field renders the browse widget and collects the picked path", async () => {
  const user = userEvent.setup();
  const { onOverridesChange } = renderPanel([
    option({ wireField: "systemPromptFile", kind: "path", label: "System prompt file", pathKind: "file" }),
  ]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));

  // The field is the browse trigger, not a text box to type a path into blind.
  expect(screen.queryByRole("textbox")).toBeNull();
  await user.click(pathTrigger("System prompt file"));
  await user.click(await screen.findByText("prompt.md"));

  await waitFor(() => expect(onOverridesChange).toHaveBeenLastCalledWith({ systemPromptFile: "/opt/prompt.md" }));
});

// The schema's pathKind is what decides whether the listing includes files, so
// it has to reach the widget: an outputFile field names a file (includeFiles
// true, so existing files are pickable references) while a dir field never
// lists one.
test.each([
  ["file", true],
  ["outputFile", true],
  ["dir", false],
])("a %s-kind path field asks for includeFiles=%s", async (pathKind, includeFiles) => {
  const user = userEvent.setup();
  const { complete } = renderPanel([option({ wireField: "somePath", kind: "path", label: "Some path", pathKind })]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.click(pathTrigger("Some path"));

  await waitFor(() => expect(complete).toHaveBeenCalledWith(expect.any(String), includeFiles));
});

test("a command-kind path option stays a plain text input, since a command is not browsable", async () => {
  const user = userEvent.setup();
  const { onOverridesChange } = renderPanel([
    option({ wireField: "agent", kind: "path", label: "Agent", pathKind: "command" }),
  ]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));

  expect(screen.getByLabelText("Agent").tagName).toBe("INPUT");
  await user.type(screen.getByLabelText("Agent"), "serf");

  await waitFor(() => expect(onOverridesChange).toHaveBeenLastCalledWith({ agent: "serf" }));
});

/** Browses to a directory row and submits it, which is the add path the picker
 * exists for: a directory click writes the picked path into CollectionEditor's
 * own draft, which the Add button then submits. */
async function browseAndAdd(user: ReturnType<typeof userEvent.setup>, rowName: string): Promise<void> {
  await user.click(pathTrigger());
  await user.click(await screen.findByText(rowName));
  await user.keyboard("{Escape}");
  await user.click(screen.getByRole("button", { name: "Add" }));
}

test("a pathList field adds from the browse widget instead of a hand-typed path", async () => {
  const user = userEvent.setup();
  const { onOverridesChange } = renderPanel([
    option({ wireField: "skillsDirs", kind: "pathList", label: "Skill directories", pathKind: "dir" }),
  ]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await browseAndAdd(user, "skills");

  await waitFor(() => expect(onOverridesChange).toHaveBeenLastCalledWith({ skillsDirs: ["/opt/skills"] }));
});

test("a pathList field also accepts a path typed into the browse panel", async () => {
  const user = userEvent.setup();
  const { onOverridesChange } = renderPanel([
    option({ wireField: "skillsDirs", kind: "pathList", label: "Skill directories", pathKind: "dir" }),
  ]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await typePath(user, pathTrigger(), "/elsewhere/skills");
  await user.click(screen.getByRole("button", { name: "Add" }));

  await waitFor(() => expect(onOverridesChange).toHaveBeenLastCalledWith({ skillsDirs: ["/elsewhere/skills"] }));
});

// All three real pathList options are perLaunch, so they render together in one
// panel. CollectionEditor renders no label of its own in renderAddField mode, so
// without the option's label on the picker all three add triggers are named by
// the same text and nothing on the page tells them apart.
test("each pathList add picker is named by its own option", async () => {
  const user = userEvent.setup();
  renderPanel([
    option({ wireField: "skillsDirs", kind: "pathList", label: "Skill directories", pathKind: "dir" }),
    option({ wireField: "pluginDirs", kind: "pathList", label: "Plugin directories", pathKind: "dir" }),
    option({ wireField: "mcpConfigs", kind: "pathList", label: "MCP config files", pathKind: "file" }),
  ]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));

  for (const label of ["Skill directories", "Plugin directories", "MCP config files"]) {
    expect(screen.getByRole("button", { name: new RegExp(`^${label}:`) })).toBeTruthy();
  }
});

test("a pathList field rejects a path already in the list", async () => {
  const user = userEvent.setup();
  const { onOverridesChange } = renderPanel([
    option({ wireField: "skillsDirs", kind: "pathList", label: "Skill directories", pathKind: "dir" }),
  ]);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await browseAndAdd(user, "skills");
  await waitFor(() => expect(onOverridesChange).toHaveBeenCalled());
  vi.mocked(onOverridesChange as (o: unknown) => void).mockClear();
  await browseAndAdd(user, "skills");

  expect(await screen.findByRole("alert")).toBeTruthy();
  expect(onOverridesChange).not.toHaveBeenCalled();
});
