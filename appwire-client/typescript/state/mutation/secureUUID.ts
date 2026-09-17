// Both methods are optional: a host with neither (no global Web Crypto, and
// no polyfill) still gets an id out of createSecureUUID rather than a throw -
// see its documented fallback below. No default source lives here; a host
// with a real one (globalThis.crypto, expo-crypto) passes it explicitly.
export interface SecureRandomSource {
  randomUUID?: () => string;
  getRandomValues?: (array: Uint8Array<ArrayBuffer>) => Uint8Array<ArrayBuffer>;
}

// Runs a host-supplied call that can throw for reasons this module has no
// business inspecting (a locked-down page, a denied permission), and hands
// back undefined instead of letting the throw escape. Shared by every
// optional host call this module and records.ts guard.
export function tryOrUndefined<T>(fn: () => T): T | undefined {
  try {
    return fn();
  } catch {
    return undefined;
  }
}

// A fresh id from whatever this source actually offers: source.randomUUID()
// when there is one, an RFC4122-shaped id built from source.getRandomValues()
// when that is all there is, or - a source can have neither, or have one that
// throws when called, and refusing to mint an id over that is not this
// function's call - a generated id with no cryptographic strength, plainly
// not UUID-shaped so nothing mistakes it for one. Never throws: every secure
// call is guarded here, the one place that owns this fallback.
export function createSecureUUID(source: SecureRandomSource): string {
  if (typeof source.randomUUID === "function") {
    const id = tryOrUndefined(() => source.randomUUID?.());
    if (id !== undefined) return id;
  }

  if (typeof source.getRandomValues === "function") {
    const uuid = tryOrUndefined(() => {
      const bytes = source.getRandomValues?.(new Uint8Array(16));
      if (!bytes) return undefined;
      bytes[6] = ((bytes[6] ?? 0) & 0x0f) | 0x40;
      bytes[8] = ((bytes[8] ?? 0) & 0x3f) | 0x80;
      const hex = Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0"));
      return [
        hex.slice(0, 4).join(""),
        hex.slice(4, 6).join(""),
        hex.slice(6, 8).join(""),
        hex.slice(8, 10).join(""),
        hex.slice(10).join(""),
      ].join("-");
    });
    if (uuid !== undefined) return uuid;
  }

  return `insecure-${Math.random().toString(36).slice(2)}-${Date.now().toString(36)}`;
}
