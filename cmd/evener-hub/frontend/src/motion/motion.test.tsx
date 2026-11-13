import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { render } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";
import { AnimatePresence, MotionProvider, m, spatialTransition } from "./index";
import { motionDurationMs } from "./tokens";

describe("MotionProvider", () => {
  test("renders children and exposes the wrapped primitives", () => {
    const { container } = render(
      <MotionProvider>
        <AnimatePresence>
          <m.div data-testid="subject" initial={{ opacity: 0 }} animate={{ opacity: 1 }} />
        </AnimatePresence>
      </MotionProvider>,
    );
    expect(container.querySelector('[data-testid="subject"]')).not.toBeNull();
  });

  // The `m` component only animates with feature support armed by LazyMotion;
  // without it the library warns AND the component never interpolates. The
  // wrapper must arm it, or every spatial transition in the app silently never
  // runs. (Confirmed empirically: without LazyMotion the transform sat at its
  // initial value for 500ms with rAF available.)
  test("arms m's animations: the value actually interpolates", async () => {
    const { container } = render(
      <MotionProvider>
        <m.div data-testid="subject" initial={{ x: 200 }} animate={{ x: 0 }} transition={{ duration: 0.1 }} />
      </MotionProvider>,
    );
    const el = container.querySelector('[data-testid="subject"]');
    expect(el?.getAttribute("style") ?? "").toContain("200");
    // Within the animation's own duration the value must leave its start.
    await vi.waitFor(() => {
      expect(el?.getAttribute("style") ?? "").not.toContain("200px");
    });
  });
});

describe("spatialTransition", () => {
  test("reads the spatial duration token", () => {
    document.documentElement.style.setProperty("--motion-duration-spatial", "300ms");
    expect(spatialTransition().duration).toBe(0.3);
    document.documentElement.style.removeProperty("--motion-duration-spatial");
  });

  test("falls back to the budget when the token is absent", () => {
    document.documentElement.style.removeProperty("--motion-duration-spatial");
    expect(spatialTransition().duration).toBeCloseTo(0.24);
  });

  test("uses the standard easing", () => {
    expect(spatialTransition().ease).toBe("easeOut");
  });

  test("maps the easing token's CSS spelling to the library's", () => {
    document.documentElement.style.setProperty("--motion-easing-standard", "ease-in");
    expect(spatialTransition().ease).toBe("easeIn");
    document.documentElement.style.setProperty("--motion-easing-standard", "cubic-bezier(0.2, 0, 0, 1)");
    expect(spatialTransition().ease).toEqual([0.2, 0, 0, 1]);
    document.documentElement.style.removeProperty("--motion-easing-standard");
    expect(spatialTransition().ease).toBe("easeOut");
  });

  test("memoizes on the raw token values: stable inputs share one object, a token change re-reads", () => {
    const first = spatialTransition();
    expect(spatialTransition()).toBe(first);
    document.documentElement.style.setProperty("--motion-duration-spatial", "300ms");
    const second = spatialTransition();
    expect(second).not.toBe(first);
    expect(second.duration).toBe(0.3);
    document.documentElement.style.removeProperty("--motion-duration-spatial");
    // Back to the absent-token values: the same memoized object returns.
    expect(spatialTransition()).toBe(first);
  });
});

describe("motionDurationMs", () => {
  test("reads a duration token in milliseconds, in either CSS unit", () => {
    document.documentElement.style.setProperty("--motion-duration-exit", "100ms");
    expect(motionDurationMs("--motion-duration-exit")).toBe(100);
    document.documentElement.style.setProperty("--motion-duration-exit", "0.2s");
    expect(motionDurationMs("--motion-duration-exit")).toBe(200);
    document.documentElement.style.removeProperty("--motion-duration-exit");
  });

  test("reads the token as the given element computes it", () => {
    const element = document.createElement("div");
    element.style.setProperty("--motion-duration-spatial-exit", "300ms");
    document.body.append(element);
    try {
      expect(motionDurationMs("--motion-duration-spatial-exit", element)).toBe(300);
    } finally {
      element.remove();
    }
  });

  // The unit-test document loads no stylesheet, so every read here takes the
  // fallback, which must be what tokens.css ships.
  test("falls back to the value tokens.css ships where the stylesheet is absent", () => {
    const tokens = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "../styles/tokens.css"), "utf8");
    const tokenNames = [
      "--motion-duration-press",
      "--motion-duration-exit",
      "--motion-duration-overlay",
      "--motion-duration-hover",
      "--motion-duration-spatial-exit",
      "--motion-duration-attention",
      "--motion-duration-spatial",
    ] as const;
    for (const token of tokenNames) {
      const shipped = new RegExp(`${token}:\\s*(\\d+)ms;`).exec(tokens);
      expect(shipped, token).not.toBeNull();
      expect(motionDurationMs(token), token).toBe(Number(shipped?.[1]));
    }
  });
});
