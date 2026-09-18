import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import type {
  AuthLogoutResponse,
  AuthStatusResponse,
  AuthTestResponse,
  InstanceCreateParams,
  InstanceEditParams,
} from "@evener/appwire-client";
import { isEndpointConflict, safeCredentialTestResult } from "@evener/appwire-client";
import {
  type CredentialInstancesStore,
  foreignListingChange,
  isStaleListingRefusal,
  staleListingHeld,
} from "@evener/appwire-client/state/credentials";

/** The one credential probe the screen is running or showing: the core's own
 * reply, sanitized so no wire text (which may echo a credential) reaches the
 * UI, and tied to the provider the user asked about. */
export interface CredentialTest {
  provider: string;
  pending: boolean;
  result?: AuthTestResponse;
}

/** What the Providers screen holds besides the core's listing: the write gate
 * and the credential probe. They are screen-local policy, not a projection of
 * the listing - the screen reads the listing fields from the core itself - and
 * they live in a hook so the rules can be driven without mounting the screen. */
export interface ProviderSurface {
  busy: boolean;
  credentialTest: CredentialTest | null;
  refresh(): void;
  // Instance mutations carry the core's applied verdict (true when the
  // listing they answered with is the one the store now holds), so a caller
  // reports success only on a confirmed write.
  create(params: InstanceCreateParams): Promise<boolean>;
  edit(params: InstanceEditParams): Promise<boolean>;
  remove(name: string, expectedEndpointFingerprint?: string): Promise<boolean>;
  setDefault(name: string): Promise<boolean>;
  // Credential writes carry expectedEndpointFingerprint so the hub refuses one
  // whose name moved to a destination the user never reviewed.
  setApiKey(
    provider: string,
    value: string,
    expectedEndpointFingerprint?: string,
  ): Promise<AuthStatusResponse>;
  setCredentialJson(
    provider: string,
    value: string,
    expectedEndpointFingerprint?: string,
  ): Promise<AuthStatusResponse>;
  clearStoredKey(
    provider: string,
    expectedEndpointFingerprint?: string,
  ): Promise<AuthStatusResponse>;
  logout(
    provider: string,
    expectedEndpointFingerprint?: string,
  ): Promise<AuthLogoutResponse>;
  testCredentials(
    provider: string,
    expectedEndpointFingerprint?: string,
  ): Promise<void>;
}

/** The screen's one-write gate. It is owned above the Providers list - by the
 * screen that survives a connection change - so a remount re-derives an
 * in-flight write instead of forgetting it. A fresh mount with no write in
 * flight starts open. */
export interface ProviderWriteGate {
  active(): boolean;
  subscribe(listener: () => void): () => void;
  setActive(writing: boolean): void;
}

export function createProviderWriteGate(): ProviderWriteGate {
  let active = false;
  const listeners = new Set<() => void>();
  return {
    active: () => active,
    subscribe(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    setActive(writing) {
      if (active === writing) return;
      active = writing;
      for (const listener of listeners) listener();
    },
  };
}

// One gate per credential store: the store is created by the screen and
// outlives the Providers list it is rendered into, so a connection change that
// remounts that list re-derives the write already in flight instead of
// starting with an open gate. A store that is gone takes its gate with it.
const writeGates = new WeakMap<CredentialInstancesStore, ProviderWriteGate>();

function providerWriteGate(store: CredentialInstancesStore): ProviderWriteGate {
  let gate = writeGates.get(store);
  if (gate === undefined) {
    gate = createProviderWriteGate();
    writeGates.set(store, gate);
  }
  return gate;
}

/** useProviderSurface binds one credential store to the shell the Providers
 * screen puts around it: one write at a time, configuration writes refused
 * while the listing refuses them (or holds a replaced connection's rows), and a
 * single sanitized credential probe that a foreign listing change retires. */
export function useProviderSurface(
  store: CredentialInstancesStore,
): ProviderSurface {
  const gate = providerWriteGate(store);
  // busy is the gate's observable state: the gate, not this hook instance, owns
  // whether a write is in flight, so a remount shows the write it inherited.
  const busy = useSyncExternalStore(
    gate.subscribe,
    gate.active,
    () => false,
  );
  const [credentialTest, setCredentialTest] = useState<CredentialTest | null>(
    null,
  );
  // testRevision retires a probe whose listing moved under it: a result, and a
  // probe still in flight, both compare against it before publishing.
  const testRevision = useRef(0);
  const testing = useRef(false);
  // disposed stops a callback the screen no longer owns - an Alert confirmation
  // retained past unmount, say - from writing to whatever hub is connected then.
  const disposed = useRef(false);

  // clearCredentialTest retires any probe - a shown result, or one still in
  // flight - so a late answer cannot publish against rows that moved.
  function clearCredentialTest() {
    testRevision.current += 1;
    testing.current = false;
    setCredentialTest(null);
  }

  useEffect(() => {
    disposed.current = false;
    return () => {
      disposed.current = true;
    };
  }, [store]);

  // A probe was checked against rows that a foreign change - another client,
  // the TUI, a failed read - has moved from under it; the core's own post-write
  // refresh is this screen's change and leaves the result standing.
  useEffect(() => {
    return store.subscribe((state, previous) => {
      if (foreignListingChange(state, previous)) clearCredentialTest();
    });
  }, [store]);

  // A listing the store already holds for this connection - a list remounted
  // after a sign-in, say - is adopted as it is, with the read state it came
  // with; anything else reads one now. The store's own refresh keeps it
  // current after that, so an unconditional read here would only repeat it.
  useEffect(() => {
    const held = store.getState();
    if (held.listingEstablished && !held.listingFromPreviousConnection) return;
    // A reconnect starts the store's own restore read before this list mounts;
    // reading again here would supersede it, so a failed second read could
    // discard a restore that already succeeded and leave the stale rows up.
    if (held.loading) return;
    void held.fetch().catch(() => {});
  }, [store]);

  // refresh re-reads the listing and retires any probe: a pull-to-refresh is
  // this screen's own read, and its answer is checked against newer rows.
  function refresh() {
    if (disposed.current) return;
    clearCredentialTest();
    if (gate.active()) return;
    // A read already in flight (a reconnect's restore, another refresh) is
    // this screen's answer too: superseding it would let a failed second read
    // discard one that already succeeded.
    if (store.getState().loading) return;
    void store.getState().fetch().catch(() => {});
  }

  // mutate is async so the gate runs synchronously up to its first await: the
  // ref is written before any promise settles, and a refusal is a rejection.
  // A refused or unconfirmed write is the hub's echo to reconcile, never a
  // replay.
  async function mutate<T>(
    action: () => Promise<T>,
    writesConfiguration: boolean,
    reconcilesOnUnconfirmed = false,
  ): Promise<T> {
    if (disposed.current) throw new Error("Provider screen is closed");
    if (gate.active())
      throw new Error("A provider operation is in progress");
    const state = store.getState();
    if (
      writesConfiguration &&
      (!state.listingEstablished ||
        state.writesRefused ||
        staleListingHeld(state))
    )
      throw new Error("Provider configuration is unavailable for editing");
    gate.setActive(true);
    clearCredentialTest();
    try {
      const result = await action();
      // An instance mutation that answers false was superseded: its answer is
      // not the listing the store now holds, so reconcile with an authoritative
      // read before the caller reports an unconfirmed save. This is the single
      // owner of that recovery read (the screen does not also fetch), and a
      // screen that is gone reconciles nothing.
      if (result === false && reconcilesOnUnconfirmed && !disposed.current)
        void store.getState().fetch().catch(() => {});
      return result;
    } finally {
      // Released even for a screen that is gone: the gate outlives it, and a
      // remounted screen must not inherit a write that has already settled.
      gate.setActive(false);
    }
  }

  // testCredentials runs one probe at a time and sanitizes its reply: the wire
  // text may echo a credential, so only the mapped message is published. A
  // refusal for a replaced connection's rows is that changed connection, not a
  // failed test: the probe is dropped and the listing re-read.
  async function testCredentials(
    provider: string,
    expectedEndpointFingerprint?: string,
  ): Promise<void> {
    if (disposed.current) return;
    const state = store.getState();
    if (gate.active() || state.loading || testing.current) return;
    testing.current = true;
    const revision = ++testRevision.current;
    setCredentialTest({ provider, pending: true });
    let result: AuthTestResponse;
    try {
      result = safeCredentialTestResult(
        provider,
        await state.testCredentials(provider, expectedEndpointFingerprint),
      );
    } catch (err) {
      if (isStaleListingRefusal(err)) {
        // A probe a refresh or a newer probe already retired has no recovery to
        // run: a stray read would transition the listing and retire the newer
        // probe, and after unmount it would mutate a store this screen no
        // longer owns.
        if (revision !== testRevision.current || disposed.current) return;
        clearCredentialTest();
        void store.getState().fetch().catch(() => {});
        return;
      }
      if (isEndpointConflict(err)) {
        // The hub refused the asserted destination: the name moved since the
        // listing was read. There is no honest test result - drop the probe,
        // re-read, and let the screen name the changed endpoint. A retired
        // probe's reply is neither acted on nor surfaced.
        if (revision !== testRevision.current || disposed.current) return;
        clearCredentialTest();
        void store.getState().fetch().catch(() => {});
        throw err;
      }
      result = safeCredentialTestResult(provider, {
        provider,
        status: "endpoint_failure",
        message: "",
      });
    }
    if (revision === testRevision.current && !disposed.current) {
      testing.current = false;
      setCredentialTest({ provider, pending: false, result });
    }
  }

  return {
    busy,
    credentialTest,
    refresh,
    create: (params: InstanceCreateParams) =>
      mutate(() => store.getState().create(params), true, true),
    edit: (params: InstanceEditParams) =>
      mutate(() => store.getState().edit(params), true, true),
    remove: (name: string, expectedEndpointFingerprint?: string) =>
      mutate(
        () => store.getState().remove(name, expectedEndpointFingerprint),
        true,
        true,
      ),
    setDefault: (name: string) =>
      // The store schedules its own reconcile for a superseded default change,
      // so this wrapper does not add a second read.
      mutate(() => store.getState().setDefault(name), true),
    setApiKey: (
      provider: string,
      value: string,
      expectedEndpointFingerprint?: string,
    ) =>
      mutate(
        () => store.getState().setApiKey(provider, value, expectedEndpointFingerprint),
        false,
      ),
    setCredentialJson: (
      provider: string,
      value: string,
      expectedEndpointFingerprint?: string,
    ) =>
      mutate(
        () => store.getState().setCredentialJson(provider, value, expectedEndpointFingerprint),
        false,
      ),
    clearStoredKey: (provider: string, expectedEndpointFingerprint?: string) =>
      mutate(
        () => store.getState().clearStoredKey(provider, expectedEndpointFingerprint),
        false,
      ),
    logout: (provider: string, expectedEndpointFingerprint?: string) =>
      mutate(
        () => store.getState().logout(provider, expectedEndpointFingerprint),
        false,
      ),
    testCredentials,
  };
}
