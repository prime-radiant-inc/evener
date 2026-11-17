// The wide touch screen: a coarse pointer above the phone breakpoint gets the
// document gesture lock while keeping the desktop layout's own scrolling.
import { expectDocumentLock } from "./shared-assert.mjs";

export default function assert(m) {
  return expectDocumentLock({ coarse: true })(m);
}
