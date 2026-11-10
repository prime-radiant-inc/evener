import { WireError } from "@evener/appwire-client";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { expect, test, vi } from "vitest";
import { Button, Dialog, type ModelCatalog } from "../../../widgets";
import { installMobileViewport } from "../testing/mobileViewport";
import { ModelSwitchTrigger } from "./ModelSwitchTrigger";
import rawStyles from "./modelswitch.module.css";

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

// The picker lists models and nothing else: provider setup is not one of the
// choices, so the picker offers no connect entry on either surface that mounts
// this component. Connecting a provider happens where it has a real flow (the
// spawn pane's own notices, the credentials pane) - never mid-list.
test("the open picker offers no connect-provider entry", async () => {
  const user = userEvent.setup();
  renderTrigger();

  await user.click(screen.getByTestId("trigger"));
  await screen.findByRole("combobox");

  expect(screen.queryByRole("button", { name: "Connect another provider" })).toBeNull();
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
  expect(document.activeElement).toBe(screen.getByTestId("trigger"));
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
// A loader-identity change while the picker is OPEN (Spawn recreates
// loadCatalog on cwd/harness/credential change) restarts the load against
// the new scope: the stale in-flight request must neither populate the
// panel nor clobber the fresh catalog when it lands late.
test("a loader change while open reloads against the new scope", async () => {
  const user = userEvent.setup();
  const stale: { resolve: ((c: ModelCatalog) => void) | null } = { resolve: null };
  const fresh: { resolve: ((c: ModelCatalog) => void) | null } = { resolve: null };
  const freshCatalog: ModelCatalog = {
    models: [{ provider: "fresh", model: "fresh-model", displayName: "fresh/fresh-model" }],
    recent: [],
  };
  const staleLoad = vi.fn(() => new Promise<ModelCatalog>((resolve) => (stale.resolve = resolve)));
  const freshLoad = vi.fn(() => new Promise<ModelCatalog>((resolve) => (fresh.resolve = resolve)));
  const { rerender } = renderTrigger({ loadCatalog: staleLoad });

  await user.click(screen.getByTestId("trigger"));
  await screen.findByRole("combobox");
  // The scope changes under the open picker: new loader identity, same mount.
  rerender(
    <ModelSwitchTrigger
      label="anthropic/claude-sonnet-4-5"
      value="anthropic/claude-sonnet-4-5"
      loadCatalog={freshLoad}
      onPick={vi.fn()}
      data-testid="trigger"
      valueTestId="trigger-value"
    />,
  );

  // The new scope's loader runs without reopening.
  await waitFor(() => expect(freshLoad).toHaveBeenCalledTimes(1));
  const optionTexts = () => screen.queryAllByRole("option").map((o) => o.textContent);
  fresh.resolve?.(freshCatalog);
  await waitFor(() => expect(optionTexts()).toContain("fresh/fresh-model"));
  // The dead request lands late and must change nothing.
  stale.resolve?.(catalog());
  await act(async () => {});

  await waitFor(() => expect(optionTexts()).toContain("fresh/fresh-model"));
  expect(optionTexts().some((t) => t?.includes("GPT-5.5"))).toBe(false);
});

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

test("a failed catalog load retries through the current loader without opening connection settings", async () => {
  const user = userEvent.setup();
  const onOpenConnection = vi.fn();
  const loadCatalog = vi
    .fn<Parameters<typeof ModelSwitchTrigger>[0]["loadCatalog"]>()
    .mockRejectedValueOnce(new Error("catalog boom"))
    .mockResolvedValueOnce(catalog());
  renderTrigger({ loadCatalog, onOpenConnection });

  await user.click(screen.getByTestId("trigger"));
  await screen.findByRole("alert");
  await user.click(screen.getByRole("button", { name: "Retry models" }));

  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(2));
  expect(loadCatalog).toHaveBeenNthCalledWith(2, true);
  expect(onOpenConnection).not.toHaveBeenCalled();
});

test("a retry result from a replaced loader cannot publish into the new scope", async () => {
  const user = userEvent.setup();
  let resolveRetry: (value: ModelCatalog) => void = () => {};
  const oldLoader = vi
    .fn<Parameters<typeof ModelSwitchTrigger>[0]["loadCatalog"]>()
    .mockRejectedValueOnce(new Error("catalog boom"))
    .mockImplementationOnce(
      () =>
        new Promise<ModelCatalog>((resolve) => {
          resolveRetry = resolve;
        }),
    );
  const newLoader = vi.fn(async () => ({
    models: [{ provider: "fresh", model: "current", displayName: "Current model" }],
    recent: [],
  }));
  const { props, rerender } = renderTrigger({ loadCatalog: oldLoader });
  await user.click(screen.getByTestId("trigger"));
  await screen.findByRole("alert");
  await user.click(screen.getByRole("button", { name: "Retry models" }));

  rerender(<ModelSwitchTrigger {...props} loadCatalog={newLoader} />);
  expect(await screen.findByRole("option", { name: /Current model/ })).toBeTruthy();
  await act(async () => resolveRetry(catalog()));

  expect(screen.getAllByRole("option")).toHaveLength(1);
  expect(screen.queryByRole("option", { name: /GPT-5.5/ })).toBeNull();
});

test("an empty catalogue opens the current connection settings and returns focus to the trigger", async () => {
  const user = userEvent.setup();
  const onOpenConnection = vi.fn();
  const loadCatalog = vi.fn(async () => ({ models: [], recent: [] }));
  renderTrigger({ loadCatalog, onOpenConnection });
  await user.click(screen.getByTestId("trigger"));

  expect(await screen.findByRole("listbox")).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "Connection settings" }));
  expect(onOpenConnection).toHaveBeenCalledTimes(1);
  expect(screen.queryByRole("combobox")).toBeNull();
  expect(document.activeElement).toBe(screen.getByTestId("trigger"));
});

test("an empty catalogue retries through the same loader", async () => {
  const user = userEvent.setup();
  const loadCatalog = vi.fn(async () => ({ models: [], recent: [] }));
  renderTrigger({ loadCatalog });
  await user.click(screen.getByTestId("trigger"));
  await screen.findByRole("listbox");

  await user.click(screen.getByRole("button", { name: "Retry models" }));

  await waitFor(() => expect(loadCatalog).toHaveBeenNthCalledWith(2, true));
});

test("recovery actions use the latest loader and connection callback identities", async () => {
  const user = userEvent.setup();
  const firstLoader = vi.fn(async () => ({ models: [], recent: [] }));
  const nextLoader = vi.fn(async () => ({ models: [], recent: [] }));
  const firstConnection = vi.fn();
  const nextConnection = vi.fn();
  const { props, rerender } = renderTrigger({ loadCatalog: firstLoader, onOpenConnection: firstConnection });
  await user.click(screen.getByTestId("trigger"));
  await screen.findByRole("listbox");

  rerender(<ModelSwitchTrigger {...props} loadCatalog={nextLoader} onOpenConnection={nextConnection} />);
  await waitFor(() => expect(nextLoader).toHaveBeenCalledTimes(1));
  await user.click(screen.getByRole("button", { name: "Connection settings" }));

  expect(firstLoader).toHaveBeenCalledTimes(1);
  expect(nextLoader).toHaveBeenCalledTimes(1);
  expect(firstConnection).not.toHaveBeenCalled();
  expect(nextConnection).toHaveBeenCalledTimes(1);
});

test("a pick uses the latest callback identity", async () => {
  const user = userEvent.setup();
  const firstPick = vi.fn();
  const nextPick = vi.fn();
  const { props, rerender } = renderTrigger({ onPick: firstPick });
  await user.click(screen.getByTestId("trigger"));
  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(2));
  rerender(<ModelSwitchTrigger {...props} onPick={nextPick} />);
  await user.click(screen.getByRole("option", { name: /GPT-5.5/ }));

  expect(firstPick).not.toHaveBeenCalled();
  expect(nextPick).toHaveBeenCalledWith(expect.objectContaining({ provider: "openai", model: "gpt-5.5" }));
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

test("a failed mobile Sheet retries through the catalogue loader", async () => {
  const restoreViewport = installMobileViewport();
  const user = userEvent.setup();
  const loadCatalog = vi
    .fn<Parameters<typeof ModelSwitchTrigger>[0]["loadCatalog"]>()
    .mockRejectedValueOnce(new Error("catalog boom"))
    .mockResolvedValueOnce(catalog());
  try {
    renderTrigger({ loadCatalog });
    await user.click(screen.getByTestId("trigger"));
    const sheet = await screen.findByRole("dialog", { name: "Choose model" });
    await user.click(within(sheet).getByRole("button", { name: "Retry models" }));

    await waitFor(() => expect(within(sheet).getAllByRole("option")).toHaveLength(2));
    expect(loadCatalog).toHaveBeenNthCalledWith(2, true);
  } finally {
    restoreViewport();
  }
});

test("mobile connection repair moves focus into its replacement dialog and dismissal restores the trigger", async () => {
  const restoreViewport = installMobileViewport();
  const user = userEvent.setup();
  function Harness() {
    const [connectionOpen, setConnectionOpen] = useState(false);
    return (
      <>
        <ModelSwitchTrigger
          label="team-local/model"
          value=""
          loadCatalog={async () => ({ models: [], recent: [] })}
          onPick={vi.fn()}
          onOpenConnection={() => setConnectionOpen(true)}
          data-testid="trigger"
        />
        <Dialog open={connectionOpen} onClose={() => setConnectionOpen(false)} title="Connection settings">
          <Button>Editor control</Button>
        </Dialog>
      </>
    );
  }
  try {
    render(<Harness />);
    const trigger = screen.getByTestId("trigger");
    await user.click(trigger);
    const sheet = await screen.findByRole("dialog", { name: "Choose model" });
    await user.click(within(sheet).getByRole("button", { name: "Connection settings" }));

    const replacement = await screen.findByRole("dialog", { name: "Connection settings" });
    expect(document.activeElement).toBe(within(replacement).getByRole("button", { name: "Editor control" }));
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Connection settings" })).toBeNull());
    expect(document.activeElement).toBe(trigger);
  } finally {
    restoreViewport();
  }
});

// A picker open while the hub refreshes its model list (the caller's
// refreshKey changes: the hub announced a changed list) updates in place: the
// list it shows stays up while the new one loads, and the new one replaces it
// when it lands (#3539). A scope change still reloads from scratch; this is
// the same scope with newer data.
test("a refresh key change under an open picker reloads the list in place", async () => {
  const user = userEvent.setup();
  let resolveRefresh: (value: ModelCatalog) => void = () => {};
  const loadCatalog = vi.fn(async (refresh?: boolean) => {
    if (!refresh) return catalog();
    return new Promise<ModelCatalog>((resolve) => {
      resolveRefresh = resolve;
    });
  });
  const { props, rerender } = renderTrigger({ loadCatalog, refreshKey: 0 });
  await user.click(screen.getByTestId("trigger"));
  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(2));

  rerender(<ModelSwitchTrigger {...props} refreshKey={1} />);
  await waitFor(() => expect(loadCatalog).toHaveBeenLastCalledWith(true));
  // The list the picker was showing stays up while the refresh loads.
  expect(screen.getAllByRole("option")).toHaveLength(2);

  await act(async () => {
    resolveRefresh({
      models: [...catalog().models, { provider: "anthropic", model: "claude-opus-5", displayName: "Opus 5" }],
      recent: [],
    });
  });
  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
});

test("a refresh key change under a closed picker loads nothing", async () => {
  const loadCatalog = vi.fn(async () => catalog());
  const { props, rerender } = renderTrigger({ loadCatalog, refreshKey: 0 });
  rerender(<ModelSwitchTrigger {...props} refreshKey={1} />);
  expect(loadCatalog).not.toHaveBeenCalled();
});

// A refresh that fails says nothing new about the list: the picker keeps the
// one it has rather than replacing it with an error.
test("a refresh that fails keeps the list the picker shows", async () => {
  const user = userEvent.setup();
  const loadCatalog = vi.fn(async (refresh?: boolean) => {
    if (refresh) throw new WireError("hub unavailable", -32000);
    return catalog();
  });
  const { props, rerender } = renderTrigger({ loadCatalog, refreshKey: 0 });
  await user.click(screen.getByTestId("trigger"));
  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(2));
  rerender(<ModelSwitchTrigger {...props} refreshKey={1} />);
  await waitFor(() => expect(loadCatalog).toHaveBeenLastCalledWith(true));
  await act(async () => {});
  expect(screen.getAllByRole("option")).toHaveLength(2);
  expect(screen.queryByRole("alert")).toBeNull();
});

// Spawn changes both at once: a credential or model-list change moves its
// refresh key and, with it, its loader's identity. That is still newer data
// for the same scope, so it too reloads in place.
test("a loader change that comes with a refresh key change reloads in place", async () => {
  const user = userEvent.setup();
  const first = vi.fn(async () => catalog());
  let resolveNext: (value: ModelCatalog) => void = () => {};
  const next = vi.fn(
    async () =>
      new Promise<ModelCatalog>((resolve) => {
        resolveNext = resolve;
      }),
  );
  const { props, rerender } = renderTrigger({ loadCatalog: first, refreshKey: 0 });
  await user.click(screen.getByTestId("trigger"));
  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(2));

  rerender(<ModelSwitchTrigger {...props} loadCatalog={next} refreshKey={1} />);
  await waitFor(() => expect(next).toHaveBeenCalled());
  expect(screen.getAllByRole("option")).toHaveLength(2);
  await act(async () => {
    resolveNext({
      models: [...catalog().models, { provider: "anthropic", model: "claude-opus-5", displayName: "Opus 5" }],
      recent: [],
    });
  });
  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
});

// A picker whose last load failed shows the error, not a list, so newer data
// is a full load that can replace the error with the list.
test("a refresh after a failed load replaces the error with the list", async () => {
  const user = userEvent.setup();
  let failing = false;
  const loadCatalog = vi.fn(async () => {
    if (failing) throw new WireError("hub unavailable", -32000);
    return catalog();
  });
  const { props, rerender } = renderTrigger({ loadCatalog, refreshKey: 0 });
  await user.click(screen.getByTestId("trigger"));
  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(2));
  await user.keyboard("{Escape}");
  failing = true;
  await user.click(screen.getByTestId("trigger"));
  await screen.findByRole("alert");

  failing = false;
  rerender(<ModelSwitchTrigger {...props} refreshKey={1} />);
  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(2));
  expect(screen.queryByRole("alert")).toBeNull();
});

// Newer data arriving while the first load is still out cannot keep a list
// that is not on screen yet; it loads again, bypassing the caller's cache.
test("a refresh key change while the first load is out reloads bypassing the cache", async () => {
  const user = userEvent.setup();
  let resolveFirst: (value: ModelCatalog) => void = () => {};
  const loadCatalog = vi.fn(async (refresh?: boolean) => {
    if (refresh) return catalog();
    return new Promise<ModelCatalog>((resolve) => {
      resolveFirst = resolve;
    });
  });
  const { props, rerender } = renderTrigger({ loadCatalog, refreshKey: 0 });
  await user.click(screen.getByTestId("trigger"));
  rerender(<ModelSwitchTrigger {...props} refreshKey={1} />);
  await waitFor(() => expect(loadCatalog).toHaveBeenLastCalledWith(true));
  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(2));
  await act(async () => resolveFirst({ models: [], recent: [] }));
  expect(screen.getAllByRole("option")).toHaveLength(2);
});

test("partial catalogue recovery keeps healthy models usable while retrying", async () => {
  const user = userEvent.setup();
  const partial: ModelCatalog = {
    models: catalog().models.filter((entry) => entry.provider === "anthropic"),
    recent: [],
    diagnostics: [{ provider: "openai", message: "fixture diagnostic" }],
  };
  let complete: (value: ModelCatalog) => void = () => {};
  const loadCatalog = vi.fn(async (refresh?: boolean) =>
    refresh
      ? new Promise<ModelCatalog>((resolve) => {
          complete = resolve;
        })
      : partial,
  );
  const onOpenConnection = vi.fn();
  const { props } = renderTrigger({ loadCatalog, onOpenConnection, value: "" });
  await user.click(screen.getByTestId("trigger"));
  await screen.findByRole("option", { name: /Sonnet/ });
  expect(screen.getByRole("button", { name: "Connection settings" })).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "Retry models" }));
  expect(screen.getByRole("option", { name: /Sonnet/ })).toBeTruthy();
  await act(async () => complete(catalog()));
  await user.click(await screen.findByRole("option", { name: /GPT-5.5/ }));
  expect(props.onPick).toHaveBeenCalledWith(catalog().models[1]);
  expect(loadCatalog).toHaveBeenLastCalledWith(true);
  expect(onOpenConnection).not.toHaveBeenCalled();
});

test("partial catalogue failure offers connection repair without blocking healthy selection", async () => {
  const user = userEvent.setup();
  const onOpenConnection = vi.fn();
  const { props } = renderTrigger({
    value: "",
    onOpenConnection,
    loadCatalog: async () => ({ ...catalog(), diagnostics: [{ provider: "other", message: "fixture diagnostic" }] }),
  });
  await user.click(screen.getByTestId("trigger"));
  await user.click(await screen.findByRole("option", { name: /Sonnet/ }));
  expect(props.onPick).toHaveBeenCalledWith(catalog().models[0]);
  await user.click(screen.getByTestId("trigger"));
  await user.click(await screen.findByRole("button", { name: "Connection settings" }));
  expect(onOpenConnection).toHaveBeenCalledOnce();
  expect(screen.queryByRole("combobox")).toBeNull();
});

test("a failed partial-catalogue retry retains selectable healthy models", async () => {
  const user = userEvent.setup();
  const loadCatalog = vi.fn(async (refresh?: boolean) => {
    if (refresh) throw new WireError("fixture transport failure", -32000);
    return { ...catalog(), diagnostics: [{ provider: "other", message: "fixture diagnostic" }] };
  });
  const { props } = renderTrigger({ value: "", loadCatalog });
  await user.click(screen.getByTestId("trigger"));
  await user.click(await screen.findByRole("button", { name: "Retry models" }));
  expect(loadCatalog).toHaveBeenLastCalledWith(true);
  await user.click(screen.getByRole("option", { name: /Sonnet/ }));
  expect(props.onPick).toHaveBeenCalledWith(catalog().models[0]);
});
