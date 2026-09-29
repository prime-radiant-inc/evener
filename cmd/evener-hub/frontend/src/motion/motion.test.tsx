import { render } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import { AnimatePresence, MotionProvider, m, spatialTransition } from "./index";

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
});
