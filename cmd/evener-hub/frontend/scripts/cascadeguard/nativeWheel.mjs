const WHEEL_STEP = 900;
const REPOSITION_BUDGET_MS = 15000;
const REPOSITION_ATTEMPT_TIMEOUT_MS = 5000;
const REPOSITION_ATTEMPTS = 3;

// `readInsideTallEntry` repositions the source reader with a native wheel
// before measuring interruption and reflow behavior. An upward wheel needs a
// full step of room to apply; a reader closer to the top than that clamps at
// the boundary and leaves the reading line at the entry's edge. So a
// repositioning wheel always points at the end that has room.
export function readerWheelDelta({ scrollTop }) {
  return scrollTop < WHEEL_STEP ? WHEEL_STEP : -WHEEL_STEP;
}

// A re-render can reset the preserved scroll offset while a gesture lands,
// swallowing the wheel or the sample that follows it. Retry the whole gesture
// inside one budget, so a swallowed gesture costs an attempt rather than the
// precondition, and a page that never answers fails within the budget instead
// of once per attempt. Each stage of the gesture gets the smaller of the
// per-attempt cap and the budget that remains.
export async function repositionReader({
  wheel,
  tallEntry,
  budgetMs = REPOSITION_BUDGET_MS,
  attemptTimeoutMs = REPOSITION_ATTEMPT_TIMEOUT_MS,
  attempts = REPOSITION_ATTEMPTS,
  now = Date.now,
}) {
  const deadline = now() + budgetMs;
  let lastError;
  for (let attempt = 1; attempt <= attempts; attempt++) {
    if (attempt > 1 && now() >= deadline) break;
    const timeoutMs = () => Math.max(1, Math.min(attemptTimeoutMs, deadline - now()));
    try {
      await wheel(timeoutMs());
      return await tallEntry(timeoutMs(), attempt);
    } catch (error) {
      // A missed gesture can clear once the re-render settles. A violated
      // input guarantee cannot, so it fails the guard without another attempt.
      if (error?.code === "ERR_ASSERTION") throw error;
      lastError = error;
    }
  }
  throw lastError;
}
