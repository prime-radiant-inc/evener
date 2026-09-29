// The guarded host mutations the web's Settings -> Hosts and the phone's Hub
// share (registry spec 08 §11-§12): Edit and Remove echo the (generation,
// incarnation id) pair of the row the user saw. An Edit's pair is the one its
// form opened on, and a `stale-entry` refusal goes back to the form: only add
// and update advance a generation, so it means someone else edited the host,
// and sending the edit again would overwrite theirs. A Remove echoes the row
// it holds and earns one re-read and one retry. Which rows a client holds and
// how it re-reads them are its own, so both are ports; so is the mutation id's
// randomness, like every host API the package touches. Pure logic.

import type { AppwireClientLike } from "./clientLike";
import { HostMutationOutcomeError, WireError } from "./errors";
import type { HostEntry, HostRow, MethodTypes, RemovedRow } from "./types.gen";

// HOST_GATE_TIMEOUT_MS is the client-side bound for a host RPC whose server
// side queues on or holds the per-host gate. A supervisor may hold that gate
// for a whole reconnect/ensure cycle, beyond the client's ordinary deadline.
export const HOST_GATE_TIMEOUT_MS = 35 * 60_000;

/** ErrorStaleEntry is the hub's discriminator for a guarded-mutation refusal
 * whose expected pair no longer matches the live entry (appwire.ErrorStaleEntry,
 * appwire/errors.go, spec 08 §12). The retry path matches this string, never
 * the numeric code - siblings share the code. */
export const ErrorStaleEntry = "stale-entry";

/** Whether a refusal is the hub's `stale-entry`: for an edit, the host
 * changed after its form opened. */
export function hostChangedSinceOpened(error: unknown): boolean {
  return error instanceof WireError && error.evenerErrorInfo === ErrorStaleEntry;
}

/** What an edit form says when the host changed after it opened. */
export const HOST_CHANGED_MESSAGE =
  "This host changed since you opened it. Cancel, then open it again to see the change.";

/** HostMutationPair is the (generation, incarnationId) pair a guarded mutation
 * echoes back to the hub. */
export interface HostMutationPair {
  generation: number;
  incarnationId: string;
}

export interface HostMutationPorts {
  client(): Pick<AppwireClientLike, "request">;
  /** The pair of the row the client holds for name, or undefined when it
   * holds none. */
  heldPair(name: string): HostMutationPair | undefined;
  /** A quiet re-read of the host list; the held rows reflect it once it
   * settles. A failed read keeps the rows it had. */
  reRead(): Promise<void>;
  /** A fresh idempotency key per attempt (a replay under the old key would
   * return the old receipt instead of applying the caller's intent). */
  newMutationId(): string;
}

export interface HostMutations {
  /** Sends a guarded update against `expected`, the pair of the row the edit
   * form opened on, and returns the committed row. The host is named by
   * `name`: a name is immutable, so it is the target, not a value. A
   * `stale-entry` refusal rejects as it is, never retried. */
  update(params: { name: string; entry: HostEntry; expected: HostMutationPair }): Promise<HostRow>;
  /** Sends a guarded remove and returns the committed removed row. */
  remove(name: string): Promise<RemovedRow>;
}

/**
 * The mutation-result union the registry's add/update/remove methods return
 * (registry spec 08 §11). The generated client types each method's result as the
 * union of its arm interfaces but publishes no alias for it, so the store names
 * the union off the catalog every arm registration rides: the three methods
 * share one registration, and naming the first of them keeps the alias in step
 * with the generated schema instead of restating the arm list by hand.
 */
export type HostMutationResult = MethodTypes["evener/host/add"]["result"];

/** committedMutationRow narrows the mutation-result union to the row a
 * successful mutation committed. The union's other arms are NOT success and
 * must never read as one (registry spec 08 §11): a
 * `committed-with-teardown-failure` arm names a committed mutation whose rebind
 * needs forward repair through `evener/host/teardown-retry` with the
 * `remnantId` it carries; a `collision-dropped` arm names a foreign hub.toml
 * edit that won the race, so nothing the caller asked for landed; and a
 * keyless add's `ambiguous` arm claims no commit at all. Each throws a
 * HostMutationOutcomeError carrying the arm, naming what happened, so the
 * caller's failure path runs instead of its success path and can still tell
 * the arm apart. */
export function committedMutationRow(result: HostMutationResult, method: string): HostRow {
  // The generated interfaces carry `outcome: string` rather than a literal
  // union, so the arms are narrowed by the fields only one of them declares —
  // the same discriminator the union's registration pins, read structurally.
  if ("observedRow" in result) {
    throw new HostMutationOutcomeError(
      `${method}: the row already exists and this keyless retry cannot tell whether it committed it; re-read the host list`,
      "ambiguous",
    );
  }
  if ("droppedEntry" in result) {
    throw new HostMutationOutcomeError(
      `${method}: a concurrent hub.toml edit won the race, so nothing the caller asked for landed; re-read the host list`,
      "collision-dropped",
    );
  }
  if ("seam" in result) {
    throw new HostMutationOutcomeError(
      `${method}: the mutation committed but its ${result.seam} teardown failed; the entry is committed and its repair handle is remnantId ${result.remnantId}`,
      "committed-with-teardown-failure",
    );
  }
  if (!("host" in result)) {
    throw new HostMutationOutcomeError(`${method}: the response carries no arm this client knows`, "unknown");
  }
  return result.host as HostRow;
}

/** committedRemovedRow is committedMutationRow for `remove`, whose committed arm
 * carries the dedicated removed-row shape. */
function committedRemovedRow(result: HostMutationResult, method: string): RemovedRow {
  committedMutationRow(result, method);
  return (result as { host: RemovedRow }).host;
}

export function createHostMutations(ports: HostMutationPorts): HostMutations {
  /** The pair a guarded mutation must send for name: the held row's pair, or -
   * when no row is held - the pair a fresh list read answers. A name the
   * registry does not list has no pair to echo, so the mutation refuses locally
   * rather than fabricating one. */
  async function pairForMutation(name: string): Promise<HostMutationPair> {
    const held = ports.heldPair(name);
    if (held !== undefined) return held;
    await ports.reRead();
    return listedPair(name);
  }

  /** The pair the rows hold after a re-read, or a local refusal when the
   * registry no longer lists the name. */
  function listedPair(name: string): HostMutationPair {
    const pair = ports.heldPair(name);
    if (pair === undefined) {
      throw new Error(`host "${name}" is not listed, so there is no guarded pair to send with the request`);
    }
    return pair;
  }

  /** Sends a guarded mutation with the retry path the spec's `stale-entry`
   * refusal promises: the row the caller held moved under it, so it re-reads
   * the current pair and retries ONCE with a fresh mutationId. A second
   * stale-entry surfaces to the caller - the row moved twice, and only a person
   * can decide what to do next. */
  async function guardedMutation<T>(
    name: string,
    send: (pair: HostMutationPair, mutationId: string) => Promise<T>,
  ): Promise<T> {
    try {
      return await send(await pairForMutation(name), ports.newMutationId());
    } catch (error) {
      if (!hostChangedSinceOpened(error)) throw error;
      // The held row is exactly what the refusal just proved stale, so the
      // retry re-reads before it echoes anything, then echoes that read's row
      // or refuses locally when the registry no longer lists the name. One
      // read only: a second could find the name re-added as another host.
      await ports.reRead();
      return await send(listedPair(name), ports.newMutationId());
    }
  }

  return {
    update: async (params) => {
      const result = await ports.client().request(
        "evener/host/update",
        {
          name: params.name,
          entry: params.entry,
          mutationId: ports.newMutationId(),
          expectedGeneration: params.expected.generation,
          expectedIncarnationId: params.expected.incarnationId,
        },
        { timeoutMs: HOST_GATE_TIMEOUT_MS },
      );
      return committedMutationRow(result, "evener/host/update");
    },
    remove: (name) =>
      guardedMutation(name, async (pair, mutationId) => {
        const result = await ports.client().request(
          "evener/host/remove",
          {
            name,
            mutationId,
            expectedGeneration: pair.generation,
            expectedIncarnationId: pair.incarnationId,
          },
          { timeoutMs: HOST_GATE_TIMEOUT_MS },
        );
        return committedRemovedRow(result, "evener/host/remove");
      }),
  };
}

/** A host field an edit can change: every HostEntry field but the immutable
 * name, under the wire's own spelling, which is the spelling a refusal's field
 * uses, so a message lands on the input it names. */
export type EditableHostField = Exclude<keyof HostEntry, "name">;

/** The editable fields in the order the Edit form shows them. */
export const HOST_ENTRY_FIELD_ORDER: readonly EditableHostField[] = [
  "address",
  "user",
  "keyPath",
  "evenerPath",
  "configPath",
  "addr",
  "roots",
];

/** Each editable field's label and help line, shared by the web's Add/Edit
 * dialog and the phone's Edit page. */
export const HOST_ENTRY_FIELD_TEXT: Readonly<Record<EditableHostField, { label: string; help: string }>> = {
  address: { label: "SSH address", help: "SSH destination, e.g. host.example or user@host.example." },
  user: { label: "User", help: "Optional SSH user; leave empty when the address already names one." },
  keyPath: { label: "Key path", help: "Optional SSH private-key path used when dialing this host." },
  evenerPath: { label: "Evener path", help: "Optional path to the evener binary on the host, when it is not on PATH." },
  configPath: { label: "Hub config path", help: "Optional path to the host's hub.toml, when it is not the default." },
  addr: { label: "Hub address", help: "Optional listen address of the host's hub, when it is not the default." },
  roots: { label: "Roots", help: "Optional directories on the host to serve. One per line." },
};

/** What each optional field means while it's empty, as the hub's ssh dial
 * treats it (sshconn/runner.go): a placeholder, where the address, the one
 * required field, has none. */
export const HOST_ENTRY_FIELD_WHEN_EMPTY: Readonly<Partial<Record<EditableHostField, string>>> = {
  user: "From the address or SSH config",
  keyPath: "From your SSH config",
  evenerPath: "evener on PATH",
  configPath: "The default hub.toml",
  addr: "The default address",
  roots: "No project roots",
};

// rootsFromText parses a host's roots field: one root per line, trimmed, with
// blank lines dropped — so a stray blank line is not an empty root the hub
// refuses (hostreg's ErrEmptyRoot). The field is plain text rather than a
// browse-assisted path picker because these paths live on the REMOTE host: a
// controller-side directory picker would offer the wrong machine's directories.
export function rootsFromText(text: string): string[] {
  return text
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line !== "");
}

export function rootsToText(roots: readonly string[] | undefined): string {
  return (roots ?? []).join("\n");
}
