import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

function extractShellContract(resolvedCss) {
  const baseShellRule = resolvedCss.match(/(^|\n)\.shell\s*\{([\s\S]*?)\n\}/)?.[0]?.trim() ?? "";
  const supportsBlock = resolvedCss.match(/@supports \(height: 100dvh\) \{([\s\S]*?)\n\}/)?.[0] ?? "";
  const supportsShellRule = supportsBlock.match(/\.shell\s*\{([\s\S]*?)\n\}/)?.[0]?.trim() ?? "";
  return { baseShellRule, supportsBlock, supportsShellRule };
}

export default function assert(measurements) {
  const failures = [];
  const tolerance = 1;
  const expectedViewport = { width: 390, height: 844 };

  const resolvedCssHref = measurements[0]?.assets?.resolvedCssHref ?? null;
  const resolvedCss = resolvedCssHref ? readFileSync(fileURLToPath(resolvedCssHref), "utf8") : "";
  const { baseShellRule, supportsBlock, supportsShellRule } = extractShellContract(resolvedCss);
  const fallbackIndex = baseShellRule.indexOf("height: 100vh");
  const misplacedDynamicIndex = baseShellRule.indexOf("height: 100dvh");
  const dynamicIndex = supportsShellRule.indexOf("height: 100dvh");

  if (fallbackIndex < 0) {
    failures.push(`base .shell rule is missing height: 100vh fallback (loaded rule: ${baseShellRule || "none"})`);
  }
  if (misplacedDynamicIndex >= 0) {
    failures.push(`base .shell rule must not contain height: 100dvh outside @supports (loaded rule: ${baseShellRule})`);
  }
  if (!supportsBlock) {
    failures.push("missing @supports (height: 100dvh) block for the dynamic viewport override");
  } else if (!supportsShellRule) {
    failures.push(`@supports (height: 100dvh) block is missing its .shell override (loaded block: ${supportsBlock})`);
  } else if (dynamicIndex < 0) {
    failures.push(`@supports (height: 100dvh) .shell override is missing height: 100dvh (loaded rule: ${supportsShellRule})`);
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
      ["top bar", measurement.topBar],
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
        reason: "session and non-session mobile shell fixtures stay within the visible viewport and .shell keeps its 100vh base fallback plus @supports(100dvh) override contract",
      }
    : {
        pass: false,
        reason: failures.join("; "),
      };
}
