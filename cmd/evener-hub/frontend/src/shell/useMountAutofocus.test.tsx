import { cleanup, render } from "@testing-library/react";
import { useRef } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { FINE_HOVER_POINTER_QUERY, MOBILE_QUERY } from "./useIsMobile";
import { useMountAutofocus } from "./useMountAutofocus";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function Harness({ enabled }: { enabled: boolean }) {
  const targetRef = useRef<HTMLButtonElement>(null);
  useMountAutofocus(targetRef, enabled);
  return <button type="button" ref={targetRef} />;
}

interface Viewport {
  mobile: boolean;
  fineHover: boolean;
}

// A desktop window has a fine hovering pointer at any width; a phone's touch
// pointer is coarse even when the phone is held landscape and its width is
// past the 899px breakpoint. That last pairing is the one a width gate gets
// wrong.
const WIDE_DESKTOP: Viewport = { mobile: false, fineHover: true };
const NARROW_DESKTOP: Viewport = { mobile: true, fineHover: true };
const PORTRAIT_PHONE: Viewport = { mobile: true, fineHover: false };
const LANDSCAPE_PHONE: Viewport = { mobile: false, fineHover: false };

function stubMatchMedia({ mobile, fineHover }: Viewport): void {
  const matchesQuery = (query: string): boolean => {
    if (query === MOBILE_QUERY) return mobile;
    if (query === FINE_HOVER_POINTER_QUERY) return fineHover;
    return false;
  };
  vi.stubGlobal("matchMedia", ((query: string) => ({
    matches: matchesQuery(query),
    media: query,
    addEventListener() {},
    removeEventListener() {},
  })) as unknown as typeof window.matchMedia);
}

test("focuses the target on mount on a wide desktop", () => {
  stubMatchMedia(WIDE_DESKTOP);
  const view = render(<Harness enabled />);
  expect(document.activeElement).toBe(view.container.querySelector("button"));
});

test("focuses the target on mount in a narrow desktop window", () => {
  stubMatchMedia(NARROW_DESKTOP);
  const view = render(<Harness enabled />);
  expect(document.activeElement).toBe(view.container.querySelector("button"));
});

test("never focuses on a portrait phone, even when enabled", () => {
  stubMatchMedia(PORTRAIT_PHONE);
  const view = render(<Harness enabled />);
  expect(document.activeElement).not.toBe(view.container.querySelector("button"));
});

test("never focuses on a wide landscape phone", () => {
  stubMatchMedia(LANDSCAPE_PHONE);
  const view = render(<Harness enabled />);
  expect(document.activeElement).not.toBe(view.container.querySelector("button"));
});

test("never focuses when disabled", () => {
  stubMatchMedia(WIDE_DESKTOP);
  const view = render(<Harness enabled={false} />);
  expect(document.activeElement).not.toBe(view.container.querySelector("button"));
});

test("treats a matchMedia-less environment as desktop", () => {
  // @ts-expect-error jsdom has no matchMedia by default.
  delete window.matchMedia;
  const view = render(<Harness enabled />);
  expect(document.activeElement).toBe(view.container.querySelector("button"));
});
