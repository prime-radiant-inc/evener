import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { Textarea } from "./index";

afterEach(cleanup);

function ControlledTextarea(props: { autoGrow?: boolean; onChange?: (value: string) => void }) {
  const [value, setValue] = useState("");
  return (
    <Textarea
      value={value}
      autoGrow={props.autoGrow}
      onChange={(e) => {
        setValue(e.target.value);
        props.onChange?.(e.target.value);
      }}
    />
  );
}

test("renders the value prop as its current value", () => {
  render(<Textarea value="hello" onChange={() => {}} />);
  expect(screen.getByRole("textbox")).toHaveProperty("value", "hello");
});

test("typing drives a controlled value through onChange", async () => {
  const user = userEvent.setup();
  render(<ControlledTextarea />);
  await user.type(screen.getByRole("textbox"), "hi");
  expect(screen.getByRole("textbox")).toHaveProperty("value", "hi");
});

test("calls onChange for each keystroke", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(<ControlledTextarea onChange={onChange} />);
  await user.type(screen.getByRole("textbox"), "ab");
  expect(onChange).toHaveBeenCalledTimes(2);
});

test("disabled blocks typing", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(<Textarea value="" onChange={onChange} disabled />);
  await user.type(screen.getByRole("textbox"), "hi");
  expect(onChange).not.toHaveBeenCalled();
});

test("disabled marks the native textarea inert (not focusable)", () => {
  render(<Textarea value="" onChange={() => {}} disabled />);
  const textarea = screen.getByRole("textbox") as HTMLTextAreaElement;
  expect(textarea.disabled).toBe(true);
  textarea.focus();
  expect(document.activeElement).not.toBe(textarea);
});

test("is keyboard-focusable when enabled", () => {
  render(<Textarea value="" onChange={() => {}} />);
  const textarea = screen.getByRole("textbox") as HTMLTextAreaElement;
  textarea.focus();
  expect(document.activeElement).toBe(textarea);
});

test("without autoGrow, rows stays at the default regardless of newlines", () => {
  render(<Textarea value={"a\nb\nc\nd\ne"} onChange={() => {}} />);
  expect(screen.getByRole("textbox").getAttribute("rows")).toBe("2");
});

test("without autoGrow, an explicit rows prop is honored", () => {
  render(<Textarea value="" onChange={() => {}} rows={6} />);
  expect(screen.getByRole("textbox").getAttribute("rows")).toBe("6");
});

test("with autoGrow, rows grows to fit the number of lines in value", () => {
  render(<Textarea value={"a\nb\nc\nd"} onChange={() => {}} autoGrow />);
  expect(screen.getByRole("textbox").getAttribute("rows")).toBe("4");
});

test("with autoGrow, rows never drops below the 2-row minimum for empty value", () => {
  render(<Textarea value="" onChange={() => {}} autoGrow />);
  expect(screen.getByRole("textbox").getAttribute("rows")).toBe("2");
});

test("with autoGrow, rows never drops below the 2-row minimum for a single short line", () => {
  render(<Textarea value="hi" onChange={() => {}} autoGrow />);
  expect(screen.getByRole("textbox").getAttribute("rows")).toBe("2");
});

test("with autoGrow, rows is clamped at a 12-row maximum however many lines value has", () => {
  const manyLines = Array.from({ length: 30 }, (_, i) => `line ${i}`).join("\n");
  render(<Textarea value={manyLines} onChange={() => {}} autoGrow />);
  expect(screen.getByRole("textbox").getAttribute("rows")).toBe("12");
});

test("with autoGrow, rows recomputes as the controlled value changes", async () => {
  const user = userEvent.setup();
  render(<ControlledTextarea autoGrow />);
  const textarea = screen.getByRole("textbox");
  expect(textarea.getAttribute("rows")).toBe("2");
  await user.type(textarea, "one{Enter}two{Enter}three");
  expect(textarea.getAttribute("rows")).toBe("3");
});

test("declares a :focus-visible rule in its CSS module, using only tokens", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "textarea.module.css"), "utf8");
  expect(css).toContain(":focus-visible");
});
