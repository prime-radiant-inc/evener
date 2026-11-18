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
 * Which producer the incoming token came from, which decides whether a bare
 * counter names an owner.
 *
 * `authoritative`: a read response, a versioned page, a `history/updated`, or
 * a structured read failure. These are the serving daemon's own account of
 * itself, and the hub qualifies a descendant with its root on every one of
 * them (`bootGenerationFor`). A bare counter here therefore names the daemon
 * serving the thread as its own root — the descendant's own daemon took over.
 *
 * `resync`: a `evener/thread/resync` hint. An older daemon stamps a
 * descendant's worktree-change resync with a bare counter while its reads name
 * the same controller qualified, so a bare token from this producer may be the
 * same owner under another name, and names nothing it can be retired by.
 */
export type BootTokenEvidence = "resync" | "authoritative";

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

/**
 * Whether two boot tokens confirm that one source REPLACED another — the only
 * question a hold retirement may act on.
 *
 * `compareBootGeneration` answers what to do with history, which is the right
 * question for a value: an ambiguous token still invalidates, and the next read
 * replaces. A hold is different, because an unchanged hold emits no update of
 * its own, so a retirement decided on a misread token cannot be undone.
 *
 * A bare counter does not say whose counter it is. An older daemon stamps a
 * descendant's worktree-change resync with `5` while its reads name the same
 * controller `5@root`; `compareBootGeneration` reads those owners as different
 * and returns "replace", but nothing has been replaced. So a mismatch between a
 * bare token and a qualified one is AMBIGUOUS, and absence of evidence is not
 * evidence of replacement: it confirms nothing.
 *
 * Comparable tokens decide normally. Two qualified tokens from different roots
 * confirm a genuine takeover — a daemon serving the thread as its own root.
 * `daemonless` names a definite absence of a daemon, so it confirms. A token
 * that parses as neither is malformed and names nothing.
 */
export function confirmedBootReplacement(
  held: string,
  incoming: string,
  evidence: BootTokenEvidence = "resync",
): boolean {
  if (held === incoming) return false;
  // An empty `held` means this model has recorded no source yet, so there is
  // nothing whose replacement could be confirmed - not even by daemonless.
  if (held === "") return false;
  const heldCounter = parseBootGeneration(held);
  const incomingCounter = parseBootGeneration(incoming);
  if (!heldCounter || !incomingCounter) {
    // `daemonless` is a definite answer about who serves the thread: no daemon
    // is running it, so no delegate controller is sampling its sequence.
    return held === DAEMONLESS_BOOT_GENERATION || incoming === DAEMONLESS_BOOT_GENERATION;
  }
  if (heldCounter.owner !== incomingCounter.owner) {
    // Both sides name their root, so the owners really differ. A bare counter
    // on either side leaves the owner unstated.
    if (heldCounter.owner !== "" && incomingCounter.owner !== "") return true;
    // An AUTHORITATIVE frame is the serving daemon's own account of itself,
    // and the hub qualifies a descendant with its root on every one of them
    // (server/appwire_histories.go's bootGenerationFor). A bare counter here
    // therefore names the daemon serving the thread as its own root: a
    // descendant previously served by "5@root" resumed under its own daemon
    // at "1", and its first hold is numbered from that daemon's start.
    //
    // A resync hint is different: an older daemon stamps a descendant's
    // worktree-change resync with a bare counter while its reads name the same
    // controller qualified, so a bare token from one may be the same owner
    // under another name and confirms nothing.
    return evidence === "authoritative";
  }
  return compareBootGeneration(held, incoming) === "replace";
}

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
