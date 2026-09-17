import { afterAll, beforeAll } from "vitest";

// jsdom has no layout. Browser guards prove geometry; these stubs let real
// ProseMirror transactions and DOM selections run in component tests.
const geometry = [
  { target: Range.prototype, name: "getClientRects", value: () => [] },
  { target: Range.prototype, name: "getBoundingClientRect", value: () => new DOMRect() },
  { target: document, name: "elementFromPoint", value: () => null },
].map((entry) => ({ ...entry, descriptor: Object.getOwnPropertyDescriptor(entry.target, entry.name) }));

beforeAll(() => {
  for (const { target, name, value } of geometry) {
    Object.defineProperty(target, name, { configurable: true, value });
  }
});

afterAll(() => {
  for (const { target, name, descriptor } of geometry) {
    if (descriptor) Object.defineProperty(target, name, descriptor);
    else Reflect.deleteProperty(target, name);
  }
});
