// A case.json "widthMatrix" lets one harness cover N body widths with
// width-keyed measurements and assertions instead of hand-copying markup
// once per width (kata zt7p). The matrix is data - a bare array of specs,
// each at minimum a numeric "width" plus whatever extra fields the harness
// wants attached to that fixture (e.g. an expected boundary width, or a
// flag toggling a short label). run.mjs splices the normalized matrix into
// harness.html in place of a placeholder token the harness declares itself,
// so the widths live in exactly one place: case.json.

function describeValue(value) {
  if (typeof value === "string") return JSON.stringify(value);
  if (typeof value === "number" && Number.isNaN(value)) return "NaN";
  return String(value);
}

// null/undefined means "this case has no width matrix" - normalizeViewportSpec
// uses the same convention for its optional case.json field.
export function normalizeWidthMatrix(widthMatrix, caseName) {
  if (widthMatrix == null) return null;
  if (!Array.isArray(widthMatrix)) {
    throw new Error(`${caseName}: widthMatrix must be an array`);
  }
  if (widthMatrix.length === 0) {
    throw new Error(`${caseName}: widthMatrix must declare at least one width`);
  }

  return widthMatrix.map((entry, index) => {
    if (typeof entry !== "object" || entry === null || Array.isArray(entry)) {
      throw new Error(`${caseName}: widthMatrix[${index}] must be an object, got ${describeValue(entry)}`);
    }
    const { width } = entry;
    if (typeof width !== "number" || !Number.isFinite(width) || width <= 0) {
      throw new Error(
        `${caseName}: widthMatrix[${index}].width must be a finite positive number, got ${describeValue(width)}`,
      );
    }
    return { ...entry };
  });
}

const PLACEHOLDER = "__LAYOUTGUARD_WIDTH_MATRIX__";

// Splices the normalized matrix into harness source as a JSON array literal,
// replacing PLACEHOLDER wherever the harness's own fixture-building script
// declares it (see cases/compact-session-footer/harness.html). A case that
// declares no widthMatrix leaves the harness untouched.
export function injectWidthMatrix(harnessSource, widthMatrix, caseName) {
  const normalized = normalizeWidthMatrix(widthMatrix, caseName);
  if (normalized === null) return harnessSource;
  if (!harnessSource.includes(PLACEHOLDER)) {
    throw new Error(`${caseName}: case.json declares widthMatrix but harness.html has no ${PLACEHOLDER} placeholder`);
  }
  return harnessSource.split(PLACEHOLDER).join(JSON.stringify(normalized));
}
