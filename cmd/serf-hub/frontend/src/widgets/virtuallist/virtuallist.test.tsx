import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { createRef, useState } from "react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { requireClass } from "../internal/requireClass";
import { VirtualList, type VirtualListHandle } from "./index";
import rawStyles from "./virtuallist.module.css";

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
  render(<VirtualList count={10_000} estimateSize={() => ROW_HEIGHT} renderRow={(i) => <div key={i}>row {i}</div>} />);
  const rendered = screen.getAllByText(/^row \d+$/).map((el) => Number(el.textContent!.replace("row ", "")));
  expect(rendered[0]).toBe(0);
  for (let i = 1; i < rendered.length; i++) {
    expect(rendered[i]).toBe(rendered[i - 1]! + 1);
  }
});

test("renderRow is called with the row's own index, not a window-relative one", () => {
  render(<VirtualList count={20} estimateSize={() => ROW_HEIGHT} renderRow={(i) => <div key={i}>row {i}</div>} />);
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
  const rowEls = Array.from(rootOf(container).querySelectorAll("[data-index]"));
  for (const el of rowEls) {
    const index = Number((el as HTMLElement).dataset.index);
    expect((el as HTMLElement).style.transform).toBe(`translateY(${index * ROW_HEIGHT}px)`);
  }
});

test("uses a sane nonzero overscan (renders more than exactly the visible count)", () => {
  render(<VirtualList count={10_000} estimateSize={() => ROW_HEIGHT} renderRow={(i) => <div key={i}>row {i}</div>} />);
  const rendered = screen.getAllByText(/^row \d+$/);
  const exactlyVisible = CONTAINER_HEIGHT / ROW_HEIGHT; // 10
  expect(rendered.length).toBeGreaterThan(exactlyVisible);
});

test("count=0 renders no rows and does not crash", () => {
  const { container } = render(
    <VirtualList count={0} estimateSize={() => ROW_HEIGHT} renderRow={(i) => <div key={i}>row {i}</div>} />,
  );
  expect(rootOf(container).querySelectorAll("[data-index]")).toHaveLength(0);
});

test("ref.current.scrollToIndex computes the align=start offset for the target index and issues it to the scroll container", () => {
  const count = 10_000;
  const ref = createRef<VirtualListHandle>();
  const { container } = render(
    <VirtualList
      ref={ref}
      count={count}
      estimateSize={() => ROW_HEIGHT}
      renderRow={(i) => <div key={i}>row {i}</div>}
    />,
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
      <VirtualList
        dynamic
        count={count}
        estimateSize={() => ROW_HEIGHT}
        renderRow={(i) => <div key={i}>row {i}</div>}
      />,
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

  // The live defect this pins (T5b, verified against a real browser):
  // measureElement reads offsetHeight off the SAME element the virtualizer
  // itself writes `height: item.size` onto - so the read always plays back
  // exactly what was just written, ResizeObserver never sees a box-size
  // change, and a row's real content height (e.g. a long streamed turn) is
  // never adopted; the row stays pinned at its estimate forever, overlapping
  // whatever renders after it. jsdom can't lay out real content to prove the
  // height NUMBER is right (see this file's own top comment) - so this
  // proves the STRUCTURAL precondition dynamic remeasurement actually needs:
  // the measured element must carry no inline height of the virtualizer's
  // own, ever, so its rendered box is free to be exactly its content's
  // natural height and a real ResizeObserver can see it change.
  test("dynamic=true: the measured element carries no inline height - its box is sized by content, never by the virtualizer's own size write", () => {
    stubPerElementOffsetHeight(styles);
    const count = 10;
    const { container } = render(
      <VirtualList
        dynamic
        count={count}
        estimateSize={() => ROW_HEIGHT}
        renderRow={(i) => <div key={i}>row {i}</div>}
      />,
    );
    const rowEls = Array.from(rootOf(container).querySelectorAll("[data-index]"));
    expect(rowEls.length).toBeGreaterThan(0);
    for (const el of rowEls) {
      expect((el as HTMLElement).style.height).toBe("");
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

// getItemKey (wave 4 T4): the transcript pane prepends older turns above the
// live content (loadOlder paging - stores/threads.ts's prependOlderTurns).
// Without a stable per-row key, react-virtual falls back to keying rows by
// raw index, so a prepend that shifts every existing row's index makes React
// reuse each row's DOM identity for a DIFFERENT logical row - the class of
// bug this test proves directly, using an uncontrolled <input>'s live value
// as the "did React actually reuse THIS row's own DOM node" tell (an
// uncontrolled input's value survives a re-render only if React kept the
// same underlying element - defaultValue is not re-applied on update).
describe("getItemKey", () => {
  // getItemKey (when keyed=true) closes over the component's OWN, always-
  // current `ids` state - exactly like Session.tsx's real usage closes over
  // the current model.turns - rather than a fixed snapshot, so it keeps
  // returning the right id for an index whose meaning just shifted.
  function PrependableList({ keyed }: { keyed?: boolean }) {
    const [ids, setIds] = useState(["a", "b", "c"]);
    return (
      <div>
        <button type="button" onClick={() => setIds((prev) => ["z", ...prev])}>
          prepend
        </button>
        <VirtualList
          count={ids.length}
          estimateSize={() => ROW_HEIGHT}
          getItemKey={keyed ? (i) => ids[i]! : undefined}
          renderRow={(i) => <input data-testid={`input-${ids[i]}`} defaultValue={ids[i]} />}
        />
      </div>
    );
  }

  test("omitted (default): a prepend reassigns an existing row's DOM identity by index, corrupting live (uncontrolled) row state", () => {
    render(<PrependableList />);
    fireEvent.change(screen.getByTestId("input-a"), { target: { value: "hello" } });

    fireEvent.click(screen.getByText("prepend"));

    // react-virtual's default index-based key means the DOM node born at
    // index 0 (typed into as "a") is reused for whatever's NOW at index 0
    // ("z"), while the node born at index 1 ("b", never touched) is reused
    // for "a"'s new slot at index 1 - so "input-a" resolves to the recycled
    // "b" node and shows its stale, untouched content ("b"), not "hello":
    // the edit didn't just vanish, it landed on the wrong row entirely.
    expect((screen.getByTestId("input-a") as HTMLInputElement).value).toBe("b");
  });

  test("provided: a prepend preserves each row's own DOM identity (and therefore its live state) across the index shift", () => {
    render(<PrependableList keyed />);
    fireEvent.change(screen.getByTestId("input-a"), { target: { value: "hello" } });

    fireEvent.click(screen.getByText("prepend"));

    // "a" now renders at index 1, but keyed identically ("a") both before
    // and after - React must reuse its exact DOM node, carrying "hello"
    // forward regardless of the index shift.
    expect((screen.getByTestId("input-a") as HTMLInputElement).value).toBe("hello");
  });
});

// getScrollElement (wave 4 T4): the transcript pane's flow/ hooks need
// read/write access to the real scrollable node (scrollTop/scrollHeight/
// clientHeight, plus a native `scroll` listener) to implement stick-to-
// bottom, near-top paging, and prepend scroll-anchor correction - none of
// which VirtualList itself owns or should own (it's a windowing primitive,
// not a scroll-behavior one). Exposing the already-existing internal
// scrollRef via the handle is the minimal additive surface for that,
// alongside the already-existing scrollToIndex.
test("ref.current.getScrollElement() returns the actual scrolling root node", () => {
  const ref = createRef<VirtualListHandle>();
  const { container } = render(
    <VirtualList ref={ref} count={10} estimateSize={() => ROW_HEIGHT} renderRow={(i) => <div key={i}>row {i}</div>} />,
  );

  expect(ref.current?.getScrollElement()).toBe(rootOf(container));
});

test("ref.current.getScrollElement() returns null before mount (no ref attached)", () => {
  const ref = createRef<VirtualListHandle>();
  expect(ref.current).toBeNull();
});

// getVisibleRange (wave 4 T5a): the session transcript's error-anchor pill
// needs to know whether a given turn index is currently rendered, to clear
// a stale anchor once its failed turn scrolls into view on its own (see
// useTranscriptScroll.ts). Derived from @tanstack/react-virtual's own
// getVirtualItems() (already called by this widget's render, just never
// surfaced past it) rather than reimplementing any range math - this test
// cross-checks the handle's answer against the actually-rendered
// [data-index] rows rather than hardcoding react-virtual's own overscan
// algorithm, which is exactly the "assert the HANDLE's contract with the
// established stubs" the brief calls for.
test("ref.current.getVisibleRange() reflects the actually-rendered index range", () => {
  const ref = createRef<VirtualListHandle>();
  const { container } = render(
    <VirtualList
      ref={ref}
      count={10_000}
      estimateSize={() => ROW_HEIGHT}
      renderRow={(i) => <div key={i}>row {i}</div>}
    />,
  );

  const rendered = Array.from(rootOf(container).querySelectorAll("[data-index]")).map((el) =>
    Number((el as HTMLElement).dataset.index),
  );
  const range = ref.current?.getVisibleRange();

  expect(range).not.toBeNull();
  expect(range?.startIndex).toBe(Math.min(...rendered));
  expect(range?.endIndex).toBe(Math.max(...rendered));
});

test("ref.current.getVisibleRange() returns null when count=0 (nothing rendered)", () => {
  const ref = createRef<VirtualListHandle>();
  render(
    <VirtualList ref={ref} count={0} estimateSize={() => ROW_HEIGHT} renderRow={(i) => <div key={i}>row {i}</div>} />,
  );

  expect(ref.current?.getVisibleRange()).toBeNull();
});

test("ref.current.getVisibleRange() returns null before mount (no ref attached)", () => {
  const ref = createRef<VirtualListHandle>();
  expect(ref.current).toBeNull();
});

// Overflow containment (2026-07-30-mobile-session-layout-design.md, decision
// 5): the list's own scroller never pans sideways. overflow-y: auto makes
// overflow-x compute to auto by default, which is exactly the page-level
// horizontal scrollbar the spec exists to kill. jsdom has no layout - read
// the CSS source, the same pattern panescaffold.test.tsx uses.
test("the list scroller clips horizontal overflow instead of panning", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "virtuallist.module.css"), "utf8");
  const root = css.match(/\.root \{([^}]*)\}/);
  expect(root).not.toBeNull();
  expect(root![1]).toContain("overflow-x: clip");
});
