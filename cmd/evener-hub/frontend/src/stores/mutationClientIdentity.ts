import type { ClientIdentity, ClientIdentityStorage, SecureRandomSource } from "@evener/appwire-client/state/mutation";
import { createClientIdentity } from "@evener/appwire-client/state/mutation";

// sessionStorage is per-tab, stable across that tab's reloads, which is what
// the durable mutation outbox's provenance rule (see the package) needs to
// route a shared origin's records back to their sender. Read lazily, per
// call, rather than cached at construction: a sandboxed page whose
// sessionStorage getter throws on the property access itself (not just on a
// method call) still falls back to a generated identity, because that access
// happens inside the package's own try/catch, one call frame down.
const sessionStorageAdapter: ClientIdentityStorage = {
  getItem: (key) => globalThis.sessionStorage?.getItem(key) ?? null,
  setItem: (key, value) => globalThis.sessionStorage?.setItem(key, value),
};

// globalThis.crypto is read once, guarded: touching the property at all can
// throw in a locked-down page, and this app's target has no other random
// source to fall back to, so a throw here still hands createSecureUUID an
// empty source (its own documented non-crypto id) instead of crashing.
function browserRandomSource(): SecureRandomSource {
  try {
    const crypto = globalThis.crypto;
    return {
      randomUUID: typeof crypto?.randomUUID === "function" ? () => crypto.randomUUID() : undefined,
      getRandomValues:
        typeof crypto?.getRandomValues === "function" ? (array) => crypto.getRandomValues(array) : undefined,
    };
  } catch {
    return {};
  }
}

let instance: ClientIdentity = createClientIdentity(sessionStorageAdapter, browserRandomSource());

export function ownClientId(): string {
  return instance.ownClientId();
}

export function isOwnMutationRecord(record: { originClientId?: string }): boolean {
  return instance.isOwnMutationRecord(record);
}

// Test seam: rebuilds the singleton over a storage that hands back `value`
// as the stored identity, or, undefined, back over the real sessionStorage
// adapter. No production code may call this.
export function setMutationClientIdentityForTests(value: string | undefined): void {
  instance =
    value === undefined
      ? createClientIdentity(sessionStorageAdapter, browserRandomSource())
      : createClientIdentity({ getItem: () => value, setItem: () => undefined }, browserRandomSource());
}
