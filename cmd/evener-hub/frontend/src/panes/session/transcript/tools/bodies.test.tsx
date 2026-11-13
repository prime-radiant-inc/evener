import type { ItemModel } from "@evener/appwire-client";
import { act, render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { revealForFind } from "../../find/findReveal";
import { HeadClippedOutputBody, TailFoldedOutputBody } from "./bodies";

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "commandExecution", text: "", ...overrides };
}

// --- HeadClippedOutputBody (grep/ls/glob's shared "cheap" body) -----------

test("HeadClippedOutputBody: shows short output verbatim", () => {
  render(<HeadClippedOutputBody item={item({ output: "hello" })} live={false} />);
  expect(screen.getByText("hello")).toBeTruthy();
});

test("HeadClippedOutputBody: head-clips at 8000 chars with an ellipsis, no elision note", () => {
  const longOutput = "y".repeat(9000);
  render(<HeadClippedOutputBody item={item({ output: longOutput })} live={false} />);
  expect(screen.getByText(`${"y".repeat(8000)}…`)).toBeTruthy();
});

test("HeadClippedOutputBody: renders nothing for blank output", () => {
  const { container } = render(<HeadClippedOutputBody item={item({ output: "" })} live={false} />);
  expect(container.textContent).toBe("");
});

test("HeadClippedOutputBody: renders nothing for undefined output", () => {
  const { container } = render(<HeadClippedOutputBody item={item({ output: undefined })} live={false} />);
  expect(container.textContent).toBe("");
});

// --- TailFoldedOutputBody (read_file/shell's shared enriched body) -------

test("TailFoldedOutputBody: shows short output verbatim whether live or settled", () => {
  render(<TailFoldedOutputBody item={item({ output: "hi" })} live={true} />);
  expect(screen.getByText("hi")).toBeTruthy();
});

test("TailFoldedOutputBody: while live, shows the raw tail with no elision note", () => {
  const longOutput = "z".repeat(9000);
  render(<TailFoldedOutputBody item={item({ output: longOutput })} live={true} />);
  expect(screen.getByText("z".repeat(8000))).toBeTruthy();
});

test("TailFoldedOutputBody: once settled, folds with an honest elision note", () => {
  const longOutput = "z".repeat(9000);
  const { container } = render(<TailFoldedOutputBody item={item({ output: longOutput })} live={false} />);
  // getByText's default normalizer collapses the embedded newline to a
  // space, so this checks raw textContent instead of using the matcher.
  expect(container.textContent).toContain(
    `earlier output not retained — showing the last 8,000 chars\n${"z".repeat(8000)}`,
  );
});

test("TailFoldedOutputBody: renders nothing for blank output", () => {
  const { container } = render(<TailFoldedOutputBody item={item({ output: "" })} live={false} />);
  expect(container.textContent).toBe("");
});

// --- Find reveals words the fold would otherwise drop ----------------------

test("a folded output shows the stretch holding the words Find landed on, and says what it left out", () => {
  const output = `${"a".repeat(3000)} the early\nquokka line ${"b".repeat(20000)} the tail`;
  const call = item({ id: "found", output });
  const view = render(
    <>
      <TailFoldedOutputBody item={call} live={false} />
      <HeadClippedOutputBody item={item({ id: "other", output: "another call" })} live={false} />
    </>,
  );
  // Folded to its tail, the early words are not on screen.
  expect(view.container.textContent).not.toContain("quokka");

  act(() => revealForFind("found", "early quokka"));
  const text = view.container.textContent ?? "";
  expect(text).toContain("quokka line");
  // Still bounded: the excerpt replaces the tail rather than unfolding it all.
  expect(text.length).toBeLessThan(output.length);
  expect(view.container.querySelectorAll("[data-find-excerpt]")).toHaveLength(1);

  expect(text).toContain("another call");
  // Another call is untouched, and leaving the match restores the fold.
  act(() => revealForFind(null, ""));
  expect(view.container.textContent).not.toContain("quokka");
});

test("a head-clipped output shows words Find landed on past the clip", () => {
  const output = `${"y".repeat(9000)} late quokka ${"z".repeat(9000)}`;
  const view = render(<HeadClippedOutputBody item={item({ id: "clipped", output })} live={false} />);
  expect(view.container.textContent).not.toContain("quokka");
  act(() => revealForFind("clipped", "quokka"));
  expect(view.container.textContent).toContain("late quokka");
  act(() => revealForFind(null, ""));
});

test("words already inside the fold leave the output as it was", () => {
  const output = `${"a".repeat(9000)} tail quokka`;
  const view = render(<TailFoldedOutputBody item={item({ id: "tail", output })} live={false} />);
  const before = view.container.textContent;
  act(() => revealForFind("tail", "quokka"));
  expect(view.container.textContent).toBe(before);
  expect(view.container.querySelector("[data-find-excerpt]")).toBeNull();
  act(() => revealForFind(null, ""));
});
