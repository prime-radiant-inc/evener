// Shared jsdom browser-geometry seam for the document selection/highlight tests
// (Task 5). jsdom lays nothing out and exposes no Range geometry, so the ONLY
// browser capability these tests supply is the rectangle an element or Range
// reports. The real bound document map, its `rangesFor` fragments, the
// production clipping/measurement and every DOM/Selection interaction all run
// for real; no mapper or measurement result is faked.
//
// Each test installs the seam in `beforeEach` and restores the exact prior
// property descriptors afterwards (deleting own properties it added), so no shim
// leaks from one test to the next.
import type { SourceRange } from "@evener/appwire-client";
import type { BoundDocumentMap } from "../../../widgets/markdown/documentSourceMap";

export interface TestRect {
  left: number;
  top: number;
  right: number;
  bottom: number;
}

const EMPTY_RECT: TestRect = { left: 0, top: 0, right: 0, bottom: 0 };

export function testDomRect(rect: TestRect): DOMRect {
  return {
    left: rect.left,
    top: rect.top,
    right: rect.right,
    bottom: rect.bottom,
    width: rect.right - rect.left,
    height: rect.bottom - rect.top,
    x: rect.left,
    y: rect.top,
    toJSON: () => ({}),
  } as DOMRect;
}

interface PropertyInstall {
  target: object;
  key: string;
  before: PropertyDescriptor | undefined;
}

export class DocumentGeometrySeam {
  private rectsByNode = new WeakMap<Text, Map<string, TestRect[]>>();
  private installs: PropertyInstall[] = [];

  // Install the Range rectangle capability for one test.
  install(): void {
    this.rectsByNode = new WeakMap();
    const seam = this;
    this.patch(Range.prototype, "getClientRects", function (this: Range): DOMRectList {
      return seam.rectsForRange(this).map(testDomRect) as unknown as DOMRectList;
    });
    this.patch(Range.prototype, "getBoundingClientRect", function (this: Range): DOMRect {
      const [first] = seam.rectsForRange(this);
      return testDomRect(first ?? EMPTY_RECT);
    });
  }

  // Put back every exact previous descriptor, deleting own properties the seam
  // added. Safe to call once per test even after a failed assertion.
  restore(): void {
    for (const install of this.installs.reverse()) {
      if (install.before) Object.defineProperty(install.target, install.key, install.before);
      else Reflect.deleteProperty(install.target, install.key);
    }
    this.installs = [];
    this.rectsByNode = new WeakMap();
  }

  // Supply the geometry the real verified fragments of `sourceRange` report.
  // The fragment count must match the real map's, so an unexpected split is a
  // loud setup error rather than a silently empty expectation.
  configure(map: BoundDocumentMap, sourceRange: SourceRange, fragments: TestRect[][]): void {
    const ranges = map.rangesFor(sourceRange);
    if (ranges.length !== fragments.length)
      throw new Error(
        `expected ${fragments.length} fragments for ${sourceRange.start}-${sourceRange.end}, saw ${ranges.length}`,
      );
    ranges.forEach((range, index) => {
      const node = range.startContainer;
      if (!(node instanceof Text)) throw new Error("fragment start is not a text node");
      let nodeRects = this.rectsByNode.get(node);
      if (!nodeRects) {
        nodeRects = new Map();
        this.rectsByNode.set(node, nodeRects);
      }
      nodeRects.set(`${range.startOffset}:${range.endOffset}`, fragments[index] ?? []);
    });
  }

  // Supply the rectangle a scroll owner reports. `Element.getBoundingClientRect`
  // exists as a jsdom prototype method; this adds an own override (removed on
  // restore) that a later call replaces for a changed clip.
  setElementRect(element: Element, rect: TestRect): void {
    this.patch(element, "getBoundingClientRect", () => testDomRect(rect));
  }

  private rectsForRange(range: Range): TestRect[] {
    const node = range.startContainer;
    if (!(node instanceof Text)) return [];
    return this.rectsByNode.get(node)?.get(`${range.startOffset}:${range.endOffset}`) ?? [];
  }

  private patch(target: object, key: string, value: unknown): void {
    const existing = this.installs.find((install) => install.target === target && install.key === key);
    if (existing) {
      Object.defineProperty(target, key, { configurable: true, writable: true, value });
      return;
    }
    this.installs.push({ target, key, before: Object.getOwnPropertyDescriptor(target, key) });
    Object.defineProperty(target, key, { configurable: true, writable: true, value });
  }
}
