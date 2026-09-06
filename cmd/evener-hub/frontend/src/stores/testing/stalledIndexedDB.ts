import { vi } from "vitest";

// Hold browser callbacks while fake-indexeddb performs the real transaction.
// This distinguishes an unobserved commit from a write that can still abort.
export function holdIndexedDBEvent(target: EventTarget, type: string) {
  const held: (() => void)[] = [];
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
        held.push(() => {
          if (typeof listener === "function") listener.call(target, event);
          else listener.handleEvent(event);
        });
        observed?.();
      },
      options,
    );
  });
  return {
    reached,
    release() {
      spy.mockRestore();
      for (const deliver of held.splice(0)) deliver();
    },
  };
}
