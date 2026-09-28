// The client half of the boot generation state machine
// (docs/superpowers/specs/2026-09-25-transcript-read-model-design.md,
// "Recorded length, entry ordinals and Seq"). It mirrors
// appwire.CompareBootGeneration (appwire/boot_generation.go) exactly, and its
// test mirrors the Go table row for row.

/** The boot generation the hub stamps on a read it serves from the transcript
 * with no daemon running for the session (appwire.DaemonlessBootGeneration). */
export const DAEMONLESS_BOOT_GENERATION = "daemonless";

/**
 * What a client does with a read response or history update, given the boot
 * generation it holds for the thread:
 * - `apply`: the same token as held; apply normally.
 * - `ignore`: a lower generation of the same counter, from a daemon boot the
 *   client already moved past.
 * - `replace`: any other token (a higher number, another counter's token, a
 *   switch between numeric and daemonless, or nothing held yet). The thread's
 *   whole history is replaced by a fresh latest-window read.
 */
export type BootGenerationAction = "apply" | "ignore" | "replace";

/**
 * Compares boot generation tokens: "<n>" (a daemon's own session),
 * "<n>@<rootSessionID>" (a descendant served by its root's daemon) or
 * "daemonless". Two tokens compare numerically only when both are counters of
 * the same owner; anything else that differs replaces.
 */
export function compareBootGeneration(held: string, incoming: string): BootGenerationAction {
  if (held === incoming) return "apply";
  const heldCounter = parseBootGeneration(held);
  const incomingCounter = parseBootGeneration(incoming);
  if (!heldCounter || !incomingCounter || heldCounter.owner !== incomingCounter.owner) return "replace";
  if (incomingCounter.counter < heldCounter.counter) return "ignore";
  if (incomingCounter.counter === heldCounter.counter) return "apply";
  return "replace";
}

const MAX_UINT64 = (1n << 64n) - 1n;

// Reads a counter token the way Go's strconv.ParseUint does (decimal digits
// only, within uint64), as a bigint so counters past 2^53 still compare
// exactly. daemonless and malformed tokens read as undefined.
function parseBootGeneration(token: string): { counter: bigint; owner: string } | undefined {
  const at = token.indexOf("@");
  const number = at === -1 ? token : token.slice(0, at);
  const owner = at === -1 ? "" : token.slice(at + 1);
  if (at !== -1 && owner === "") return undefined;
  if (!/^[0-9]+$/.test(number)) return undefined;
  const counter = BigInt(number);
  return counter > MAX_UINT64 ? undefined : { counter, owner };
}
