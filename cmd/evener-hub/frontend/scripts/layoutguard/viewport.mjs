function isPositiveInteger(value) {
  return Number.isInteger(value) && value > 0;
}

function isPositiveFiniteNumber(value) {
  return typeof value === "number" && Number.isFinite(value) && value > 0;
}

export function normalizeViewportSpec(viewport, caseName) {
  if (viewport == null) return null;
  if (typeof viewport !== "object" || Array.isArray(viewport)) {
    throw new Error(`${caseName}: viewport must be an object`);
  }

  const { width, height, deviceScaleFactor, mobile } = viewport;

  if (!isPositiveInteger(width)) {
    throw new Error(`${caseName}: viewport.width must be a finite positive integer`);
  }
  if (!isPositiveInteger(height)) {
    throw new Error(`${caseName}: viewport.height must be a finite positive integer`);
  }
  if (deviceScaleFactor !== undefined && !isPositiveFiniteNumber(deviceScaleFactor)) {
    throw new Error(`${caseName}: viewport.deviceScaleFactor must be a finite positive number`);
  }
  if (mobile !== undefined && typeof mobile !== "boolean") {
    throw new Error(`${caseName}: viewport.mobile must be a boolean`);
  }

  return {
    width,
    height,
    ...(deviceScaleFactor !== undefined ? { deviceScaleFactor } : {}),
    ...(mobile !== undefined ? { mobile } : {}),
  };
}

export function diagnoseRealizedViewport(expected, realized) {
  if (expected == null) return null;
  if (realized == null || typeof realized !== "object") {
    return `viewport realization mismatch: requested ${expected.width}x${expected.height} CSS px but no realized viewport data was returned`;
  }

  const mismatches = [];
  if (realized.windowInnerWidth !== expected.width || realized.windowInnerHeight !== expected.height) {
    mismatches.push(`window.innerWidth/innerHeight=${realized.windowInnerWidth}x${realized.windowInnerHeight}`);
  }
  if (realized.documentClientWidth !== expected.width || realized.documentClientHeight !== expected.height) {
    mismatches.push(
      `document.documentElement.clientWidth/clientHeight=${realized.documentClientWidth}x${realized.documentClientHeight}`,
    );
  }
  if (
    realized.visualViewportWidth !== null &&
    realized.visualViewportHeight !== null &&
    (realized.visualViewportWidth !== expected.width || realized.visualViewportHeight !== expected.height)
  ) {
    mismatches.push(`visualViewport.width/height=${realized.visualViewportWidth}x${realized.visualViewportHeight}`);
  }

  if (mismatches.length === 0) return null;
  return `viewport realization mismatch: requested ${expected.width}x${expected.height} CSS px, got ${mismatches.join(", ")}`;
}
