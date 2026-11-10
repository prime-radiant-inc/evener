/**
 * Resolves with `read` unless it takes longer than `ms`, in which case it
 * resolves `undefined` after running `onTimeout` (when given). A rejected read
 * still rejects. The deadline's timer is always cleared on the path that
 * settles first, so a completed read leaves no callback behind.
 */
export function withDeadline<T>(read: Promise<T>, ms: number, onTimeout?: () => void): Promise<T | undefined> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const deadline = new Promise<undefined>((resolve) => {
    timer = setTimeout(() => {
      resolve(undefined);
      // Resolve first: a throwing timeout action must not leave the caller
      // waiting on a promise this helper already owns the deadline for. The
      // action still runs before the awaiting continuation.
      onTimeout?.();
    }, ms);
  });
  return Promise.race([read, deadline]).finally(() => clearTimeout(timer));
}
