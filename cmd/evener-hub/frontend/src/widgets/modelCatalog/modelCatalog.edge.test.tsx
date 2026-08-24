// Edge cases for modelCatalog that close the remaining uncovered lines:
// - Enter key with exact match on display name (lines 217-223)

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { ModelCatalog, type ModelCatalogEntry } from "./index";

afterEach(() => cleanup());

const SONNET: ModelCatalogEntry = {
  provider: "anthropic",
  model: "claude-sonnet-4-5",
  displayName: "Claude Sonnet 4.5",
  supportsTools: true,
  inputCostPerMillion: 3,
  outputCostPerMillion: 15,
  contextWindow: 200000,
};
const CATALOG = { models: [SONNET], recent: [] };

function renderPicker(props: Partial<Parameters<typeof ModelCatalog>[0]> = {}) {
  const onChange = props.onChange ?? vi.fn();
  render(
    <ModelCatalog
      value={props.value ?? ""}
      onChange={onChange}
      loadCatalog={props.loadCatalog ?? vi.fn().mockResolvedValue(CATALOG)}
    />,
  );
  return { onChange };
}

async function openPicker(user: ReturnType<typeof userEvent.setup>): Promise<HTMLInputElement> {
  await user.click(screen.getByRole("button", { name: /change model/i }));
  return (await screen.findByRole("combobox", { name: "Model" })) as HTMLInputElement;
}

test("Enter on an exact display name match selects it", async () => {
  const user = userEvent.setup();
  const { onChange } = renderPicker();
  const input = await openPicker(user);

  // Type the exact display name
  await user.type(input, "Claude Sonnet 4.5");
  await user.keyboard("{Enter}");

  expect(onChange).toHaveBeenCalledWith("anthropic/claude-sonnet-4-5");
});

test("Enter on a non-matching typed text does not select anything", async () => {
  const user = userEvent.setup();
  const { onChange } = renderPicker();
  const input = await openPicker(user);

  // Type something that doesn't match any model exactly
  await user.type(input, "nonexistent-model");
  await user.keyboard("{Enter}");

  expect(onChange).not.toHaveBeenCalled();
});

test("End key moves to the last pick", async () => {
  const user = userEvent.setup();
  const { onChange } = renderPicker({
    loadCatalog: vi.fn().mockResolvedValue({
      models: [
        SONNET,
        {
          provider: "openai",
          model: "gpt-5",
          displayName: "GPT-5",
          supportsTools: true,
          inputCostPerMillion: 1.25,
          outputCostPerMillion: 10,
          contextWindow: 400000,
        },
      ],
      recent: [],
    }),
  });
  const _input = await openPicker(user);

  // Press End to jump to the last pick
  await user.keyboard("{End}");
  await user.keyboard("{Enter}");

  // The last model should be selected
  expect(onChange).toHaveBeenCalledWith("openai/gpt-5");
});
