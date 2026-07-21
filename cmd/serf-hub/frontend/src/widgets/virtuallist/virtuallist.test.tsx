import { afterEach, beforeEach, describe, test, expect, vi } from "vitest";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { createRef } from "react";
import { cleanup, render, screen } from "@testing-library/react";
import { VirtualList, type VirtualListHandle } from "./index";
import rawStyles from "./virtuallist.module.css";
import { requireClass } from "../internal/requireClass";

// jsdom performs no real layout: every element's offsetHeight/offsetWidth
// is 0 and it has no ResizeObserver at all (@tanstack/react-virtual reads
// offsetHeight - see its getRect() - and falls back to a one-time initial
// read with no further updates when ResizeObserver is absent, per its own
// observeElementRect()). Without a mocked measurement the virtualizer
// would see a 0px-tall viewport and never render a single row, which
// wouldn't exercise anything - so every test here overrides offsetHeight
// on HTMLElement.prototype for a fixed CONTAINER_HEIGHT before rendering.
// This proves the virtualizer WIRING (row windowing, positions, total
// size, scrollToIndex's computed offset) against that fixed, fake
// viewport - it does NOT and CANNOT prove real browser scrolling/layout
// behavior, which jsdom has no way to exercise. Don't read the numeric
// assertions below as "scrolling works"; read them as "given this
// viewport height, the virtualizer computes the range/offsets react-virtual
// promises to compute". The scrollToIndex test mocks two more properties
// (scrollHeight/clientHeight) for the same reason, on the one element that
// needs them - see the comment at that test.
const CONTAINER_HEIGHT = 500;
let offsetHeightDescriptor: PropertyDescriptor | undefined;

beforeEach(() => {
  offsetHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
    configurable: true,
    value: CONTAINER_HEIGHT,
  });
});

afterEach(() => {
  cleanup();
  if (offsetHeightDescriptor) {
    Object.defineProperty(HTMLElement.prototype, "offsetHeight", offsetHeightDescriptor);
  }
});

const styles = {
  root: requireClass(rawStyles.root, "virtuallist.module.css", "root"),
};

const ROW_HEIGHT = 50;

function rootOf(container: HTMLElement): HTMLElement {
  return container.querySelector(`.${styles.root}`) as HTMLElement;
}

test("renders a windowed subset of rows, not all of a large count", () => {
  render(<VirtualList count={10_000} estimateSize={() => ROW_HEIGHT} renderRow={(i) => <div key={i}>row {i}</div>} />);
  const rendered = screen.getAllByText(/^row \d+$/);
  // 500px viewport / 50px rows = 10 fully visible + overscan on top; well
  // under the full 10,000, which is the property being proven here.
  expect(rendered.length).toBeGreaterThan(0);
  expect(rendered.length).toBeLessThan(100);
});

test("the initial window starts at index 0 and is contiguous", () => {
  render(
    <VirtualList count={10_000} estimateSize={() => ROW_HEIGHT} renderRow={(i) => <div key={i}>row {i}</div>} />,
  );
  const rendered = screen.getAllByText(/^row \d+$/).map((el) => Number(el.textContent!.replace("row ", "")));
  expect(rendered[0]).toBe(0);
  for (let i = 1; i < rendered.length; i++) {
    expect(rendered[i]).toBe(rendered[i - 1]! + 1);
  }
});

test("renderRow is called with the row's own index, not a window-relative one", () => {
  render(
    <VirtualList count={20} estimateSize={() => ROW_HEIGHT} renderRow={(i) => <div key={i}>row {i}</div>} />,
  );
  expect(screen.getByText("row 0")).toBeTruthy();
});

test("the sizer's total height reflects count * row height for a uniform estimateSize", () => {
  const count = 200;
  const { container } = render(
    <VirtualList count={count} estimateSize={() => ROW_HEIGHT} renderRow={(i) => <div key={i}>row {i}</div>} />,
  );
  const sizer = rootOf(container).firstElementChild as HTMLElement;
  expect(sizer.style.height).toBe(`${count * ROW_HEIGHT}px`);
});

test("the sizer's total height sums a non-uniform estimateSize correctly", () => {
  const count = 4;
  const sizes = [10, 20, 30, 40];
  const { container } = render(
    <VirtualList count={count} estimateSize={(i) => sizes[i]!} renderRow={(i) => <div key={i}>row {i}</div>} />,
  );
  const sizer = rootOf(container).firstElementChild as HTMLElement;
  expect(sizer.style.height).toBe(`${10 + 20 + 30 + 40}px`);
});

test("each rendered row is positioned at index * row height for a uniform estimateSize", () => {
  const { container } = render(
    <VirtualList count={10_000} estimateSize={() => ROW_HEIGHT} renderRow={(i) => <div key={i}>row {i}</div>} />,
  );
  const rowEls = Array.from(rootOf(container).querySelectorAll('[data-index]'));
  for (const el of rowEls) {
    const index = Number((el as HTMLElement).dataset.index);
    expect((el as HTMLElement).style.transform).toBe(`translateY(${index * ROW_HEIGHT}px)`);
  }
});

test("uses a sane nonzero overscan (renders more than exactly the visible count)", () => {
  render(
    <VirtualList count={10_000} estimateSize={() => ROW_HEIGHT} renderRow={(i) => <div key={i}>row {i}</div>} />,
  );
  const rendered = screen.getAllByText(/^row \d+$/);
  const exactlyVisible = CONTAINER_HEIGHT / ROW_HEIGHT; // 10
  expect(rendered.length).toBeGreaterThan(exactlyVisible);
});

test("count=0 renders no rows and does not crash", () => {
  const { container } = render(<VirtualList count={0} estimateSize={() => ROW_HEIGHT} renderRow={(i) => <div key={i}>row {i}</div>} />);
  expect(rootOf(container).querySelectorAll("[data-index]")).toHaveLength(0);
});

test("ref.current.scrollToIndex computes the align=start offset for the target index and issues it to the scroll container", () => {
  const count = 10_000;
  const ref = createRef<VirtualListHandle>();
  const { container } = render(
    <VirtualList ref={ref} count={count} estimateSize={() => ROW_HEIGHT} renderRow={(i) => <div key={i}>row {i}</div>} />,
  );
  const root = rootOf(container);
  // jsdom has no Element.scrollTo of its own to spy on; installing one is
  // the wiring seam this test is actually about - see the file-level
  // comment on why this can't be (and doesn't try to be) a real scroll
  // assertion.
  const scrollTo = vi.fn();
  root.scrollTo = scrollTo;
  // scrollToIndex clamps its target against the scroll element's real
  // scrollHeight/clientHeight (not against anything the virtualizer
  // computes itself), so - like offsetHeight above - these need mocking
  // too, or every target clamps down to jsdom's default 0/0 = maxOffset 0.
  Object.defineProperty(root, "scrollHeight", { configurable: true, value: count * ROW_HEIGHT });
  Object.defineProperty(root, "clientHeight", { configurable: true, value: CONTAINER_HEIGHT });

  expect(ref.current).not.toBeNull();
  ref.current!.scrollToIndex(500, { align: "start" });

  expect(scrollTo).toHaveBeenCalledExactlyOnceWith({ top: 500 * ROW_HEIGHT, behavior: "auto" });
});

// dynamic mode (transcript turns have wildly variable height - one tool call
// vs. a long streamed response - so estimateSize alone is not good enough
// for that consumer). Opt-in and off by default so every existing
// fixed-height consumer above is completely unaffected: measureElement's
// default measurement reads element.offsetHeight, so a per-element getter
// (rather than the flat stub the tests above use) is what lets these two
// tests tell "the estimate" and "the real measured height" apart.
describe("dynamic sizing", () => {
  const MEASURED_HEIGHT = 120;

  function stubPerElementOffsetHeight(styles: { root: string }) {
    Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
      configurable: true,
      get(this: HTMLElement) {
        return this.classList.contains(styles.root) ? CONTAINER_HEIGHT : MEASURED_HEIGHT;
      },
    });
  }

  test("dynamic omitted (default false) never measures - the estimate stays authoritative even when the real height differs", () => {
    stubPerElementOffsetHeight(styles);
    const count = 10; // all 10 fit the 500px viewport at the 50px estimate, so all 10 mount
    const { container } = render(
      <VirtualList count={count} estimateSize={() => ROW_HEIGHT} renderRow={(i) => <div key={i}>row {i}</div>} />,
    );
    const sizer = rootOf(container).firstElementChild as HTMLElement;
    expect(sizer.style.height).toBe(`${count * ROW_HEIGHT}px`);
  });

  test("dynamic=true measures each rendered row's real height and corrects the total", () => {
    stubPerElementOffsetHeight(styles);
    const count = 10;
    const { container } = render(
      <VirtualList dynamic count={count} estimateSize={() => ROW_HEIGHT} renderRow={(i) => <div key={i}>row {i}</div>} />,
    );
    const sizer = rootOf(container).firstElementChild as HTMLElement;
    expect(sizer.style.height).toBe(`${count * MEASURED_HEIGHT}px`);
  });

  test("dynamic=true still positions rows correctly (each translateY reflects the corrected running total, not index * estimate)", () => {
    stubPerElementOffsetHeight(styles);
    const { container } = render(
      <VirtualList dynamic count={5} estimateSize={() => ROW_HEIGHT} renderRow={(i) => <div key={i}>row {i}</div>} />,
    );
    const rowEls = Array.from(rootOf(container).querySelectorAll("[data-index]"));
    for (const el of rowEls) {
      const index = Number((el as HTMLElement).dataset.index);
      expect((el as HTMLElement).style.transform).toBe(`translateY(${index * MEASURED_HEIGHT}px)`);
    }
  });
});

test("declares no :focus-visible rule of its own (rows are consumer content, not this widget's concern)", () => {
  // VirtualList is a windowing primitive with no interactive chrome of its
  // own - renderRow's content (if focusable) is responsible for its own
  // focus styling, the same way DiffBlock/CodeBlock's line content is.
  // This just documents that omission is deliberate rather than an oversight.
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "virtuallist.module.css"), "utf8");
  expect(css).not.toContain(":focus-visible");
});
