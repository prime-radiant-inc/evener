// A guard's wait runs IN the page, not across the wire.
//
// Before issue #3633 the sessioncacheguard's waits polled from Node: every tick
// re-issued one Runtime.evaluate per context, so a wait that never settles burns
// ~250 CDP round trips on its 5s deadline (up to ~1000 evaluations across the
// five waits). This polls the SAME predicate at the SAME 20ms cadence on the
// SAME 5s deadline, but as ONE awaited Runtime.evaluate per context: the round
// trips that remain are the results, not the ticks.
//
// The predicate and the read stay the caller's - this only moves where the loop
// runs. `read` and `accepts` are page source, not Node closures: they are
// interpolated into the expression sent to the page, so neither may name a Node
// value (a closure that closes over one throws ReferenceError in the page and
// fails the guard on the first tick). `send` is the context's CDP send: a page's
// own, or a realm's with its executionContextId already bound.
//
// Chrome can throttle a hidden page's timers, which would only DELAY a poll, not
// skip the predicate: every predicate the guard waits on is monotone (once it
// holds it keeps holding), so a coarser cadence cannot change the verdict, and
// the deadline still bounds the wait.

import { evaluate } from "../browserGuardCdp.mjs";

export const WAIT_MS = 5000;
export const POLL_MS = 20;

/**
 * Poll one context in the page until `accepts(observed)` holds or the deadline
 * elapses, then return { ok, observed }: `ok` says the predicate held before the
 * deadline, and `observed` is the last value read either way, so the caller can
 * keep its own deadline message. `read` must be a CALL expression (the template
 * awaits it); `accepts` must be a function expression.
 */
export function pollInPage(send, read, accepts, { waitMs = WAIT_MS, pollMs = POLL_MS } = {}) {
  return evaluate(
    send,
    `(async () => {
      const deadline = performance.now() + ${Number(waitMs)};
      let observed;
      do {
        observed = await (${read});
        if ((${accepts})(observed)) return { ok: true, observed };
        await new Promise((resolve) => setTimeout(resolve, ${Number(pollMs)}));
      } while (performance.now() < deadline);
      return { ok: false, observed };
    })()`,
  );
}

/**
 * The message a wait that spent its whole deadline throws: the caller's own
 * sentence, the deadline, and the last value read. Kept here so the deadline
 * the two share cannot drift apart.
 */
export function waitDeadline(message, observed) {
  return new Error(`${message} (${WAIT_MS}ms deadline); observed ${JSON.stringify(observed)}`);
}
