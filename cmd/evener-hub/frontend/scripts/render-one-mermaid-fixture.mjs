// Renders one .mmd source to SVG on stdout. Own process per fixture (see
// gen-mermaid-fixtures.mjs). Six shims cover jsdom's missing SVG layout
// APIs; verified sufficient for all eight common diagram types on mermaid
// 11.17.2 and 12.0.0 (direct ESM import keeps Node's structuredClone).
import { readFileSync } from "node:fs";
import { JSDOM } from "jsdom";

const { window } = new JSDOM("<!doctype html><html><body></body></html>", { pretendToBeVisual: true });
globalThis.window = window;
globalThis.document = window.document;
Object.defineProperty(globalThis, "navigator", { value: window.navigator, configurable: true });
globalThis.Element = window.Element;
globalThis.SVGElement = window.SVGElement;
globalThis.Node = window.Node;
globalThis.HTMLElement = window.HTMLElement;
globalThis.getComputedStyle = window.getComputedStyle.bind(window);
globalThis.CSSStyleSheet = window.CSSStyleSheet ?? class CSSStyleSheet { replaceSync() {} replace() {} insertRule() {} };
window.CSSStyleSheet = globalThis.CSSStyleSheet;
if (!("adoptedStyleSheets" in window.document)) window.document.adoptedStyleSheets = [];
window.SVGElement.prototype.getBBox = () => ({ x: 0, y: 0, width: 50, height: 20 });
window.SVGElement.prototype.getComputedTextLength = () => 50;
window.SVGElement.prototype.getScreenCTM = function () {
  return { a: 1, b: 0, c: 0, d: 1, e: 0, f: 0, inverse() { return this; }, multiply() { return this; } };
};
window.SVGElement.prototype.getPointAtLength = () => ({ x: 0, y: 0 });

const file = process.argv[2];
const source = readFileSync(new URL(`../src/widgets/mermaid/testdata/sources/${file}`, import.meta.url), "utf8");
const { default: mermaid } = await import("mermaid");
mermaid.initialize({ startOnLoad: false, securityLevel: "strict" });
const { svg } = await mermaid.render("fixture", source);
process.stdout.write(svg);
