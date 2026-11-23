import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createRef, useState } from "react";
import { expect, test, vi } from "vitest";
import { Input } from "./index";
import rawStyles from "./input.module.css";

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

test("forwards credential validation and autocomplete without changing defaults", () => {
  const { rerender } = render(<Input value="" onChange={() => {}} required autoComplete="off" />);
  const input = screen.getByRole("textbox") as HTMLInputElement;
  expect(input.required).toBe(true);
  expect(input.autocomplete).toBe("off");
  rerender(<Input value="" onChange={() => {}} />);
  expect(input.required).toBe(false);
  expect(input.getAttribute("autocomplete")).toBeNull();
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

test("aria-label names a field that has no visible label", () => {
  render(<Input value="" onChange={() => {}} placeholder="Filter settings" aria-label="Filter settings" />);
  expect(screen.getByRole("textbox", { name: "Filter settings" })).toBeTruthy();
});

test("aria-describedby passes through only when supplied", () => {
  const { rerender } = render(<Input value="" onChange={() => {}} aria-describedby="name-error" />);
  expect(screen.getByRole("textbox").getAttribute("aria-describedby")).toBe("name-error");

  rerender(<Input value="" onChange={() => {}} />);
  expect(screen.getByRole("textbox").getAttribute("aria-describedby")).toBeNull();
});

// --- native pass-through: combobox semantics, data attributes, handlers ----
test("passes combobox semantics and data attributes to the native input", () => {
  render(
    <Input
      value=""
      onChange={() => {}}
      role="combobox"
      aria-label="Command palette"
      aria-expanded="true"
      aria-controls="palette-results"
      aria-activedescendant="palette-row-0"
      aria-labelledby="palette-label"
      data-testid="palette-input"
    />,
  );
  const input = screen.getByRole("combobox", { name: "Command palette" });
  expect(input.tagName).toBe("INPUT");
  expect(input.getAttribute("aria-expanded")).toBe("true");
  expect(input.getAttribute("aria-controls")).toBe("palette-results");
  expect(input.getAttribute("aria-activedescendant")).toBe("palette-row-0");
  expect(input.getAttribute("aria-labelledby")).toBe("palette-label");
  expect(input.getAttribute("data-testid")).toBe("palette-input");
});

test("forwards onKeyDown from the native input", () => {
  const onKeyDown = vi.fn();
  render(<Input value="" onChange={() => {}} onKeyDown={onKeyDown} />);
  fireEvent.keyDown(screen.getByRole("textbox"), { key: "ArrowDown" });
  expect(onKeyDown).toHaveBeenCalledTimes(1);
  expect(onKeyDown.mock.calls[0]?.[0]).toMatchObject({ key: "ArrowDown" });
});

test("autoFocus focuses the native input on mount", () => {
  render(<Input value="" onChange={() => {}} autoFocus />);
  expect(document.activeElement).toBe(screen.getByRole("textbox"));
});

// --- ref + fill: opt-in imperative access and an opt-in width -------------
test("forwards a ref to the underlying native input element", () => {
  const ref = createRef<HTMLInputElement>();
  render(<Input ref={ref} value="hi" onChange={() => {}} />);
  expect(ref.current).toBe(screen.getByRole("textbox"));
});

test("fill adds its modifier class alongside the unchanged base class", () => {
  const { rerender } = render(<Input value="" onChange={() => {}} fill />);
  const classes = screen.getByRole("textbox").className.split(" ");
  expect(classes).toContain(rawStyles.input);
  expect(classes).toContain(rawStyles.fillWidth);

  rerender(<Input value="" onChange={() => {}} />);
  expect(screen.getByRole("textbox").className.split(" ")).not.toContain(rawStyles.fillWidth);
});

test("the fill modifier sets width 100% and min-width 0", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "input.module.css"), "utf8");
  const rule = /\.fillWidth\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
  expect(rule).toContain("width: 100%");
  expect(rule).toContain("min-width: 0");
});

test("roomy adds its modifier class alongside the unchanged base class", () => {
  const { rerender } = render(<Input value="" onChange={() => {}} padding="roomy" />);
  const classes = screen.getByRole("textbox").className.split(" ");
  expect(classes).toContain(rawStyles.input);
  expect(classes).toContain(rawStyles.roomy);

  rerender(<Input value="" onChange={() => {}} />);
  expect(screen.getByRole("textbox").className.split(" ")).not.toContain(rawStyles.roomy);
});

test("the roomy modifier carries the palette's padding and control-floored body size", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "input.module.css"), "utf8");
  const rule = /\.input\.roomy\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
  expect(rule).toContain("padding: var(--space-3)");
  expect(rule).toContain("font-size: max(var(--font-size-control), var(--font-size-body))");
});

test("declares a :focus-visible rule in its CSS module, using only tokens", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "input.module.css"), "utf8");
  expect(css).toContain(":focus-visible");
});

// Beautiful UI "sunken field" treatment: the field sinks into --field via
// --shadow-inset-field, and its resting hairline (--edge) strengthens on
// hover/focus-within - see docs/superpowers/specs/2026-08-13-webui-
// beautiful-ui-retheme-design.md §6.
test("declares the sunken-field background/shadow and an edge-strong hover/focus-within border", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "input.module.css"), "utf8");
  const rule = /\.input\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
  expect(rule).toContain("background: var(--field)");
  expect(rule).toContain("box-shadow: var(--shadow-inset-field)");
  expect(css).toMatch(
    /\.input:hover:not\(:disabled\),\s*\n\.input:focus-within\s*\{[^}]*border-color: var\(--edge-strong\)/,
  );
});
