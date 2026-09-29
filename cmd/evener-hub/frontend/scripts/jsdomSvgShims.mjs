// The four SVG layout APIs jsdom does not implement, installed on a jsdom
// window's SVGElement.prototype. Shared byte-for-byte between the fixture
// generator (scripts/render-one-mermaid-fixture.mjs) and the vitest wrapper
// (src/widgets/mermaid/jsdomSvgShims.ts): mermaid measures label boxes and
// connector endpoints through these methods, and a jsdom render throws before
// producing any SVG without them.
//
// Plain JS on purpose - the generator runs under plain node, so this module
// cannot be TypeScript. Geometry is a constant, not a measurement: jsdom has no
// layout, and the suites only assert structure and labels, never coordinates.
export function installJsdomSvgShims(target = globalThis.window) {
  const proto = target.SVGElement.prototype;
  proto.getBBox = () => ({ x: 0, y: 0, width: 50, height: 20 });
  proto.getComputedTextLength = () => 50;
  proto.getScreenCTM = () => {
    const matrix = { a: 1, b: 0, c: 0, d: 1, e: 0, f: 0 };
    matrix.inverse = () => matrix;
    matrix.multiply = () => matrix;
    return matrix;
  };
  proto.getPointAtLength = () => ({ x: 0, y: 0 });
}
