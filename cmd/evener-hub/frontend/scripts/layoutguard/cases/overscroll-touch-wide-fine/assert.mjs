// The same width with no touch screen: nothing about the desktop document
// gestures changes, so the widened lock cannot quietly escape its gate.
import { expectDocumentLock } from "../overscroll-touch-wide/shared-assert.mjs";

export default function assert(m) {
  return expectDocumentLock({ coarse: false })(m);
}
