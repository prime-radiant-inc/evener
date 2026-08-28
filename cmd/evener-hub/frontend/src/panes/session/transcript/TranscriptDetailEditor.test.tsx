import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { startTransition, useState } from "react";
import { afterEach, expect, test, vi } from "vitest";
import {
  makeTranscriptDisplayConfig,
  presetContent,
  type TranscriptDisplayConfigV1,
} from "../../../transcriptDisplay/config";
import { TranscriptDetailEditor } from "./TranscriptDetailEditor";

afterEach(cleanup);

function ControlledEditor({
  initial,
  onChange,
  disabled,
}: {
  initial: TranscriptDisplayConfigV1;
  onChange?: (value: TranscriptDisplayConfigV1) => void;
  disabled?: boolean;
}) {
  const [value, setValue] = useState(initial);
  return (
    <TranscriptDetailEditor
      value={value}
      disabled={disabled}
      onChange={(next) => {
        setValue(next);
        onChange?.(next);
      }}
    />
  );
}

function latestContent(changes: TranscriptDisplayConfigV1[]): TranscriptDisplayConfigV1["content"] {
  const latest = changes.at(-1);
  if (latest === undefined) throw new Error("editor did not emit a configuration");
  return latest.content;
}

function clickAdvanced() {
  return screen.getByText(/^Customize & advanced/);
}

test("renders six explicit choices and gives Full its full accessible name", () => {
  render(
    <TranscriptDetailEditor
      value={makeTranscriptDisplayConfig({ kind: "preset", level: "tools" })}
      onChange={() => {}}
    />,
  );

  expect(screen.getByRole("radiogroup", { name: "Transcript detail" })).toBeTruthy();
  expect(screen.getByRole("radio", { name: "Chat" })).toBeTruthy();
  expect(screen.getByRole("radio", { name: "Intent" })).toBeTruthy();
  expect(screen.getByRole("radio", { name: "Tools" })).toBeTruthy();
  expect(screen.getByRole("radio", { name: "Activity" })).toBeTruthy();
  expect(screen.getByRole("radio", { name: "Full detail" })).toBeTruthy();
  expect(screen.getByRole("radio", { name: "Custom" })).toBeTruthy();
  expect(screen.getByText("Full")).toBeTruthy();
  expect(screen.getByRole("radio", { name: "Tools" }).getAttribute("aria-checked")).toBe("true");
  expect(screen.queryByText(/Current detail|Critical rows remain visible/)).toBeNull();
});

test("keeps Custom selected for all 16 explicit Custom vectors", () => {
  const booleans = [false, true];
  const vectors = booleans.flatMap((toolIntent) =>
    booleans.flatMap((toolCalls) =>
      booleans.flatMap((reasoning) =>
        booleans.map((expandByDefault) => ({ toolIntent, toolCalls, reasoning, expandByDefault })),
      ),
    ),
  );
  const presetLabels = ["Chat", "Intent", "Tools", "Activity", "Full detail"];

  for (const vector of vectors) {
    const { unmount } = render(
      <TranscriptDetailEditor value={makeTranscriptDisplayConfig({ kind: "custom", ...vector })} onChange={() => {}} />,
    );
    expect(screen.getByRole("radio", { name: "Custom" }).getAttribute("aria-checked")).toBe("true");
    for (const name of presetLabels) {
      expect(screen.getByRole("radio", { name }).getAttribute("aria-checked")).toBe("false");
    }
    unmount();
  }
});

test("the six-choice track keeps Arrow, Home, and End selection behavior", async () => {
  const user = userEvent.setup();
  render(<ControlledEditor initial={makeTranscriptDisplayConfig({ kind: "preset", level: "tools" })} />);

  screen.getByRole("radio", { name: "Tools" }).focus();
  await user.keyboard("{ArrowRight}");
  expect(screen.getByRole("radio", { name: "Activity" }).getAttribute("aria-checked")).toBe("true");

  await user.keyboard("{End}");
  expect(screen.getByRole("radio", { name: "Custom" }).getAttribute("aria-checked")).toBe("true");

  await user.keyboard("{Home}");
  expect(screen.getByRole("radio", { name: "Chat" }).getAttribute("aria-checked")).toBe("true");
});

test("selecting a regular level preserves every Advanced value", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  const initial = makeTranscriptDisplayConfig(
    { kind: "preset", level: "tools" },
    {
      roundTimings: true,
      tokenCounts: true,
      estimatedCost: false,
      systemEvents: true,
      promptEvents: true,
      hookExits: "all",
    },
  );
  render(<TranscriptDetailEditor value={initial} onChange={onChange} />);

  await user.click(screen.getByRole("radio", { name: "Chat" }));

  expect(onChange).toHaveBeenCalledWith({
    version: 1,
    content: { kind: "preset", level: "chat" },
    advanced: initial.advanced,
  });
});

test("selecting Custom restores its mounted vector and content edits update the cache", async () => {
  const user = userEvent.setup();
  const changes: TranscriptDisplayConfigV1[] = [];
  render(
    <ControlledEditor
      initial={makeTranscriptDisplayConfig({ kind: "preset", level: "tools" })}
      onChange={(next) => changes.push(next)}
    />,
  );

  await user.click(screen.getByRole("radio", { name: "Custom" }));
  expect(latestContent(changes)).toEqual({ kind: "custom", ...presetContent("tools") });
  await user.click(clickAdvanced());
  await user.click(screen.getByRole("switch", { name: "Reasoning" }));
  const remembered = latestContent(changes);
  await user.click(screen.getByRole("radio", { name: "Chat" }));
  await user.click(screen.getByRole("radio", { name: "Custom" }));
  expect(latestContent(changes)).toEqual(remembered);
});

test("received Custom values refresh the mounted cache", async () => {
  const user = userEvent.setup();
  const customA = makeTranscriptDisplayConfig({
    kind: "custom",
    toolIntent: false,
    toolCalls: false,
    reasoning: true,
    expandByDefault: false,
  });
  const customB = makeTranscriptDisplayConfig({
    kind: "custom",
    toolIntent: true,
    toolCalls: false,
    reasoning: true,
    expandByDefault: true,
  });
  const preset = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  const changes: TranscriptDisplayConfigV1[] = [];
  const view = render(<TranscriptDetailEditor value={customA} onChange={(next) => changes.push(next)} />);

  view.rerender(<TranscriptDetailEditor value={customB} onChange={(next) => changes.push(next)} />);
  view.rerender(<TranscriptDetailEditor value={preset} onChange={(next) => changes.push(next)} />);
  await user.click(screen.getByRole("radio", { name: "Custom" }));

  expect(latestContent(changes)).toEqual(customB.content);
});

test("an aborted Custom render cannot poison the committed mounted cache", () => {
  const committedCustom = makeTranscriptDisplayConfig({
    kind: "custom",
    toolIntent: false,
    toolCalls: false,
    reasoning: false,
    expandByDefault: false,
  });
  const preset = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  const uncommittedCustom = makeTranscriptDisplayConfig({
    kind: "custom",
    toolIntent: true,
    toolCalls: false,
    reasoning: true,
    expandByDefault: true,
  });
  const never = new Promise<never>(() => {});
  let update: (next: { value: TranscriptDisplayConfigV1; suspend: boolean }) => void = () => {};
  const changes: TranscriptDisplayConfigV1[] = [];

  function Suspender(): never {
    throw never;
  }

  function Harness() {
    const [state, setState] = useState({ value: committedCustom, suspend: false });
    update = setState;
    return (
      <>
        <TranscriptDetailEditor value={state.value} onChange={(next) => changes.push(next)} />
        {state.suspend && <Suspender />}
      </>
    );
  }

  render(<Harness />);
  act(() => update({ value: preset, suspend: false }));
  expect(screen.getByRole("radio", { name: "Tools" }).getAttribute("aria-checked")).toBe("true");

  act(() => {
    startTransition(() => update({ value: uncommittedCustom, suspend: true }));
  });
  expect(screen.getByRole("radio", { name: "Tools" }).getAttribute("aria-checked")).toBe("true");

  fireEvent.click(screen.getByRole("radio", { name: "Custom" }));
  expect(latestContent(changes)).toEqual(committedCustom.content);
});

test("first Custom selection clones the current preset and remount clears dormant cache", async () => {
  const user = userEvent.setup();
  const changes: TranscriptDisplayConfigV1[] = [];
  const view = render(
    <ControlledEditor
      initial={makeTranscriptDisplayConfig({ kind: "preset", level: "tools" })}
      onChange={(next) => changes.push(next)}
    />,
  );

  await user.click(screen.getByRole("radio", { name: "Custom" }));
  await user.click(clickAdvanced());
  await user.click(screen.getByRole("switch", { name: "Reasoning" }));
  view.unmount();

  render(
    <ControlledEditor
      initial={makeTranscriptDisplayConfig({ kind: "preset", level: "intent" })}
      onChange={(next) => changes.push(next)}
    />,
  );
  await user.click(screen.getByRole("radio", { name: "Custom" }));
  expect(latestContent(changes)).toEqual({ kind: "custom", ...presetContent("intent") });
});

test("content and Advanced changes remain independent in both directions", async () => {
  const user = userEvent.setup();
  const changes: TranscriptDisplayConfigV1[] = [];
  const initial = makeTranscriptDisplayConfig(
    { kind: "custom", toolIntent: false, toolCalls: true, reasoning: false, expandByDefault: true },
    { tokenCounts: true, systemEvents: true, hookExits: "successful" },
  );
  render(<ControlledEditor initial={initial} onChange={(next) => changes.push(next)} />);

  await user.click(clickAdvanced());
  await user.click(screen.getByRole("switch", { name: "Tool intent" }));
  const contentAfterContentChange = latestContent(changes);
  expect(changes.at(-1)?.advanced).toEqual(initial.advanced);

  await user.click(screen.getByRole("switch", { name: "Round timings" }));
  expect(latestContent(changes)).toEqual(contentAfterContentChange);
  await user.click(screen.getByRole("switch", { name: "Low-level system events" }));
  expect(latestContent(changes)).toEqual(contentAfterContentChange);
});

test("Disclosure summary counts only Metrics and Diagnostics extras", async () => {
  const user = userEvent.setup();
  const preset = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" }, { roundTimings: true });
  const presetView = render(<TranscriptDetailEditor value={preset} onChange={() => {}} />);
  expect(screen.getByText("Customize & advanced · 1 extras")).toBeTruthy();
  presetView.unmount();

  const config = makeTranscriptDisplayConfig(
    { kind: "custom", toolIntent: true, toolCalls: false, reasoning: true, expandByDefault: false },
    { roundTimings: true, systemEvents: true },
  );
  render(<TranscriptDetailEditor value={config} onChange={() => {}} />);

  expect(screen.getByText("Customize & advanced · Custom content · 2 extras")).toBeTruthy();
  await user.click(clickAdvanced());
  expect(screen.getByRole("group", { name: "Content" })).toBeTruthy();
  expect(screen.getByRole("group", { name: "Metrics" })).toBeTruthy();
  expect(screen.getByRole("group", { name: "Diagnostics" })).toBeTruthy();
});

test("Disclosure is controlled across rerenders and starts closed after remount", async () => {
  const user = userEvent.setup();
  const config = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  const view = render(<TranscriptDetailEditor value={config} onChange={() => {}} />);

  expect(screen.queryByRole("group", { name: "Content" })).toBeNull();
  await user.click(clickAdvanced());
  expect(screen.getByRole("group", { name: "Content" })).toBeTruthy();
  view.rerender(
    <TranscriptDetailEditor
      value={makeTranscriptDisplayConfig({ kind: "preset", level: "full" })}
      onChange={() => {}}
    />,
  );
  expect(screen.getByRole("group", { name: "Content" })).toBeTruthy();
  view.unmount();

  render(<TranscriptDetailEditor value={config} onChange={() => {}} />);
  expect(screen.queryByRole("group", { name: "Content" })).toBeNull();
});

test("disabled editor leaves the open Disclosure inert and disables its controls", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  const config = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  const { rerender } = render(<TranscriptDetailEditor value={config} onChange={onChange} />);

  await user.click(clickAdvanced());
  rerender(<TranscriptDetailEditor value={config} onChange={onChange} disabled />);
  const summary = clickAdvanced().closest("summary");
  expect(summary?.getAttribute("aria-disabled")).toBe("true");
  expect(summary?.getAttribute("tabindex")).toBe("-1");
  for (const control of screen.getAllByRole("switch")) expect((control as HTMLButtonElement).disabled).toBe(true);
  expect((screen.getByRole("combobox", { name: "Hook exit messages" }) as HTMLSelectElement).disabled).toBe(true);
  await user.click(screen.getByRole("radio", { name: "Custom" }));
  expect(onChange).not.toHaveBeenCalled();
});

test("Hook exit messages is labelled through FormRow and Select", async () => {
  const user = userEvent.setup();
  render(<TranscriptDetailEditor value={makeTranscriptDisplayConfig()} onChange={() => {}} />);
  await user.click(clickAdvanced());

  const select = screen.getByRole("combobox", { name: "Hook exit messages" });
  expect(select.id).toBeTruthy();
  expect(screen.getByLabelText("Hook exit messages")).toBe(select);
});

test("styles only layout and retains semantic fieldsets", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "transcriptDisplay.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

  expect(css).toContain("container-name: transcript-detail-editor");
  expect(css).toMatch(/\.root[^{]*\{[^}]*gap:\s*var\(--space-3\)/);
  expect(css).toMatch(/\.compact[^{]*\{[^}]*gap:\s*var\(--space-2\)/);
  expect(css).toMatch(/\.fieldsets[^{]*\{[^}]*grid-template-columns:\s*repeat\(3/);
  expect(css).toMatch(/@container transcript-detail-editor \(max-width: 34rem\)/);
  expect(css).not.toMatch(/:global\(/);
  expect(css).not.toMatch(/role="(radio|switch)"/);
  expect(css).not.toMatch(/advancedToggle|selectLabel|critical|Current detail/);
});
