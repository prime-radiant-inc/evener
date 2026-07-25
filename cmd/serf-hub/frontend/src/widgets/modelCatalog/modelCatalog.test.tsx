import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi } from "vitest";
// ModelCatalog is both the component (value) and the envelope interface (type);
// a single import brings in both meanings via declaration merging.
import { ModelCatalog, type ModelCatalogEntry } from "./index";

afterEach(() => cleanup());

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
const HAIKU: ModelCatalogEntry = {
  provider: "anthropic",
  model: "claude-haiku-4-5",
  displayName: "Claude Haiku 4.5",
  supportsTools: true,
  inputCostPerMillion: 1,
  outputCostPerMillion: 5,
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
const CATALOG: ModelCatalog = { models: [SONNET, HAIKU, GPT5], recent: [] };

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

function openTrigger() {
  return screen.getByRole("button", { name: /change model/i });
}

async function openPicker(user: ReturnType<typeof userEvent.setup>): Promise<HTMLInputElement> {
  await user.click(openTrigger());
  return (await screen.findByRole("combobox", { name: "Model" })) as HTMLInputElement;
}

// --- closed state (unchanged: the chip IS the trigger) ---------------------

test("shows the interim default marker when no model is chosen", () => {
  renderPicker();
  expect(screen.getByText("(default)")).toBeTruthy();
});

// The visible label is the model id and the trigger's action rides along
// visually-hidden, so the spoken name is their concatenation - with a real
// space between them, which only a separating text node can supply (the name
// computation trims each child's own text; see this widget's own trigger).
test("the trigger's spoken name separates the value from the action", () => {
  renderPicker({ value: "openai/gpt-5" });
  expect(screen.getByRole("button", { name: "openai/gpt-5 — change model" })).toBe(openTrigger());
});

test("shows the qualified provider/model when a model is set", () => {
  renderPicker({ value: "openai/gpt-5" });
  expect(screen.getByText("openai/gpt-5")).toBeTruthy();
});

test("the closed state has no separate Change-model button - the chip itself is the trigger", () => {
  renderPicker({ value: "openai/gpt-5" });
  expect(screen.queryByRole("button", { name: "Change model" })).toBeNull();
  expect(openTrigger()).toBeTruthy();
});

test("clicking the chip trigger opens the panel as a portaled overlay, not an inline sibling that reflows", async () => {
  const user = userEvent.setup();
  renderPicker({ value: "openai/gpt-5" });

  const triggerWrapper = openTrigger().parentElement;
  expect(triggerWrapper).not.toBeNull();
  const combo = await openPicker(user);

  expect(openTrigger().parentElement).toBe(triggerWrapper);
  let ancestor: HTMLElement | null = combo.parentElement;
  let reachedBody = false;
  while (ancestor) {
    expect(ancestor).not.toBe(triggerWrapper);
    if (ancestor === document.body) {
      reachedBody = true;
      break;
    }
    ancestor = ancestor.parentElement;
  }
  expect(reachedBody).toBe(true);
});

// --- the list is expanded on open, no keystroke needed ---------------------

describe("open state", () => {
  test("renders the FULL grouped list immediately, with no typing or arrow key", async () => {
    const user = userEvent.setup();
    renderPicker();

    await openPicker(user);

    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
    expect(screen.getByText("anthropic")).toBeTruthy();
    expect(screen.getByText("openai")).toBeTruthy();
  });

  test("an option carries its capabilities, cost, and context window as small text", async () => {
    const user = userEvent.setup();
    renderPicker();

    await openPicker(user);
    const row = (await screen.findByText("Claude Sonnet 4.5")).closest("li");
    if (!row) throw new Error("expected the Sonnet option to render inside a listbox <li>");

    expect(within(row).getByText("tools · vision · reasoning · $3 in · $15 out /Mtok · 200k")).toBeTruthy();
  });

  test("there is no Cancel button", async () => {
    const user = userEvent.setup();
    renderPicker({ value: "openai/gpt-5" });

    await openPicker(user);

    expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull();
  });

  test("surfaces an inline error when the catalog fails to load", async () => {
    const user = userEvent.setup();
    renderPicker({ loadCatalog: vi.fn().mockRejectedValue(new Error("providers unavailable")) });

    await user.click(openTrigger());

    expect(await screen.findByText(/providers unavailable/i)).toBeTruthy();
  });
});

// --- the input replaces the previously-selected value ---------------------

describe("input pre-fill", () => {
  test("opens pre-filled with the current qualified value, focused, and fully selected", async () => {
    const user = userEvent.setup();
    renderPicker({ value: "openai/gpt-5" });

    const combo = await openPicker(user);

    await waitFor(() => expect(document.activeElement).toBe(combo));
    expect(combo.value).toBe("openai/gpt-5");
    expect(combo.selectionStart).toBe(0);
    expect(combo.selectionEnd).toBe("openai/gpt-5".length);
  });

  test("the pre-filled value does NOT pre-filter the list", async () => {
    const user = userEvent.setup();
    renderPicker({ value: "openai/gpt-5" });

    await openPicker(user);

    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
  });

  test("the first keystroke REPLACES the pre-filled value", async () => {
    const user = userEvent.setup();
    renderPicker({ value: "openai/gpt-5" });

    const combo = await openPicker(user);
    // Type into the already-focused input (never user.type, which clicks
    // first and collapses the selection to the caret) - this is exactly the
    // keystroke-over-selection the pre-fill exists for.
    await user.keyboard("haiku");

    expect(combo.value).toBe("haiku");
  });
});

// --- typing filters in place -----------------------------------------------

describe("filtering", () => {
  test("narrows the list and drops the group heads left empty", async () => {
    const user = userEvent.setup();
    renderPicker();

    await openPicker(user);
    await user.keyboard("sonnet");

    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(1));
    expect(screen.getByText("anthropic")).toBeTruthy();
    expect(screen.queryByText("openai")).toBeNull();
  });

  test("clearing the query restores the full list", async () => {
    const user = userEvent.setup();
    renderPicker();

    const combo = await openPicker(user);
    await user.keyboard("sonnet");
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(1));
    await user.clear(combo);

    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
  });
});

// --- Recent is the first group --------------------------------------------

describe("Recent group", () => {
  test("renders first, with the provider in the row's small text, and picks without typing", async () => {
    const user = userEvent.setup();
    const withRecent: ModelCatalog = { models: [SONNET, HAIKU, GPT5], recent: [GPT5] };
    const { onChange } = renderPicker({ loadCatalog: vi.fn().mockResolvedValue(withRecent) });

    await openPicker(user);
    const heads = await screen.findAllByText(/^(Recent|anthropic|openai)$/);
    expect(heads[0]?.textContent).toBe("Recent");
    const options = screen.getAllByRole("option");
    expect(options[0]?.textContent).toContain("GPT-5");
    expect(options[0]?.textContent).toContain("openai");

    await user.click(options[0] as HTMLElement);

    expect(onChange).toHaveBeenCalledWith("openai/gpt-5");
  });

  test("no Recent group renders when the envelope carries none", async () => {
    const user = userEvent.setup();
    renderPicker();

    await openPicker(user);
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));

    expect(screen.queryByText("Recent")).toBeNull();
  });
});

// --- unavailable providers, in place, in small text ------------------------

describe("unavailable providers", () => {
  test("render as non-interactive in-place lines carrying provider and message", async () => {
    const user = userEvent.setup();
    const withDiag: ModelCatalog = {
      models: [SONNET],
      recent: [],
      diagnostics: [{ provider: "ollama", message: "connection refused", hint: "Is it running?" }],
    };
    renderPicker({ loadCatalog: vi.fn().mockResolvedValue(withDiag) });

    await openPicker(user);

    // The wire's `hint` is generic boilerplate, so it stays out of the list.
    const line = await screen.findByText("ollama — connection refused");
    expect(line.closest("li")?.getAttribute("role")).toBe("presentation");
    // No toggle button gating them anymore.
    expect(screen.queryByRole("button", { name: /unavailable/i })).toBeNull();
  });

  test("no unavailable lines render when the envelope reports none", async () => {
    const user = userEvent.setup();
    renderPicker();

    await openPicker(user);
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));

    // Scoped to the list: the trigger's own "— change model" screen-reader
    // text carries an em dash too, and it is not a diagnostic.
    expect(within(screen.getByRole("listbox")).queryByText(/—/)).toBeNull();
  });
});

// --- the current value is marked and scrolled into view -------------------

describe("current value", () => {
  test("marks the current row with aria-selected and a check glyph", async () => {
    const user = userEvent.setup();
    renderPicker({ value: "openai/gpt-5" });

    await openPicker(user);

    const current = await waitFor(() => screen.getByRole("option", { selected: true }));
    expect(current.textContent).toContain("GPT-5");
    expect(within(current).getByText("✓")).toBeTruthy();
  });

  // A single-select listbox may have exactly ONE aria-selected option, but the
  // current model legitimately appears twice when it's also in Recent.
  test("marks only the FIRST occurrence when the current model is also in Recent", async () => {
    const user = userEvent.setup();
    const withRecent: ModelCatalog = { models: [SONNET, HAIKU, GPT5], recent: [GPT5] };
    renderPicker({ value: "openai/gpt-5", loadCatalog: vi.fn().mockResolvedValue(withRecent) });

    await openPicker(user);
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(4));

    const selected = screen.getAllByRole("option", { selected: true });
    expect(selected).toHaveLength(1);
    expect(selected[0]).toBe(screen.getAllByRole("option")[0]); // the Recent copy
  });

  test("scrolls the current row into view on open", async () => {
    const user = userEvent.setup();
    // jsdom implements no scrollIntoView at all, so the panel calls it
    // optionally; stub it to observe the call.
    const scrollSpy = vi.fn();
    HTMLElement.prototype.scrollIntoView = scrollSpy as unknown as typeof HTMLElement.prototype.scrollIntoView;
    try {
      renderPicker({ value: "openai/gpt-5" });
      await openPicker(user);
      await waitFor(() => expect(scrollSpy).toHaveBeenCalled());
    } finally {
      // @ts-expect-error restore jsdom's honest absence of scrollIntoView
      delete HTMLElement.prototype.scrollIntoView;
    }
  });
});

// --- keyboard --------------------------------------------------------------

describe("keyboard", () => {
  test("ArrowDown/ArrowUp walk the options and never land on a group head or an unavailable line", async () => {
    const user = userEvent.setup();
    const withDiag: ModelCatalog = {
      models: [SONNET, HAIKU, GPT5],
      recent: [],
      diagnostics: [{ provider: "ollama", message: "connection refused" }],
    };
    renderPicker({ loadCatalog: vi.fn().mockResolvedValue(withDiag) });

    const combo = await openPicker(user);
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));

    const activeText = () => {
      const id = combo.getAttribute("aria-activedescendant");
      return id ? document.getElementById(id)?.textContent : null;
    };
    await user.keyboard("{ArrowDown}");
    expect(activeText()).toContain("Claude Sonnet 4.5");
    // Past the end of the list: clamps on the LAST option, never the
    // unavailable line that follows it.
    await user.keyboard("{ArrowDown}{ArrowDown}{ArrowDown}{ArrowDown}");
    expect(activeText()).toContain("GPT-5");
    await user.keyboard("{ArrowUp}");
    expect(activeText()).toContain("Claude Haiku 4.5");
  });

  test("Home and End jump to the first and last option", async () => {
    const user = userEvent.setup();
    renderPicker();

    const combo = await openPicker(user);
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
    const activeText = () => {
      const id = combo.getAttribute("aria-activedescendant");
      return id ? document.getElementById(id)?.textContent : null;
    };

    await user.keyboard("{End}");
    expect(activeText()).toContain("GPT-5");
    await user.keyboard("{Home}");
    expect(activeText()).toContain("Claude Sonnet 4.5");
  });

  test("Enter picks the highlighted option", async () => {
    const user = userEvent.setup();
    const { onChange } = renderPicker();

    await openPicker(user);
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
    await user.keyboard("gpt{Enter}");

    expect(onChange).toHaveBeenCalledWith("openai/gpt-5");
    expect(screen.queryByRole("combobox", { name: "Model" })).toBeNull();
  });

  test("Escape closes the picker without changing the value", async () => {
    const user = userEvent.setup();
    const { onChange } = renderPicker({ value: "openai/gpt-5" });

    await openPicker(user);
    await user.keyboard("{Escape}");

    await waitFor(() => expect(screen.queryByRole("combobox", { name: "Model" })).toBeNull());
    expect(screen.getByText("openai/gpt-5")).toBeTruthy();
    expect(onChange).not.toHaveBeenCalled();
  });

  // Popover's FocusScope is opted out of focus management (autoFocus={false})
  // so the panel's input can own focus AND its text selection - which makes
  // returning focus to the trigger on close ModelCatalog's own job. Without
  // it, focus falls to <body> and a keyboard user is stranded.
  test("closing returns focus to the trigger", async () => {
    const user = userEvent.setup();
    renderPicker({ value: "openai/gpt-5" });
    const trigger = openTrigger();

    await openPicker(user);
    await user.keyboard("{Escape}");

    await waitFor(() => expect(screen.queryByRole("combobox", { name: "Model" })).toBeNull());
    expect(document.activeElement).toBe(trigger);
  });
});

// --- picking with the mouse -----------------------------------------------

test("clicking an option reports the qualified id and closes the picker", async () => {
  const user = userEvent.setup();
  const { onChange } = renderPicker();

  await openPicker(user);
  await user.click(await screen.findByText("GPT-5"));

  expect(onChange).toHaveBeenCalledWith("openai/gpt-5");
  expect(screen.queryByRole("combobox", { name: "Model" })).toBeNull();
});

// --- scrolling never dismisses -------------------------------------------

test("a scroll does not dismiss the open picker", async () => {
  const user = userEvent.setup();
  renderPicker();

  await openPicker(user);
  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
  window.dispatchEvent(new Event("scroll"));

  expect(screen.getByRole("combobox", { name: "Model" })).toBeTruthy();
  expect(screen.getAllByRole("option")).toHaveLength(3);
});
