import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createRef, useState } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { MAX_HEIGHT_VIEWPORT_FRACTION, MIN_ROWS, Textarea } from "./index";
import rawStyles from "./textarea.module.css";

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

// --- seamless: opt-in only, and only ADDS a class ------------------------
//
// The default appearance is what four other consumers already render
// (panes/settings' launch fields, panes/spawn, chrome/GoalControl), so the
// variant must be additive: same base class, one extra modifier.
function moduleCss(): string {
  return readFileSync(join(dirname(fileURLToPath(import.meta.url)), "textarea.module.css"), "utf8");
}

test("without seamless, only the base class is applied", () => {
  render(<Textarea value="" onChange={() => {}} />);
  expect(screen.getByRole("textbox").className).toBe(rawStyles.textarea);
});

test("seamless adds its modifier class alongside the unchanged base class", () => {
  render(<Textarea value="" onChange={() => {}} seamless />);
  const classes = screen.getByRole("textbox").className.split(" ");
  expect(classes).toContain(rawStyles.textarea);
  expect(classes).toContain(rawStyles.seamless);
});

test("the seamless modifier removes the field's own box and its focus ring", () => {
  const css = moduleCss();
  const rule = /\.seamless\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
  expect(rule).toContain("border: none");
  expect(rule).toContain("background: transparent");
  expect(rule).toContain("border-radius: 0");
  expect(rule).toContain("padding: 0");
  // The native resize grabber has no corner to sit in on a seamless field.
  expect(rule).toContain("resize: none");
  // The enclosing card owns the focus affordance instead (:focus-within).
  expect(css).toMatch(/\.seamless:focus-visible\s*\{\s*outline: none;\s*\}/);
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

// Pasted rather than typed key-by-key: what's under test is that the height
// tracks the VALUE's measured scrollHeight, so one 400-character change proves
// it exactly as well as 400 of them - and a per-keystroke version spends 400
// renders plus 400 layout-effect remeasures inside the default 5s budget,
// which is enough to time out on a loaded machine and leave the next test
// holding a dirty textarea (an unbounded cost for no extra coverage).
test("with autoGrow, height grows to track a long single unbroken line with no literal newlines at all - the wrapped-line case the old row-count heuristic could not handle", async () => {
  const user = userEvent.setup();
  render(<ControlledTextarea autoGrow />);
  const textarea = screen.getByRole("textbox") as HTMLTextAreaElement;
  const longLine = "x".repeat(400);

  await user.click(textarea);
  await user.paste(longLine);

  expect(textarea.value).toBe(longLine); // the whole line really landed
  expect(textarea.style.height).toBe(`${simulatedScrollHeight(longLine)}px`);
});

// Also pasted, for the same reason as the grow case above.
test("with autoGrow, height recomputes (shrinks back down) as the controlled value shrinks", async () => {
  const user = userEvent.setup();
  render(<ControlledTextarea autoGrow />);
  const textarea = screen.getByRole("textbox") as HTMLTextAreaElement;
  const longLine = "x".repeat(200);

  await user.click(textarea);
  await user.paste(longLine);
  expect(textarea.style.height).toBe(`${simulatedScrollHeight(longLine)}px`);

  await user.clear(textarea);
  expect(textarea.value).toBe(""); // the shrink really happened
  expect(textarea.style.height).toBe(`${simulatedScrollHeight("")}px`);
});

// --- unmeasurable mounts: a field that has no layout box when the effect runs
//
// A textarea that is detached from the document, or under a display:none
// ancestor, reports scrollHeight 0 in every browser (verified live in Chrome:
// detached, display:none, and display:none-ancestor all report 0). dockview
// builds a panel's content element detached and mounts the React tree into it
// before attaching, so whichever pane is not the boot-active one runs its
// layout effect with no layout box - which used to pin height:0px forever,
// leaving an invisible, unclickable field.
//
// WHAT THESE TESTS CAN PROVE: that a 0 measurement is never written as a
// height, and that a ResizeObserver notification re-measures. jsdom performs
// no layout at all (scrollHeight is 0 unconditionally without the stub above,
// and there is no ResizeObserver), so the stub below models the browser rule
// - 0 while any ancestor is display:none or the node is detached - and the
// observer is a hand-driven fake.
// WHAT THEY CANNOT PROVE: that a real browser delivers a resize notification
// on reveal, or the real pixel heights. Both are verified in Chrome against a
// real hub instead.
function hasLayoutBox(el: HTMLElement): boolean {
  if (!el.isConnected) return false;
  for (let node: HTMLElement | null = el; node !== null; node = node.parentElement) {
    if (node.style.display === "none") return false;
  }
  return true;
}

function stubLayoutAwareScrollHeight(): () => void {
  const previous = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollHeight");
  Object.defineProperty(HTMLElement.prototype, "scrollHeight", {
    configurable: true,
    get(this: HTMLTextAreaElement) {
      return hasLayoutBox(this) ? simulatedScrollHeight(this.value ?? "") : 0;
    },
  });
  return () => {
    if (previous) Object.defineProperty(HTMLElement.prototype, "scrollHeight", previous);
  };
}

/** A hand-driven stand-in for the browser's ResizeObserver (absent from
 * jsdom): records what the widget observes and lets a test deliver the
 * notification a real browser would deliver when a revealed element gains a
 * box. */
function stubResizeObserver(): { notify: () => void; observed: () => Element[]; restore: () => void } {
  const callbacks = new Map<Element, () => void>();
  const original = (globalThis as { ResizeObserver?: unknown }).ResizeObserver;
  class FakeResizeObserver {
    constructor(private readonly callback: () => void) {}
    observe(target: Element): void {
      callbacks.set(target, this.callback);
    }
    unobserve(target: Element): void {
      callbacks.delete(target);
    }
    disconnect(): void {
      for (const target of [...callbacks.keys()]) {
        if (callbacks.get(target) === this.callback) callbacks.delete(target);
      }
    }
  }
  (globalThis as { ResizeObserver?: unknown }).ResizeObserver = FakeResizeObserver;
  return {
    notify: () => {
      for (const callback of callbacks.values()) callback();
    },
    observed: () => [...callbacks.keys()],
    restore: () => {
      (globalThis as { ResizeObserver?: unknown }).ResizeObserver = original;
    },
  };
}

test("with autoGrow, a field measured with no layout box is never pinned to a bogus 0 height", () => {
  const restoreScrollHeight = stubLayoutAwareScrollHeight();
  try {
    const host = document.createElement("div");
    host.style.display = "none";
    document.body.appendChild(host);
    render(<Textarea value="hi" onChange={() => {}} autoGrow />, { container: host });

    const textarea = host.querySelector("textarea") as HTMLTextAreaElement;
    expect(textarea.scrollHeight).toBe(0); // the fixture really is unmeasurable
    expect(textarea.style.height).not.toBe("0px");
    expect(textarea.style.height).not.toBe("auto");

    host.remove();
  } finally {
    restoreScrollHeight();
  }
});

test("with autoGrow, a field revealed after mount re-measures on the resize notification", () => {
  const restoreScrollHeight = stubLayoutAwareScrollHeight();
  const observer = stubResizeObserver();
  try {
    const host = document.createElement("div");
    host.style.display = "none";
    document.body.appendChild(host);
    render(<Textarea value="hi" onChange={() => {}} autoGrow />, { container: host });

    const textarea = host.querySelector("textarea") as HTMLTextAreaElement;
    expect(observer.observed()).toContain(textarea);

    host.style.display = "block";
    observer.notify();
    expect(textarea.style.height).toBe(`${simulatedScrollHeight("hi")}px`);

    host.remove();
  } finally {
    observer.restore();
    restoreScrollHeight();
  }
});

// The clamp ceiling is a fraction of window.innerHeight, not of the field's
// own box, so growing the window has to re-measure a clamped field even though
// nothing about that field's box changed - a ResizeObserver alone never fires
// for it. Verified live in Chrome too (a 20000-char composer stuck at the old
// 300px ceiling after the viewport went 600 -> 900 tall).
test("with autoGrow, a clamped field re-clamps to the new ceiling when the window grows", () => {
  const longLine = "x".repeat(4000);
  render(<Textarea value={longLine} onChange={() => {}} autoGrow />);
  const textarea = screen.getByRole("textbox") as HTMLTextAreaElement;
  expect(textarea.style.height).toBe(`${window.innerHeight * MAX_HEIGHT_VIEWPORT_FRACTION}px`);

  const original = Object.getOwnPropertyDescriptor(window, "innerHeight");
  const taller = window.innerHeight * 2;
  try {
    Object.defineProperty(window, "innerHeight", { configurable: true, value: taller });
    window.dispatchEvent(new Event("resize"));
    // Still clamped, just to the new ceiling - the fixture's content stays
    // taller than either.
    expect(simulatedScrollHeight(longLine)).toBeGreaterThan(taller * MAX_HEIGHT_VIEWPORT_FRACTION);
    expect(textarea.style.height).toBe(`${taller * MAX_HEIGHT_VIEWPORT_FRACTION}px`);
  } finally {
    if (original) Object.defineProperty(window, "innerHeight", original);
  }
});

// --- minLines: a raised floor for a field whose job asks for room ---------

test("minLines drives BOTH the stylesheet's floor property and the native rows attribute", () => {
  render(<Textarea value="" onChange={() => {}} autoGrow minLines={6} />);
  const textarea = screen.getByRole("textbox") as HTMLTextAreaElement;
  expect(textarea.style.getPropertyValue("--textarea-min-lines")).toBe("6");
  // rows matters as much as the floor: it is what an autoGrow field's first
  // real measurement measures, so a floor raised without it is immediately
  // overwritten by a MIN_ROWS-tall measurement. Verified in Chrome - a 1-line
  // field stayed at 2 lines until rows followed.
  expect(textarea.getAttribute("rows")).toBe("6");
});

test("minLines can also LOWER the resting size below MIN_ROWS", () => {
  render(<Textarea value="" onChange={() => {}} autoGrow minLines={1} />);
  const textarea = screen.getByRole("textbox") as HTMLTextAreaElement;
  expect(textarea.getAttribute("rows")).toBe("1");
  expect(textarea.style.getPropertyValue("--textarea-min-lines")).toBe("1");
});

// A FLOOR, not a size: the measured height still governs above it, so raising
// the floor must not stop autoGrow from writing a taller measurement.
test("minLines does not stop autoGrow from applying its own measured height", () => {
  const longLine = "x".repeat(200);
  render(<Textarea value={longLine} onChange={() => {}} autoGrow minLines={6} />);
  const textarea = screen.getByRole("textbox") as HTMLTextAreaElement;
  expect(textarea.style.height).toBe(`${simulatedScrollHeight(longLine)}px`);
});

// Absent by default: the vast majority of fields want the shared MIN_ROWS floor
// from the stylesheet, and an inline property would shadow it forever.
test("without minLines no inline custom property is written, and rows stays at MIN_ROWS", () => {
  render(<Textarea value="" onChange={() => {}} autoGrow />);
  expect(screen.getByRole("textbox").getAttribute("style") ?? "").not.toContain("--textarea-min-lines");
  expect(screen.getByRole("textbox").getAttribute("rows")).toBe(String(MIN_ROWS));
});

test("onFocus and onBlur reach the native element", async () => {
  const user = userEvent.setup();
  const onFocus = vi.fn();
  const onBlur = vi.fn();
  render(
    <>
      <Textarea value="" onChange={() => {}} onFocus={onFocus} onBlur={onBlur} />
      <button type="button">elsewhere</button>
    </>,
  );
  await user.click(screen.getByRole("textbox"));
  expect(onFocus).toHaveBeenCalledTimes(1);
  await user.click(screen.getByRole("button"));
  expect(onBlur).toHaveBeenCalledTimes(1);
});

test("the CSS floor keeps the field at least MIN_ROWS lines tall in both variants, so it can never render unclickable", () => {
  const css = moduleCss();
  const base = /\.textarea\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
  const seamless = /\.seamless\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
  expect(base).toContain("min-height:");
  expect(seamless).toContain("min-height:");
  // Both floors are expressed in the same shared line count, which must agree
  // with the widget's own MIN_ROWS (CSS cannot read the TS constant).
  expect(css).toContain(`--textarea-min-lines: ${MIN_ROWS}`);
});

test("with autoGrow, height clamps at MAX_HEIGHT_VIEWPORT_FRACTION of the viewport height for very tall content", () => {
  const longLine = "x".repeat(4000); // simulated scrollHeight comfortably exceeds any real viewport
  render(<Textarea value={longLine} onChange={() => {}} autoGrow />);
  const textarea = screen.getByRole("textbox") as HTMLTextAreaElement;

  const expectedMax = window.innerHeight * MAX_HEIGHT_VIEWPORT_FRACTION;
  expect(simulatedScrollHeight(longLine)).toBeGreaterThan(expectedMax); // the fixture actually exercises the clamp
  expect(textarea.style.height).toBe(`${expectedMax}px`);
});
