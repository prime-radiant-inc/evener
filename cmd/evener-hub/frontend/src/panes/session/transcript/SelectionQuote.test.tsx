import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { createRef } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { SelectionQuote } from "./SelectionQuote";

// jsdom's window.getSelection() exists but performs no real layout/range
// geometry (SelectionQuote.tsx's own header comment), so every test here
// installs a fake Selection - just enough of the interface this component
// actually reads (isCollapsed/rangeCount/toString/getRangeAt) - rather than
// depending on jsdom's real (very thin) selection APIs. getBoundingClientRect
// is stubbed on both the fake range and every rendered element for the same
// reason: jsdom performs no layout, so every real call returns an all-zero
// rect.
function fakeRect(overrides: Partial<DOMRect> = {}): DOMRect {
  return {
    x: 0,
    y: 0,
    width: 100,
    height: 20,
    top: 100,
    left: 100,
    right: 200,
    bottom: 120,
    toJSON: () => ({}),
    ...overrides,
  } as DOMRect;
}

function installFakeSelection(options: { text: string; anchorNode: Node; rect?: DOMRect } | null) {
  const selection = options
    ? ({
        isCollapsed: false,
        rangeCount: 1,
        toString: () => options.text,
        getRangeAt: () => ({
          commonAncestorContainer: options.anchorNode,
          getBoundingClientRect: () => options.rect ?? fakeRect(),
        }),
      } as unknown as Selection)
    : ({ isCollapsed: true, rangeCount: 0, toString: () => "" } as unknown as Selection);
  vi.spyOn(window, "getSelection").mockReturnValue(selection);
}

// Renders a transcript container with one marked "message content" node
// (data-view-anchor-message="true", the same attribute TurnBlock.tsx and
// Session.tsx stamp on real message wrappers) and one unmarked "chrome" node,
// so containment tests can select text under each and assert different
// outcomes. Returns the container ref SelectionQuote is given, plus both
// target text nodes to use as a fake selection's anchorNode.
function renderHarness(onInvoke: (text: string) => void) {
  const containerRef = createRef<HTMLDivElement>();
  render(
    <div ref={containerRef} data-testid="transcript-container">
      <div data-view-anchor-message="true" data-testid="message-node">
        selectable message prose
      </div>
      <div data-view-anchor-message="false" data-testid="chrome-node">
        turn separator chrome
      </div>
      <SelectionQuote containerRef={containerRef} actions={[{ label: "Quote in reply", onInvoke }]} />
    </div>,
  );
  const messageNode = screen.getByTestId("message-node").firstChild as Text;
  const chromeNode = screen.getByTestId("chrome-node").firstChild as Text;
  return { containerRef, messageNode, chromeNode };
}

beforeEach(() => {
  for (const el of document.querySelectorAll("*")) {
    vi.spyOn(el, "getBoundingClientRect").mockReturnValue(fakeRect());
  }
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

test("renders nothing when there is no active selection", () => {
  installFakeSelection(null);
  renderHarness(() => {});
  expect(screen.queryByRole("toolbar", { name: "Selection actions" })).toBeNull();
});

test("shows the bar with its action once a selection lands inside marked message content", () => {
  const { messageNode, containerRef } = renderHarness(() => {});
  installFakeSelection({ text: "quoted prose", anchorNode: messageNode });
  act(() => {
    containerRef.current?.dispatchEvent(new Event("pointerup", { bubbles: true }));
  });
  expect(screen.getByRole("toolbar", { name: "Selection actions" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Quote in reply" })).toBeTruthy();
});

test("stays hidden for a selection outside marked message content (transcript chrome)", () => {
  const { chromeNode, containerRef } = renderHarness(() => {});
  installFakeSelection({ text: "chrome text", anchorNode: chromeNode });
  act(() => {
    containerRef.current?.dispatchEvent(new Event("pointerup", { bubbles: true }));
  });
  expect(screen.queryByRole("toolbar", { name: "Selection actions" })).toBeNull();
});

test("stays hidden for a collapsed (empty) selection", () => {
  const { containerRef } = renderHarness(() => {});
  installFakeSelection(null);
  act(() => {
    containerRef.current?.dispatchEvent(new Event("pointerup", { bubbles: true }));
  });
  expect(screen.queryByRole("toolbar", { name: "Selection actions" })).toBeNull();
});

test("clicking the action invokes it with the captured selected text, then clears the bar", () => {
  const onInvoke = vi.fn();
  const { messageNode, containerRef } = renderHarness(onInvoke);
  installFakeSelection({ text: "quoted prose", anchorNode: messageNode });
  act(() => {
    containerRef.current?.dispatchEvent(new Event("pointerup", { bubbles: true }));
  });

  fireEvent.click(screen.getByRole("button", { name: "Quote in reply" }));

  expect(onInvoke).toHaveBeenCalledExactlyOnceWith("quoted prose");
  expect(screen.queryByRole("toolbar", { name: "Selection actions" })).toBeNull();
});

test("pressing Escape dismisses the bar", () => {
  const { messageNode, containerRef } = renderHarness(() => {});
  installFakeSelection({ text: "quoted prose", anchorNode: messageNode });
  act(() => {
    containerRef.current?.dispatchEvent(new Event("pointerup", { bubbles: true }));
  });
  expect(screen.getByRole("toolbar", { name: "Selection actions" })).toBeTruthy();

  fireEvent.keyDown(document, { key: "Escape" });

  expect(screen.queryByRole("toolbar", { name: "Selection actions" })).toBeNull();
});

test("scrolling dismisses a visible bar - it is position:fixed and does not track the selection", () => {
  const { messageNode, containerRef } = renderHarness(() => {});
  installFakeSelection({ text: "quoted prose", anchorNode: messageNode });
  act(() => {
    containerRef.current?.dispatchEvent(new Event("pointerup", { bubbles: true }));
  });
  expect(screen.getByRole("toolbar", { name: "Selection actions" })).toBeTruthy();

  act(() => {
    document.dispatchEvent(new Event("scroll", { bubbles: true }));
  });

  expect(screen.queryByRole("toolbar", { name: "Selection actions" })).toBeNull();
});

test("Mod+' invokes the first action directly with the captured selection, then clears the bar", () => {
  const onInvoke = vi.fn();
  const { messageNode, containerRef } = renderHarness(onInvoke);
  installFakeSelection({ text: "quoted prose", anchorNode: messageNode });
  act(() => {
    containerRef.current?.dispatchEvent(new Event("pointerup", { bubbles: true }));
  });
  expect(screen.getByRole("toolbar", { name: "Selection actions" })).toBeTruthy();

  fireEvent.keyDown(document, { key: "'", metaKey: true });

  expect(onInvoke).toHaveBeenCalledExactlyOnceWith("quoted prose");
  expect(screen.queryByRole("toolbar", { name: "Selection actions" })).toBeNull();
});

test("Mod+' with ctrlKey also invokes the first action", () => {
  const onInvoke = vi.fn();
  const { messageNode, containerRef } = renderHarness(onInvoke);
  installFakeSelection({ text: "quoted prose", anchorNode: messageNode });
  act(() => {
    containerRef.current?.dispatchEvent(new Event("pointerup", { bubbles: true }));
  });

  fireEvent.keyDown(document, { key: "'", ctrlKey: true });

  expect(onInvoke).toHaveBeenCalledExactlyOnceWith("quoted prose");
});

test("Mod+' does nothing when there is no captured selection", () => {
  const onInvoke = vi.fn();
  renderHarness(onInvoke);
  installFakeSelection(null);

  fireEvent.keyDown(document, { key: "'", metaKey: true });

  expect(onInvoke).not.toHaveBeenCalled();
});

test("Mod+' with altKey set is ignored (AltGr types a literal ' on some layouts)", () => {
  const onInvoke = vi.fn();
  const { messageNode, containerRef } = renderHarness(onInvoke);
  installFakeSelection({ text: "quoted prose", anchorNode: messageNode });
  act(() => {
    containerRef.current?.dispatchEvent(new Event("pointerup", { bubbles: true }));
  });

  // AltGr is reported by browsers as ctrlKey+altKey both true.
  fireEvent.keyDown(document, { key: "'", ctrlKey: true, altKey: true });

  expect(onInvoke).not.toHaveBeenCalled();
  expect(screen.getByRole("toolbar", { name: "Selection actions" })).toBeTruthy();
});

test("Mod+' is ignored once another handler has already called preventDefault on the keydown", () => {
  const onInvoke = vi.fn();
  const { messageNode, containerRef } = renderHarness(onInvoke);
  installFakeSelection({ text: "quoted prose", anchorNode: messageNode });
  act(() => {
    containerRef.current?.dispatchEvent(new Event("pointerup", { bubbles: true }));
  });
  const preventIt = (event: globalThis.KeyboardEvent) => event.preventDefault();
  document.addEventListener("keydown", preventIt, true);

  try {
    fireEvent.keyDown(document, { key: "'", metaKey: true });
  } finally {
    document.removeEventListener("keydown", preventIt, true);
  }

  expect(onInvoke).not.toHaveBeenCalled();
});

// BLOCKER fix: onInvoke used to run INSIDE the setSelection updater
// function, which React calls twice under StrictMode - see SelectionQuote.
// tsx's own comment on this fix. Rendering under React.StrictMode and
// confirming exactly one invocation (not two) is the regression test for
// that double-insert bug.
test("Mod+' invokes the action exactly once under React.StrictMode (no impure double-invoke)", async () => {
  const { StrictMode } = await import("react");
  const onInvoke = vi.fn();
  const containerRef = createRef<HTMLDivElement>();
  render(
    <StrictMode>
      <div ref={containerRef} data-testid="transcript-container">
        <div data-view-anchor-message="true" data-testid="message-node">
          selectable message prose
        </div>
        <SelectionQuote containerRef={containerRef} actions={[{ label: "Quote in reply", onInvoke }]} />
      </div>
    </StrictMode>,
  );
  const messageNode = screen.getByTestId("message-node").firstChild as Text;
  installFakeSelection({ text: "quoted prose", anchorNode: messageNode });
  act(() => {
    containerRef.current?.dispatchEvent(new Event("pointerup", { bubbles: true }));
  });
  expect(screen.getByRole("toolbar", { name: "Selection actions" })).toBeTruthy();

  fireEvent.keyDown(document, { key: "'", metaKey: true });

  expect(onInvoke).toHaveBeenCalledExactlyOnceWith("quoted prose");
});

test('plain "\'" with no modifier is not treated as the quote chord', () => {
  const onInvoke = vi.fn();
  const { messageNode, containerRef } = renderHarness(onInvoke);
  installFakeSelection({ text: "quoted prose", anchorNode: messageNode });
  act(() => {
    containerRef.current?.dispatchEvent(new Event("pointerup", { bubbles: true }));
  });

  fireEvent.keyDown(document, { key: "'" });

  expect(onInvoke).not.toHaveBeenCalled();
  expect(screen.getByRole("toolbar", { name: "Selection actions" })).toBeTruthy();
});

test("mousedown on the bar is prevented, so clicking its action never collapses the live text selection first", () => {
  const { messageNode, containerRef } = renderHarness(() => {});
  installFakeSelection({ text: "quoted prose", anchorNode: messageNode });
  act(() => {
    containerRef.current?.dispatchEvent(new Event("pointerup", { bubbles: true }));
  });

  const event = fireEvent.mouseDown(screen.getByRole("toolbar", { name: "Selection actions" }));
  expect(event).toBe(false); // fireEvent returns false when preventDefault() was called
});

// Multi-pane workspaces mount one SelectionQuote per session pane
// (Session.tsx), and each instance registers the selection.quote action.
// These two tests pin the multi-instance contract against the two ways a
// shared registry can break it: the LAST-registered instance clobbering the
// one holding the selection, and an instance's unmount deadening the chord
// for every remaining instance.
function renderInstance(label: string, onInvoke: (text: string) => void) {
  const containerRef = createRef<HTMLDivElement>();
  const rendered = render(
    <div ref={containerRef} data-testid={`${label}-container`}>
      <div data-view-anchor-message="true" data-testid={`${label}-message`}>
        selectable message prose
      </div>
      <SelectionQuote containerRef={containerRef} actions={[{ label: "Quote in reply", onInvoke }]} />
    </div>,
  );
  return {
    containerRef,
    unmount: rendered.unmount,
    messageNode: screen.getByTestId(`${label}-message`).firstChild as Text,
  };
}

test("two mounted instances: the one holding the selection handles Mod+', not the last-registered", () => {
  const onInvokeA = vi.fn();
  const onInvokeB = vi.fn();
  const a = renderInstance("a", onInvokeA);
  renderInstance("b", onInvokeB);
  installFakeSelection({ text: "quoted prose", anchorNode: a.messageNode });
  act(() => {
    a.containerRef.current?.dispatchEvent(new Event("pointerup", { bubbles: true }));
  });

  fireEvent.keyDown(document, { key: "'", metaKey: true });

  expect(onInvokeA).toHaveBeenCalledExactlyOnceWith("quoted prose");
  expect(onInvokeB).not.toHaveBeenCalled();
});

test("unmounting the last-registered instance does not deaden Mod+' for the remaining ones", () => {
  const onInvokeA = vi.fn();
  const onInvokeB = vi.fn();
  const a = renderInstance("a", onInvokeA);
  const b = renderInstance("b", onInvokeB);
  b.unmount();
  installFakeSelection({ text: "quoted prose", anchorNode: a.messageNode });
  act(() => {
    a.containerRef.current?.dispatchEvent(new Event("pointerup", { bubbles: true }));
  });

  fireEvent.keyDown(document, { key: "'", metaKey: true });

  expect(onInvokeA).toHaveBeenCalledExactlyOnceWith("quoted prose");
});
