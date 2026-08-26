import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { makeTranscriptDisplayConfig, type TranscriptDisplayConfigV1 } from "../../../transcriptDisplay/config";
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

test("renders all five visible levels and gives Full its full accessible name", () => {
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
  expect(screen.getByText("Full")).toBeTruthy();
});

test("uses Full detail in the current readout while the visible stop stays Full", () => {
  render(
    <TranscriptDetailEditor
      value={makeTranscriptDisplayConfig({ kind: "preset", level: "full" })}
      onChange={() => {}}
    />,
  );

  expect(screen.getByText("Full")).toBeTruthy();
  expect(screen.getByText("Full detail")).toBeTruthy();
});

test("the five-level radio track keeps Arrow, Home, and End selection behavior", async () => {
  const user = userEvent.setup();
  render(<ControlledEditor initial={makeTranscriptDisplayConfig({ kind: "preset", level: "tools" })} />);

  screen.getByRole("radio", { name: "Tools" }).focus();
  await user.keyboard("{ArrowRight}");
  expect(screen.getByRole("radio", { name: "Activity" }).getAttribute("aria-checked")).toBe("true");

  await user.keyboard("{End}");
  expect(screen.getByRole("radio", { name: "Full detail" }).getAttribute("aria-checked")).toBe("true");

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

test("Advanced content changes show Custom and normalize exact preset vectors", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(
    <ControlledEditor initial={makeTranscriptDisplayConfig({ kind: "preset", level: "tools" })} onChange={onChange} />,
  );

  await user.click(screen.getByRole("button", { name: /Advanced/ }));
  await user.click(screen.getByRole("switch", { name: "Reasoning" }));
  expect(screen.getByRole("radio", { name: "Activity" }).getAttribute("aria-checked")).toBe("true");
  expect(onChange).toHaveBeenLastCalledWith(
    expect.objectContaining({ content: { kind: "preset", level: "activity" } }),
  );

  await user.click(screen.getByRole("switch", { name: "Tool calls" }));
  expect(screen.getByText("Custom")).toBeTruthy();
  expect(onChange).toHaveBeenLastCalledWith(
    expect.objectContaining({
      content: { kind: "custom", toolIntent: true, toolCalls: false, reasoning: true, expandByDefault: false },
    }),
  );
});

test("Advanced summary includes Custom content and independent extras", async () => {
  const user = userEvent.setup();
  const config = makeTranscriptDisplayConfig(
    { kind: "custom", toolIntent: true, toolCalls: false, reasoning: true, expandByDefault: false },
    { roundTimings: true, systemEvents: true },
  );
  render(<TranscriptDetailEditor value={config} onChange={() => {}} />);

  await user.click(screen.getByRole("button", { name: /Advanced/ }));

  expect(screen.getByRole("button", { name: /Advanced · Custom content · 2 extras/ })).toBeTruthy();
});

test("Metrics and Diagnostics toggles remain independent of content", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(
    <ControlledEditor initial={makeTranscriptDisplayConfig({ kind: "preset", level: "intent" })} onChange={onChange} />,
  );

  await user.click(screen.getByRole("button", { name: /Advanced/ }));
  await user.click(screen.getByRole("switch", { name: "Round timings" }));
  await user.click(screen.getByRole("switch", { name: "Low-level system events" }));

  expect(onChange).toHaveBeenLastCalledWith(
    expect.objectContaining({
      content: { kind: "preset", level: "intent" },
      advanced: expect.objectContaining({ roundTimings: true, systemEvents: true }),
    }),
  );
});

test("disabled editor exposes disabled controls and cannot change the value", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(
    <TranscriptDetailEditor
      value={makeTranscriptDisplayConfig({ kind: "preset", level: "tools" })}
      onChange={onChange}
      disabled
    />,
  );

  expect((screen.getByRole("radio", { name: "Tools" }) as HTMLButtonElement).disabled).toBe(true);
  expect((screen.getByRole("button", { name: /Advanced/ }) as HTMLButtonElement).disabled).toBe(true);
  await user.click(screen.getByRole("radio", { name: "Chat" }));
  expect(onChange).not.toHaveBeenCalled();
});

test("Custom has no regular radio checked", () => {
  render(
    <TranscriptDetailEditor
      value={makeTranscriptDisplayConfig({
        kind: "custom",
        toolIntent: true,
        toolCalls: false,
        reasoning: false,
        expandByDefault: true,
      })}
      onChange={() => {}}
    />,
  );

  for (const radio of screen.getAllByRole("radio")) {
    expect(radio.getAttribute("aria-checked")).toBe("false");
  }
  expect(screen.getByText("Custom")).toBeTruthy();
});

test("disabled Advanced switches and select cannot change the value", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  const config = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  const { rerender } = render(<TranscriptDetailEditor value={config} onChange={onChange} />);

  await user.click(screen.getByRole("button", { name: /Advanced/ }));
  rerender(<TranscriptDetailEditor value={config} onChange={onChange} disabled />);

  for (const control of screen.getAllByRole("switch")) {
    expect((control as HTMLButtonElement).disabled).toBe(true);
  }
  const hookSelect = screen.getByRole("combobox", { name: "Hook exit messages" }) as HTMLSelectElement;
  expect(hookSelect.disabled).toBe(true);
  await user.click(screen.getByRole("switch", { name: "Tool calls" }));
  await user.selectOptions(hookSelect, "all");
  expect(onChange).not.toHaveBeenCalled();
  expect(hookSelect.value).toBe("none");
});

test("explains that critical rows are locked and are not editor controls", () => {
  render(<TranscriptDetailEditor value={makeTranscriptDisplayConfig()} onChange={() => {}} />);

  expect(screen.getByText(/Critical rows remain visible at every detail level/)).toBeTruthy();
  expect(
    screen.getByText(
      /questions, requests, active work, steering, warnings, failures, interruptions, and recovery actions/,
    ),
  ).toBeTruthy();
  expect(screen.queryByRole("switch", { name: /critical/i })).toBeNull();
});

test("styles the five-stop track with non-color selection and 44px touch targets", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "transcriptDisplay.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

  expect(css).toMatch(/grid-template-columns:\s*repeat\(5/);
  expect(css).toMatch(/button\[role="radio"\][^{]*\{[^}]*min-height:\s*44px/);
  expect(css).not.toContain("overflow-x: auto");
  expect(css).not.toContain("min-width: 440px");
  expect(css).toMatch(/aria-checked="true"/);
  expect(css).toMatch(/\.advancedPanel[^{]*button\[role="switch"\][^{]*\{[^}]*min-height:\s*44px/);
});
