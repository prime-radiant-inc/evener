/**
 * Resolves with `read` unless it takes longer than `ms`, in which case it
 * resolves `undefined` after running `onTimeout` (when given). A rejected read
 * still rejects. The deadline's timer is always cleared on the path that
 * settles first, so a completed read leaves no callback behind.
 *
 * The callers use this for bounded repository reads whose semantics are "a
 * slow answer is a miss". It deliberately does not own a deadline for the
 * foreground opening, where a slow-but-answering read must win.
 */
export async function withDeadline<T>(read: Promise<T>, ms: number, onTimeout?: () => void): Promise<T | undefined> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([
      read,
      new Promise<undefined>((resolve) => {
        timer = setTimeout(() => {
          onTimeout?.();
          resolve(undefined);
        }, ms);
      }),
    ]);
  } finally {
    if (timer !== undefined) clearTimeout(timer);
  }
}
