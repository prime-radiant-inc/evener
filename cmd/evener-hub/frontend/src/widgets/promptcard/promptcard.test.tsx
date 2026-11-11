import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useLayoutEffect, useState } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { blockBody, readModuleCss } from "../../styles/cssBlock";
import { Textarea } from "../textarea";
import { PromptCard } from "./index";
import promptCardStyles from "./promptcard.module.css";

const motionMock = vi.hoisted(() => ({
  animate: vi.fn((_element: Element, _target: Record<string, string>, _transition: unknown) => ({
    finished: Promise.resolve(),
    stop: vi.fn(),
  })),
  reduced: false,
}));

vi.mock("../../motion", () => ({
  animate: motionMock.animate,
  spatialTransition: () => ({ duration: 0.24, ease: "easeOut" }),
  useReducedMotion: () => motionMock.reduced,
}));

function moduleCss(): string {
  return readModuleCss(import.meta.url, "promptcard.module.css");
}

/** A live card wrapping a controlled seamless field, the shape both real
 * callers (the session composer and the spawn form) render. */
function LiveCard(props: {
  leading?: React.ReactNode;
  actions?: React.ReactNode;
  hidden?: boolean;
  folded?: boolean;
  foldedCompact?: boolean;
  controlsTestId?: string;
}) {
  const [value, setValue] = useState("");
  return (
    <PromptCard
      data-testid="card"
      hidden={props.hidden}
      folded={props.folded}
      foldedCompact={props.foldedCompact}
      controlsTestId={props.controlsTestId}
      leading={props.leading}
      actions={props.actions}
      field={
        <Textarea
          value={value}
          onChange={(e) => setValue(e.target.value)}
          autoGrow
          seamless
          aria-label="Message"
          placeholder="Message the agent…"
        />
      }
    />
  );
}

test("renders the caller's field", () => {
  render(<LiveCard />);
  expect(screen.getByRole("textbox", { name: "Message" })).toBeTruthy();
});

test("typing in the field works through the card", async () => {
  const user = userEvent.setup();
  render(<LiveCard />);
  await user.type(screen.getByRole("textbox", { name: "Message" }), "hello");
  expect((screen.getByRole("textbox", { name: "Message" }) as HTMLTextAreaElement).value).toBe("hello");
});

test("renders the caller's leading and action controls", () => {
  render(
    <LiveCard
      leading={<button type="button">Attach</button>}
      actions={
        <>
          <button type="button">Stop</button>
          <button type="button">Send</button>
        </>
      }
    />,
  );
  expect(screen.getByRole("button", { name: "Attach" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Stop" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Send" })).toBeTruthy();
});

// The control row is the card's own, and only the card styles it: a caller
// hook that let one reach in and restyle the row is how spawn turned it into a
// fixed viewport band on a phone, stranding the attach button at the bottom of
// the screen (issue #198). Callers get a testid and their own controls.
test("forwards the caller's control-row hook without changing its child order", () => {
  render(
    <LiveCard
      controlsTestId="controls"
      leading={<button type="button">Attach</button>}
      actions={<button type="button">Send</button>}
    />,
  );

  expect(screen.getByTestId("controls").className).toBe(promptCardStyles.controls);
  expect(screen.getByTestId("controls").querySelectorAll("button")).toHaveLength(2);
});

// The action cluster's DOM order IS its visual order (the row is a plain flex
// row, no reordering), so a caller that puts Stop first gets Stop leftmost -
// which is the composer's whole reason for wanting the slot.
test("action controls keep the caller's order inside the trailing cluster", () => {
  render(
    <LiveCard
      actions={
        <>
          <button type="button">Stop</button>
          <button type="button">Send</button>
          <button type="button">Steer</button>
        </>
      }
    />,
  );
  const labels = [...screen.getByTestId("card").querySelectorAll("button")].map((b) => b.textContent);
  expect(labels).toEqual(["Stop", "Send", "Steer"]);
});

// A card with no controls at all is a legitimate shape (a finished session's
// collapsed follow-up affordance) - it must not render an empty control row
// reserving vertical space for nothing.
test("renders no control row at all when neither slot is supplied", () => {
  render(<LiveCard />);
  const card = screen.getByTestId("card");
  expect(card.children).toHaveLength(1); // the field alone
});

test("hidden takes the card out of the interaction tree, not just out of sight", () => {
  render(<LiveCard hidden actions={<button type="button">Send</button>} />);
  const card = screen.getByTestId("card");
  expect(card.hasAttribute("hidden")).toBe(true);
  expect(card.hasAttribute("inert")).toBe(true);
});

// The seamless field inside draws no ring of its own (widgets/textarea's
// `seamless` drops it), so the card MUST carry the focus affordance or focusing
// the field would show nothing at all. jsdom applies no stylesheet, so the rule
// is checked against the stylesheet text and the state against the DOM.
test("the card owns the focus ring for the seamless field inside it", () => {
  expect(moduleCss()).toMatch(/\.card:focus-within\s*\{[^}]*outline: var\(--focus-ring\)/);
});

test("the focus ring sits on the card edge, so the field shows one line instead of two", () => {
  const rule = /\.card:focus-within\s*\{([^}]*)\}/.exec(moduleCss())?.[1] ?? "";
  expect(rule).toMatch(/outline-offset:\s*0;/);
  expect(rule).toMatch(/border-color:\s*var\(--accent\);/);
});

// Queried only after focusing: jsdom's selector engine caches a :focus-within
// result per element, so an earlier "not yet focused" call on the same node
// would keep answering false afterwards.
test("focusing the field puts the card in :focus-within", () => {
  render(<LiveCard />);
  screen.getByRole("textbox", { name: "Message" }).focus();
  expect(screen.getByTestId("card").matches(":focus-within")).toBe(true);
});

test("the card draws exactly one hairline border, so a seamless field inside is not a box in a box", () => {
  const rule = /\.card\s*\{([^}]*)\}/.exec(moduleCss())?.[1] ?? "";
  expect(rule).toMatch(/border:\s*1px solid var\(--edge\)/);
});

test("the field slot stretches the caller's field across the card", () => {
  const rule = /\.field\s*\{([^}]*)\}/.exec(moduleCss())?.[1] ?? "";
  expect(rule).toMatch(/display:\s*flex/);
  expect(rule).toMatch(/flex-direction:\s*column/);
});

test("the card owns compact spacing for an empty, unfocused Duo composer", () => {
  const css = moduleCss();
  const mediaStart = css.indexOf("@media (min-width: 440px) and (max-width: 500px) and (max-aspect-ratio: 3 / 4)");
  expect(mediaStart).toBeGreaterThanOrEqual(0);
  const compact = blockBody(css, css.indexOf("{", mediaStart));
  expect(compact).toMatch(
    /:global\(\[data-composer\]\):not\(\[data-composer-editing\]\) \.card:has\(\[role="textbox"\]\[data-empty="true"\]\)\s*\{[^}]*gap:\s*0;[^}]*padding-block:\s*0;/,
  );
});

// The folded shape is the caller's signal: an empty, resting composer asks
// for it, and every other state renders the stacked card.
test("folded marks the card for the one-line layout", () => {
  render(<LiveCard folded />);
  expect(screen.getByTestId("card").getAttribute("data-folded")).toBe("true");
});

test("an expanded card carries no folded marker", () => {
  render(<LiveCard />);
  expect(screen.getByTestId("card").hasAttribute("data-folded")).toBe(false);
});

// The width container exists so a spread-out composer gets the one-line
// shape while a narrow pane keeps the stacked one, regardless of the
// viewport. jsdom applies no layout, so the rule itself is the assertion.
test("the folded card goes one line only when the card has room", () => {
  const css = moduleCss();
  expect(css).toMatch(/\.root\s*\{[^}]*container-type:\s*inline-size;/);
  const queryStart = css.indexOf("@container (min-width: 620px)");
  expect(queryStart).toBeGreaterThanOrEqual(0);
  const oneLine = blockBody(css, css.indexOf("{", queryStart));
  expect(oneLine).toMatch(/\.card\[data-folded="true"\]\s*\{[^}]*flex-direction:\s*row;/);
  expect(css).toMatch(/\.card\[data-folded="true"\] \.field\s*\{[^}]*flex:\s*1 1 10ch;/);
});

test("a compact folded card goes one line below the normal width threshold", () => {
  const css = moduleCss();
  expect(css).toMatch(/\.card\[data-folded="true"\]\[data-folded-compact="true"\]\s*\{[^}]*flex-direction:\s*row;/);
  expect(css).toMatch(/\.card\[data-folded="true"\]\[data-folded-compact="true"\] \.field\s*\{[^}]*flex:\s*1 1 10ch;/);
});

test("foldedCompact marks the card for the narrow one-line row", () => {
  render(<LiveCard folded foldedCompact />);
  expect(screen.getByTestId("card").getAttribute("data-folded-compact")).toBe("true");
});

// jsdom has no layout, so the height measurement that drives the transition
// is stubbed at the browser boundary. The widget's own logic still runs.
let originalOffsetHeight: PropertyDescriptor | undefined;
beforeEach(() => {
  motionMock.reduced = false;
  motionMock.animate.mockClear();
  originalOffsetHeight = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
    configurable: true,
    get(this: HTMLElement) {
      if (this.hasAttribute("hidden")) return 0;
      return this.getAttribute("data-folded") === "true" ? 38 : 90;
    },
  });
});

afterEach(() => {
  if (originalOffsetHeight) Object.defineProperty(HTMLElement.prototype, "offsetHeight", originalOffsetHeight);
});

test("folding animates from the card's previous height", () => {
  const { rerender } = render(<LiveCard />);
  expect(motionMock.animate).not.toHaveBeenCalled();
  rerender(<LiveCard folded />);
  expect(motionMock.animate).toHaveBeenCalledTimes(1);
  expect(motionMock.animate.mock.calls[0]?.[1]).toEqual({ height: "38px" });
});

// The page lays out around the card's root. A scroll port above the composer
// that grows for even one layout clamps its offset and keeps it after it
// shrinks back, so through the commit that swaps the shape, and the layout the
// card forces to measure it, the root keeps the outgoing height. A layout read
// earlier in the same commit (a transcript list above measuring its rows) sees
// the same height.
test("folding keeps the outgoing height around the card until the animation starts", () => {
  const rootHeights: number[] = [];
  let root: HTMLElement | null = null;
  const natural = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
    configurable: true,
    get(this: HTMLElement) {
      const height = natural?.get?.call(this) as number;
      const card = root?.firstElementChild;
      if (card instanceof HTMLElement && (this === card || this === root)) {
        const cardHeight = Number.parseFloat(card.style.height) || (natural?.get?.call(card) as number);
        const rootHeight = Number.parseFloat(root?.style.height ?? "") || cardHeight;
        rootHeights.push(rootHeight);
        return this === card ? cardHeight : rootHeight;
      }
      return height;
    },
  });
  function TranscriptAbove({ folded }: { folded: boolean }) {
    useLayoutEffect(() => {
      void root?.offsetHeight;
    }, [folded]);
    return null;
  }
  function Footer({ folded }: { folded: boolean }) {
    return (
      <>
        <TranscriptAbove folded={folded} />
        <LiveCard folded={folded} />
      </>
    );
  }
  const { rerender } = render(<Footer folded={false} />);
  root = screen.getByTestId("card").parentElement;
  rootHeights.length = 0;
  rerender(<Footer folded />);
  expect(motionMock.animate).toHaveBeenCalledTimes(1);
  expect(rootHeights.length).toBeGreaterThan(1);
  expect(new Set(rootHeights)).toEqual(new Set([90]));
});

test("the animating card carries the marker that anchors its controls", () => {
  const { rerender } = render(<LiveCard />);
  motionMock.animate.mockImplementationOnce(() => ({ finished: new Promise<void>(() => {}), stop: vi.fn() }));
  rerender(<LiveCard folded />);
  expect(screen.getByTestId("card").getAttribute("data-animating")).toBe("true");
});

test("a card returning from ask-pending appears without animating", () => {
  const { rerender } = render(<LiveCard hidden />);
  motionMock.animate.mockClear();
  rerender(<LiveCard folded />);
  expect(motionMock.animate).not.toHaveBeenCalled();
});

test("reduced motion applies the folded shape without animating", () => {
  motionMock.reduced = true;
  const { rerender } = render(<LiveCard />);
  rerender(<LiveCard folded />);
  expect(motionMock.animate).not.toHaveBeenCalled();
});
