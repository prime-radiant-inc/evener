// Typed face of scripts/jsdomSvgShims.mjs, the plain-JS installer shared with
// the fixture generator (which runs under plain node). tsc resolves the import
// of the .mjs through this .d.mts; the wrapper installs on the ambient jsdom
// window.
export function installJsdomSvgShims(target?: { SVGElement: { prototype: Record<string, unknown> } }): void;
