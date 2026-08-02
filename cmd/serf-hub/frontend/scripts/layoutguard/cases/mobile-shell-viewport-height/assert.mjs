import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

export default function assert(measurements) {
  const failures = [];
  const tolerance = 1;
  const expectedViewport = { width: 390, height: 844 };

  const resolvedCssHref = measurements[0]?.assets?.resolvedCssHref ?? null;
  const resolvedCss = resolvedCssHref ? readFileSync(fileURLToPath(resolvedCssHref), "utf8") : "";
  const shellRule = resolvedCss.match(/\.shell\s*\{[\s\S]*?\n\}/)?.[0] ?? "";
  const fallbackIndex = shellRule.indexOf("height: 100vh");
  const dynamicIndex = shellRule.indexOf("height: 100dvh");

  if (fallbackIndex < 0) {
    failures.push(`shell rule is missing height: 100vh fallback (loaded rule: ${shellRule || "none"})`);
  }
  if (dynamicIndex < 0) {
    failures.push(`shell rule is missing height: 100dvh visible-viewport override (loaded rule: ${shellRule || "none"})`);
  } else if (dynamicIndex <= fallbackIndex) {
    failures.push(`shell rule orders height: 100dvh before height: 100vh (loaded rule: ${shellRule})`);
  }

  for (const measurement of measurements) {
    const fixtureName = measurement.fixture;
    const visibleViewportHeight = measurement.visualViewport?.height ?? measurement.viewport.height;

    if (measurement.viewport.width !== expectedViewport.width || measurement.viewport.height !== expectedViewport.height) {
      failures.push(
        `${fixtureName}: viewport is ${measurement.viewport.width}x${measurement.viewport.height}, expected ${expectedViewport.width}x${expectedViewport.height}`,
      );
    }

    if (!measurement.topBar) failures.push(`${fixtureName}: missing StackHost .topBar`);

    if (measurement.document.scrollHeight > visibleViewportHeight + tolerance) {
      failures.push(
        `${fixtureName}: document is ${measurement.document.scrollHeight}px tall inside a ${visibleViewportHeight}px viewport`,
      );
    }

    for (const [name, box] of [
      ["shell", measurement.shell],
      ["pane", measurement.pane],
      ["pane body", measurement.paneBody?.rect ?? null],
      ["footer", measurement.footer],
    ]) {
      if (!box) continue;
      if (box.bottom > visibleViewportHeight + tolerance) {
        failures.push(`${fixtureName}: ${name} escapes the viewport by ${(box.bottom - visibleViewportHeight).toFixed(1)}px`);
      }
    }

    if (!measurement.shell) failures.push(`${fixtureName}: missing [data-shell]`);
    if (!measurement.pane) failures.push(`${fixtureName}: missing [data-pane]`);
    if (!measurement.paneBody) failures.push(`${fixtureName}: missing [data-pane-body]`);
  }

  return failures.length === 0
    ? {
        pass: true,
        reason: "session and non-session mobile shell fixtures stay within the visible viewport and .shell keeps its 100vh/100dvh contract",
      }
    : {
        pass: false,
        reason: failures.join("; "),
      };
}
