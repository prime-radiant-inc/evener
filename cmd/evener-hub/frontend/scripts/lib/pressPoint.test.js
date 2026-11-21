// @vitest-environment jsdom

// `uncoveredCenter` is interpolated into the skillguard and cascadeguard
// one-page-turn press expressions, so its contract is shared by both guards:
// the caller presses only at a returned point, and a null means "not
// pressable yet". These cases pin that contract directly, since neither guard
// runs jsdom layout of its own.
import { beforeEach, expect, test, vi } from "vitest";
import { uncoveredCenter } from "./pressPoint.mjs";

const at = (x, y, width = 120, height = 40) => ({ x, y, width, height });
const middle = (rect) => ({ x: rect.x + rect.width / 2, y: rect.y + rect.height / 2 });

let boxes;
let topmost;

beforeEach(() => {
  document.body.innerHTML =
    '<button id="target"><span id="inner">target</span></button><button id="cover">cover</button>';
  boxes = new Map();
  topmost = null;
  for (const el of document.querySelectorAll("*")) {
    el.getBoundingClientRect = () => boxes.get(el) ?? at(0, 0, 0, 0);
    el.scrollIntoView = vi.fn();
  }
  document.elementFromPoint = () => topmost;
});

test("returns the centre of a topmost element and scrolls it into view", () => {
  const target = document.querySelector("#target");
  boxes.set(target, at(100, 200));
  topmost = target;
  expect(uncoveredCenter(target, "center")).toEqual(middle(at(100, 200)));
  expect(target.scrollIntoView).toHaveBeenCalledWith({ block: "center" });
});

test("returns null when another element is topmost at the centre", () => {
  const target = document.querySelector("#target");
  boxes.set(target, at(100, 200));
  topmost = document.querySelector("#cover");
  expect(uncoveredCenter(target, "center")).toBeNull();
});

test("returns a descendant as uncovered, as a real press would hit it", () => {
  const target = document.querySelector("#target");
  boxes.set(target, at(100, 200));
  topmost = document.querySelector("#inner");
  expect(uncoveredCenter(target, "center")).toEqual(middle(at(100, 200)));
});

test("returns null for an element with no area, even on its boundary point", () => {
  const target = document.querySelector("#target");
  boxes.set(target, at(100, 200, 0, 0));
  topmost = target;
  expect(uncoveredCenter(target, "center")).toBeNull();
});
