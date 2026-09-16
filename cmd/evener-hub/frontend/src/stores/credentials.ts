// credentials.ts is the web's Providers & credentials store: the package's
// credential instances listing core (@evener/appwire-client/state/credentials)
// wired to connectionStore, extended with the evener/auth/* RPCs the section's
// OAuth/API-key/device flows drive, and the evener/auth/updated refetch with
// its own-echo correlation. Follows stores/threads.ts's own connectionStore
// pattern (this store has no connect() of its own): the connection
// subscription below is what tells the core which client the rows belong to.
//
// Never-echo invariant: no method here stores a secret VALUE anywhere in
// this store's state - setApiKey/loginComplete/deviceStart/devicePoll return
// (and this store passes through) only AuthStatusResponse/AuthDeviceStart
// Response/AuthDevicePollResponse shapes, none of which carry the secret
// itself (write-only fields on the wire).

import type {
  AnyNotification,
  AppwireClientLike,
  AuthDevicePollResponse,
  AuthDeviceStartResponse,
  AuthLoginCompleteResponse,
  AuthLoginStartResponse,
  AuthLogoutResponse,
  AuthStatusResponse,
  AuthTestResponse,
} from "@evener/appwire-client";
import {
  type CredentialInstancesSeam,
  type CredentialInstancesState,
  createCredentialInstancesStore,
} from "@evener/appwire-client/state/credentials";
import { useStore } from "zustand";
import { connectionStore } from "./connection";
import { ownClientId } from "./mutationClientIdentity";

export { isStaleListingRefusal, StaleListingRefusal, staleListingHeld } from "@evener/appwire-client/state/credentials";

export interface CredentialsStoreState extends CredentialInstancesState {
  // Auth mutations return the raw wire response and never touch
  // instances/availableProviders synchronously - on success the store
  // schedules its own listing refresh (see the wrappers below), which
  // survives the issuing dialog unmounting before the RPC resolves;
  // callers may still fetch for their own steering, and failures surface as
  // inline errors/toasts in the caller rather than this store swallowing
  // them into an `error` field.
  // expectedEndpointFingerprint is the endpoint the caller showed the user
  // (InstanceEntry.endpointFingerprint). The hub refuses the write when the
  // name resolves elsewhere by then, which is the one gap the caller cannot
  // close itself: its own comparison reads a listing a concurrent change can
  // outdate.
  setApiKey(provider: string, value: string, expectedEndpointFingerprint?: string): Promise<AuthStatusResponse>;
  setCredentialJson(provider: string, value: string, expectedEndpointFingerprint?: string): Promise<AuthStatusResponse>;
  // clearStoredKey removes only the credentials.toml entry, leaving any
  // OAuth/ADC/env credential untouched - the counterpart to setApiKey, and
  // the narrow alternative to logout() for a stray stored key shadowed
  // behind an active oauth/adc sign-in (issue #713).
  clearStoredKey(provider: string, expectedEndpointFingerprint?: string): Promise<AuthStatusResponse>;
  logout(provider: string, expectedEndpointFingerprint?: string): Promise<AuthLogoutResponse>;
  loginStart(provider: string): Promise<AuthLoginStartResponse>;
  loginComplete(provider: string, flowId: string, redirectUrl: string): Promise<AuthLoginCompleteResponse>;
  deviceStart(provider: string): Promise<AuthDeviceStartResponse>;
  devicePoll(provider: string, flowId: string): Promise<AuthDevicePollResponse>;
  // testCredentials is a probe, not a listing read: it dials the endpoint the
  // row names and asserts the fingerprint it carries, so it takes the same gate
  // as a write (requireWritableClient) - a probe issued from a listing that
  // belongs to a connection that is gone would reach a destination this
  // connection never read. Callers treat the refusal as the changed connection
  // it is (isStaleListingRefusal), never as a failed test.
  testCredentials(provider: string, expectedEndpointFingerprint?: string): Promise<AuthTestResponse>;
}

type CredentialAuthActions = Omit<CredentialsStoreState, keyof CredentialInstancesState>;

// The auth RPCs, bound to the core's write gate, landed-write count and
// coalesced refetch through the seam the core hands its extension.
function authActions(seam: CredentialInstancesSeam): CredentialAuthActions {
  return {
    async setApiKey(provider, value, expectedEndpointFingerprint) {
      const client = seam.requireWritableClient();
      const generation = connectionGeneration;
      noteLocalAuthMutation(provider);
      try {
        const result = await client.request("evener/auth/apiKey/set", {
          provider,
          value,
          // The hub echoes this into the evener/auth/updated broadcast, so this page
          // attributes its own echo by identity rather than provider plus timing.
          originClientId: ownClientId(),
          ...(expectedEndpointFingerprint ? { expectedEndpointFingerprint } : {}),
        });
        // The store owns the post-mutation listing refresh: the caller that
        // issued the save may be canceled, hidden, or unmounted before this
        // resolves, and its own refresh would die with it. The helper also
        // re-stamps this mutation's echo window from the response, and the
        // `self` mark lets subscriptions (ProviderConnection's invalidation
        // watch) tell the refresh it schedules apart from a foreign listing
        // change.
        completeLocalAuthMutation(provider, generation);
        // A write landed: count it against the instance it named so only that
        // instance's in-flight refresh is retired (see the core's landedMutations).
        if (generation === connectionGeneration) seam.noteLandedMutation(provider);
        return result;
      } catch (err) {
        endUnconfirmedAuthMutation(provider, generation); // refused: no echo will follow
        throw err;
      }
    },

    async setCredentialJson(provider, value, expectedEndpointFingerprint) {
      const client = seam.requireWritableClient();
      const generation = connectionGeneration;
      noteLocalAuthMutation(provider);
      try {
        const result = await client.request("evener/auth/credentialJson/set", {
          provider,
          value,
          // The hub echoes this into the evener/auth/updated broadcast, so this page
          // attributes its own echo by identity rather than provider plus timing.
          originClientId: ownClientId(),
          ...(expectedEndpointFingerprint ? { expectedEndpointFingerprint } : {}),
        });
        completeLocalAuthMutation(provider, generation);
        if (generation === connectionGeneration) seam.noteLandedMutation(provider); // same rationale as setApiKey
        return result;
      } catch (err) {
        endUnconfirmedAuthMutation(provider, generation); // refused: no echo will follow
        throw err;
      }
    },

    async clearStoredKey(provider, expectedEndpointFingerprint) {
      const client = seam.requireWritableClient();
      const generation = connectionGeneration;
      noteLocalAuthMutation(provider);
      try {
        const result = await client.request("evener/auth/apiKey/clear", {
          provider,
          // The hub echoes this into the evener/auth/updated broadcast, so this page
          // attributes its own echo by identity rather than provider plus timing.
          originClientId: ownClientId(),
          ...(expectedEndpointFingerprint ? { expectedEndpointFingerprint } : {}),
        });
        completeLocalAuthMutation(provider, generation);
        if (generation === connectionGeneration) seam.noteLandedMutation(provider); // same rationale as setApiKey
        return result;
      } catch (err) {
        endUnconfirmedAuthMutation(provider, generation); // refused: no echo will follow
        throw err;
      }
    },

    async logout(provider, expectedEndpointFingerprint) {
      const client = seam.requireWritableClient();
      const generation = connectionGeneration;
      noteLocalAuthMutation(provider);
      try {
        const result = await client.request("evener/auth/logout", {
          provider,
          // The hub echoes this into the evener/auth/updated broadcast, so this page
          // attributes its own echo by identity rather than provider plus timing.
          originClientId: ownClientId(),
          ...(expectedEndpointFingerprint ? { expectedEndpointFingerprint } : {}),
        });
        completeLocalAuthMutation(provider, generation);
        if (generation === connectionGeneration) seam.noteLandedMutation(provider); // same rationale as setApiKey
        return result;
      } catch (err) {
        endUnconfirmedAuthMutation(provider, generation); // refused: no echo will follow
        throw err;
      }
    },

    async loginStart(provider) {
      const client = seam.requireWritableClient();
      return client.request("evener/auth/login/start", { provider });
    },

    async loginComplete(provider, flowId, redirectUrl) {
      const client = seam.requireWritableClient();
      const generation = connectionGeneration;
      noteLocalAuthMutation(provider);
      try {
        // The origin id rides along so the hub's echo names this page as its originator.
        const result = await client.request("evener/auth/login/complete", {
          provider,
          flowId,
          redirectUrl,
          originClientId: ownClientId(),
        });
        completeLocalAuthMutation(provider, generation);
        if (generation === connectionGeneration) seam.noteLandedMutation(provider); // same rationale as setApiKey
        return result;
      } catch (err) {
        endUnconfirmedAuthMutation(provider, generation); // refused: no echo will follow
        throw err;
      }
    },

    async deviceStart(provider) {
      const client = seam.requireWritableClient();
      return client.request("evener/auth/device/start", { provider });
    },

    async devicePoll(provider, flowId) {
      const client = seam.requireWritableClient();
      const generation = connectionGeneration;
      noteLocalAuthMutation(provider);
      try {
        const resp = await client.request("evener/auth/device/poll", {
          provider,
          flowId,
          originClientId: ownClientId(),
        });
        // Only an authorized poll broadcasts evener/auth/updated; a routine
        // pending/expired tick must not keep the marker armed, or a poll loop
        // would silence unrelated same-provider changes tick after tick. An
        // authorized poll also refreshes the listing through the store - the
        // polling dialog may already be closed by the time authorization lands.
        if (resp.state === "authorized") {
          completeLocalAuthMutation(provider, generation);
          if (generation === connectionGeneration) seam.noteLandedMutation(provider);
        } else endUnconfirmedAuthMutation(provider, generation);
        return resp;
      } catch (err) {
        endUnconfirmedAuthMutation(provider, generation); // refused: no echo will follow
        throw err;
      }
    },

    async testCredentials(provider, expectedEndpointFingerprint) {
      // A write's own gate: the probe dials the endpoint its row names, so a
      // probe from the previous connection's listing would reach a destination
      // this connection never read.
      const client = seam.requireWritableClient();
      return client.request("evener/auth/test", {
        provider,
        ...(expectedEndpointFingerprint ? { expectedEndpointFingerprint } : {}),
      });
    },
  };
}

export const credentialsStore = createCredentialInstancesStore<CredentialAuthActions>({ extend: authActions });

export function useCredentialsStore(): CredentialsStoreState;
export function useCredentialsStore<T>(selector: (state: CredentialsStoreState) => T): T;
export function useCredentialsStore<T>(selector?: (state: CredentialsStoreState) => T): T | CredentialsStoreState {
  // Not a real conditional hook call - see stores/connection.ts's own
  // useConnectionStore for the full explanation.
  // biome-ignore lint/correctness/useHookAtTopLevel: same hook both arms, JS default param not a real conditional - see stores/connection.ts
  return selector ? useStore(credentialsStore, selector) : useStore(credentialsStore);
}

// --- notification-triggered refetch --------------------------------------
//
// evener/auth/updated BroadcastAlls to every connected client after a
// successful auth mutation (login/logout/apiKey set/an authorized device
// poll) from ANY of them - InstanceEntry's own activeSource/hasStoredOAuth/
// hasStoredFile/storedEmail fields are exactly what such a mutation changes,
// so a browser tab that already loaded the instance list goes stale
// otherwise. Mirrors stores/extensions.ts's identical
// wiring, applied here to this store's one wire-truth list. On the wire
// evener/auth/updated carries {provider, activeSource} (notifyAuthUpdated,
// cmd/evener-hub/app_rpc.go) matching the generated EvenerAuthUpdatedParams,
// and `provider` is what the own-echo correlation below reads.
//
// The originator is in that audience too, and it must keep receiving the
// notification - other consumers (the model-list cache's epoch guard) depend
// on the originating client's own echo to refresh after its own save. But the
// LISTING refetch is redundant for the originator: the STORE schedules its own
// refresh the moment a local auth mutation succeeds (see the wrappers above) -
// a refresh owned by the store survives the issuing dialog being canceled,
// hidden, or unmounted before the RPC resolves, which a caller-scoped refresh
// (ProviderConnection's refreshAndCheck, CredentialValueDialog, the OAuth
// dialogs' completions) does not. Worse, the echo's refetch is misread by a
// save/check in flight: ProviderConnection's subscription treats the
// echo-driven listing change as an unrelated change and invalidates the fresh
// result ("Connection or configuration changed") or cancels the check. So the
// originator's own echo schedules a self-marked refresh rather than a foreign
// one: it coalesces with the store's own post-save refresh, and the mark is
// what keeps the flow from invalidating on the read that follows. It is
// correlated narrowly:
//
// - Marked per provider when the mutation is ISSUED, then re-stamped when its
//   RPC response lands (completeLocalAuthMutation below): the broadcast can
//   reach this client before the RPC response does, so a resolve-time marker
//   alone would miss an early echo, and a hub whose mutation + broadcast
//   outlasts the window would otherwise have its own LATE echo read as
//   foreign. The echo's window runs from whichever came last - issue or
//   response.
// - COUNTED, not a single timestamp: back-to-back same-provider mutations
//   (two saves in the guided flow, a retry, a poll landing on top of a save)
//   each broadcast one echo, so a lone per-provider marker would let the
//   first echo consume the second mutation's marker and leave the second self
//   echo to be misread as an unrelated client's change - spuriously
//   invalidating the guided flow. The per-provider entry holds the count of
//   outstanding mutations plus the latest stamp (the issue, or the response
//   that re-stamped it); each matching notification consumes exactly one, and
//   only a notification beyond the outstanding count is foreign and still
//   refetches.
// - Cleared when the response proves no broadcast will follow: a failed RPC,
//   or a device poll that comes back pending/expired rather than authorized.
//   One outstanding marker is retired per such outcome (a floor, not an
//   unconditional clear): which mutation failed does not matter, only how
//   many echoed mutations remain outstanding. A failed save must not silence
//   the next unrelated change, and a poll loop must not keep re-arming the
//   window tick after tick.
// - Bounded by a short age window from the marker's latest stamp (issue or
//   response), so a marker that is never consumed (the echo was lost, or the
//   notification arrived pre-response and the client disconnected) cannot
//   outlive its meaning.
//
// Anything unmatched still refetches - other providers, unattributed
// notifications, the same provider with no live marker - so unrelated
// clients' changes keep arriving.
//
// Age budget from the marker's latest stamp - the issue, or the RPC response
// that re-stamped it (completeLocalAuthMutation below).
const SELF_ECHO_WINDOW_MS = 2000;
interface LocalAuthMutationMarker {
  // Outstanding same-provider mutations issued but not yet consumed (by an
  // echo) or retired (by an unconfirmed outcome).
  count: number;
  // The entry's latest life event: the most recent issue, or the RPC
  // response that re-stamped it; ages the whole entry out together.
  issuedAt: number;
}
const localAuthMutations = new Map<string, LocalAuthMutationMarker>();

// The connection the markers belong to. A marker's echo can only arrive on the
// connection its mutation was issued on, so a callback that lands after that
// connection is replaced or reconnects must not touch the markers of the
// connection now in place - nor schedule a `self`-marked read there, which
// would tell the guided flow that a read carrying someone else's change was
// its own. Bumped exactly where the markers are cleared, so "the marker is
// gone" and "the callback is stale" stay one fact (see the subscription below).
let connectionGeneration = 0;

function noteLocalAuthMutation(provider: string): void {
  const existing = localAuthMutations.get(provider);
  localAuthMutations.set(provider, { count: (existing?.count ?? 0) + 1, issuedAt: Date.now() });
}

function clearLocalAuthMutation(provider: string): void {
  localAuthMutations.delete(provider);
}

// Ends a local auth mutation whose outcome proves it will NOT broadcast (a
// failed RPC, or a device poll that returned pending/expired rather than
// authorized). The marker only ever suppressed a notification's own refetch,
// and a matched notification now schedules the store's own refresh itself, so
// there is no swallowed change left to make up: retiring one outstanding
// marker is all this owes, and once the count reaches zero the next
// same-provider notification is foreign again. Which mutation ended does not
// matter - only how many echoed mutations remain outstanding - so this
// decrements whether or not it was the mutation that failed. A callback that
// lands after the issuing connection is gone is ignored whole: its marker was
// cleared with that connection, and an entry still here belongs to the new one.
function endUnconfirmedAuthMutation(provider: string, generation: number): void {
  if (generation !== connectionGeneration) return;
  const existing = localAuthMutations.get(provider);
  if (existing === undefined) return;
  if (existing.count <= 1) localAuthMutations.delete(provider);
  else localAuthMutations.set(provider, { count: existing.count - 1, issuedAt: existing.issuedAt });
}

// Confirms a local auth mutation whose RPC response landed: the hub accepted
// the write, so its broadcast is (or was) on its way. Re-stamping the
// provider's marker moves the echo window to this moment, which covers the
// order the issue-time stamp alone misses - a hub whose mutation and broadcast
// outlast the window (a loaded host, a network-mounted state root) answers
// later than SELF_ECHO_WINDOW_MS after the issue, and its own echo would then
// be read as a foreign change: the coalesced post-save refresh loses the self
// mark and the guided flow answers a successful save with "Connection or
// configuration changed". A marker an early echo already consumed is gone, so
// re-stamping it is a no-op; a marker with no echo still ages out, now from
// the later stamp. Also schedules the store's own refresh, which the echo
// coalesces with instead of duplicating (see the correlation comment above).
// A response from a connection that has since been replaced or reconnected is
// ignored whole: its marker went with that connection, and the read it would
// schedule would carry the new connection's `self` mark. Whatever the old
// connection's write did to the listing is covered by the reconnect's own
// restore load instead.
function completeLocalAuthMutation(provider: string, generation: number): void {
  if (generation !== connectionGeneration) return;
  const existing = localAuthMutations.get(provider);
  if (existing !== undefined) {
    localAuthMutations.set(provider, { count: existing.count, issuedAt: Date.now() });
  }
  credentialsStore.scheduleRefetch(true);
}

// True exactly when this notification is this client's own echo of a
// just-issued auth mutation; consumes one outstanding marker either way, so a
// stale entry cannot suppress a later notification. A stale entry (its latest
// stamp - the mutation's issue time, or the RPC response that re-stamped it -
// older than the window) counts as no marker at all and is dropped whole.
//
// The broadcast carries the id the originating mutation sent, so an echo is
// attributed by identity first: a notification whose originClientId is this
// page's own is its echo, and one naming a different client is foreign however
// close in time. That retires the old same-provider corner, where another
// client's change inside the window was read as this client's own echo. The
// provider-plus-latest-stamp rule stays for a notification with no id - an
// older build, or a mutation made from the TUI - where it is still as exact as
// that wire allows, and where the residual stays bounded the way it always
// was: a matched notification still re-reads the listing, so only the guided
// flow's invalidation is skipped, and only within two seconds of this client's
// own mutation's latest stamp.
function consumeOwnAuthEcho(provider: string | undefined, originClientId: string | undefined): boolean {
  if (provider === undefined) return false;
  const marker = localAuthMutations.get(provider);
  if (marker === undefined) return false;
  const echoedId = originClientId ?? "";
  if (echoedId !== "") {
    // The hub said whose change this is: only this page's own echo consumes one
    // of its markers, so a foreign change no longer retires the marker of a
    // mutation whose echo is still in flight.
    if (echoedId !== ownClientId()) return false;
  } else if (Date.now() - marker.issuedAt > SELF_ECHO_WINDOW_MS) {
    localAuthMutations.delete(provider); // stale: no marker, no echo of ours left
    return false;
  }
  // One mutation broadcasts one echo: consume exactly one outstanding marker.
  if (marker.count <= 1) clearLocalAuthMutation(provider);
  else localAuthMutations.set(provider, { count: marker.count - 1, issuedAt: marker.issuedAt });
  return true;
}

let wiredClient: AppwireClientLike | null = null;
let unsubscribeNotifications: (() => void) | undefined;

function handleNotification(n: AnyNotification): void {
  if (n.method !== "evener/auth/updated") return;
  // A notification this client takes for its own echo is suppressed as a
  // separate refresh, but it still schedules the store's own refresh: if the
  // real echo was lost and another client's same-provider change arrived
  // first, the marker is consumed by that change, and the store's post-save
  // refresh has already run - without this the foreign change would stay
  // invisible until something else refetched. The self mark keeps the guided
  // flow from invalidating on the coalesced read while the listing moves.
  credentialsStore.scheduleRefetch(consumeOwnAuthEcho(n.params.provider, n.params.originClientId));
}

function attachNotifications(client: AppwireClientLike | null): void {
  if (client === wiredClient) return; // already wired to this exact client
  unsubscribeNotifications?.();
  wiredClient = client;
  unsubscribeNotifications = client?.onNotification(handleNotification);
}

// Watches connectionStore for the client becoming available and attaches
// this store's own notification handler to it - see stores/extensions.ts's
// identical wiring for the full "why react to the store instead of reading
// it once" rationale (a mount-order race between this module and AppShell's
// own connect() effect). The core learns of the same transition through
// connectionChanged: it retires in-flight requests, marks a replaced
// connection's listing, and restores the listing on ready.
connectionStore.subscribe((state, previous) => {
  if (state.client !== previous.client || state.state !== previous.state) {
    // A marker belongs to the connection its mutation was issued on: the echo
    // cannot arrive on a different one, so a marker left over from a replaced
    // or reconnected client is pure suppression risk for whatever
    // same-provider notification comes next on the new connection, and the
    // callbacks of mutations still in flight on the old one belong to a
    // connection that is gone (see connectionGeneration).
    localAuthMutations.clear();
    connectionGeneration += 1;
  }
  attachNotifications(state.client);
  credentialsStore.connectionChanged(state.client, state.state);
});
const initialConnection = connectionStore.getState();
if (initialConnection.client) {
  attachNotifications(initialConnection.client);
  credentialsStore.connectionChanged(initialConnection.client, initialConnection.state);
}

// resetCredentialsStoreForTests resets this singleton store's state between
// tests, including the module-private wiring/marker bookkeeping above and
// the core's own - mirroring resetThreadsStoreForTests/resetTreeStoreForTests.
// No production code should ever call this.
export function resetCredentialsStoreForTests(): void {
  localAuthMutations.clear();
  connectionGeneration += 1;
  unsubscribeNotifications?.();
  unsubscribeNotifications = undefined;
  wiredClient = null;
  credentialsStore.resetForTests();
}
