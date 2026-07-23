import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { ModelDescriptor } from "../../protocol/types.gen";
import { fetchModelCatalog } from "../../widgets/modelCatalog/catalogClient";
import { ModelField } from "./ModelField";

// ModelField now renders the rich ModelCatalog widget, enriching the injected
// scoped model/list with the /api/models catalog. The wire loader is mocked so
// these render tests stay hermetic; the default is an empty enrichment, so the
// picker degrades to the label-only scoped list the interim tests expect.
vi.mock("../../widgets/modelCatalog/catalogClient", () => ({ fetchModelCatalog: vi.fn() }));

beforeEach(() => {
  vi.mocked(fetchModelCatalog).mockReset();
  vi.mocked(fetchModelCatalog).mockResolvedValue({ models: [], recent: [], diagnostics: [] });
});
afterEach(() => cleanup());

const MODELS: ModelDescriptor[] = [
  { provider: "anthropic", model: "claude-sonnet-4-5" },
  { provider: "openai", model: "gpt-5" },
];

test("shows the interim default marker when no model is chosen", () => {
  render(<ModelField value="" onChange={vi.fn()} loadModels={vi.fn().mockResolvedValue([])} />);
  expect(screen.getByText("(default)")).toBeTruthy();
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

  await user.click(screen.getByRole("button", { name: "Change model" }));
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

  await user.click(screen.getByRole("button", { name: "Change model" }));
  expect(await screen.findByText(/providers unavailable/i)).toBeTruthy();
});

test("Cancel returns to the closed display without changing the value", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(<ModelField value="openai/gpt-5" onChange={onChange} loadModels={vi.fn().mockResolvedValue(MODELS)} />);

  await user.click(screen.getByRole("button", { name: "Change model" }));
  await screen.findByRole("combobox", { name: "Model" });
  await user.click(screen.getByRole("button", { name: "Cancel" }));

  expect(screen.getByText("openai/gpt-5")).toBeTruthy();
  expect(onChange).not.toHaveBeenCalled();
});

// --- wave 8: the rich catalog enrichment ---

test("enriches a scoped model with /api/models metadata (display name + capability badge)", async () => {
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

  await user.click(screen.getByRole("button", { name: "Change model" }));
  const combo = await screen.findByRole("combobox", { name: "Model" });
  await user.type(combo, "{arrowdown}");

  expect(await screen.findByText("GPT-5")).toBeTruthy(); // prettified display name from the catalog
  expect(screen.getByText("tools")).toBeTruthy(); // capability badge from the catalog
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

  await user.click(screen.getByRole("button", { name: "Change model" }));
  const combo = await screen.findByRole("combobox", { name: "Model" });
  await user.type(combo, "{arrowdown}");

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

  await user.click(screen.getByRole("button", { name: "Change model" }));
  const combo = await screen.findByRole("combobox", { name: "Model" });
  await user.type(combo, "{arrowdown}");

  expect(await screen.findByText("openai/gpt-5")).toBeTruthy();
});
