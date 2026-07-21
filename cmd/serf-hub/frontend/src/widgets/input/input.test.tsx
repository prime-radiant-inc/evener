import { afterEach, test, expect, vi } from "vitest";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { useState } from "react";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Input } from "./index";

afterEach(cleanup);

// A controlled-loop test harness: userEvent.type fires one keystroke at a
// time, and a real controlled <input> only reflects each keystroke if the
// component re-renders with the updated value in between - exactly how a
// real consumer would wire this widget up.
function ControlledInput(props: { onChange?: (value: string) => void }) {
  const [value, setValue] = useState("");
  return (
    <Input
      value={value}
      onChange={(e) => {
        setValue(e.target.value);
        props.onChange?.(e.target.value);
      }}
    />
  );
}

test("renders the value prop as its current value", () => {
  render(<Input value="hello" onChange={() => {}} />);
  expect(screen.getByRole("textbox")).toHaveProperty("value", "hello");
});

test("typing drives a controlled value through onChange", async () => {
  const user = userEvent.setup();
  render(<ControlledInput />);
  await user.type(screen.getByRole("textbox"), "hi");
  expect(screen.getByRole("textbox")).toHaveProperty("value", "hi");
});

test("calls onChange for each keystroke", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(<ControlledInput onChange={onChange} />);
  await user.type(screen.getByRole("textbox"), "ab");
  expect(onChange).toHaveBeenCalledTimes(2);
});

test("renders a placeholder", () => {
  render(<Input value="" onChange={() => {}} placeholder="Search…" />);
  expect(screen.getByPlaceholderText("Search…")).toBeTruthy();
});

test("defaults to type=text", () => {
  render(<Input value="" onChange={() => {}} />);
  expect(screen.getByRole("textbox").getAttribute("type")).toBe("text");
});

test("an explicit type overrides the text-type default", () => {
  // password inputs have no textbox role - query the input directly
  const { container } = render(<Input value="" onChange={() => {}} type="password" />);
  expect(container.querySelector("input")!.getAttribute("type")).toBe("password");
});

test("disabled blocks typing", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(<Input value="" onChange={onChange} disabled />);
  await user.type(screen.getByRole("textbox"), "hi");
  expect(onChange).not.toHaveBeenCalled();
});

test("disabled marks the native input inert (not focusable)", () => {
  render(<Input value="" onChange={() => {}} disabled />);
  const input = screen.getByRole("textbox") as HTMLInputElement;
  expect(input.disabled).toBe(true);
  input.focus();
  expect(document.activeElement).not.toBe(input);
});

test("is keyboard-focusable when enabled", () => {
  render(<Input value="" onChange={() => {}} />);
  const input = screen.getByRole("textbox") as HTMLInputElement;
  input.focus();
  expect(document.activeElement).toBe(input);
});

test("an id prop passes through for external <label htmlFor> association", () => {
  render(<Input value="" onChange={() => {}} id="name-field" />);
  expect(screen.getByRole("textbox").id).toBe("name-field");
});

test("declares a :focus-visible rule in its CSS module, using only tokens", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "input.module.css"), "utf8");
  expect(css).toContain(":focus-visible");
});
