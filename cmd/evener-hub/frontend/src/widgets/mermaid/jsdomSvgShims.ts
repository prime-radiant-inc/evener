// Thin typed wrapper over the shared installer. The four SVG layout method
// shims jsdom lacks live once, in scripts/jsdomSvgShims.mjs, shared byte-for-
// byte with the fixture generator (scripts/render-one-mermaid-fixture.mjs) that
// produced testdata/*.svg. That module is plain JS because the generator runs
// under plain node; it installs on the ambient jsdom window here, while the
// generator passes its own JSDOM window.
export { installJsdomSvgShims } from "../../../scripts/jsdomSvgShims.mjs";
