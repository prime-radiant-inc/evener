import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { ModelDescriptor } from "../../protocol/types.gen";
import * as catalogClientModule from "../../widgets/modelCatalog/catalogClient";
import { ModelField } from "./ModelField";

// ModelField now renders the rich ModelCatalog widget, enriching the injected
// scoped model/list with the /api/models catalog. The wire loader is mocked so
// these render tests stay hermetic; the default is an empty enrichment, so the
// picker degrades to the label-only scoped list the interim tests expect.
//
// vi.spyOn, not vi.mock: LaunchConfigForm.test.tsx renders ScalarField/
// ModelListField (fields.tsx/collectionFields.tsx) without ever mocking this
// module, so under a shared module registry those production modules' own
// `import { fetchModelCatalog }` binding is already resolved to the real
// function by the time this file's tests run - a vi.mock() factory
// registered this late replaces what THIS file's own import resolves to, but
// not what an already-loaded importer calls internally. Spying on the real
// module's own export patches the one binding every importer actually
// shares, regardless of import order.
let fetchModelCatalog: typeof catalogClientModule.fetchModelCatalog;
beforeEach(() => {
  fetchModelCatalog = vi
    .spyOn(catalogClientModule, "fetchModelCatalog")
    .mockResolvedValue({ models: [], recent: [], diagnostics: [] });
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const MODELS: ModelDescriptor[] = [
  { provider: "anthropic", model: "claude-sonnet-4-5" },
  { provider: "openai", model: "gpt-5" },
];

test("shows the interim default marker when no model is chosen", () => {
  render(<ModelField value="" onChange={vi.fn()} loadModels={vi.fn().mockResolvedValue([])} />);
  expect(screen.getByText("(default)")).toBeTruthy();
});

// kata xgk8: the spawn form overrides the empty-value label when the daemon
// has no resolvable default model, so the field never claims to be
// already-answered when a submit would be refused.
test("forwards emptyLabel to the underlying catalog trigger", () => {
  render(
    <ModelField value="" onChange={vi.fn()} loadModels={vi.fn().mockResolvedValue([])} emptyLabel="Choose a model" />,
  );
  expect(screen.getByText("Choose a model")).toBeTruthy();
  expect(screen.queryByText("(default)")).toBeNull();
});

test("shows the qualified provider/model when a model is set", () => {
  render(<ModelField value="openai/gpt-5" onChange={vi.fn()} loadModels={vi.fn().mockResolvedValue(MODELS)} />);
  expect(screen.getByText("openai/gpt-5")).toBeTruthy();
});

test("Change model loads the catalog and picking one reports the qualified id", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  const loadModels = vi.fn().mockResolvedValue(MODELS);
  render(<ModelField value="" onChange={onChange} loadModels={loadModels} />);

  await user.click(screen.getByRole("button", { name: /change model/i }));
  const combo = await screen.findByRole("combobox", { name: "Model" });
  await user.type(combo, "gpt");
  await user.click(await screen.findByText("openai/gpt-5"));

  expect(onChange).toHaveBeenCalledWith("openai/gpt-5");
  expect(loadModels).toHaveBeenCalledTimes(1);
});

test("surfaces an inline error when the model catalog fails to load", async () => {
  const user = userEvent.setup();
  render(
    <ModelField
      value=""
      onChange={vi.fn()}
      loadModels={vi.fn().mockRejectedValue(new Error("providers unavailable"))}
    />,
  );

  await user.click(screen.getByRole("button", { name: /change model/i }));
  expect(await screen.findByText(/providers unavailable/i)).toBeTruthy();
});

test("Escape returns to the closed display without changing the value", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(<ModelField value="openai/gpt-5" onChange={onChange} loadModels={vi.fn().mockResolvedValue(MODELS)} />);

  await user.click(screen.getByRole("button", { name: /change model/i }));
  await screen.findByRole("combobox", { name: "Model" });
  await user.keyboard("{Escape}");

  await waitFor(() => expect(screen.queryByRole("combobox", { name: "Model" })).toBeNull());
  expect(screen.getByText("openai/gpt-5")).toBeTruthy();
  expect(onChange).not.toHaveBeenCalled();
});

// --- wave 8: the rich catalog enrichment ---

test("enriches a scoped model with /api/models metadata (display name + capability/cost meta)", async () => {
  const user = userEvent.setup();
  vi.mocked(fetchModelCatalog).mockResolvedValue({
    models: [
      {
        provider: "openai",
        model: "gpt-5",
        displayName: "GPT-5",
        supportsTools: true,
        inputCostPerMillion: 1.25,
        outputCostPerMillion: 10,
      },
    ],
    recent: [],
    diagnostics: [],
  });
  render(
    <ModelField
      value=""
      onChange={vi.fn()}
      loadModels={vi.fn().mockResolvedValue([{ provider: "openai", model: "gpt-5" }])}
    />,
  );

  await user.click(screen.getByRole("button", { name: /change model/i }));
  await screen.findByRole("combobox", { name: "Model" });

  expect(await screen.findByText("GPT-5")).toBeTruthy(); // prettified display name from the catalog
  // Capabilities and cost arrive as the row's one small-text meta line.
  expect(screen.getByText("tools · $1.25 in · $10 out /Mtok")).toBeTruthy();
});

test("scopes the /api/models enrichment to the spawn harness and cwd", async () => {
  const user = userEvent.setup();
  render(
    <ModelField
      value=""
      onChange={vi.fn()}
      loadModels={vi.fn().mockResolvedValue([{ provider: "openai", model: "gpt-5" }])}
      harness="codex"
      cwd="/tmp/project"
    />,
  );

  await user.click(screen.getByRole("button", { name: /change model/i }));
  await screen.findByRole("combobox", { name: "Model" });
  await screen.findByText("openai/gpt-5"); // loadCatalog (incl. the enrichment fetch) has resolved

  // The enrichment must be scoped to the SAME harness+cwd as the authoritative
  // model/list SET, so a non-default harness enriches its own models rather than
  // the default serf catalog.
  expect(fetchModelCatalog).toHaveBeenCalledWith(expect.objectContaining({ harness: "codex", cwd: "/tmp/project" }));
});

test("keeps the scoped model SET even when /api/models offers a different one", async () => {
  const user = userEvent.setup();
  vi.mocked(fetchModelCatalog).mockResolvedValue({
    models: [{ provider: "anthropic", model: "claude", displayName: "Claude" }],
    recent: [],
    diagnostics: [],
  });
  render(
    <ModelField
      value=""
      onChange={vi.fn()}
      loadModels={vi.fn().mockResolvedValue([{ provider: "openai", model: "gpt-5" }])}
    />,
  );

  await user.click(screen.getByRole("button", { name: /change model/i }));
  await screen.findByRole("combobox", { name: "Model" });

  expect(await screen.findByText("openai/gpt-5")).toBeTruthy(); // the scoped model, label-only
  expect(screen.queryByText("Claude")).toBeNull(); // an enrichment-only model is never launchable here
});

test("still lists the scoped models when /api/models is unavailable", async () => {
  const user = userEvent.setup();
  vi.mocked(fetchModelCatalog).mockRejectedValue(new Error("network down"));
  render(
    <ModelField
      value=""
      onChange={vi.fn()}
      loadModels={vi.fn().mockResolvedValue([{ provider: "openai", model: "gpt-5" }])}
    />,
  );

  await user.click(screen.getByRole("button", { name: /change model/i }));
  await screen.findByRole("combobox", { name: "Model" });

  expect(await screen.findByText("openai/gpt-5")).toBeTruthy();
});
