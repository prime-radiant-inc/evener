import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import type { ModelDescriptor } from "../../protocol/types.gen";
import { ModelField } from "./ModelField";

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
