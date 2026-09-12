import { vi } from "vitest";

// Hold browser callbacks while fake-indexeddb performs the real transaction.
// This distinguishes an unobserved commit from a write that can still abort.
export function holdIndexedDBEvent(target: EventTarget, type: string) {
  const held: (() => void)[] = [];
  let released = false;
  let observed: (() => void) | undefined;
  const reached = new Promise<void>((resolve) => {
    observed = resolve;
  });
  const add = target.addEventListener.bind(target);
  const spy = vi.spyOn(target, "addEventListener").mockImplementation((eventType, listener, options) => {
    if (eventType !== type || !listener) return add(eventType, listener, options);
    add(
      eventType,
      (event) => {
        const deliver = () => {
          if (typeof listener === "function") listener.call(target, event);
          else listener.handleEvent(event);
        };
        // Release means "stop holding", not "deliver what has already
        // arrived": restoring the spy stops NEW listeners from being wrapped,
        // but every listener registered while the hold was up keeps this
        // wrapper for good. An event still in flight at release time would
        // otherwise land in a queue nobody drains again, which for an
        // IndexedDB request is a promise that never settles (issue #1187).
        if (released) {
          deliver();
          return;
        }
        held.push(deliver);
        observed?.();
      },
      options,
    );
  });
  return {
    reached,
    release() {
      released = true;
      spy.mockRestore();
      for (const deliver of held.splice(0)) deliver();
    },
  };
}
