import { cleanup, render } from "@testing-library/react";
import { useRef } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { stubViewportCapabilities } from "./appShellTestUtils";
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

test.each([
  { label: "wide desktop", viewport: WIDE_DESKTOP, focuses: true },
  { label: "narrow desktop window", viewport: NARROW_DESKTOP, focuses: true },
  { label: "portrait phone", viewport: PORTRAIT_PHONE, focuses: false },
  { label: "wide landscape phone", viewport: LANDSCAPE_PHONE, focuses: false },
])("mount autofocus on $label (focuses=$focuses)", ({ viewport, focuses }) => {
  stubViewportCapabilities(viewport);
  const view = render(<Harness enabled />);
  if (focuses) {
    expect(document.activeElement).toBe(view.container.querySelector("button"));
  } else {
    expect(document.activeElement).not.toBe(view.container.querySelector("button"));
  }
});

test("never focuses when disabled", () => {
  stubViewportCapabilities(WIDE_DESKTOP);
  const view = render(<Harness enabled={false} />);
  expect(document.activeElement).not.toBe(view.container.querySelector("button"));
});

test("treats a matchMedia-less environment as desktop", () => {
  // @ts-expect-error jsdom has no matchMedia by default.
  delete window.matchMedia;
  const view = render(<Harness enabled />);
  expect(document.activeElement).toBe(view.container.querySelector("button"));
});
