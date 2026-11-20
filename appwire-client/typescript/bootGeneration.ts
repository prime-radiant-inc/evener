// The client half of the boot generation state machine
// (docs/superpowers/specs/2026-09-25-transcript-read-model-design.md,
// "Recorded length, entry ordinals and Seq"). It mirrors
// appwire.CompareBootGeneration (appwire/boot_generation.go) exactly, and its
// test mirrors the Go table row for row.

/** The boot generation the hub stamps on a read it serves from the transcript
 * with no daemon running for the session (appwire.DaemonlessBootGeneration). */
export const DAEMONLESS_BOOT_GENERATION = "daemonless";

/** The boot generation the hub stamps on a failed read that never established
 * who serves the thread (appwire.UnknownOwnerBootGeneration). It is not a
 * claim that no daemon runs, and it is no counter: it names no owner, so it
 * adopts no source and retires nothing a source published. */
export const UNKNOWN_OWNER_BOOT_GENERATION = "unknown-owner";

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
 *
 * `undefined` is a token an older peer omitted: nothing was said about
 * identity, which is the same nothing-held an empty string spells.
 */
export function compareBootGeneration(held: string | undefined, incoming: string | undefined): BootGenerationAction {
  const heldToken = held ?? "";
  const incomingToken = incoming ?? "";
  if (heldToken === incomingToken) return "apply";
  const heldCounter = parseBootGeneration(heldToken);
  const incomingCounter = parseBootGeneration(incomingToken);
  if (!heldCounter || !incomingCounter || heldCounter.owner !== incomingCounter.owner) return "replace";
  if (incomingCounter.counter < heldCounter.counter) return "ignore";
  if (incomingCounter.counter === heldCounter.counter) return "apply";
  return "replace";
}

/**
 * The generation a wire token lets a client adopt, or undefined when it names
 * none. A peer too old to stamp the field omits it, sending nothing about
 * identity; `unknown-owner` is the hub's explicit refusal to claim one. Neither
 * may adopt a source, and neither may retire what one published.
 */
export function adoptableBootGeneration(token: string | undefined): string | undefined {
  return token === undefined || token === "" || token === UNKNOWN_OWNER_BOOT_GENERATION ? undefined : token;
}

/**
 * The generation a *successful* read, history update or subscription read lets
 * a client adopt, or undefined when a success must adopt nothing. A success
 * carrying `daemonless` is saved-history fallback provenance: the hub served
 * recorded history, which says nothing about whether a daemon still serves the
 * thread (docs/product/history-content.md, "A successful saved fallback"), so
 * it may neither displace the live source a client already holds nor retire
 * what that source published. A live daemon never stamps it on its own frames.
 *
 * A *failed* read is the other case, and it is not this function's: there the
 * hub stamps `daemonless` only on attested absence, so a caller reading a
 * rejection keeps using `adoptableBootGeneration`.
 */
export function liveBootGeneration(token: string | undefined): string | undefined {
  const adopted = adoptableBootGeneration(token);
  return adopted === DAEMONLESS_BOOT_GENERATION ? undefined : adopted;
}

/**
 * Whether adopting `adopted` retires a sample stamped `candidate`. Only a
 * generation the adopted one ignores retires: a lower counter of the same
 * owner, published by a daemon boot that owner has moved past. A sample from a
 * different owner, or from a generation ahead of the adopted one, is kept, as
 * is one whose publisher named no generation at all.
 *
 * A caller that holds no adopted generation retires nothing, so it can ask
 * this of whatever it holds without guarding the question.
 */
export function retiresBootGeneration(adopted: string | undefined, candidate: string | undefined): boolean {
  if (adopted === undefined) return false;
  const stamp = adoptableBootGeneration(candidate);
  if (stamp === undefined) return false;
  return compareBootGeneration(adopted, stamp) === "ignore";
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
