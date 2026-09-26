import type { SecureRandomSource } from "@evener/appwire-client/state/mutation";

// globalThis.crypto is read once per call, guarded: touching the property at
// all can throw in a locked-down page, and this app has no other random
// source to fall back to, so a throw here still hands the package an empty
// source (its own documented non-crypto id) instead of crashing. Shared by
// secureUUID.ts and mutationClientIdentity.ts, the web's two callers of a
// package function that takes this port.
export function browserRandomSource(): SecureRandomSource {
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
