import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import type { ModelCatalog } from "../../widgets/modelCatalog";
import { ModelField } from "./ModelField";

afterEach(() => {
  cleanup();
});

const CATALOG: ModelCatalog = {
  models: [
    { provider: "anthropic", model: "claude-sonnet-4-5", displayName: "Claude Sonnet 4.5" },
    { provider: "openai", model: "gpt-5", displayName: "GPT-5" },
  ],
  recent: [],
  diagnostics: [],
};

test("shows the (default) marker when no model is chosen", () => {
  render(<ModelField value="" onChange={vi.fn()} loadCatalog={vi.fn().mockResolvedValue(CATALOG)} />);
  expect(screen.getByText("(default)")).toBeTruthy();
});

// kata xgk8: the spawn form overrides the empty-value label when the daemon
// has no resolvable default model, so the field never claims to be
// already-answered when a submit would be refused.
test("forwards emptyLabel to the underlying catalog trigger", () => {
  render(
    <ModelField
      value=""
      onChange={vi.fn()}
      loadCatalog={vi.fn().mockResolvedValue(CATALOG)}
      emptyLabel="Choose a model"
    />,
  );
  expect(screen.getByText("Choose a model")).toBeTruthy();
  expect(screen.queryByText("(default)")).toBeNull();
});

test("shows the qualified provider/model when a model is set", () => {
  render(<ModelField value="openai/gpt-5" onChange={vi.fn()} loadCatalog={vi.fn().mockResolvedValue(CATALOG)} />);
  expect(screen.getByText("openai/gpt-5")).toBeTruthy();
});

test("Change model loads the catalog and picking one reports the qualified id", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  const loadCatalog = vi.fn().mockResolvedValue(CATALOG);
  render(<ModelField value="" onChange={onChange} loadCatalog={loadCatalog} />);

  await user.click(screen.getByRole("button", { name: /change model/i }));
  const combo = await screen.findByRole("combobox", { name: "Model" });
  await user.type(combo, "gpt");
  await user.click(await screen.findByText("GPT-5"));

  expect(onChange).toHaveBeenCalledWith("openai/gpt-5");
  expect(loadCatalog).toHaveBeenCalledTimes(1);
});

test("surfaces a friendly inline error when the model catalog fails to load", async () => {
  const user = userEvent.setup();
  render(
    <ModelField
      value=""
      onChange={vi.fn()}
      loadCatalog={vi.fn().mockRejectedValue(new Error("providers unavailable"))}
    />,
  );

  await user.click(screen.getByRole("button", { name: /change model/i }));
  expect(await screen.findByText("Couldn't load models: Something went wrong.")).toBeTruthy();
  expect(screen.queryByText(/providers unavailable/i)).toBeNull();
});

test("Escape returns to the closed display without changing the value", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(<ModelField value="openai/gpt-5" onChange={onChange} loadCatalog={vi.fn().mockResolvedValue(CATALOG)} />);

  await user.click(screen.getByRole("button", { name: /change model/i }));
  await screen.findByRole("combobox", { name: "Model" });
  await user.keyboard("{Escape}");

  await waitFor(() => expect(screen.queryByRole("combobox", { name: "Model" })).toBeNull());
  expect(screen.getByText("openai/gpt-5")).toBeTruthy();
  expect(onChange).not.toHaveBeenCalled();
});

test("renders the metadata carried by the typed catalog response", async () => {
  const user = userEvent.setup();
  const loadCatalog = vi.fn().mockResolvedValue({
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
  } satisfies ModelCatalog);
  render(<ModelField value="" onChange={vi.fn()} loadCatalog={loadCatalog} />);

  await user.click(screen.getByRole("button", { name: /change model/i }));
  await screen.findByRole("combobox", { name: "Model" });

  expect(await screen.findByText("GPT-5")).toBeTruthy();
  expect(screen.getByText("tools · $1.25 in · $10 out /Mtok")).toBeTruthy();
});
