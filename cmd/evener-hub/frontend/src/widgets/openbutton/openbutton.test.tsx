import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { OpenButton, OpenIcon } from ".";

afterEach(cleanup);

test("the word form renders the word followed by the glyph and names itself from label", () => {
  const onClick = vi.fn();
  const { container } = render(<OpenButton label="Open transcript" onClick={onClick} />);
  const button = screen.getByRole("button", { name: "Open transcript" });
  expect(button.textContent).toContain("open");
  // The glyph is present but decorative - the label carries the name.
  const svg = container.querySelector("svg");
  expect(svg?.getAttribute("aria-hidden")).toBe("true");
  fireEvent.click(button);
  expect(onClick).toHaveBeenCalledTimes(1);
});

test("the word form's accessible name falls back to the visible word", () => {
  render(<OpenButton word="open transcript" onClick={() => {}} />);
  expect(screen.getByRole("button", { name: "open transcript" })).toBeTruthy();
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

test("the iconOnly form shows no word, names itself from label, and defaults its title to it", () => {
  render(<OpenButton iconOnly label="Open transcript" size="xs" onClick={() => {}} />);
  const button = screen.getByRole("button", { name: "Open transcript" });
  expect(button.textContent).toBe("");
  expect(button.getAttribute("title")).toBe("Open transcript");
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
  expect(container.querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");
  // Riding a disclosure costs the enclosing row nothing, same as the buttons.
  fireEvent.click(link);
  expect(onParentClick).not.toHaveBeenCalled();
});

test("tabIndex forwards to the underlying control (dense tree rows take -1)", () => {
  render(<OpenButton iconOnly label="Open transcript" tabIndex={-1} onClick={() => {}} />);
  expect(screen.getByRole("button", { name: "Open transcript" }).tabIndex).toBe(-1);
});

test("OpenIcon renders the box-arrow glyph on the 16px grid", () => {
  const { container } = render(<OpenIcon />);
  const svg = container.querySelector("svg");
  expect(svg?.getAttribute("viewBox")).toBe("0 0 16 16");
  expect(svg?.getAttribute("aria-hidden")).toBe("true");
});
