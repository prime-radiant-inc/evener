// Test-only installer for the SVG layout APIs jsdom does not implement.
//
// Real mermaid measures label boxes and connector endpoints through
// SVGElement methods that jsdom leaves undefined, so a jsdom render of a
// diagram throws before it produces any SVG. These four method shims are the
// SVG half of the preamble in scripts/render-one-mermaid-fixture.mjs (the
// generator that produced testdata/*.svg): getBBox, getComputedTextLength,
// getScreenCTM, and getPointAtLength. That script also assigns the
// window/document/Element globals, but vitest's jsdom environment already
// provides those, so only the SVG prototype methods need installing here.
//
// Geometry is a constant, not a measurement - jsdom has no layout, and this
// suite only asserts structure and labels, never coordinates. Real browser
// geometry is Task 6's concern.
type ScreenCtm = {
  a: number;
  b: number;
  c: number;
  d: number;
  e: number;
  f: number;
  inverse(): ScreenCtm;
  multiply(): ScreenCtm;
};

type SvgLayoutShims = {
  getBBox(): { x: number; y: number; width: number; height: number };
  getComputedTextLength(): number;
  getScreenCTM(): ScreenCtm;
  getPointAtLength(): { x: number; y: number };
};

export function installJsdomSvgShims(): void {
  const proto = window.SVGElement.prototype as unknown as SvgLayoutShims;
  proto.getBBox = () => ({ x: 0, y: 0, width: 50, height: 20 });
  proto.getComputedTextLength = () => 50;
  proto.getScreenCTM = () => {
    const matrix = { a: 1, b: 0, c: 0, d: 1, e: 0, f: 0 } as ScreenCtm;
    matrix.inverse = () => matrix;
    matrix.multiply = () => matrix;
    return matrix;
  };
  proto.getPointAtLength = () => ({ x: 0, y: 0 });
}
