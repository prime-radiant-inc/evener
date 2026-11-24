import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { expect, test, vi } from "vitest";
import { MOBILE_QUERY } from "../../shell/useIsMobile";
import { escapeRegExp, readModuleCss, topRuleBlock } from "../../styles/cssBlock";
import { Select } from "./index";

const OPTIONS = [
  { value: "us-east", label: "US East" },
  { value: "us-west", label: "US West" },
  { value: "eu-central", label: "EU Central" },
];

// A controlled-loop harness, same reason as ControlledInput in
// input.test.tsx: with a fixed value prop and a no-op onChange, React's
// controlled-element reconciliation resets the DOM select back to that
// fixed value right after the user's pick, and since the stored change
// event's target is a live DOM node (not a snapshot), reading its .value
// after that reset sees the reset value, not what the user picked.
function ControlledSelect(props: { onChange?: (value: string) => void }) {
  const [value, setValue] = useState("us-east");
  return (
    <Select
      value={value}
      options={OPTIONS}
      onChange={(e) => {
        setValue(e.target.value);
        props.onChange?.(e.target.value);
      }}
    />
  );
}

function ControlledQuietSelect(props: { onChange?: (value: string) => void }) {
  const [value, setValue] = useState("us-east");
  return (
    <Select
      variant="quiet"
      ariaLabel="Region"
      value={value}
      options={OPTIONS}
      valueTestId="quiet-value"
      onChange={(e) => {
        setValue(e.target.value);
        props.onChange?.(e.target.value);
      }}
    />
  );
}

test("renders every option", () => {
  render(<Select value="us-east" options={OPTIONS} onChange={() => {}} />);
  for (const option of OPTIONS) {
    expect(screen.getByRole("option", { name: option.label })).toBeTruthy();
  }
});

test("reflects the value prop as the selected option", () => {
  render(<Select value="us-west" options={OPTIONS} onChange={() => {}} />);
  expect(screen.getByRole("combobox")).toHaveProperty("value", "us-west");
});

test("selecting a different option fires onChange with its value", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(<ControlledSelect onChange={onChange} />);
  await user.selectOptions(screen.getByRole("combobox"), "eu-central");
  expect(onChange).toHaveBeenCalledOnce();
  expect(onChange).toHaveBeenCalledWith("eu-central");
});

test("selecting an option updates the controlled value", async () => {
  const user = userEvent.setup();
  render(<ControlledSelect />);
  await user.selectOptions(screen.getByRole("combobox"), "eu-central");
  expect(screen.getByRole("combobox")).toHaveProperty("value", "eu-central");
});

test("a disabled option is listed but cannot be picked", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(
    <Select
      value="local"
      options={[
        { value: "local", label: "This hub" },
        { value: "build-box", label: "build-box (offline)", disabled: true },
      ]}
      onChange={onChange}
    />,
  );
  const offline = screen.getByRole("option", { name: "build-box (offline)" }) as HTMLOptionElement;
  expect(offline.disabled).toBe(true);
  expect((screen.getByRole("option", { name: "This hub" }) as HTMLOptionElement).disabled).toBe(false);
  await user.selectOptions(screen.getByRole("combobox"), "build-box");
  expect(onChange).not.toHaveBeenCalled();
  expect(screen.getByRole("combobox")).toHaveProperty("value", "local");
});

test("disabled marks the native select inert (not focusable)", () => {
  render(<Select value="us-east" options={OPTIONS} onChange={() => {}} disabled />);
  const select = screen.getByRole("combobox") as HTMLSelectElement;
  expect(select.disabled).toBe(true);
  select.focus();
  expect(document.activeElement).not.toBe(select);
});

test("is keyboard-focusable when enabled", () => {
  render(<Select value="us-east" options={OPTIONS} onChange={() => {}} />);
  const select = screen.getByRole("combobox") as HTMLSelectElement;
  select.focus();
  expect(document.activeElement).toBe(select);
});

test("an id prop passes through for external <label htmlFor> association", () => {
  render(<Select value="us-east" options={OPTIONS} onChange={() => {}} id="region" />);
  expect(screen.getByRole("combobox").id).toBe("region");
});

// The sunken-field box, the edge-strong hover/focus-within border and the
// focus ring live once in widgets/internal/control.module.css (pinned by
// control.contract.test.ts); Select composes that recipe and keeps only its
// own metrics.
test("composes the shared control field for its box, hover edge, and focus ring", () => {
  const rule = topRuleBlock(readModuleCss(import.meta.url, "select.module.css"), ".select");
  expect(rule).toContain('composes: field from "../internal/control.module.css"');
});

test("keeps the native select at 32px on desktop and the mobile touch floor", () => {
  const css = readModuleCss(import.meta.url, "select.module.css");
  expect(css).toMatch(/\.select\s*\{[^}]*height:\s*32px/);
  expect(css).toMatch(
    new RegExp(
      `@media ${escapeRegExp(MOBILE_QUERY)}, \\(any-pointer: coarse\\) \\{[\\s\\S]*?\\.select\\s*\\{[^}]*min-height:\\s*var\\(--tap-min\\)`,
    ),
  );
});

// --- quiet variant ---------------------------------------------------------
//
// The quiet variant's face is the current value with a chevron, and the native
// select is laid over it at zero opacity. These tests hold the behavior of
// both layers: the readout must track the value, and the overlay must stay the
// real, hittable control rather than swallowing the interaction.

test("quiet variant shows the selected option's label as an aria-hidden readout", () => {
  render(
    <Select
      variant="quiet"
      ariaLabel="Region"
      value="us-west"
      options={OPTIONS}
      onChange={() => {}}
      valueTestId="quiet-value"
    />,
  );
  expect(screen.getByTestId("quiet-value").textContent).toBe("US West");
  // The select already speaks its value; the visible copy must not repeat it.
  expect(screen.getByTestId("quiet-value").getAttribute("aria-hidden")).toBe("true");
  expect((screen.getByRole("combobox") as HTMLSelectElement).value).toBe("us-west");
});

test("quiet variant lets the caller's closed-state readout override the option label", () => {
  render(
    <Select
      variant="quiet"
      ariaLabel="Reasoning effort"
      value=""
      readout="high"
      options={[
        { value: "", label: "high (default)" },
        { value: "high", label: "high" },
      ]}
      onChange={() => {}}
      valueTestId="quiet-value"
    />,
  );
  expect(screen.getByTestId("quiet-value").textContent).toBe("high");
  expect((screen.getByRole("combobox") as HTMLSelectElement).value).toBe("");
});

test("quiet variant routes a pick through onChange and updates its readout", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(<ControlledQuietSelect onChange={onChange} />);
  await user.selectOptions(screen.getByRole("combobox"), "eu-central");
  expect(onChange).toHaveBeenCalledOnce();
  expect(onChange).toHaveBeenCalledWith("eu-central");
  expect(screen.getByTestId("quiet-value").textContent).toBe("EU Central");
});

test("quiet variant honors disabled on the native select and marks the trigger", () => {
  render(
    <Select
      variant="quiet"
      ariaLabel="Region"
      value="us-east"
      options={OPTIONS}
      onChange={() => {}}
      data-testid="quiet-trigger"
      disabled
    />,
  );
  const select = screen.getByRole("combobox") as HTMLSelectElement;
  expect(select.disabled).toBe(true);
  expect(screen.getByTestId("quiet-trigger").getAttribute("data-disabled")).toBe("true");
  select.focus();
  expect(document.activeElement).not.toBe(select);
});

test("quiet variant exposes ariaLabel as the native select's accessible name", () => {
  render(<Select variant="quiet" ariaLabel="Reasoning effort" value="us-east" options={OPTIONS} onChange={() => {}} />);
  const select = screen.getByRole("combobox", { name: "Reasoning effort" }) as HTMLSelectElement;
  expect(select.getAttribute("aria-label")).toBe("Reasoning effort");
});

test("the quiet overlay is the hittable control: a click focuses the native select", async () => {
  const user = userEvent.setup();
  render(<ControlledQuietSelect />);
  const select = screen.getByRole("combobox") as HTMLSelectElement;
  await user.click(select);
  expect(document.activeElement).toBe(select);
});

test("the quiet select is absolutely positioned over the whole trigger with no pointer-events opt-out", () => {
  const css = readModuleCss(import.meta.url, "select.module.css");
  const overlay = topRuleBlock(css, ".quietSelect");
  expect(overlay).toContain("position: absolute");
  expect(overlay).toContain("inset: 0");
  expect(overlay).toContain("opacity: 0");
  // A transparent overlay that also refused pointer events would look right and
  // swallow every click on the control it covers.
  expect(overlay).not.toContain("pointer-events");
});
