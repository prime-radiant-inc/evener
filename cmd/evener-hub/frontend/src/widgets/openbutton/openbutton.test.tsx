import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { OpenButton, OpenIcon } from ".";

afterEach(cleanup);

// The repo's CSS-source test idiom (difftable.test.tsx, select.test.tsx):
// jsdom has no layout, so geometry contracts are pinned by reading the
// stylesheet's own source.
const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "openbutton.module.css"), "utf8");

test("the button form is icon-only: named by label, tooltip defaults to the one word 'Open'", () => {
  const onClick = vi.fn();
  const { container } = render(<OpenButton label="Open transcript" onClick={onClick} />);
  const button = screen.getByRole("button", { name: "Open transcript" });
  expect(button.textContent).toBe("");
  expect(button.getAttribute("title")).toBe("Open");
  expect(container.querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");
  fireEvent.click(button);
  expect(onClick).toHaveBeenCalledTimes(1);
});

test("the accessible name falls back to 'Open' when no label is given", () => {
  render(<OpenButton onClick={() => {}} />);
  expect(screen.getByRole("button", { name: "Open" })).toBeTruthy();
});

test("a click never reaches the enclosing row - the affordance rides disclosures", () => {
  const onParentClick = vi.fn();
  render(
    // biome-ignore lint/a11y/useKeyWithClickEvents: a stand-in disclosure row, not the component under test
    // biome-ignore lint/a11y/noStaticElementInteractions: same
    <div onClick={onParentClick}>
      <OpenButton label="Open subagent" onClick={() => {}} />
    </div>,
  );
  fireEvent.click(screen.getByRole("button", { name: "Open subagent" }));
  expect(onParentClick).not.toHaveBeenCalled();
});

test("the button form rides in a 1em layout shell: the hit size never reaches the line box", () => {
  // M1: the wrapper is the full hit size; the negative margin-block hands
  // exactly one text line back to layout, clamped at 0 so a large font never
  // earns a positive margin.
  expect(css).toMatch(/\.inline\s*\{[^}]*height:\s*28px/);
  expect(css).toMatch(/\.inline\s*\{[^}]*margin-block:\s*min\(0px,\s*calc\(\(1em - 28px\) \/ 2\)\)/);
  const media = /@media\s*\(max-width:\s*899px\)\s*\{([\s\S]*?)\n\}/.exec(css);
  expect(media, "openbutton.module.css must have a max-width:899px block").not.toBeNull();
  expect(media![1]).toMatch(/\.inline\s*\{[^}]*height:\s*var\(--tap-min\)/);
  expect(media![1]).toMatch(/margin-block:\s*min\(0px,\s*calc\(\(1em - var\(--tap-min\)\) \/ 2\)\)/);
});

test("the anchor form renders a real link to an external target, glyph following the words", () => {
  const onParentClick = vi.fn();
  const { container } = render(
    // biome-ignore lint/a11y/useKeyWithClickEvents: a stand-in disclosure row, not the component under test
    // biome-ignore lint/a11y/noStaticElementInteractions: same
    <div onClick={onParentClick}>
      <OpenButton href="vscode://file/src/agents/foo.md" word="open in editor" />
    </div>,
  );
  const link = screen.getByRole("link", { name: "open in editor" });
  expect(link.getAttribute("href")).toBe("vscode://file/src/agents/foo.md");
  expect(link.getAttribute("target")).toBe("_blank");
  expect(link.getAttribute("rel")).toContain("noopener");
  // The tooltip is the one word everywhere - the anchor form included.
  expect(link.getAttribute("title")).toBe("Open");
  expect(container.querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");
  fireEvent.click(link);
  expect(onParentClick).not.toHaveBeenCalled();
});

test("tabIndex forwards to the underlying control (dense tree rows take -1)", () => {
  render(<OpenButton label="Open transcript" tabIndex={-1} onClick={() => {}} />);
  expect(screen.getByRole("button", { name: "Open transcript" }).tabIndex).toBe(-1);
});

test("OpenIcon renders the box-arrow glyph on the 16px grid", () => {
  const { container } = render(<OpenIcon />);
  const svg = container.querySelector("svg");
  expect(svg?.getAttribute("viewBox")).toBe("0 0 16 16");
  expect(svg?.getAttribute("aria-hidden")).toBe("true");
});
