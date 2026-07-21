import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createRef, useState } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { MAX_HEIGHT_VIEWPORT_FRACTION, Textarea } from "./index";

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

// --- ref forwarding + onKeyDown/onPaste: composer-consumers need imperative
// access (focus(), selectionStart/selectionEnd for cursor-position work) and
// native keydown/paste interception (Enter-to-send routing, clipboard-image
// capture) that a controlled value/onChange pair alone can't provide - see
// panes/session/composer's own attachment + submit-routing modules for the
// real consumers of both.
test("forwards a ref to the underlying native textarea element", () => {
  const ref = createRef<HTMLTextAreaElement>();
  render(<Textarea ref={ref} value="hi" onChange={() => {}} />);
  expect(ref.current).toBe(screen.getByRole("textbox"));
});

test("calls onKeyDown with the native keyboard event", async () => {
  const user = userEvent.setup();
  const onKeyDown = vi.fn();
  render(<Textarea value="" onChange={() => {}} onKeyDown={onKeyDown} />);
  await user.type(screen.getByRole("textbox"), "{Enter}");
  expect(onKeyDown).toHaveBeenCalledTimes(1);
  expect(onKeyDown.mock.calls[0]?.[0]).toHaveProperty("key", "Enter");
});

test("calls onPaste with the native clipboard event", () => {
  const onPaste = vi.fn();
  render(<Textarea value="" onChange={() => {}} onPaste={onPaste} />);
  const textarea = screen.getByRole("textbox");
  const pasteEvent = Object.assign(new Event("paste", { bubbles: true, cancelable: true }), {
    clipboardData: { items: [] },
  });
  textarea.dispatchEvent(pasteEvent);
  expect(onPaste).toHaveBeenCalledTimes(1);
});

test("without autoGrow, rows stays at the default regardless of newlines", () => {
  render(<Textarea value={"a\nb\nc\nd\ne"} onChange={() => {}} />);
  expect(screen.getByRole("textbox").getAttribute("rows")).toBe("2");
});

test("without autoGrow, an explicit rows prop is honored", () => {
  render(<Textarea value="" onChange={() => {}} rows={6} />);
  expect(screen.getByRole("textbox").getAttribute("rows")).toBe("6");
});

test("without autoGrow, no inline height style is ever applied - sizing stays rows-only", () => {
  render(<Textarea value={"a\nb\nc\nd\ne"} onChange={() => {}} />);
  const textarea = screen.getByRole("textbox") as HTMLTextAreaElement;
  expect(textarea.style.height).toBe("");
});

test("declares a :focus-visible rule in its CSS module, using only tokens", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "textarea.module.css"), "utf8");
  expect(css).toContain(":focus-visible");
});

// --- autoGrow: scrollHeight-based, not newline-counting -------------------
//
// jsdom performs no real layout/text measurement (scrollHeight is always 0
// with no stub - same rationale as widgets/virtuallist's own test suite,
// see its file-level comment). This stubs HTMLElement.prototype.scrollHeight
// as a pure function of the element's OWN current value length (20px per
// 40 chars, 40px floor) rather than a fixed constant, specifically so a
// test can type one long UNBROKEN line (no literal "\n" at all) and still
// observe scrollHeight - and therefore the rendered height - grow. That is
// exactly the case the OLD newline-counting autoGrow could never handle
// (computeRows only ever split on "\n"); a fixed-value stub would pass
// either implementation and prove nothing about which one is under test.
let scrollHeightDescriptor: PropertyDescriptor | undefined;

function simulatedScrollHeight(value: string): number {
  return Math.max(40, Math.ceil(value.length / 40) * 20);
}

beforeEach(() => {
  scrollHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollHeight");
  Object.defineProperty(HTMLElement.prototype, "scrollHeight", {
    configurable: true,
    get(this: HTMLTextAreaElement) {
      return simulatedScrollHeight(this.value ?? "");
    },
  });
});

afterEach(() => {
  if (scrollHeightDescriptor) {
    Object.defineProperty(HTMLElement.prototype, "scrollHeight", scrollHeightDescriptor);
  }
});

test("with autoGrow, the rows attribute stays at the fixed baseline - height is governed by inline style, not rows", () => {
  render(<Textarea value={"a\nb\nc\nd"} onChange={() => {}} autoGrow />);
  expect(screen.getByRole("textbox").getAttribute("rows")).toBe("2");
});

test("with autoGrow, height is set from the native scrollHeight after mount", () => {
  render(<Textarea value="hi" onChange={() => {}} autoGrow />);
  const textarea = screen.getByRole("textbox") as HTMLTextAreaElement;
  expect(textarea.style.height).toBe(`${simulatedScrollHeight("hi")}px`);
});

test("with autoGrow, height grows to track a long single unbroken line with no literal newlines at all - the wrapped-line case the old row-count heuristic could not handle", async () => {
  const user = userEvent.setup();
  render(<ControlledTextarea autoGrow />);
  const textarea = screen.getByRole("textbox") as HTMLTextAreaElement;
  const longLine = "x".repeat(400);

  await user.type(textarea, longLine);

  expect(textarea.style.height).toBe(`${simulatedScrollHeight(longLine)}px`);
});

test("with autoGrow, height recomputes (shrinks back down) as the controlled value shrinks", async () => {
  const user = userEvent.setup();
  render(<ControlledTextarea autoGrow />);
  const textarea = screen.getByRole("textbox") as HTMLTextAreaElement;

  await user.type(textarea, "x".repeat(200));
  expect(textarea.style.height).toBe(`${simulatedScrollHeight("x".repeat(200))}px`);

  await user.clear(textarea);
  expect(textarea.style.height).toBe(`${simulatedScrollHeight("")}px`);
});

test("with autoGrow, height clamps at MAX_HEIGHT_VIEWPORT_FRACTION of the viewport height for very tall content", () => {
  const longLine = "x".repeat(4000); // simulated scrollHeight comfortably exceeds any real viewport
  render(<Textarea value={longLine} onChange={() => {}} autoGrow />);
  const textarea = screen.getByRole("textbox") as HTMLTextAreaElement;

  const expectedMax = window.innerHeight * MAX_HEIGHT_VIEWPORT_FRACTION;
  expect(simulatedScrollHeight(longLine)).toBeGreaterThan(expectedMax); // the fixture actually exercises the clamp
  expect(textarea.style.height).toBe(`${expectedMax}px`);
});
