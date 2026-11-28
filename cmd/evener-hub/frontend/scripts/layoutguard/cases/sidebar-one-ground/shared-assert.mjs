// Shared measurements for the sidebar-one-ground case family: the page
// grounds and hairline edges of both themes, and the parsers for the
// one-sided cast (24px blur, -12px spread; the offset's sign is the side).
// One owner, so the siblings can never disagree on what "page ground" means.
export const PAGE_DARK = "rgb(17, 18, 20)";
export const PAGE_LIGHT = "rgb(246, 247, 249)";
export const EDGE_DARK = "rgb(51, 56, 66)";
export const EDGE_LIGHT = "rgb(213, 217, 224)";

export function shadowOffsetX(shadow) {
  const m = /rgba?\([^)]*\) (-?\d+(?:\.\d+)?)px 0px 24px -12px/.exec(shadow);
  return m ? Number(m[1]) : null;
}

export function shadowAlpha(shadow) {
  const m = /rgba\([^,]+,[^,]+,[^,]+,\s*([\d.]+)\)/.exec(shadow);
  return m ? Number(m[1]) : 1;
}
