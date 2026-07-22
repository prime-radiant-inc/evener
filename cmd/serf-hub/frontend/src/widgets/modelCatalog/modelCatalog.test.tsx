import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
// ModelCatalog is both the component (value) and the envelope interface (type);
// a single import brings in both meanings via declaration merging.
import { ModelCatalog } from "./index";

afterEach(() => cleanup());

const CATALOG: ModelCatalog = {
  models: [
    { provider: "anthropic", model: "claude-sonnet-4-5", displayName: "Claude Sonnet 4.5" },
    { provider: "openai", model: "gpt-5", displayName: "GPT-5" },
  ],
  recent: [],
};

test("shows the interim default marker when no model is chosen", () => {
  render(<ModelCatalog value="" onChange={vi.fn()} loadCatalog={vi.fn().mockResolvedValue(CATALOG)} />);
  expect(screen.getByText("(default)")).toBeTruthy();
});

test("shows the qualified provider/model when a model is set", () => {
  render(<ModelCatalog value="openai/gpt-5" onChange={vi.fn()} loadCatalog={vi.fn().mockResolvedValue(CATALOG)} />);
  expect(screen.getByText("openai/gpt-5")).toBeTruthy();
});

test("Change model loads the catalog and picking one (by display name) reports the qualified id", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  const loadCatalog = vi.fn().mockResolvedValue(CATALOG);
  render(<ModelCatalog value="" onChange={onChange} loadCatalog={loadCatalog} />);

  await user.click(screen.getByRole("button", { name: "Change model" }));
  const combo = await screen.findByRole("combobox", { name: "Model" });
  await user.type(combo, "GPT");
  await user.click(await screen.findByText("GPT-5"));

  expect(onChange).toHaveBeenCalledWith("openai/gpt-5");
  expect(loadCatalog).toHaveBeenCalledTimes(1);
});

test("surfaces an inline error when the catalog fails to load", async () => {
  const user = userEvent.setup();
  render(
    <ModelCatalog
      value=""
      onChange={vi.fn()}
      loadCatalog={vi.fn().mockRejectedValue(new Error("providers unavailable"))}
    />,
  );

  await user.click(screen.getByRole("button", { name: "Change model" }));
  expect(await screen.findByText(/providers unavailable/i)).toBeTruthy();
});

test("Cancel returns to the closed display without changing the value", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(<ModelCatalog value="openai/gpt-5" onChange={onChange} loadCatalog={vi.fn().mockResolvedValue(CATALOG)} />);

  await user.click(screen.getByRole("button", { name: "Change model" }));
  await screen.findByRole("combobox", { name: "Model" });
  await user.click(screen.getByRole("button", { name: "Cancel" }));

  expect(screen.getByText("openai/gpt-5")).toBeTruthy();
  expect(onChange).not.toHaveBeenCalled();
});
