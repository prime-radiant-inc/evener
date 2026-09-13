import { cleanup, render } from "@testing-library/react";
import { useRef } from "react";
import { afterEach, expect, test, vi } from "vitest";
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

function stubMatchMedia(matches: boolean): void {
  vi.stubGlobal("matchMedia", ((query: string) => ({
    matches,
    media: query,
    addEventListener() {},
    removeEventListener() {},
  })) as unknown as typeof window.matchMedia);
}

test("focuses the target on mount when enabled on desktop", () => {
  stubMatchMedia(false);
  const view = render(<Harness enabled />);
  expect(document.activeElement).toBe(view.container.querySelector("button"));
});

test("never focuses when disabled", () => {
  stubMatchMedia(false);
  const view = render(<Harness enabled={false} />);
  expect(document.activeElement).not.toBe(view.container.querySelector("button"));
});

test("never focuses on mobile, even when enabled", () => {
  stubMatchMedia(true);
  const view = render(<Harness enabled />);
  expect(document.activeElement).not.toBe(view.container.querySelector("button"));
});

test("treats a matchMedia-less environment as desktop", () => {
  // @ts-expect-error jsdom has no matchMedia by default.
  delete window.matchMedia;
  const view = render(<Harness enabled />);
  expect(document.activeElement).toBe(view.container.querySelector("button"));
});
