import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { SeenDivider } from "./SeenDivider";
import { unseenReplyLandingOffset } from "./viewAnchor";

test("renders a labelled marker naming this as new content, not a bare line", () => {
  render(<SeenDivider />);
  const row = screen.getByTestId("seen-divider");
  expect(row.textContent!.toLowerCase()).toContain("new");
});

test("carries no interactive role - it's a passive marker, unlike NewContentPill's button", () => {
  render(<SeenDivider />);
  expect(screen.queryByRole("button")).toBeNull();
});

// jsdom computes no layout, so each element's box is defined outright.
function box(el: Element, top: number): void {
  el.getBoundingClientRect = () => ({ top }) as DOMRect;
}

function portWithDivider(turnTop: number, dividerTop: number): HTMLElement {
  const port = document.createElement("div");
  Object.defineProperty(port, "clientHeight", { value: 600 });
  box(port, 100);
  const turn = document.createElement("div");
  turn.setAttribute("data-turn-id", "t2");
  box(turn, 100 + turnTop);
  const divider = document.createElement("div");
  divider.setAttribute("data-seen-divider", "");
  box(divider, 100 + dividerTop);
  turn.append(divider);
  port.append(turn);
  return port;
}

test("no landing is measured until the divider's row renders", () => {
  expect(unseenReplyLandingOffset(document.createElement("div"))).toBeUndefined();
});

test("a short message above the divider keeps the landing at the top of its turn", () => {
  expect(unseenReplyLandingOffset(portWithDivider(300, 420))).toBe(300);
});

test("a message long enough to push the divider down the viewport lands on the divider", () => {
  expect(unseenReplyLandingOffset(portWithDivider(300, 900))).toBe(900);
});

test("a divider outside any turn block is its own landing", () => {
  const port = document.createElement("div");
  box(port, 100);
  const divider = document.createElement("div");
  divider.setAttribute("data-seen-divider", "");
  box(divider, 250);
  port.append(divider);
  expect(unseenReplyLandingOffset(port)).toBe(150);
});
