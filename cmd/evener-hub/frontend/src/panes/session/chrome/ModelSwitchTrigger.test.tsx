import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { WireError } from "../../../protocol/errors";
import type { ModelCatalog } from "../../../widgets";
import { installMobileViewport } from "../testing/mobileViewport";
import { ModelSwitchTrigger } from "./ModelSwitchTrigger";
import rawStyles from "./modelswitch.module.css";

afterEach(cleanup);

function catalog(): ModelCatalog {
  return {
    models: [
      { provider: "anthropic", model: "claude-sonnet-4-5", displayName: "Sonnet 4.5" },
      { provider: "openai", model: "gpt-5.5", displayName: "GPT-5.5" },
    ],
    recent: [],
  };
}

function renderTrigger(overrides: Partial<Parameters<typeof ModelSwitchTrigger>[0]> = {}) {
  const props = {
    label: "anthropic/claude-sonnet-4-5",
    value: "anthropic/claude-sonnet-4-5",
    loadCatalog: vi.fn(async () => catalog()),
    onPick: vi.fn(),
    "data-testid": "trigger",
    valueTestId: "trigger-value",
    ...overrides,
  };
  return { props, ...render(<ModelSwitchTrigger {...props} />) };
}

// The label the caller supplies is the whole visible text; the action rides
// along visually-hidden so the spoken name says both. This is the contract
// ModelSwitch's own accessible-name test depends on, now owned here.
test("the label is the visible text and the spoken name also names the action", () => {
  renderTrigger();
  expect(screen.getByTestId("trigger-value").textContent).toBe("anthropic/claude-sonnet-4-5");
  expect(screen.getByRole("button", { name: "anthropic/claude-sonnet-4-5 — change model" })).toBe(
    screen.getByTestId("trigger"),
  );
});

test("an action label overrides only the visually hidden accessible suffix", () => {
  renderTrigger({ actionLabel: "change vision model" });
  expect(screen.getByRole("button", { name: "anthropic/claude-sonnet-4-5 — change vision model" })).toBe(
    screen.getByTestId("trigger"),
  );
});

// The whitespace text node between the chevron and the screen-reader suffix is
// load-bearing: each child's text is trimmed before the accessible name is
// concatenated, so without it the name runs together. Asserted as the rendered
// name rather than as markup, since the name is the thing that matters.
test("the accessible name does not run the label into the action", () => {
  renderTrigger({ label: "openai/gpt-5.5" });
  expect(screen.getByTestId("trigger").textContent).not.toContain("gpt-5.5— change model");
});

test("the label carries no box of its own - plain text, not a bordered chip", () => {
  renderTrigger();
  const label = screen.getByTestId("trigger-value");
  expect(label.className).toBe(rawStyles.value);
  expect(label.querySelector("*")).toBeNull();
});

// type="button": this trigger renders inside surfaces that can sit within a
// form (the spawn pane's advanced options reach a real one), and a default
// submit type would make opening the picker submit it.
test("the trigger is a non-submitting button", () => {
  renderTrigger();
  expect((screen.getByTestId("trigger") as HTMLButtonElement).type).toBe("button");
});

test("disabled refuses to open the picker at all", async () => {
  const user = userEvent.setup();
  const loadCatalog = vi.fn(async () => catalog());
  renderTrigger({ disabled: true, loadCatalog });

  expect((screen.getByTestId("trigger") as HTMLButtonElement).disabled).toBe(true);
  await user.click(screen.getByTestId("trigger"));
  expect(loadCatalog).not.toHaveBeenCalled();
  expect(screen.queryByRole("combobox")).toBeNull();
});

// Value-controlled: this component reports the pick and holds no model state of
// its own, so the caller decides what a pick MEANS (the session sends
// thread/model/set; the spawn pane records the choice for its next launch).
test("picking reports the entry to the caller and closes the picker without changing the label", async () => {
  const user = userEvent.setup();
  const onPick = vi.fn();
  renderTrigger({ onPick });

  await user.click(screen.getByTestId("trigger"));
  const combobox = await screen.findByRole("combobox");
  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(2));
  await user.clear(combobox);
  await user.keyboard("gpt");
  await user.click(await screen.findByRole("option", { name: /gpt-5\.5/i }));

  expect(onPick).toHaveBeenCalledWith(expect.objectContaining({ provider: "openai", model: "gpt-5.5" }));
  await waitFor(() => expect(screen.queryByRole("combobox")).toBeNull());
  // The label is the caller's to change; this component never rewrites it.
  expect(screen.getByTestId("trigger-value").textContent).toBe("anthropic/claude-sonnet-4-5");
});

test("the panel pre-fills and marks the caller's value, which need not be the label", async () => {
  const user = userEvent.setup();
  renderTrigger({ label: "(default)", value: "openai/gpt-5.5" });

  await user.click(screen.getByTestId("trigger"));
  const combobox = (await screen.findByRole("combobox")) as HTMLInputElement;
  expect(combobox.value).toBe("openai/gpt-5.5");
});

// A superseded load must never overwrite the open picker: open, close while
// the catalog is in flight (cwd/harness/credential change), reopen against a
// new loader scope, then let the STALE request land - the panel keeps the
// fresh catalog, not the dead one.
test("a stale catalog load never overwrites the reopened picker", async () => {
  const user = userEvent.setup();
  const stale: { resolve: ((c: ModelCatalog) => void) | null } = { resolve: null };
  const fresh: { resolve: ((c: ModelCatalog) => void) | null } = { resolve: null };
  const staleCatalog = catalog();
  const freshCatalog: ModelCatalog = {
    models: [{ provider: "fresh", model: "fresh-model", displayName: "fresh/fresh-model" }],
    recent: [],
  };
  const { rerender } = renderTrigger({
    loadCatalog: vi.fn(() => new Promise<ModelCatalog>((resolve) => (stale.resolve = resolve))),
  });

  await user.click(screen.getByTestId("trigger"));
  // Wait for the panel before dismissing: Escape must reach the mounted
  // panel's own handler, not the body behind a still-loading open.
  await screen.findByRole("combobox");
  await user.keyboard("{Escape}");
  await waitFor(() => expect(screen.queryByRole("combobox")).toBeNull());
  rerender(
    <ModelSwitchTrigger
      label="anthropic/claude-sonnet-4-5"
      value="anthropic/claude-sonnet-4-5"
      loadCatalog={vi.fn(() => new Promise<ModelCatalog>((resolve) => (fresh.resolve = resolve)))}
      onPick={vi.fn()}
      data-testid="trigger"
      valueTestId="trigger-value"
    />,
  );
  await user.click(screen.getByTestId("trigger"));
  // Fresh lands first, then the dead request: the stale one must not clobber it.
  const optionTexts = () => screen.queryAllByRole("option").map((o) => o.textContent);
  fresh.resolve?.(freshCatalog);
  await waitFor(() => expect(optionTexts()).toContain("fresh/fresh-model"));
  stale.resolve?.(staleCatalog);
  await act(async () => {});

  await waitFor(() => expect(optionTexts()).toContain("fresh/fresh-model"));
  expect(optionTexts().some((t) => t?.includes("GPT-5.5"))).toBe(false);
});

test("opening shows a loading state until the catalog resolves", async () => {
  const user = userEvent.setup();
  const box: { resolve: ((c: ModelCatalog) => void) | null } = { resolve: null };
  renderTrigger({ loadCatalog: vi.fn(() => new Promise<ModelCatalog>((resolve) => (box.resolve = resolve))) });

  await user.click(screen.getByTestId("trigger"));
  expect(screen.getByRole("status", { name: "Loading" })).toBeTruthy();
  box.resolve?.(catalog());
  await waitFor(() => expect(screen.getByRole("combobox")).toBeTruthy());
});

// A plain Error's own message is internal detail, so the framing replaces it
// with a generic sentence rather than leaking the raw text - the same framing
// (sessionActionHeadline + friendlyLaunchErrorMessage) the session's model
// switch has always used, now owned here for both callers.
test("a failed catalog load surfaces a friendly message inline, keeping the field and the trigger", async () => {
  const user = userEvent.setup();
  renderTrigger({
    loadCatalog: vi.fn(async () => {
      throw new Error("catalog boom");
    }),
  });

  await user.click(screen.getByTestId("trigger"));

  const alert = await screen.findByRole("alert");
  expect(alert.textContent).toBe("Couldn't load models: Something went wrong.");
  expect(alert.textContent).not.toMatch(/catalog boom/i);
  expect(screen.queryByRole("listbox")).toBeNull();
  expect(screen.getByRole("combobox")).toBeTruthy();
  expect(screen.getByTestId("trigger")).toBeTruthy();
});

// T3, the first-run worst moment: the hub answered but no agent daemon could be
// reached, so the headline names the session start and the detail is actionable
// copy rather than the launch-check's own raw text.
test("a daemon-missing load shows actionable copy under the session-start headline", async () => {
  const user = userEvent.setup();
  renderTrigger({
    loadCatalog: vi.fn(async () => {
      throw new WireError("evener launch-check timed out", -32014, { evenerErrorInfo: "hubLaunch" });
    }),
  });

  await user.click(screen.getByTestId("trigger"));

  const alert = await screen.findByRole("alert");
  expect(alert.textContent).toBe(
    "Couldn't start this session: No agent daemon responded for this project. Start one by running evener in the repo, then retry.",
  );
  expect(alert.textContent).not.toMatch(/launch-check timed out/i);
});

// Popover runs with autoFocus={false} so the panel's input owns focus, which
// makes restoring focus to the trigger on close this component's job.
test("Escape closes the picker and returns focus to the trigger", async () => {
  const user = userEvent.setup();
  renderTrigger();

  const trigger = screen.getByTestId("trigger");
  await user.click(trigger);
  await screen.findByRole("combobox");
  await user.keyboard("{Escape}");

  await waitFor(() => expect(screen.queryByRole("combobox")).toBeNull());
  expect(document.activeElement).toBe(trigger);
});

// The picker's own list scrolls and whatever is behind it scrolls too: neither
// may dismiss it mid-interaction (closeOnScroll={false}).
test("a scroll does not close the open picker", async () => {
  const user = userEvent.setup();
  renderTrigger();

  await user.click(screen.getByTestId("trigger"));
  await screen.findByRole("combobox");
  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(2));
  window.dispatchEvent(new Event("scroll"));

  expect(screen.getByRole("combobox")).toBeTruthy();
});

// --- mobile: the same trigger opens a bottom Sheet, not a Popover -------------
// docs/web-ui/design-system.md §11: mobile choice controls use the bottom Sheet
// pattern with >=48px options and no search input. Both surfaces that mount this
// component (the spawn pane's prompt card, the session composer's status row)
// get it from this one branch.

test("on a mobile viewport the trigger opens a Choose-model bottom Sheet, not a Popover", async () => {
  const restoreViewport = installMobileViewport();
  const user = userEvent.setup();
  try {
    renderTrigger();

    await user.click(screen.getByTestId("trigger"));

    const sheet = await screen.findByRole("dialog", { name: "Choose model" });
    expect(sheet.className).toContain("bottom");
    expect(screen.queryByRole("combobox")).toBeNull();
    expect(within(sheet).getAllByRole("option")).toHaveLength(2);
  } finally {
    restoreViewport();
  }
});

test("picking from the mobile sheet reports the entry and closes it", async () => {
  const restoreViewport = installMobileViewport();
  const user = userEvent.setup();
  const onPick = vi.fn();
  try {
    renderTrigger({ onPick });

    await user.click(screen.getByTestId("trigger"));
    const sheet = await screen.findByRole("dialog", { name: "Choose model" });
    await user.click(within(sheet).getByRole("option", { name: /gpt-5\.5/i }));

    expect(onPick).toHaveBeenCalledWith(expect.objectContaining({ provider: "openai", model: "gpt-5.5" }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Choose model" })).toBeNull());
  } finally {
    restoreViewport();
  }
});

test("Escape closes the mobile sheet and returns focus to the trigger", async () => {
  const restoreViewport = installMobileViewport();
  const user = userEvent.setup();
  try {
    renderTrigger();

    const trigger = screen.getByTestId("trigger");
    await user.click(trigger);
    await screen.findByRole("dialog", { name: "Choose model" });
    await user.keyboard("{Escape}");

    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Choose model" })).toBeNull());
    expect(document.activeElement).toBe(trigger);
  } finally {
    restoreViewport();
  }
});
