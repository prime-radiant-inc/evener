import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { Dialog } from "../dialog";
import { Combobox, type ComboboxOption } from "./index";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

function setupUser() {
  return userEvent.setup({ delay: null, advanceTimers: vi.advanceTimersByTime });
}

// userEvent.type() hangs under fake timers (some internal yield never
// resolves even with delay: null - a known friction point, see the
// tooltip/toast widgets' tests for the same fix), so typing-driven tests
// use fireEvent.change directly instead. Keyboard-navigation tests
// (ArrowDown/Up/Enter/Escape/Tab via user.keyboard()/user.tab()) are
// unaffected and keep using userEvent.
function type(input: HTMLElement, value: string) {
  fireEvent.change(input, { target: { value } });
}

// The debounce timer fires outside any React-tracked event, so advancing
// it must be wrapped in act() or the resulting state update isn't flushed
// before the next assertion reads the DOM.
function advance(ms: number) {
  act(() => {
    vi.advanceTimersByTime(ms);
  });
}

const MODELS: ComboboxOption[] = [
  { id: "opus", label: "Claude Opus" },
  { id: "sonnet", label: "Claude Sonnet" },
  { id: "haiku", label: "Claude Haiku" },
];

// A native <label> wrapping the whole widget gives the input an accessible
// name via descendant containment, without Combobox needing a label prop
// of its own (not in the locked API) - see this task's report.
function renderCombobox(props: Partial<Parameters<typeof Combobox>[0]> = {}) {
  return render(
    <label>
      Model
      <Combobox
        options={props.options ?? []}
        onQuery={props.onQuery ?? vi.fn()}
        onPick={props.onPick ?? vi.fn()}
        renderOption={props.renderOption}
      />
    </label>,
  );
}

test("renders an ARIA 1.2 combobox input, closed and collapsed initially", () => {
  renderCombobox();
  const input = screen.getByRole("combobox", { name: "Model" });
  expect(input.getAttribute("aria-expanded")).toBe("false");
  expect(input.getAttribute("aria-autocomplete")).toBe("list");
  expect(input.getAttribute("aria-activedescendant")).toBeNull();
});

test("typing debounces onQuery by 150ms, firing once with the final value", () => {
  vi.useFakeTimers();
  const onQuery = vi.fn();
  renderCombobox({ onQuery });
  const input = screen.getByRole("combobox", { name: "Model" });

  // Three separate change events, as three keystrokes would produce -
  // debouncing must collapse them into exactly one call, 150ms after the
  // last one, carrying only the final value.
  type(input, "c");
  advance(50);
  type(input, "cl");
  advance(50);
  type(input, "cla");
  expect(onQuery).not.toHaveBeenCalled();

  advance(150);
  expect(onQuery).toHaveBeenCalledOnce();
  expect(onQuery).toHaveBeenCalledWith("cla");
});

test("does not show a listbox while there are no options, even while typing", () => {
  renderCombobox({ options: [] });
  type(screen.getByRole("combobox", { name: "Model" }), "cla");
  expect(screen.queryByRole("listbox")).toBeNull();
});

test("shows the listbox once options are non-empty", () => {
  const { rerender } = renderCombobox({ options: [] });
  const input = screen.getByRole("combobox", { name: "Model" });
  // Typing (rather than a bare focus) is what opens the popup - matches
  // the realistic flow of query text driving results, and see the
  // "ArrowDown/ArrowUp on an idle-but-populated combobox" tests below for
  // the other way to open it (browsing without typing).
  type(input, "c");
  expect(screen.queryByRole("listbox")).toBeNull(); // no options yet

  rerender(
    <label>
      Model
      <Combobox options={MODELS} onQuery={vi.fn()} onPick={vi.fn()} />
    </label>,
  );
  expect(screen.getByRole("listbox")).toBeTruthy();
  expect(screen.getAllByRole("option")).toHaveLength(3);
  expect(input.getAttribute("aria-expanded")).toBe("true");
});

test("ArrowDown on an idle-but-populated combobox opens and highlights the first option", async () => {
  const user = setupUser();
  renderCombobox({ options: MODELS });
  const input = screen.getByRole("combobox", { name: "Model" });
  input.focus();
  await user.keyboard("{ArrowDown}");
  const first = screen.getByRole("option", { name: "Claude Opus" });
  expect(input.getAttribute("aria-activedescendant")).toBe(first.id);
});

test("ArrowUp on an idle-but-populated combobox opens and highlights the last option", async () => {
  const user = setupUser();
  renderCombobox({ options: MODELS });
  screen.getByRole("combobox", { name: "Model" }).focus();
  await user.keyboard("{ArrowUp}");
  const last = screen.getByRole("option", { name: "Claude Haiku" });
  expect(screen.getByRole("combobox").getAttribute("aria-activedescendant")).toBe(last.id);
});

test("ArrowDown steps forward and clamps at the last option (no wrap)", async () => {
  const user = setupUser();
  renderCombobox({ options: MODELS });
  const input = screen.getByRole("combobox", { name: "Model" });
  input.focus();
  await user.keyboard("{ArrowDown}{ArrowDown}{ArrowDown}{ArrowDown}");
  const last = screen.getByRole("option", { name: "Claude Haiku" });
  expect(input.getAttribute("aria-activedescendant")).toBe(last.id);
});

test("ArrowUp steps backward and clamps at the first option (no wrap)", async () => {
  const user = setupUser();
  renderCombobox({ options: MODELS });
  const input = screen.getByRole("combobox", { name: "Model" });
  input.focus();
  await user.keyboard("{ArrowDown}{ArrowDown}{ArrowUp}{ArrowUp}{ArrowUp}");
  const first = screen.getByRole("option", { name: "Claude Opus" });
  expect(input.getAttribute("aria-activedescendant")).toBe(first.id);
});

test("Enter on the active option calls onPick, fills the input, and closes the popup", async () => {
  const user = setupUser();
  const onPick = vi.fn();
  renderCombobox({ options: MODELS, onPick });
  const input = screen.getByRole("combobox", { name: "Model" }) as HTMLInputElement;
  input.focus();
  await user.keyboard("{ArrowDown}{ArrowDown}{Enter}");
  expect(onPick).toHaveBeenCalledOnce();
  expect(onPick).toHaveBeenCalledWith(MODELS[1]);
  expect(input.value).toBe("Claude Sonnet");
  expect(screen.queryByRole("listbox")).toBeNull();
});

test("Enter with no active option does not call onPick", async () => {
  const user = setupUser();
  const onPick = vi.fn();
  renderCombobox({ options: MODELS, onPick });
  screen.getByRole("combobox", { name: "Model" }).focus();
  await user.keyboard("{Enter}");
  expect(onPick).not.toHaveBeenCalled();
});

test("clicking an option calls onPick, fills the input, closes the popup, and keeps input focus", async () => {
  const user = setupUser();
  const onPick = vi.fn();
  renderCombobox({ options: MODELS, onPick });
  const input = screen.getByRole("combobox", { name: "Model" }) as HTMLInputElement;
  input.focus();
  await user.keyboard("{ArrowDown}"); // open the popup
  await user.click(screen.getByRole("option", { name: "Claude Haiku" }));
  expect(onPick).toHaveBeenCalledOnce();
  expect(onPick).toHaveBeenCalledWith(MODELS[2]);
  expect(input.value).toBe("Claude Haiku");
  expect(screen.queryByRole("listbox")).toBeNull();
  expect(document.activeElement).toBe(input);
});

test("Escape closes the popup without clearing the typed text", async () => {
  const user = setupUser();
  renderCombobox({ options: MODELS });
  const input = screen.getByRole("combobox", { name: "Model" }) as HTMLInputElement;
  type(input, "cla");
  input.focus();
  expect(screen.getByRole("listbox")).toBeTruthy();
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("listbox")).toBeNull();
  expect(input.value).toBe("cla");
});

test("blurring the input closes the popup", async () => {
  const user = setupUser();
  render(
    <div>
      <label>
        Model
        <Combobox options={MODELS} onQuery={vi.fn()} onPick={vi.fn()} />
      </label>
      <button type="button">Elsewhere</button>
    </div>,
  );
  const input = screen.getByRole("combobox", { name: "Model" });
  input.focus();
  await user.keyboard("{ArrowDown}");
  expect(screen.getByRole("listbox")).toBeTruthy();
  await user.tab();
  expect(screen.queryByRole("listbox")).toBeNull();
  expect(document.activeElement).toBe(screen.getByRole("button", { name: "Elsewhere" }));
});

test("renderOption customizes option content; the default rendering is the option's label", () => {
  const { rerender } = renderCombobox({ options: MODELS });
  type(screen.getByRole("combobox", { name: "Model" }), "c");
  expect(screen.getByRole("option", { name: "Claude Opus" })).toBeTruthy();

  rerender(
    <label>
      Model
      <Combobox
        options={MODELS}
        onQuery={vi.fn()}
        onPick={vi.fn()}
        renderOption={(option) => <em>{option.label.toUpperCase()}</em>}
      />
    </label>,
  );
  expect(screen.getByText("CLAUDE OPUS").tagName).toBe("EM");
});

test("declares a :focus-visible rule in its CSS module, using only tokens", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "combobox.module.css"), "utf8");
  expect(css).toContain(":focus-visible");
});

// --- fix-wave: options shrinking under a stale activeIndex (Critical) ---

test("options shrinking below the active index does not crash, clears activedescendant, and makes Enter a no-op", async () => {
  const user = setupUser();
  const onPick = vi.fn();
  const many: ComboboxOption[] = [
    { id: "a", label: "Alpha" },
    { id: "b", label: "Bravo" },
    { id: "c", label: "Charlie" },
    { id: "d", label: "Delta" },
  ];
  const { rerender } = renderCombobox({ options: many, onPick });
  const input = screen.getByRole("combobox", { name: "Model" }) as HTMLInputElement;
  input.focus();
  await user.keyboard("{ArrowDown}{ArrowDown}{ArrowDown}"); // activeIndex -> 2 ("Charlie")
  expect(input.getAttribute("aria-activedescendant")).toContain("-c");

  const fewer: ComboboxOption[] = [{ id: "a", label: "Alpha" }];
  expect(() =>
    rerender(
      <label>
        Model
        <Combobox options={fewer} onQuery={vi.fn()} onPick={onPick} />
      </label>,
    ),
  ).not.toThrow();

  expect(input.getAttribute("aria-activedescendant")).toBeNull();

  await user.keyboard("{Enter}");
  expect(onPick).not.toHaveBeenCalled();
});

// --- fix-wave: nested-overlay Escape containment (Important) -----------

test("Escape closes only the combobox's own popup when nested in a Dialog; a second Escape then closes the Dialog", async () => {
  const user = setupUser();
  const onDialogClose = vi.fn();
  render(
    <Dialog open onClose={onDialogClose} title="Pick a model">
      <label>
        Model
        <Combobox options={MODELS} onQuery={vi.fn()} onPick={vi.fn()} />
      </label>
    </Dialog>,
  );
  const input = screen.getByRole("combobox", { name: "Model" });
  input.focus();
  await user.keyboard("{ArrowDown}");
  expect(screen.getByRole("listbox")).toBeTruthy();

  await user.keyboard("{Escape}");
  expect(screen.queryByRole("listbox")).toBeNull();
  expect(onDialogClose).not.toHaveBeenCalled();

  await user.keyboard("{Escape}");
  expect(onDialogClose).toHaveBeenCalledOnce();
});

// --- fix-wave: aria-label / aria-labelledby forwarding (controller-approved) ---

test("forwards aria-label to the input", () => {
  render(<Combobox options={[]} onQuery={vi.fn()} onPick={vi.fn()} aria-label="Model" />);
  expect(screen.getByRole("combobox", { name: "Model" })).toBeTruthy();
});

test("forwards aria-labelledby to the input", () => {
  render(
    <div>
      <span id="model-label">Model</span>
      <Combobox options={[]} onQuery={vi.fn()} onPick={vi.fn()} aria-labelledby="model-label" />
    </div>,
  );
  expect(screen.getByRole("combobox", { name: "Model" })).toBeTruthy();
});

// --- fix-wave: role=listbox accessible name (Important) ----------------
// The popup listbox had no name of its own - same gap as an unlabelled
// role=menu (see menu.test.tsx). It carries the same aria-label/
// aria-labelledby the input was given, rather than a separate id: the
// listbox and its input describe the same one picker, so there is exactly
// one label source to point both at, not two independent ones to keep in
// sync.
test("the popup listbox shares the input's aria-label", async () => {
  const user = setupUser();
  render(<Combobox options={MODELS} onQuery={vi.fn()} onPick={vi.fn()} aria-label="Model" />);
  screen.getByRole("combobox", { name: "Model" }).focus();
  await user.keyboard("{ArrowDown}");
  expect(screen.getByRole("listbox", { name: "Model" })).toBeTruthy();
});

test("the popup listbox shares the input's aria-labelledby", async () => {
  const user = setupUser();
  render(
    <div>
      <span id="model-label">Model</span>
      <Combobox options={MODELS} onQuery={vi.fn()} onPick={vi.fn()} aria-labelledby="model-label" />
    </div>,
  );
  screen.getByRole("combobox", { name: "Model" }).focus();
  await user.keyboard("{ArrowDown}");
  expect(screen.getByRole("listbox", { name: "Model" })).toBeTruthy();
});
