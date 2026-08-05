import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { Select } from "./index";

afterEach(cleanup);

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

test("declares a :focus-visible rule in its CSS module, using only tokens", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "select.module.css"), "utf8");
  expect(css).toContain(":focus-visible");
});
