import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi } from "vitest";
// ModelCatalog is both the component (value) and the envelope interface (type);
// a single import brings in both meanings via declaration merging.
import { ModelCatalog, type ModelCatalogEntry } from "./index";

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

// --- rich catalog: badges, cost, context, provider grouping, Recent, diagnostics ---

const SONNET: ModelCatalogEntry = {
  provider: "anthropic",
  model: "claude-sonnet-4-5",
  displayName: "Claude Sonnet 4.5",
  supportsTools: true,
  supportsVision: true,
  supportsReasoning: true,
  inputCostPerMillion: 3,
  outputCostPerMillion: 15,
  contextWindow: 200000,
};
const GPT5: ModelCatalogEntry = {
  provider: "openai",
  model: "gpt-5",
  displayName: "GPT-5",
  supportsTools: true,
  inputCostPerMillion: 1.25,
  outputCostPerMillion: 10,
  contextWindow: 400000,
};
const RICH: ModelCatalog = { models: [SONNET, GPT5], recent: [] };

describe("rich catalog rows", () => {
  test("an opened option shows its capability badges, cost, and context window", async () => {
    const user = userEvent.setup();
    render(<ModelCatalog value="" onChange={vi.fn()} loadCatalog={vi.fn().mockResolvedValue(RICH)} />);

    await user.click(screen.getByRole("button", { name: "Change model" }));
    const combo = await screen.findByRole("combobox", { name: "Model" });
    await user.type(combo, "sonnet");
    // Scope to the Sonnet row: the query debounces, so both rows are briefly
    // present before the list narrows - within() reads only this option.
    const sonnetRow = (await screen.findByText("Claude Sonnet 4.5")).closest("li");
    if (!sonnetRow) throw new Error("expected the Sonnet option to render inside a listbox <li>");
    const row = within(sonnetRow);

    expect(row.getByText("tools")).toBeTruthy();
    expect(row.getByText("vision")).toBeTruthy();
    expect(row.getByText("reasoning")).toBeTruthy();
    expect(row.getByText("$3 in · $15 out /Mtok")).toBeTruthy();
    expect(row.getByText("200k")).toBeTruthy();
  });

  test("options are grouped under a provider head", async () => {
    const user = userEvent.setup();
    render(<ModelCatalog value="" onChange={vi.fn()} loadCatalog={vi.fn().mockResolvedValue(RICH)} />);

    await user.click(screen.getByRole("button", { name: "Change model" }));
    const combo = await screen.findByRole("combobox", { name: "Model" });
    // ArrowDown reveals the full (unfiltered) list so both provider runs show.
    await user.type(combo, "{arrowdown}");

    expect(await screen.findByText("anthropic")).toBeTruthy();
    expect(screen.getByText("openai")).toBeTruthy();
  });
});

describe("Recent section", () => {
  test("lists recent models and picking one reports its qualified id without typing", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    const withRecent: ModelCatalog = { models: [SONNET, GPT5], recent: [GPT5] };
    render(<ModelCatalog value="" onChange={onChange} loadCatalog={vi.fn().mockResolvedValue(withRecent)} />);

    await user.click(screen.getByRole("button", { name: "Change model" }));
    await user.click(await screen.findByRole("button", { name: "GPT-5" }));

    expect(onChange).toHaveBeenCalledWith("openai/gpt-5");
  });

  test("no Recent section renders when the envelope carries none", async () => {
    const user = userEvent.setup();
    render(<ModelCatalog value="" onChange={vi.fn()} loadCatalog={vi.fn().mockResolvedValue(RICH)} />);

    await user.click(screen.getByRole("button", { name: "Change model" }));
    await screen.findByRole("combobox", { name: "Model" });
    expect(screen.queryByText("Recent")).toBeNull();
  });
});

describe("diagnostics affordance", () => {
  test("provider diagnostics stay hidden until the affordance is opened on demand", async () => {
    const user = userEvent.setup();
    const withDiag: ModelCatalog = {
      models: [SONNET],
      recent: [],
      diagnostics: [{ provider: "kimi", message: "list models: HTTP 401" }],
    };
    render(<ModelCatalog value="" onChange={vi.fn()} loadCatalog={vi.fn().mockResolvedValue(withDiag)} />);

    await user.click(screen.getByRole("button", { name: "Change model" }));
    const toggle = await screen.findByRole("button", { name: /unavailable/i });
    expect(screen.queryByText(/HTTP 401/)).toBeNull();

    await user.click(toggle);
    expect(screen.getByText(/HTTP 401/)).toBeTruthy();
  });

  test("no diagnostics affordance renders when the envelope reports none", async () => {
    const user = userEvent.setup();
    render(<ModelCatalog value="" onChange={vi.fn()} loadCatalog={vi.fn().mockResolvedValue(RICH)} />);

    await user.click(screen.getByRole("button", { name: "Change model" }));
    await screen.findByRole("combobox", { name: "Model" });
    expect(screen.queryByRole("button", { name: /unavailable/i })).toBeNull();
  });
});
