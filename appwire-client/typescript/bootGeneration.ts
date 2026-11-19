// The client half of the boot generation state machine
// (docs/superpowers/specs/2026-09-25-transcript-read-model-design.md,
// "Recorded length, entry ordinals and Seq"). It mirrors
// appwire.CompareBootGeneration (appwire/boot_generation.go) exactly, and its
// test mirrors the Go table row for row.

/** The boot generation the hub stamps on a read it serves from the transcript
 * with no daemon running for the session (appwire.DaemonlessBootGeneration). */
export const DAEMONLESS_BOOT_GENERATION = "daemonless";

/**
 * The boot generation the hub stamps on a FAILED history read whose serving
 * daemon it never established (appwire.UnknownOwnerBootGeneration): the saved
 * fallback that answered the read failed, and the evidence that made the
 * fallback necessary named no owner.
 *
 * It is not a claim that no daemon runs, and it names no owner: a hold may not
 * be retired on it and it may not become a hold owner. `confirmedBootReplacement`
 * answers false for it in every direction, which is why it needs no special
 * case there. The binding test (bootGeneration.test.ts) reads the Go constant,
 * so a hub-side rename breaks there rather than silently misclassifying.
 */
export const UNKNOWN_OWNER_BOOT_GENERATION = "unknown-owner";

/**
 * Whether a FAILED history read's token is the hub's positive claim that no
 * daemon serves the thread (appwire.DaemonlessBootGeneration on a failure).
 *
 * This is the one place the client decides that question. A hold retires on a
 * confirmed replacement, and a confirmed ABSENCE retires it too - no daemon
 * runs the thread, so no delegate controller is sampling its sequence, and a
 * buffered sample from the departed source would otherwise put a wait on
 * screen after the daemon that owned it exited.
 *
 * `unknown-owner` is NOT an absence: it says the hub never established who was
 * serving the thread, which is not a claim that nobody was. Retiring a hold on
 * it is the same inversion one token over.
 *
 * It applies to a FAILED read's token only. On a SUCCESSFUL saved read the hub
 * stamps `daemonless` even when ownership was never resolved (a relay outage,
 * a protocol refusal, a deadline after a live read failed), so a client must
 * not read it there as proof the daemon is gone.
 */
export function isConfirmedAbsence(bootGeneration: string | undefined): boolean {
  return bootGeneration === DAEMONLESS_BOOT_GENERATION;
}

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

/**
 * Where two boot tokens stand relative to each other: `candidate` was minted
 * before (`older`), after (`newer`) or in the same boot as (`same`) the
 * `reference`, or the two cannot be ordered at all (`unorderable`).
 *
 * Only tokens that both parse and name the same owner have an order. A bare
 * counter names no owner, `daemonless` names no counter, and two different
 * owners have no shared numbering, so none of those pairs says which line
 * ended first. Callers use this where "older" is the only reading that
 * licenses discarding something: everything else is absence of evidence.
 */
export type BootGenerationOrder = "older" | "same" | "newer" | "unorderable";

/**
 * Orders `candidate` against `reference`. See `BootGenerationOrder`: only a
 * same-owner counter pair has an order, and every other pair is `unorderable`
 * rather than guessed.
 */
export function orderBootGeneration(candidate: string, reference: string): BootGenerationOrder {
  if (candidate === reference) return "same";
  const candidateCounter = parseBootGeneration(candidate);
  const referenceCounter = parseBootGeneration(reference);
  if (!candidateCounter || !referenceCounter || candidateCounter.owner !== referenceCounter.owner) return "unorderable";
  if (candidateCounter.counter < referenceCounter.counter) return "older";
  if (candidateCounter.counter > referenceCounter.counter) return "newer";
  return "same";
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
    // A token that parses as nothing is not evidence about who serves the
    // thread, so it cannot be the thing that ends another source's ownership.
    // Trusting it here is what let a malformed hint erase a live hold and then
    // install itself as that hold's owner.
    if (!incomingCounter && incoming !== DAEMONLESS_BOOT_GENERATION) return false;
    // `daemonless` is a definite answer about who serves the thread: no daemon
    // is running it, so no delegate controller is sampling its sequence.
    return held === DAEMONLESS_BOOT_GENERATION || incoming === DAEMONLESS_BOOT_GENERATION;
  }
  if (heldCounter.owner !== incomingCounter.owner) {
    // Both sides name their root, so the owners really differ. A bare counter
    // on either side leaves the owner unstated.
    if (heldCounter.owner !== "" && incomingCounter.owner !== "") return true;
    // An AUTHORITATIVE frame is the serving daemon's own account of itself, so
    // a bare counter here names the daemon serving the thread as its own root:
    // a descendant previously served by "5@root" resumed under its own daemon
    // at "1", and its first hold is numbered from that daemon's start.
    //
    // ASSUMPTION: the hub qualifies a descendant with its root on EVERY
    // authoritative path (server/appwire_histories.go's bootGenerationFor),
    // which is what makes a bare counter mean "its own root" rather than
    // "an unstated owner". A daemon old enough to send a bare counter on a
    // descendant's READ would retire that descendant's hold instead of
    // preserving it. Only the resync side has evidence behind it, so only that
    // side is treated as ambiguous.
    //
    // A resync hint is different: an older daemon stamps a descendant's
    // worktree-change resync with a bare counter while its reads name the same
    // controller qualified, so a bare token from one may be the same owner
    // under another name and confirms nothing.
    return evidence === "authoritative";
  }
  return incomingCounter.counter > heldCounter.counter;
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
