// A failure witness is evidence, never a replacement for the guard's error.
// Keep suffix composition and cause preservation shared by every guard.
import { evaluate } from "./browserGuardCdp.mjs";

const messageOf = (error) => (error instanceof Error ? error.message : String(error));

// A caller may need evidence sooner than the normal CDP tripwire. Preserve the
// short read's existing timer semantics without changing evaluate or its wire.
function bounded(promise, ms) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`witness read did not return within ${ms}ms`)), ms);
    timer.unref?.();
    promise.then(
      (value) => {
        clearTimeout(timer);
        resolve(value);
      },
      (error) => {
        clearTimeout(timer);
        reject(error);
      },
    );
  });
}

// `context` is serializable host-owned evidence. `partial` preserves guards
// that mark incomplete evidence explicitly. Only callers with an existing short
// witness bound supply `readMs`; others retain evaluate's normal tripwire.
export async function witnessFailure(driver, expr, error, { context = {}, readMs, partial = false } = {}) {
  const incomplete = partial ? { partial: true } : {};
  let witness;
  try {
    const reading = evaluate(driver.send, expr);
    witness = await (readMs === undefined ? reading : bounded(reading, readMs));
  } catch (witnessError) {
    witness = { ...incomplete, unreadable: ["witness"], error: messageOf(witnessError) };
  }
  let body;
  try {
    body = JSON.stringify({ ...context, ...witness });
  } catch {
    // Drop only the page evidence that could not be serialized, not host fields.
    body = JSON.stringify({ ...context, ...incomplete, unreadable: ["payload"], error: "witness not serializable" });
  }
  return new Error(`${messageOf(error)}; witness ${body}`, { cause: error });
}
