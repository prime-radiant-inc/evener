import { act } from "@testing-library/react";

/**
 * HeldChunk is a lazy chunk's import() that the test settles itself, inside
 * an awaited act. React 19 holds back a Suspense reveal that commits outside
 * act for its 300ms fallback throttle, on a real timer. A loader mock that
 * returns an already-settled promise settles during whichever render or
 * user-event call asked for the chunk, and user-event runs outside act, so
 * each such reveal would cost that wait. Settling inside act commits the
 * reveal at once.
 */
export interface HeldChunk<T> {
  promise: Promise<T>;
  resolve(module: T): Promise<void>;
  reject(error: Error): Promise<void>;
}

export function holdChunk<T>(): HeldChunk<T> {
  let resolvePromise!: (module: T) => void;
  let rejectPromise!: (error: Error) => void;
  const promise = new Promise<T>((resolve, reject) => {
    resolvePromise = resolve;
    rejectPromise = reject;
  });
  const settle = async (settleNow: () => void) => {
    await act(async () => {
      settleNow();
      // The lazy() boundary reports the rejection; this only waits for it.
      await promise.catch(() => {});
    });
  };
  return {
    promise,
    resolve: (module) => settle(() => resolvePromise(module)),
    reject: (error) => settle(() => rejectPromise(error)),
  };
}
