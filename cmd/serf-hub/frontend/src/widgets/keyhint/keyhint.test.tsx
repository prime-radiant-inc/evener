import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { chordLabel, KeyHint } from "./index";

function setPlatform(platform: string) {
  Object.defineProperty(window.navigator, "platform", { value: platform, configurable: true });
}

const ORIGINAL_PLATFORM = window.navigator.platform;

afterEach(() => {
  cleanup();
  setPlatform(ORIGINAL_PLATFORM);
});

test("renders one kbd element per key", () => {
  render(<KeyHint keys={["Shift", "K"]} />);
  expect(document.querySelectorAll("kbd")).toHaveLength(2);
});

test("renders a non-Mod key verbatim", () => {
  render(<KeyHint keys={["K"]} />);
  expect(screen.getByText("K")).toBeTruthy();
});

test("splits Mod to the platform symbol: Mac shows the command glyph", () => {
  setPlatform("MacIntel");
  render(<KeyHint keys={["Mod", "K"]} />);
  expect(screen.getByText("⌘")).toBeTruthy();
  expect(screen.queryByText("Ctrl")).toBeNull();
});

test("splits Mod to the platform symbol: non-Mac shows Ctrl", () => {
  setPlatform("Win32");
  render(<KeyHint keys={["Mod", "K"]} />);
  expect(screen.getByText("Ctrl")).toBeTruthy();
  expect(screen.queryByText("⌘")).toBeNull();
});

test("separates multiple keys visibly", () => {
  render(<KeyHint keys={["Mod", "Shift", "K"]} />);
  // three kbd elements, two literal "+" separators between them
  expect(document.querySelectorAll("kbd")).toHaveLength(3);
  expect(screen.getAllByText("+")).toHaveLength(2);
});

test("renders a single key with no separator", () => {
  render(<KeyHint keys={["Enter"]} />);
  expect(document.querySelectorAll("kbd")).toHaveLength(1);
  expect(screen.queryByText("+")).toBeNull();
});

// --- compact: a bare glyph run for inside a button ----------------------
//
// Three bordered <kbd> boxes plus a "+" inside a Send/Steer button dominate
// the button. Compact renders the same chord as one unadorned glyph run
// (⇧↵, ⌘↵) - the convention the command palette's own help rows already
// use - while keeping the SPOKEN name as words.

test("compact renders one glyph run with no kbd boxes and no separator", () => {
  render(<KeyHint keys={["Shift", "Enter"]} compact />);
  expect(document.querySelectorAll("kbd")).toHaveLength(0);
  expect(screen.getByText("⇧↵")).toBeTruthy();
  expect(screen.queryByText("+")).toBeNull();
});

test("compact maps Mod to the platform glyph: Mac shows the command glyph", () => {
  setPlatform("MacIntel");
  render(<KeyHint keys={["Mod", "Enter"]} compact />);
  expect(screen.getByText("⌘↵")).toBeTruthy();
});

test("compact maps Mod to the platform glyph: non-Mac shows Ctrl verbatim", () => {
  setPlatform("Win32");
  render(<KeyHint keys={["Mod", "Enter"]} compact />);
  expect(screen.getByText("Ctrl↵")).toBeTruthy();
});

test("compact renders a key with no glyph mapping verbatim", () => {
  render(<KeyHint keys={["Shift", "K"]} compact />);
  expect(screen.getByText("⇧K")).toBeTruthy();
});

// The glyph run must never become an enclosing control's accessible name: a
// screen reader announcing "Steer ⇧↵" is worse than useless. The glyphs are
// aria-hidden and the words ride along in a visually-hidden span, so the
// spoken name is identical to the non-compact form's.
test("compact keeps the enclosing button's spoken name as words, not glyphs", () => {
  render(
    <button type="button">
      Steer <KeyHint keys={["Shift", "Enter"]} compact />
    </button>,
  );
  expect(screen.getByRole("button", { name: "Steer Shift+Enter" })).toBeTruthy();
});

test("compact's spoken words match the non-compact form's own accessible name", () => {
  const { unmount } = render(
    <button type="button">
      Send <KeyHint keys={["Mod", "Enter"]} />
    </button>,
  );
  const plainName = screen.getByRole("button").textContent;
  unmount();

  render(
    <button type="button">
      Send <KeyHint keys={["Mod", "Enter"]} compact />
    </button>,
  );
  expect(screen.getByRole("button", { name: plainName ?? "" })).toBeTruthy();
});

// The compact form sits inside buttons of every variant, including a filled
// primary whose own label already sits near the AA contrast floor - so it
// stays subordinate by size and face, never by a thinner color that would go
// illegible on that background.
test("compact inherits the host control's own text color rather than thinning it", () => {
  const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "keyhint.module.css"), "utf8");
  const rule = /\.glyphs\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
  expect(rule).toContain("color: inherit");
  expect(rule).toContain("font-size: var(--font-size-caption)");
});

test("compact hides the glyph run from assistive tech", () => {
  render(<KeyHint keys={["Shift", "Enter"]} compact />);
  expect(screen.getByText("⇧↵").getAttribute("aria-hidden")).toBe("true");
});

// --- chordLabel: the same chord as a plain string ------------------------
//
// A Tooltip label is a string, not an element, so a control whose visible face
// is just its verb (the composer's Send/Steer) needs the chord in that form.
// It has to be the SAME text the rendered forms produce, which is exactly why
// it shares displayOf rather than re-spelling the platform split.
test("chordLabel joins the chord with +, matching what the compact form speaks", () => {
  expect(chordLabel(["Shift", "Enter"])).toBe("Shift+Enter");
  // The compact form's visually-hidden text is that same string - which is what
  // makes "Steer ⇧↵" speakable - so a tooltip built from chordLabel and a
  // KeyHint inside a control agree on the wording.
  const { container } = render(<KeyHint keys={["Shift", "Enter"]} compact />);
  expect(container.textContent).toContain(chordLabel(["Shift", "Enter"]));
});

test("chordLabel applies the same Mod platform split the rendered forms do", () => {
  const expected = /Mac|iPhone|iPad|iPod/.test(window.navigator.platform) ? "⌘" : "Ctrl";
  expect(chordLabel(["Mod", "Enter"])).toBe(`${expected}+Enter`);
});
