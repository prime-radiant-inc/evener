import {
  ErrorInstanceRemovePersisted,
  friendlyErrorMessage,
  safeCredentialTestResult,
  sessionActionError,
  WireError,
} from "@evener/appwire-client";
import type {
  AuthTestResponse,
  InstanceCreateParams,
  InstanceEditParams,
} from "@evener/appwire-client";
import {
  type CredentialInstancesState,
  type CredentialInstancesStore,
  type CredentialListing,
  foreignListingChange,
  listingChanged,
  listingOf,
} from "@evener/appwire-client/state/credentials";

interface ProviderState {
  credentialTest: {
    provider: string;
    pending: boolean;
    result?: AuthTestResponse;
  } | null;
  // Null until a listing has landed (the core's listingEstablished): the
  // screen shows a spinner for one and an empty list for the other.
  data: CredentialListing | null;
  loading: boolean;
  busy: boolean;
  error: string | null;
}

// isInstanceRemovePersisted reads the hub's own discriminator for a removal that
// stood in the config but could not finish
// (appwire.ErrorInstanceRemovePersisted, exported by the AppWire package so every
// client reads the one value its ErrorData carries, and bound to the Go constant
// by that package's errors.test.ts). A refusal or any other failure carries no
// such info, so it stays a plain failure.
export function isInstanceRemovePersisted(err: unknown): boolean {
  return err instanceof WireError && err.evenerErrorInfo === ErrorInstanceRemovePersisted;
}

// removalFailureMessage is what the screen shows when a removal is refused or
// otherwise fails (a rejection without the persisted discriminator). The generic
// copy the credential flows use guards against a provider or transport error
// echoing a submitted secret; a removal carries no secret - only the instance
// name and the endpoint fingerprint the caller asserted - so the hub's own
// message, which names the refusal's remedy, reaches the user the same way the
// web pane's "Remove failed: <message>" toast does.
export function removalFailureMessage(err: unknown): string {
  return `Remove failed: ${friendlyErrorMessage(err)}`;
}

// RemovalOutcome is what a removal resolved to. A removal that stood in the
// config but could not finish is not a failure - the entry is out of
// providers.toml - so it resolves with the hub's own message (it says what was
// left unfinished) for the screen to warn with. Every other failure rejects, as
// before.
export type RemovalOutcome =
  | { kind: "removed" }
  | { kind: "removedPersisted"; message: string };

// applyRemovalOutcome is what the providers screen does with a resolved
// removal. The editor closes for EVERY successful removal - the authored entry
// is gone, and for a name the environment also supplies the implicit row that
// survives must open fresh rather than keep the removed instance's draft (a
// save from that draft would author a new override against the replacement
// row). Only a removal that stood with a leftover copy carries a warning; a
// clean removal leaves the list-level warning silent.
export function applyRemovalOutcome(
  outcome: RemovalOutcome,
  close: () => void,
  setWarning: (message: string | null) => void,
): void {
  close();
  setWarning(outcome.kind === "removedPersisted" ? outcome.message : null);
}

/** Provider data and operations for one hub's provider list: the credential
 * store it is handed, projected into the snapshot the list renders. The
 * core owns the listing, its ordering, the evener/auth/updated refetch, the
 * post-write refresh, and the rule that a refused write reads nothing of its
 * own (the hub's echo of a write it applied is what re-reads); the list's own
 * rules stay here - one write at a time (`busy`) and a credential test that
 * never echoes the wire. */
export class ProviderInstances {
  private state: ProviderState = {
    credentialTest: null,
    data: null,
    loading: false,
    busy: false,
    error: null,
  };
  private listeners = new Set<() => void>();
  private stopProjecting?: () => void;
  private disposed = false;
  private started = false;
  private testRevision = 0;
  constructor(private readonly core: CredentialInstancesStore) {}
  // The projection subscribes on first use, not in the constructor: an
  // instance a render built and discarded without start() must not keep
  // projecting a store it will never publish for.
  private connect() {
    if (this.stopProjecting || this.disposed) return;
    this.stopProjecting = this.core.subscribe((state, previous) => this.project(state, previous));
  }
  getSnapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(change: Partial<ProviderState>) {
    if (this.disposed) return;
    this.state = { ...this.state, ...change };
    for (const listener of this.listeners) listener();
  }
  private project(state: CredentialInstancesState, previous: CredentialInstancesState) {
    const change: Partial<ProviderState> = {};
    const moved = listingChanged(state, previous);
    if (moved || state.listingEstablished !== previous.listingEstablished) change.data = this.listing(state);
    // A credential test was checked against rows that a foreign change - another
    // client, the TUI, a failed read - has moved from under it; the store's own
    // post-write refresh is this screen's change and leaves the result standing.
    if (foreignListingChange(state, previous, moved)) Object.assign(change, this.invalidateTest());
    if (state.loading !== previous.loading) change.loading = state.loading;
    if (state.error !== previous.error) change.error = this.loadFailure(state.error);
    if (Object.keys(change).length > 0) this.publish(change);
  }
  private listing(state: CredentialInstancesState): CredentialListing | null {
    return state.listingEstablished ? listingOf(state) : null;
  }
  // loadFailure is the sentence the screen shows for the core's read error.
  private loadFailure(error: string | null): string | null {
    return error === null ? null : sessionActionError("Could not load providers", error);
  }
  // invalidateTest retires any credential test - a shown result, or a probe
  // still in flight - and returns the state change that takes it down.
  private invalidateTest(): Pick<ProviderState, "credentialTest"> {
    this.testRevision += 1;
    return { credentialTest: null };
  }
  start() {
    if (this.disposed || this.started) return;
    this.started = true;
    this.connect();
    // A listing the store already holds for this connection - a list remounted
    // after a sign-in, say - is published as it is, with the read state it came
    // with (a failed background refetch keeps its rows and its error): the
    // store's own refresh keeps it current, and a read here would only repeat
    // it. A listing that belongs to a replaced connection is read past, as ever.
    const held = this.core.getState();
    if (held.listingEstablished && !held.listingFromPreviousConnection) {
      this.publish({ data: listingOf(held), loading: held.loading, error: this.loadFailure(held.error) });
      return;
    }
    void this.refresh();
  }
  refresh = async (): Promise<void> => {
    if (this.disposed) return;
    this.connect();
    this.publish(this.invalidateTest());
    if (this.state.busy) return;
    await this.core.getState().fetch();
  };
  private async mutate(action: () => Promise<unknown>, configuration: boolean) {
    if (this.disposed) throw new Error("Provider screen is closed");
    if (this.state.busy) throw new Error("A provider operation is in progress");
    if (configuration && (!this.state.data || this.state.data.writesRefused))
      throw new Error("Provider configuration is unavailable for editing");
    this.connect();
    this.publish({ busy: true, ...this.invalidateTest() });
    // Never retry a credential or instance mutation: a lost reply is the hub's
    // echo to reconcile.
    try {
      await action();
    } finally {
      this.publish({ busy: false });
    }
  }
  create = (params: InstanceCreateParams) =>
    this.mutate(() => this.core.getState().create(params), true);
  edit = (params: InstanceEditParams) =>
    this.mutate(() => this.core.getState().edit(params), true);
  // The fingerprint is what the hub checks the removal against, so a screen
  // holding a listing another client has since re-pointed refuses instead of
  // deleting whatever now answers to the name (the web pane's removal asserts
  // the same value).
  remove = async (
    name: string,
    expectedEndpointFingerprint?: string,
  ): Promise<RemovalOutcome> => {
    try {
      await this.mutate(
        () => this.core.getState().remove(name, expectedEndpointFingerprint),
        true,
      );
      return { kind: "removed" };
    } catch (err) {
      if (!isInstanceRemovePersisted(err)) throw err;
      // The removal stood: re-read the listing it left behind so the removed row
      // leaves it. A lost read is the connection banner's to report, not this
      // removal's - the hub's message is what the screen has to show.
      await this.core.getState().fetch().catch(() => {});
      return { kind: "removedPersisted", message: friendlyErrorMessage(err) };
    }
  };
  setDefault = (name: string) =>
    this.mutate(() => this.core.getState().setDefault(name), true);
  setApiKey = (provider: string, value: string) =>
    this.mutate(() => this.core.getState().setApiKey(provider, value), false);
  setCredentialJson = (provider: string, value: string) =>
    this.mutate(() => this.core.getState().setCredentialJson(provider, value), false);
  clearStoredKey = (provider: string) =>
    this.mutate(() => this.core.getState().clearStoredKey(provider), false);
  logout = (provider: string) =>
    this.mutate(() => this.core.getState().logout(provider), false);
  testCredentials = async (provider: string): Promise<void> => {
    if (
      this.disposed ||
      this.state.busy ||
      this.state.loading ||
      this.state.credentialTest?.pending
    )
      return;
    this.connect();
    const revision = ++this.testRevision;
    this.publish({ credentialTest: { provider, pending: true } });
    let result: AuthTestResponse;
    try {
      result = safeCredentialTestResult(
        provider,
        await this.core.getState().testCredentials(provider),
      );
    } catch {
      result = safeCredentialTestResult(provider, {
        provider,
        status: "endpoint_failure",
        message: "",
      });
    }
    if (!this.disposed && revision === this.testRevision)
      this.publish({ credentialTest: { provider, pending: false, result } });
  };
  dispose() {
    this.disposed = true;
    this.stopProjecting?.();
    this.listeners.clear();
  }
}
